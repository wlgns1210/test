package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// LoadTester orchestrates the entire load test.
type LoadTester struct {
	cfg     *Config
	metrics *Metrics
	client  *http.Client
}

func NewLoadTester(cfg *Config) *LoadTester {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Size the connection pool generously so workers are never waiting for a slot.
		MaxIdleConns:          cfg.Workers * 2,
		MaxIdleConnsPerHost:   cfg.Workers * 2,
		MaxConnsPerHost:       cfg.Workers * 2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     !cfg.KeepAlive,
	}

	return &LoadTester{
		cfg:     cfg,
		metrics: NewMetrics(),
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}
}

// Run starts the load test and blocks until it finishes or ctx is cancelled.
func (lt *LoadTester) Run(ctx context.Context) {
	reporter := NewReporter(lt.cfg, lt.metrics)
	reporter.PrintBanner()

	// Derive a test-scoped context that also respects the configured duration.
	testCtx, cancel := context.WithTimeout(ctx, lt.cfg.Duration)
	defer cancel()

	// workCh carries dispatch tokens to workers.
	// A generous buffer smooths short bursts without blocking the dispatcher.
	workCh := make(chan struct{}, lt.cfg.Workers*4)

	// Start live reporter (independent context so it outlives testCtx until we
	// explicitly stop it).
	reportCtx, reportCancel := context.WithCancel(context.Background())
	var reporterWg sync.WaitGroup
	reporterWg.Add(1)
	go func() {
		defer reporterWg.Done()
		reporter.RunLive(reportCtx)
	}()

	// Start worker pool.
	var workerWg sync.WaitGroup
	for i := 0; i < lt.cfg.Workers; i++ {
		workerWg.Add(1)
		w := NewWorker(lt.cfg, lt.client, lt.metrics)
		go func() {
			defer workerWg.Done()
			for range workCh {
				w.Do(testCtx)
			}
		}()
	}

	// Dispatch tokens at the configured rate until testCtx is done.
	lt.dispatch(testCtx, workCh)

	// Signal workers to stop and wait for them to finish in-flight requests.
	close(workCh)
	workerWg.Wait()

	// Stop the reporter and wait for it to flush its last line.
	reportCancel()
	reporterWg.Wait()
}

// dispatch feeds the workCh at exactly the configured rate.
//
// Strategy:
//   - rate ≤ 1000 req/s  → per-request ticker  (interval ≥ 1 ms)
//   - rate >  1000 req/s → batch ticker every 10 ms (smoother at high rates)
func (lt *LoadTester) dispatch(ctx context.Context, workCh chan<- struct{}) {
	rps := lt.cfg.GetRPS()
	maxReq := lt.cfg.MaxRequests
	var sent int64

	if rps <= 1000 {
		lt.dispatchTicker(ctx, workCh, rps, maxReq, &sent)
	} else {
		lt.dispatchBatch(ctx, workCh, rps, maxReq, &sent)
	}
}

// dispatchTicker sends one token per tick interval.
func (lt *LoadTester) dispatchTicker(ctx context.Context, workCh chan<- struct{}, rps float64, maxReq int64, sent *int64) {
	interval := time.Duration(float64(time.Second) / rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if maxReq > 0 && atomic.LoadInt64(sent) >= maxReq {
				return
			}
			select {
			case workCh <- struct{}{}:
				atomic.AddInt64(sent, 1)
			case <-ctx.Done():
				return
			}
		}
	}
}

// dispatchBatch sends batches of tokens every 10 ms.
// An accumulator handles fractional batch sizes to maintain long-term accuracy.
func (lt *LoadTester) dispatchBatch(ctx context.Context, workCh chan<- struct{}, rps float64, maxReq int64, sent *int64) {
	const batchInterval = 10 * time.Millisecond
	batchSize := rps * batchInterval.Seconds() // tokens per batch (may be fractional)

	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	var accumulator float64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			accumulator += batchSize
			toSend := int(accumulator)
			accumulator -= float64(toSend)

			for i := 0; i < toSend; i++ {
				if maxReq > 0 && atomic.LoadInt64(sent) >= maxReq {
					return
				}
				select {
				case workCh <- struct{}{}:
					atomic.AddInt64(sent, 1)
				case <-ctx.Done():
					return
				default:
					// Worker pool saturated — record the drop and move on.
					lt.metrics.DropRequest()
				}
			}
		}
	}
}

// PrintFinalReport prints the full summary to stdout.
func (lt *LoadTester) PrintFinalReport() {
	NewReporter(lt.cfg, lt.metrics).PrintFinal()
}

// SaveJSON writes a machine-readable JSON report to path.
func (lt *LoadTester) SaveJSON(path string) error {
	data, err := NewReporter(lt.cfg, lt.metrics).ToJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ---------------------------------------------------------------------------
// JSON report structure
// ---------------------------------------------------------------------------

type jsonReport struct {
	Config struct {
		URL         string  `json:"url"`
		Method      string  `json:"method"`
		RatePerHour int64   `json:"rate_per_hour"`
		RatePerSec  float64 `json:"rate_per_sec"`
		Duration    string  `json:"duration"`
		Workers     int     `json:"workers"`
	} `json:"config"`
	Results struct {
		Elapsed         string           `json:"elapsed"`
		TotalRequests   int64            `json:"total_requests"`
		SuccessRequests int64            `json:"success_requests"`
		FailedRequests  int64            `json:"failed_requests"`
		DroppedRequests int64            `json:"dropped_requests"`
		SuccessRatePct  float64          `json:"success_rate_pct"`
		ActualRPS       float64          `json:"actual_rps"`
		StatusCodes     map[string]int64 `json:"status_codes"`
		Errors          map[string]int64 `json:"errors"`
	} `json:"results"`
	Latency struct {
		SampleCount int     `json:"sample_count"`
		MinMs       float64 `json:"min_ms"`
		MeanMs      float64 `json:"mean_ms"`
		StdDevMs    float64 `json:"stddev_ms"`
		P50Ms       float64 `json:"p50_ms"`
		P75Ms       float64 `json:"p75_ms"`
		P90Ms       float64 `json:"p90_ms"`
		P95Ms       float64 `json:"p95_ms"`
		P99Ms       float64 `json:"p99_ms"`
		P999Ms      float64 `json:"p99_9_ms"`
		MaxMs       float64 `json:"max_ms"`
	} `json:"latency"`
}

func buildJSONReport(cfg *Config, m *Metrics) jsonReport {
	total := m.TotalRequests()
	success := m.SuccessRequests()
	failed := m.FailedRequests()
	dropped := m.DroppedRequests()
	elapsed := m.Elapsed()
	rps := m.TotalRPS()
	pct := m.GetPercentiles()
	sc := m.StatusCodes()
	errs := m.Errors()

	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}

	scStr := make(map[string]int64, len(sc))
	for k, v := range sc {
		scStr[commaInt(int64(k))] = v
	}

	var r jsonReport
	r.Config.URL = cfg.URL
	r.Config.Method = cfg.Method
	r.Config.RatePerHour = cfg.GetRPH()
	r.Config.RatePerSec = cfg.GetRPS()
	r.Config.Duration = cfg.Duration.String()
	r.Config.Workers = cfg.Workers

	r.Results.Elapsed = elapsed.Round(time.Millisecond).String()
	r.Results.TotalRequests = total
	r.Results.SuccessRequests = success
	r.Results.FailedRequests = failed
	r.Results.DroppedRequests = dropped
	r.Results.SuccessRatePct = successRate
	r.Results.ActualRPS = rps
	r.Results.StatusCodes = scStr
	r.Results.Errors = errs

	r.Latency.SampleCount = pct.Count
	r.Latency.MinMs = pct.Min
	r.Latency.MeanMs = pct.Mean
	r.Latency.StdDevMs = pct.StdDev
	r.Latency.P50Ms = pct.P50
	r.Latency.P75Ms = pct.P75
	r.Latency.P90Ms = pct.P90
	r.Latency.P95Ms = pct.P95
	r.Latency.P99Ms = pct.P99
	r.Latency.P999Ms = pct.P999
	r.Latency.MaxMs = pct.Max
	return r
}

func (r *Reporter) ToJSON() ([]byte, error) {
	rep := buildJSONReport(r.cfg, r.metrics)
	return json.MarshalIndent(rep, "", "  ")
}

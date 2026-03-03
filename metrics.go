package main

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const reservoirSize = 100_000 // max latency samples kept in memory

// Metrics collects and stores all test metrics in a thread-safe manner.
type Metrics struct {
	// --- atomic counters (hot path, no lock needed) ---
	totalReqs   int64
	successReqs int64
	failedReqs  int64
	droppedReqs int64 // skipped because worker pool was saturated

	// --- mutex-protected fields ---
	mu          sync.RWMutex
	statusCodes map[int]int64
	errors      map[string]int64

	// latency reservoir (Vitter's Algorithm R)
	samples     []float64 // milliseconds
	samplesSeen int64     // total samples offered (for reservoir replacement)

	// --- timing ---
	startTime time.Time

	// --- sliding-window RPS (protected by windowMu) ---
	windowMu    sync.Mutex
	windowStart time.Time
	windowReqs  int64
	lastRPS     float64
}

func NewMetrics() *Metrics {
	now := time.Now()
	return &Metrics{
		statusCodes: make(map[int]int64),
		errors:      make(map[string]int64),
		samples:     make([]float64, 0, reservoirSize),
		startTime:   now,
		windowStart: now,
	}
}

// RecordSuccess records a successful response (2xx/3xx).
// errType should be "" on success; a short category string on failure.
func (m *Metrics) RecordRequest(statusCode int, latency time.Duration, errType string) {
	atomic.AddInt64(&m.totalReqs, 1)

	if errType != "" {
		atomic.AddInt64(&m.failedReqs, 1)
		m.mu.Lock()
		m.errors[errType]++
		m.mu.Unlock()
		m.updateWindow()
		return
	}

	if statusCode >= 200 && statusCode < 400 {
		atomic.AddInt64(&m.successReqs, 1)
	} else {
		atomic.AddInt64(&m.failedReqs, 1)
	}

	latMs := float64(latency.Microseconds()) / 1000.0

	m.mu.Lock()
	m.statusCodes[statusCode]++
	// Reservoir sampling
	m.samplesSeen++
	if int(m.samplesSeen) <= reservoirSize {
		m.samples = append(m.samples, latMs)
	} else {
		idx := rand.Int63n(m.samplesSeen)
		if idx < int64(reservoirSize) {
			m.samples[idx] = latMs
		}
	}
	m.mu.Unlock()

	m.updateWindow()
}

func (m *Metrics) DropRequest() {
	atomic.AddInt64(&m.droppedReqs, 1)
}

func (m *Metrics) updateWindow() {
	m.windowMu.Lock()
	m.windowReqs++
	elapsed := time.Since(m.windowStart)
	if elapsed >= time.Second {
		m.lastRPS = float64(m.windowReqs) / elapsed.Seconds()
		m.windowReqs = 0
		m.windowStart = time.Now()
	}
	m.windowMu.Unlock()
}

// TotalRequests returns the total number of requests attempted.
func (m *Metrics) TotalRequests() int64 { return atomic.LoadInt64(&m.totalReqs) }

// SuccessRequests returns the count of 2xx/3xx responses.
func (m *Metrics) SuccessRequests() int64 { return atomic.LoadInt64(&m.successReqs) }

// FailedRequests returns the count of errors + non-2xx/3xx responses.
func (m *Metrics) FailedRequests() int64 { return atomic.LoadInt64(&m.failedReqs) }

// DroppedRequests returns the count of tokens skipped due to worker saturation.
func (m *Metrics) DroppedRequests() int64 { return atomic.LoadInt64(&m.droppedReqs) }

// Elapsed returns time since the test started.
func (m *Metrics) Elapsed() time.Duration { return time.Since(m.startTime) }

// TotalRPS returns overall average requests per second since start.
func (m *Metrics) TotalRPS() float64 {
	secs := time.Since(m.startTime).Seconds()
	if secs == 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&m.totalReqs)) / secs
}

// RecentRPS returns the recent (sliding-window) requests per second.
func (m *Metrics) RecentRPS() float64 {
	m.windowMu.Lock()
	defer m.windowMu.Unlock()

	elapsed := time.Since(m.windowStart).Seconds()
	if elapsed > 0.2 {
		current := float64(m.windowReqs) / elapsed
		if m.lastRPS == 0 {
			return current
		}
		return (m.lastRPS + current) / 2
	}
	return m.lastRPS
}

// StatusCodes returns a copy of the status code → count map.
func (m *Metrics) StatusCodes() map[int]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[int]int64, len(m.statusCodes))
	for k, v := range m.statusCodes {
		out[k] = v
	}
	return out
}

// Errors returns a copy of the error-type → count map.
func (m *Metrics) Errors() map[string]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int64, len(m.errors))
	for k, v := range m.errors {
		out[k] = v
	}
	return out
}

// Percentiles holds latency distribution statistics.
type Percentiles struct {
	Count  int
	Min    float64
	Max    float64
	Mean   float64
	StdDev float64
	P50    float64
	P75    float64
	P90    float64
	P95    float64
	P99    float64
	P999   float64
}

// GetPercentiles computes latency percentiles from the reservoir sample.
func (m *Metrics) GetPercentiles() Percentiles {
	m.mu.RLock()
	samples := make([]float64, len(m.samples))
	copy(samples, m.samples)
	m.mu.RUnlock()

	n := len(samples)
	if n == 0 {
		return Percentiles{}
	}

	sort.Float64s(samples)

	var sum float64
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(n)

	var variance float64
	for _, s := range samples {
		d := s - mean
		variance += d * d
	}
	variance /= float64(n)

	pct := func(p float64) float64 {
		if n == 1 {
			return samples[0]
		}
		pos := (p / 100.0) * float64(n-1)
		lo := int(pos)
		hi := lo + 1
		if hi >= n {
			return samples[n-1]
		}
		frac := pos - float64(lo)
		return samples[lo]*(1-frac) + samples[hi]*frac
	}

	return Percentiles{
		Count:  n,
		Min:    samples[0],
		Max:    samples[n-1],
		Mean:   mean,
		StdDev: math.Sqrt(variance),
		P50:    pct(50),
		P75:    pct(75),
		P90:    pct(90),
		P95:    pct(95),
		P99:    pct(99),
		P999:   pct(99.9),
	}
}

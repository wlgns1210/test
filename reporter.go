package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Reporter handles live progress display and final summary output.
type Reporter struct {
	cfg     *Config
	metrics *Metrics
}

func NewReporter(cfg *Config, metrics *Metrics) *Reporter {
	return &Reporter{cfg: cfg, metrics: metrics}
}

// PrintBanner prints the test configuration header.
func (r *Reporter) PrintBanner() {
	fmt.Print(r.cfg.Banner())
	fmt.Println("  Starting... (press Ctrl+C to stop early)\n")
}

// RunLive updates a single live-status line once per second.
func (r *Reporter) RunLive(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Print one final live line before stopping.
			r.printLiveLine()
			fmt.Println()
			return
		case <-ticker.C:
			r.printLiveLine()
		}
	}
}

func (r *Reporter) printLiveLine() {
	total := r.metrics.TotalRequests()
	success := r.metrics.SuccessRequests()
	failed := r.metrics.FailedRequests()
	elapsed := r.metrics.Elapsed()
	recentRPS := r.metrics.RecentRPS()
	targetRPS := r.cfg.GetRPS()

	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}

	// \r returns to the start of the line; \033[K clears to end of line.
	fmt.Printf("\r\033[K  [%s] Total: %-10s | RPS: %6.1f / %-6.1f | OK: %-10s | Fail: %-8s | %.1f%%",
		fmtElapsed(elapsed),
		commaInt(total),
		recentRPS,
		targetRPS,
		commaInt(success),
		commaInt(failed),
		successRate,
	)
}

// PrintFinal prints the complete summary report.
func (r *Reporter) PrintFinal() {
	total := r.metrics.TotalRequests()
	success := r.metrics.SuccessRequests()
	failed := r.metrics.FailedRequests()
	dropped := r.metrics.DroppedRequests()
	elapsed := r.metrics.Elapsed()
	rps := r.metrics.TotalRPS()
	targetRPS := r.cfg.GetRPS()
	pct := r.metrics.GetPercentiles()
	sc := r.metrics.StatusCodes()
	errs := r.metrics.Errors()

	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}

	sep := strings.Repeat("─", 58)
	doubleSep := strings.Repeat("═", 58)

	fmt.Println(doubleSep)
	fmt.Println("  LOAD TEST COMPLETE")
	fmt.Println(doubleSep)

	// ── Overview ──────────────────────────────────────────────
	fmt.Println()
	fmt.Printf("  %-22s %s\n", "Duration:", fmtElapsed(elapsed))
	fmt.Printf("  %-22s %s req/h  (~%.2f req/s)\n", "Target Rate:", commaInt(r.cfg.GetRPH()), targetRPS)
	fmt.Printf("  %-22s %.2f req/s\n", "Actual Rate:", rps)

	// ── Request counts ────────────────────────────────────────
	fmt.Println()
	fmt.Println(sep)
	fmt.Println("  REQUESTS")
	fmt.Println(sep)
	fmt.Printf("  %-22s %s\n", "Total:", commaInt(total))
	fmt.Printf("  %-22s %s  (%.2f%%)\n", "Success:", commaInt(success), successRate)
	fmt.Printf("  %-22s %s  (%.2f%%)\n", "Failed:", commaInt(failed), 100-successRate)
	if dropped > 0 {
		fmt.Printf("  %-22s %s  (worker pool saturated)\n", "Dropped:", commaInt(dropped))
	}

	// ── Status codes ──────────────────────────────────────────
	if len(sc) > 0 {
		fmt.Println()
		fmt.Println(sep)
		fmt.Println("  HTTP STATUS CODES")
		fmt.Println(sep)
		codes := make([]int, 0, len(sc))
		for c := range sc {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		for _, code := range codes {
			count := sc[code]
			pctVal := float64(count) / float64(total) * 100
			fmt.Printf("  %-22s %s  (%.1f%%)\n",
				fmt.Sprintf("HTTP %d:", code),
				commaInt(count),
				pctVal,
			)
		}
	}

	// ── Latency ───────────────────────────────────────────────
	if pct.Count > 0 {
		fmt.Println()
		fmt.Println(sep)
		fmt.Println("  LATENCY  (milliseconds)")
		fmt.Println(sep)
		fmt.Printf("  %-22s %10.3f ms\n", "Min:", pct.Min)
		fmt.Printf("  %-22s %10.3f ms\n", "Mean:", pct.Mean)
		fmt.Printf("  %-22s %10.3f ms\n", "StdDev:", pct.StdDev)
		fmt.Println()
		fmt.Printf("  %-22s %10.3f ms\n", "P50 (median):", pct.P50)
		fmt.Printf("  %-22s %10.3f ms\n", "P75:", pct.P75)
		fmt.Printf("  %-22s %10.3f ms\n", "P90:", pct.P90)
		fmt.Printf("  %-22s %10.3f ms\n", "P95:", pct.P95)
		fmt.Printf("  %-22s %10.3f ms\n", "P99:", pct.P99)
		fmt.Printf("  %-22s %10.3f ms\n", "P99.9:", pct.P999)
		fmt.Println()
		fmt.Printf("  %-22s %10.3f ms\n", "Max:", pct.Max)
		fmt.Printf("  %-22s %10d\n", "Samples:", pct.Count)
	}

	// ── Errors ────────────────────────────────────────────────
	if len(errs) > 0 {
		fmt.Println()
		fmt.Println(sep)
		fmt.Println("  ERRORS")
		fmt.Println(sep)

		type errRow struct {
			name  string
			count int64
		}
		rows := make([]errRow, 0, len(errs))
		for k, v := range errs {
			rows = append(rows, errRow{k, v})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })

		for _, row := range rows {
			pctVal := float64(row.count) / float64(total) * 100
			fmt.Printf("  %-22s %s  (%.1f%%)\n",
				row.name+":",
				commaInt(row.count),
				pctVal,
			)
		}
	}

	fmt.Println()
	fmt.Println(doubleSep)
	fmt.Println()
}

// fmtElapsed formats a duration as a short human-readable string.
func fmtElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

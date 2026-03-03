package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 대시보드 레이아웃 상수
const (
	dashboardLines = 18  // drawDashboard 가 출력하는 정확한 줄 수
	boxInner       = 60  // ║ 안쪽 폭 (문자 수)
	progressBarW   = 34  // 진행 바 폭
	sparkW         = 42  // RPS 스파크라인 폭
)

// ── Reporter ──────────────────────────────────────────────────────────────────

// Reporter handles live dashboard display and the final summary report.
type Reporter struct {
	cfg       *Config
	metrics   *Metrics
	firstDraw bool
}

func NewReporter(cfg *Config, metrics *Metrics) *Reporter {
	return &Reporter{cfg: cfg, metrics: metrics, firstDraw: true}
}

// PrintBanner prints the configuration header before the test starts.
func (r *Reporter) PrintBanner() {
	fmt.Print(r.cfg.Banner())
	fmt.Println("  Starting... (press Ctrl+C to stop early)\n")
}

// RunLive redraws the dashboard once per second until ctx is cancelled.
func (r *Reporter) RunLive(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	r.firstDraw = true

	for {
		select {
		case <-ctx.Done():
			r.drawDashboard() // 최종 상태 한 번 더 출력
			return
		case <-ticker.C:
			r.drawDashboard()
		}
	}
}

// drawDashboard renders exactly dashboardLines lines.
// On every call after the first it moves the cursor up to overwrite the previous frame.
func (r *Reporter) drawDashboard() {
	if !r.firstDraw {
		fmt.Printf("\033[%dA", dashboardLines) // 커서를 위로 이동해서 덮어쓰기
	}
	r.firstDraw = false

	m := r.metrics
	cfg := r.cfg

	// ── 스냅샷 수집 ────────────────────────────────────────────
	total := m.TotalRequests()
	success := m.SuccessRequests()
	failed := m.FailedRequests()
	dropped := m.DroppedRequests()
	elapsed := m.Elapsed()
	recentRPS := m.RecentRPS()
	targetRPS := cfg.GetRPS()
	history := m.RPSHistory()
	sc := m.StatusCodes()
	pct := m.GetPercentiles()

	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}

	// ── 진행률 바 ───────────────────────────────────────────────
	progress := elapsed.Seconds() / cfg.Duration.Seconds()
	if progress > 1 {
		progress = 1
	}
	filled := int(progress * float64(progressBarW))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", progressBarW-filled)

	// ── RPS 상태 표시 ──────────────────────────────────────────
	diff := recentRPS - targetRPS
	var rpsStatus string
	switch {
	case diff > 1:
		rpsStatus = fmt.Sprintf("  ↑ +%.1f", diff)
	case diff < -1:
		rpsStatus = fmt.Sprintf("  ↓ %.1f", diff)
	default:
		rpsStatus = "  ✓"
	}

	// ── 스파크라인 ─────────────────────────────────────────────
	maxSpark := targetRPS * 1.3
	for _, v := range history {
		if v > maxSpark {
			maxSpark = v
		}
	}
	spark := buildSparkline(history, sparkW, maxSpark)

	// ── 레이턴시 문자열 ────────────────────────────────────────
	latLine1 := "  데이터 없음 (요청 응답 대기 중)"
	latLine2 := ""
	if pct.Count > 0 {
		latLine1 = fmt.Sprintf("  P50 %8.2fms   P95 %8.2fms   P99 %8.2fms",
			pct.P50, pct.P95, pct.P99)
		latLine2 = fmt.Sprintf("  Min %8.2fms  Mean %8.2fms   Max %8.2fms",
			pct.Min, pct.Mean, pct.Max)
	}

	// ── HTTP 상태 코드 ─────────────────────────────────────────
	codes := make([]int, 0, len(sc))
	for c := range sc {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	var scParts []string
	for _, code := range codes {
		scParts = append(scParts, fmt.Sprintf("HTTP %d: %s", code, commaInt(sc[code])))
	}
	scStr := strings.Join(scParts, "   ")
	if scStr == "" {
		scStr = "첫 번째 응답 대기 중..."
	}

	// ── 박스 출력 헬퍼 ─────────────────────────────────────────
	W := boxInner
	brow := func(s string) string { return boxRow(s, W) }
	top := "╔" + strings.Repeat("═", W) + "╗"
	mid := "╠" + strings.Repeat("═", W) + "╣"
	bot := "╚" + strings.Repeat("═", W) + "╝"

	// ── 타이틀 ────────────────────────────────────────────────
	timeStr := fmt.Sprintf("[%s / %s]", fmtElapsed(elapsed), fmtElapsed(cfg.Duration))

	// ━━━ 대시보드 출력 (정확히 dashboardLines = 18줄) ━━━━━━━━━━━━

	// 1
	fmt.Println(top)
	// 2  타이틀
	fmt.Println(brow(fmt.Sprintf("  ⚡ Load Test Monitor              %s", timeStr)))
	// 3
	fmt.Println(mid)
	// 4  진행률
	fmt.Println(brow(fmt.Sprintf("  Progress  %s  %5.1f%%", bar, progress*100)))
	// 5
	fmt.Println(mid)
	// 6  목표 RPS
	fmt.Println(brow(fmt.Sprintf("  Target  :  %9.2f req/s   (%s req/h)",
		targetRPS, commaInt(cfg.GetRPH()))))
	// 7  실제 RPS
	fmt.Println(brow(fmt.Sprintf("  Actual  :  %9.2f req/s%s",
		recentRPS, rpsStatus)))
	// 8
	fmt.Println(mid)
	// 9  요청 수 — 총계 / 성공
	fmt.Println(brow(fmt.Sprintf("  Total   : %10s    Success : %10s  (%5.2f%%)",
		commaInt(total), commaInt(success), successRate)))
	// 10 요청 수 — 실패 / 드롭
	fmt.Println(brow(fmt.Sprintf("  Failed  : %10s    Dropped : %10s",
		commaInt(failed), commaInt(dropped))))
	// 11
	fmt.Println(mid)
	// 12 레이턴시 line 1
	fmt.Println(brow(latLine1))
	// 13 레이턴시 line 2
	fmt.Println(brow(latLine2))
	// 14
	fmt.Println(mid)
	// 15 RPS 트렌드 스파크라인
	fmt.Println(brow(fmt.Sprintf("  RPS  %s  (%ds)", spark, len(history))))
	// 16
	fmt.Println(mid)
	// 17 HTTP 상태 코드
	fmt.Println(brow(fmt.Sprintf("  %s", scStr)))
	// 18
	fmt.Println(bot)
}

// ── 박스 그리기 헬퍼 ──────────────────────────────────────────────────────────

// boxRow pads/truncates content to exactly `width` runes and wraps with ║.
func boxRow(content string, width int) string {
	runes := []rune(content)
	if len(runes) > width {
		runes = runes[:width]
	}
	for len(runes) < width {
		runes = append(runes, ' ')
	}
	return "║" + string(runes) + "║"
}

// ── 스파크라인 ────────────────────────────────────────────────────────────────

// buildSparkline converts float64 values → single-line Unicode bar chart.
// Always returns exactly `width` characters (left-padded with spaces for missing data).
//
//	▁▂▃▄▅▆▇█  ← 8 levels
func buildSparkline(values []float64, width int, maxVal float64) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	if maxVal <= 0 {
		maxVal = 1
	}
	data := values
	if len(data) > width {
		data = data[len(data)-width:]
	}

	var sb strings.Builder
	// 데이터가 부족한 경우 왼쪽을 공백으로 채움
	for i := len(data); i < width; i++ {
		sb.WriteRune(' ')
	}
	for _, v := range data {
		ratio := v / maxVal
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		idx := int(ratio*float64(len(blocks)-1) + 0.5)
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		sb.WriteRune(blocks[idx])
	}
	return sb.String()
}

// ── 최종 리포트 ────────────────────────────────────────────────────────────────

// PrintFinal prints the complete summary after the test finishes.
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

	sep := strings.Repeat("─", 60)
	dbl := strings.Repeat("═", 60)

	fmt.Println("\n" + dbl)
	fmt.Println("  LOAD TEST COMPLETE")
	fmt.Println(dbl)

	// ── 개요 ──────────────────────────────────────────────────
	fmt.Printf("\n  %-24s %s\n", "Duration:", fmtElapsed(elapsed))
	fmt.Printf("  %-24s %s req/h  (~%.2f req/s)\n", "Target Rate:", commaInt(r.cfg.GetRPH()), targetRPS)
	fmt.Printf("  %-24s %.2f req/s\n", "Actual Rate:", rps)

	// ── 요청 수 ───────────────────────────────────────────────
	fmt.Printf("\n%s\n  REQUESTS\n%s\n", sep, sep)
	fmt.Printf("  %-24s %s\n", "Total:", commaInt(total))
	fmt.Printf("  %-24s %s  (%.2f%%)\n", "Success:", commaInt(success), successRate)
	fmt.Printf("  %-24s %s  (%.2f%%)\n", "Failed:", commaInt(failed), 100-successRate)
	if dropped > 0 {
		fmt.Printf("  %-24s %s  (worker pool 포화)\n", "Dropped:", commaInt(dropped))
	}

	// ── HTTP 상태 코드 ─────────────────────────────────────────
	if len(sc) > 0 {
		fmt.Printf("\n%s\n  HTTP STATUS CODES\n%s\n", sep, sep)
		codes := make([]int, 0, len(sc))
		for c := range sc {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		for _, code := range codes {
			count := sc[code]
			pctV := float64(count) / float64(total) * 100
			fmt.Printf("  %-24s %s  (%.1f%%)\n",
				fmt.Sprintf("HTTP %d:", code), commaInt(count), pctV)
		}
	}

	// ── 레이턴시 ──────────────────────────────────────────────
	if pct.Count > 0 {
		fmt.Printf("\n%s\n  LATENCY  (milliseconds)\n%s\n", sep, sep)
		fmt.Printf("  %-24s %10.3f ms\n", "Min:", pct.Min)
		fmt.Printf("  %-24s %10.3f ms\n", "Mean:", pct.Mean)
		fmt.Printf("  %-24s %10.3f ms\n", "StdDev:", pct.StdDev)
		fmt.Println()
		fmt.Printf("  %-24s %10.3f ms\n", "P50 (median):", pct.P50)
		fmt.Printf("  %-24s %10.3f ms\n", "P75:", pct.P75)
		fmt.Printf("  %-24s %10.3f ms\n", "P90:", pct.P90)
		fmt.Printf("  %-24s %10.3f ms\n", "P95:", pct.P95)
		fmt.Printf("  %-24s %10.3f ms\n", "P99:", pct.P99)
		fmt.Printf("  %-24s %10.3f ms\n", "P99.9:", pct.P999)
		fmt.Println()
		fmt.Printf("  %-24s %10.3f ms\n", "Max:", pct.Max)
		fmt.Printf("  %-24s %10d\n", "Samples:", pct.Count)
	}

	// ── 에러 ──────────────────────────────────────────────────
	if len(errs) > 0 {
		fmt.Printf("\n%s\n  ERRORS\n%s\n", sep, sep)
		type errRow struct {
			name  string
			count int64
		}
		rows := make([]errRow, 0, len(errs))
		for k, v := range errs {
			rows = append(rows, errRow{k, v})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
		for _, e := range rows {
			pctV := float64(e.count) / float64(total) * 100
			fmt.Printf("  %-24s %s  (%.1f%%)\n", e.name+":", commaInt(e.count), pctV)
		}
	}

	fmt.Println("\n" + dbl + "\n")
}

// ── 유틸 ──────────────────────────────────────────────────────────────────────

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

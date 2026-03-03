package main

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// Worker executes HTTP requests and records results into Metrics.
type Worker struct {
	cfg     *Config
	client  *http.Client
	metrics *Metrics
}

func NewWorker(cfg *Config, client *http.Client, metrics *Metrics) *Worker {
	return &Worker{cfg: cfg, client: client, metrics: metrics}
}

// Do performs one HTTP request and records the outcome.
func (w *Worker) Do(ctx context.Context) {
	req, err := w.buildRequest(ctx)
	if err != nil {
		w.metrics.RecordRequest(0, 0, "build_error")
		return
	}

	start := time.Now()
	resp, err := w.client.Do(req)
	latency := time.Since(start)

	if err != nil {
		w.metrics.RecordRequest(0, latency, classifyError(err.Error()))
		return
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused.
	io.Copy(io.Discard, resp.Body)

	w.metrics.RecordRequest(resp.StatusCode, latency, "")
}

func (w *Worker) buildRequest(ctx context.Context) (*http.Request, error) {
	var body io.Reader
	if w.cfg.Body != "" {
		// 매 요청마다 플레이스홀더를 새 랜덤 값으로 교체
		body = strings.NewReader(expandPlaceholders(w.cfg.Body))
	}

	req, err := http.NewRequestWithContext(ctx, w.cfg.Method, w.cfg.URL, body)
	if err != nil {
		return nil, err
	}

	for k, v := range w.cfg.Headers {
		req.Header.Set(k, v)
	}
	if w.cfg.ContentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", w.cfg.ContentType)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "LoadTest/1.0")
	}

	return req, nil
}

// ─── 플레이스홀더 치환 ────────────────────────────────────────────────────────

// expandPlaceholders는 body 문자열 안의 템플릿 토큰을 랜덤 값으로 교체합니다.
//
//	{{uuid}}      → UUID v4  (예: 7c5a3c6a-758f-4bc5-9bdf-3e573a0ad729)
//	{{requestid}} → 12자리 랜덤 숫자 문자열 (예: 999999999999)
func expandPlaceholders(s string) string {
	if !strings.Contains(s, "{{") {
		return s // 플레이스홀더 없으면 그대로 반환 (성능 최적화)
	}
	s = strings.ReplaceAll(s, "{{uuid}}", newUUID())
	s = strings.ReplaceAll(s, "{{requestid}}", newRequestID())
	return s
}

// newUUID는 랜덤 UUID v4를 생성합니다.
func newUUID() string {
	a := rand.Uint32()
	b := uint16(rand.Uint32())
	c := uint16((rand.Uint32()&0x0fff) | 0x4000) // version 4
	d := uint16((rand.Uint32()&0x3fff) | 0x8000) // variant RFC 4122
	e := (uint64(rand.Uint32()) << 16) | uint64(uint16(rand.Uint32()))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", a, b, c, d, e)
}

// newRequestID는 12자리 랜덤 숫자 문자열을 생성합니다.
func newRequestID() string {
	return fmt.Sprintf("%012d", rand.Int63n(1_000_000_000_000))
}

// ─── 에러 분류 ────────────────────────────────────────────────────────────────

// classifyError maps raw error strings to short, stable category keys.
func classifyError(s string) string {
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection_refused"
	case strings.Contains(s, "no such host"):
		return "dns_error"
	case strings.Contains(s, "connection reset"):
		return "connection_reset"
	case strings.Contains(s, "EOF"):
		return "unexpected_eof"
	case strings.Contains(s, "timeout") || strings.Contains(s, "Timeout") ||
		strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "context canceled"):
		return "canceled"
	case strings.Contains(s, "too many open files"):
		return "too_many_open_files"
	default:
		return "request_error"
	}
}

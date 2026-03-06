package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// ── InfluxDB 연결 설정 ────────────────────────────────────────────

var influxBaseURL string

func getInfluxBaseURL() string {
	if influxBaseURL != "" {
		return influxBaseURL
	}
	u := os.Getenv("INFLUX_URL")
	if u == "" {
		u = "http://localhost:8086"
	}
	influxBaseURL = u
	return u
}

// ── 응답 구조체 ───────────────────────────────────────────────────

// APIMetric은 하나의 API 경로에 대한 실시간 지표입니다.
type APIMetric struct {
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	SLOThresholdMs int     `json:"sloThresholdMs"` // PDF 기준 SLO (ms)
	TotalReqs      int64   `json:"totalReqs"`      // 최근 5분 전체 요청수
	SuccessReqs    int64   `json:"successReqs"`    // 최근 5분 성공 요청수
	WithinSLOReqs  int64   `json:"withinSLOReqs"`  // 최근 5분 SLO 이내 요청수 (성공 기준으로 보정)
	Throughput     float64 `json:"throughput"`     // 처리율 = 성공/전체 × 100, -1=N/A
	Efficiency     float64 `json:"efficiency"`     // 효율성 = SLO이내(성공한것만)/전체 × 100, -1=N/A
	P90Ms          float64 `json:"p90Ms"`          // -1 = 데이터 없음
}

// MetricsPayload는 /api/metrics 엔드포인트의 응답입니다.
type MetricsPayload struct {
	Available        bool        `json:"available"`        // InfluxDB 연결 여부
	TotalReqs        int64       `json:"totalReqs"`        // 최근 1시간 총 요청 수
	CurrentRPS       float64     `json:"currentRps"`       // 최근 30초 평균 req/s
	GlobalThroughput float64     `json:"globalThroughput"` // 전체 처리율 %, -1=N/A
	GlobalEfficiency float64     `json:"globalEfficiency"` // 전체 효율성 %, -1=N/A
	APIs             []APIMetric `json:"apis"`
	UpdatedAt        string      `json:"updatedAt"`
}

// ── InfluxDB 쿼리 헬퍼 ───────────────────────────────────────────

// influxQuery는 InfluxDB 1.x HTTP API로 InfluxQL을 실행하고 결과를 반환합니다.
func influxQuery(q string) (map[string]any, error) {
	apiURL := getInfluxBaseURL() + "/query?db=k6&q=" + url.QueryEscape(q)
	c := &http.Client{Timeout: 3 * time.Second}

	resp, err := c.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// firstFloat은 InfluxDB JSON 응답에서 첫 번째 숫자 값을 추출합니다.
// 데이터가 없으면 (0, false)를 반환합니다.
func firstFloat(m map[string]any) (float64, bool) {
	results, _ := m["results"].([]any)
	if len(results) == 0 {
		return 0, false
	}
	r, _ := results[0].(map[string]any)
	series, _ := r["series"].([]any)
	if len(series) == 0 {
		return 0, false
	}
	s, _ := series[0].(map[string]any)
	vals, _ := s["values"].([]any)
	if len(vals) == 0 {
		return 0, false
	}
	row, _ := vals[0].([]any)
	if len(row) < 2 || row[1] == nil {
		return 0, false
	}
	f, ok := row[1].(float64)
	return f, ok
}

// ── PDF SLO 정의 ─────────────────────────────────────────────────

// sloTargets는 PDF 명세에 정의된 API별 SLO 목표값입니다.
//
//	user/product : p95 ≤ 200ms
//	stress       : p95 ≤ 1000ms
var sloTargets = []struct {
	name    string
	urlFrag string // target_url 태그에서 매칭할 URL 조각 (InfluxQL 정규식용: / → \/)
	path    string // 표시용 경로
	sloMs   int    // SLO 임계값 (ms)
}{
	// InfluxQL 정규식에서 '/'는 구분자이므로 '\/'로 이스케이프 필요
	{"user",    "v1\\/user",    "/v1/user",    200},
	{"product", "v1\\/product", "/v1/product", 200},
	{"stress",  "v1\\/stress",  "/v1/stress",  1000},
}

// ── 핸들러 ───────────────────────────────────────────────────────

// getMetrics는 GET /api/metrics를 처리합니다.
func getMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	payload := MetricsPayload{
		UpdatedAt:        time.Now().Format("15:04:05"),
		GlobalThroughput: -1,
		GlobalEfficiency: -1,
	}

	// InfluxDB 연결 확인
	if _, err := influxQuery("SHOW DATABASES"); err != nil {
		payload.Available = false
		json.NewEncoder(w).Encode(payload)
		return
	}
	payload.Available = true

	// ── 전체 요청수 (최근 1시간) ───────────────────────────────
	if res, err := influxQuery(
		`SELECT COUNT("value") FROM "http_req_duration" WHERE time > now() - 1h`,
	); err == nil {
		if v, ok := firstFloat(res); ok {
			payload.TotalReqs = int64(v)
		}
	}

	// ── 현재 RPS (최근 30초 요청수 ÷ 30) ──────────────────────
	if res, err := influxQuery(
		`SELECT COUNT("value") FROM "http_req_duration" WHERE time > now() - 30s`,
	); err == nil {
		if v, ok := firstFloat(res); ok {
			payload.CurrentRPS = v / 30.0
		}
	}

	// ── 전체 처리율 (최근 5분) ────────────────────────────────
	// http_req_failed: 0=성공, 1=실패 → MEAN = 실패율 → 1-실패율 = 처리율
	if res, err := influxQuery(
		`SELECT MEAN("value") FROM "http_req_failed" WHERE time > now() - 5m`,
	); err == nil {
		if v, ok := firstFloat(res); ok {
			payload.GlobalThroughput = (1 - v) * 100
		}
	}

	// ── API별 지표 ─────────────────────────────────────────────
	var successAll, withinAll int64
	for _, t := range sloTargets {
		m := APIMetric{
			Name:           t.name,
			Path:           t.path,
			SLOThresholdMs: t.sloMs,
			Throughput:     -1,
			Efficiency:     -1,
			P90Ms:          -1,
		}

		// 전체 요청수 (최근 5분): 성공/실패 모두 포함
		if res, err := influxQuery(fmt.Sprintf(
			`SELECT COUNT("value") FROM "http_req_duration" WHERE "target_url" =~ /%s/ AND time > now() - 5m`,
			t.urlFrag,
		)); err == nil {
			if v, ok := firstFloat(res); ok {
				m.TotalReqs = int64(v)
			}
		}

		// 성공 요청수 (최근 5분): status =~ /^2/ → 2xx 응답만 카운트
		// MEAN(http_req_failed) 역산 방식은 실패 요청이 빠른 경우 오차 발생
		if res, err := influxQuery(fmt.Sprintf(
			`SELECT COUNT("value") FROM "http_req_duration" WHERE "target_url" =~ /%s/ AND "status" =~ /^2/ AND time > now() - 5m`,
			t.urlFrag,
		)); err == nil {
			if v, ok := firstFloat(res); ok {
				m.SuccessReqs = int64(v)
				successAll += m.SuccessReqs
				if m.TotalReqs > 0 {
					m.Throughput = float64(m.SuccessReqs) / float64(m.TotalReqs) * 100
				}
			}
		}

		// SLO 이내 성공 요청수 (최근 5분): 2xx 응답 AND 응답시간 ≤ SLO threshold
		// status =~ /^2/ 필터로 실패 요청(4xx/5xx)이 빠르게 응답해도 제외
		if res, err := influxQuery(fmt.Sprintf(
			`SELECT COUNT("value") FROM "http_req_duration" WHERE "target_url" =~ /%s/ AND "status" =~ /^2/ AND "value" <= %d AND time > now() - 5m`,
			t.urlFrag, t.sloMs,
		)); err == nil {
			if v, ok := firstFloat(res); ok {
				m.WithinSLOReqs = int64(v)
				withinAll += m.WithinSLOReqs
				// 효율성 = 성공한 요청 중 SLO 이내 비율
				if m.SuccessReqs > 0 {
					m.Efficiency = float64(m.WithinSLOReqs) / float64(m.SuccessReqs) * 100
				}
			}
		}

		// p90 응답시간 (최근 5분): 성공 요청(2xx)만 기준
		if res, err := influxQuery(fmt.Sprintf(
			`SELECT PERCENTILE("value",90) FROM "http_req_duration" WHERE "target_url" =~ /%s/ AND "status" =~ /^2/ AND time > now() - 5m`,
			t.urlFrag,
		)); err == nil {
			if v, ok := firstFloat(res); ok {
				m.P90Ms = v
			}
		}

		payload.APIs = append(payload.APIs, m)
	}

	// ── 전체 효율성 = 전체 SLO이내 / 전체 성공 요청수 ──────────
	// 효율성: 성공한 요청 중 SLO 기준을 만족한 비율
	if successAll > 0 {
		payload.GlobalEfficiency = float64(withinAll) / float64(successAll) * 100
	}

	json.NewEncoder(w).Encode(payload)
}

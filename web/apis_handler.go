package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// apisFilePath는 apis.json의 절대 경로를 반환합니다.
func apisFilePath() string {
	return filepath.Join(projectRoot, "apis.json")
}

// generateAPIsFromEndpoint는 엔드포인트 URL 하나로 PDF 명세의 고정 API 목록을 생성합니다.
//
// 생성되는 API (총 5개):
//  user 그룹   : POST /v1/user    → 201  (weight 3)
//               GET  /v1/user    → 200  (weight 3)
//  product 그룹: POST /v1/product → 201  (weight 3)
//               GET  /v1/product → 200  (weight 3)
//  stress      : POST /v1/stress  → 201  (weight 2)
func generateAPIsFromEndpoint(endpoint string) []APIEntry {
	base := strings.TrimRight(endpoint, "/")

	return []APIEntry{
		{
			Name:   "user 생성",
			URL:    base + "/v1/user",
			Method: "POST",
			Weight: 3,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: map[string]any{
				"requestid":      "{{requestid}}",
				"uuid":           "{{uuid}}",
				"username":       "dbdump{{random6}}",
				"email":          "dbdump{{random6}}@example.org",
				"status_message": "I'm happy",
			},
			ExpectedStatus: 201,
		},
		{
			Name:   "user 조회",
			URL:    base + "/v1/user",
			Method: "GET",
			Weight: 3,
			Params: map[string]any{
				"email":     "dbdump{{random6}}@example.org",
				"requestid": "{{requestid}}",
				"uuid":      "{{uuid}}",
			},
			ExpectedStatus: 200,
		},
		{
			Name:   "product 생성",
			URL:    base + "/v1/product",
			Method: "POST",
			Weight: 3,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: map[string]any{
				"requestid": "{{requestid}}",
				"uuid":      "{{uuid}}",
				"id":        "dbdump{{random6}}",
				"name":      "dbdump{{random6}}",
				"price":     "{{randomInt}}",
			},
			ExpectedStatus: 201,
		},
		{
			Name:   "product 조회",
			URL:    base + "/v1/product",
			Method: "GET",
			Weight: 3,
			Params: map[string]any{
				"id":        "dbdump{{random6}}",
				"requestid": "{{requestid}}",
				"uuid":      "{{uuid}}",
			},
			ExpectedStatus: 200,
		},
		{
			Name:   "stress",
			URL:    base + "/v1/stress",
			Method: "POST",
			Weight: 2,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: map[string]any{
				"requestid": "{{requestid}}",
				"uuid":      "{{uuid}}",
				"length":    256,
			},
			ExpectedStatus: 201,
		},
	}
}

// extractEndpoint는 apis.json의 첫 엔트리 URL에서 base URL을 추출합니다.
// e.g. "https://alb.example.com/v1/user" → "https://alb.example.com"
func extractEndpoint(rawURL string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(rawURL, prefix) {
			rest := rawURL[len(prefix):]
			if idx := strings.Index(rest, "/"); idx != -1 {
				return prefix + rest[:idx]
			}
			return rawURL
		}
	}
	return rawURL
}

// ─── 핸들러 ─────────────────────────────────────────────────────────────────

// getEndpoint는 GET /api/endpoint를 처리합니다.
// 현재 apis.json에 설정된 엔드포인트 URL을 반환합니다.
func getEndpoint(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(apisFilePath())
	endpoint := ""
	if err == nil && len(data) > 0 {
		var entries []APIEntry
		if json.Unmarshal(data, &entries) == nil && len(entries) > 0 {
			endpoint = extractEndpoint(entries[0].URL)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"endpoint": endpoint})
}

// saveEndpoint는 POST /api/endpoint를 처리합니다.
// {"endpoint":"https://..."} 를 받아 고정 API 목록을 apis.json에 저장합니다.
func saveEndpoint(w http.ResponseWriter, r *http.Request) {
	if globalStore.GetTestStatus() == StatusRunning {
		w.Header().Set("Content-Type", "application/json")
		http.Error(w, `{"error":"테스트 실행 중에는 엔드포인트를 변경할 수 없습니다"}`, http.StatusConflict)
		return
	}

	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Endpoint) == "" {
		http.Error(w, "endpoint는 필수입니다", http.StatusBadRequest)
		return
	}

	// 고정 API 목록 생성
	entries := generateAPIsFromEndpoint(strings.TrimSpace(req.Endpoint))

	// 원자적 쓰기: 임시 파일 후 rename
	tmpPath := apisFilePath() + ".tmp"
	data, _ := json.MarshalIndent(entries, "", "  ")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		http.Error(w, "Write error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, apisFilePath()); err != nil {
		http.Error(w, "Rename error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"endpoint": req.Endpoint,
		"apis":     entries,
	})
}

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// apisFilePath는 apis.json의 절대 경로를 반환합니다.
func apisFilePath() string {
	return filepath.Join(projectRoot, "apis.json")
}

// getAPIs는 GET /api/apis를 처리합니다. apis.json을 읽어 JSON으로 반환합니다.
func getAPIs(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(apisFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			// 파일 없으면 빈 배열 반환
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
			return
		}
		http.Error(w, "Failed to read apis.json: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// JSON 유효성 검증
	var entries []APIEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		http.Error(w, "apis.json parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// saveAPIs는 POST /api/apis를 처리합니다. 요청 바디의 []APIEntry를 apis.json에 저장합니다.
func saveAPIs(w http.ResponseWriter, r *http.Request) {
	// 테스트 실행 중에는 저장 불가
	if globalStore.GetTestStatus() == StatusRunning {
		http.Error(w, `{"error":"테스트 실행 중에는 API 설정을 변경할 수 없습니다"}`, http.StatusConflict)
		return
	}

	var entries []APIEntry
	if err := json.NewDecoder(r.Body).Decode(&entries); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// 기본값 보완 및 검증
	for i := range entries {
		if entries[i].Method == "" {
			entries[i].Method = "GET"
		}
		if entries[i].ExpectedStatus == 0 {
			entries[i].ExpectedStatus = 200
		}
		if entries[i].Weight == 0 {
			entries[i].Weight = 1
		}
		if entries[i].URL == "" {
			http.Error(w, "URL은 필수입니다", http.StatusBadRequest)
			return
		}
	}

	// 원자적 쓰기: 임시 파일 후 rename
	tmpPath := apisFilePath() + ".tmp"
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		http.Error(w, "JSON marshal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		http.Error(w, "Write error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, apisFilePath()); err != nil {
		http.Error(w, "Rename error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(entries)
}

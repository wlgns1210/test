package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
)

// getTestConfig는 GET /api/test/config를 처리합니다.
func getTestConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(globalStore.GetTestConfig())
}

// saveTestConfig는 POST /api/test/config를 처리합니다.
func saveTestConfig(w http.ResponseWriter, r *http.Request) {
	var cfg TestConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	// 기본값 보완
	if cfg.Rate == 0 {
		cfg.Rate = 56
	}
	if cfg.Pattern == "" {
		cfg.Pattern = "rampup"
	}
	globalStore.SetTestConfig(cfg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

// getTestStatus는 GET /api/test/status를 처리합니다.
func getTestStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": string(globalStore.GetTestStatus()),
	})
}

// startTest는 POST /api/test/start를 처리합니다.
func startTest(w http.ResponseWriter, r *http.Request) {
	if globalStore.GetTestStatus() == StatusRunning {
		http.Error(w, `{"error":"이미 테스트가 실행 중입니다"}`, http.StatusConflict)
		return
	}

	cfg := globalStore.GetTestConfig()
	globalStore.ClearLogs()

	// 환경변수 빌드
	env := buildEnv(cfg)

	// 컨텍스트로 프로세스 제어
	ctx, cancel := context.WithCancel(context.Background())

	// run.sh를 bash로 실행
	cmd := exec.CommandContext(ctx, "bash", "./run.sh")
	cmd.Dir = projectRoot
	cmd.Env = env

	// stdout + stderr 통합 파이프
	// pw: cmd이 쓰는 쪽 / pr: scanner가 읽는 쪽
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		cancel()
		pr.Close()
		pw.Close()
		http.Error(w, `{"error":"k6 시작 실패: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	globalStore.mu.Lock()
	globalStore.k6Cmd = cmd
	globalStore.k6Cancel = cancel
	globalStore.testStatus = StatusRunning
	globalStore.mu.Unlock()

	// 프로세스 종료 대기 goroutine: cmd 종료 시 pw 닫아 scanner에 EOF 신호
	go func() {
		cmd.Wait()
		pw.Close() // EOF 신호 → scanner goroutine이 종료됨
		globalStore.mu.Lock()
		globalStore.testStatus = StatusStopped
		globalStore.k6Cmd = nil
		globalStore.k6Cancel = nil
		globalStore.mu.Unlock()
		globalStore.AddLogLine("▶ 테스트가 종료되었습니다.")
		globalStore.BroadcastDone()
	}()

	// 로그 수집 goroutine: pw.Close() 시 scanner.Scan()이 false 반환
	go func() {
		defer pr.Close()
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 큰 라인 처리
		for scanner.Scan() {
			globalStore.AddLogLine(scanner.Text())
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// stopTest는 POST /api/test/stop을 처리합니다.
func stopTest(w http.ResponseWriter, r *http.Request) {
	globalStore.mu.Lock()
	status := globalStore.testStatus
	cmd := globalStore.k6Cmd
	cancel := globalStore.k6Cancel
	globalStore.mu.Unlock()

	if status != StatusRunning || cmd == nil {
		http.Error(w, `{"error":"실행 중인 테스트가 없습니다"}`, http.StatusConflict)
		return
	}

	// Windows: taskkill로 프로세스 트리 종료
	if cmd.Process != nil {
		pid := cmd.Process.Pid
		// taskkill /F /T: 자식 프로세스 포함 강제 종료
		killCmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
		killCmd.Run() // 에러 무시 (bash가 이미 종료됐을 수 있음)
	}

	if cancel != nil {
		cancel()
	}

	globalStore.mu.Lock()
	globalStore.testStatus = StatusStopped
	globalStore.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// streamLogs는 GET /api/test/logs SSE 엔드포인트입니다.
func streamLogs(w http.ResponseWriter, r *http.Request) {
	// SSE 헤더 설정
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// 새 클라이언트 채널 등록 + 링버퍼 기존 로그 수신
	ch := make(chan string, 256)
	history := globalStore.RegisterLogClient(ch)
	defer globalStore.UnregisterLogClient(ch)

	// 기존 로그 먼저 전송
	for _, line := range history {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	// 실시간 스트리밍
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			if line == "__DONE__" {
				fmt.Fprintf(w, "data: __DONE__\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// buildEnv는 TestConfig를 run.sh 환경변수 슬라이스로 변환합니다.
// os.Environ()을 기반으로 테스트 전용 변수를 덮어씁니다.
func buildEnv(cfg TestConfig) []string {
	// 시스템 환경변수 전체를 기반으로 시작
	env := os.Environ()
	// 테스트 전용 변수 추가 (같은 키가 두 번 있으면 bash는 마지막 값을 사용)
	env = append(env,
		fmt.Sprintf("RATE=%d", cfg.Rate),
		fmt.Sprintf("PATTERN=%s", cfg.Pattern),
		fmt.Sprintf("ABNORMAL_RATE=%d", cfg.AbnormalRate),
		fmt.Sprintf("RAMPUP=%s", ifEmpty(cfg.Rampup, "2m")),
		fmt.Sprintf("SUSTAIN=%s", ifEmpty(cfg.Sustain, "3m")),
		fmt.Sprintf("RAMPDOWN=%s", ifEmpty(cfg.Rampdown, "1m")),
		fmt.Sprintf("STEPS=%d", ifZero(cfg.Steps, 4)),
		fmt.Sprintf("STEP_DURATION=%s", ifEmpty(cfg.StepDuration, "1m")),
		fmt.Sprintf("BASELINE=%d", ifZero(cfg.Baseline, 5)),
		fmt.Sprintf("WARMUP=%s", ifEmpty(cfg.Warmup, "30s")),
		fmt.Sprintf("SPIKE_DURATION=%s", ifEmpty(cfg.SpikeDuration, "3m")),
		fmt.Sprintf("COOLDOWN=%s", ifEmpty(cfg.Cooldown, "30s")),
	)
	return env
}

func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func ifZero(n, fallback int) int {
	if n == 0 {
		return fallback
	}
	return n
}

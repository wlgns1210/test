package main

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// TestStatus는 k6 테스트 프로세스의 현재 상태입니다.
type TestStatus string

const (
	StatusIdle    TestStatus = "idle"
	StatusRunning TestStatus = "running"
	StatusStopped TestStatus = "stopped"
)

// Session은 로그인된 브라우저 세션을 나타냅니다.
type Session struct {
	Username  string
	Role      string // "user" | "admin"
	ExpiresAt time.Time
}

// APIEntry는 apis.json의 단일 API 엔트리입니다.
type APIEntry struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Weight         int               `json:"weight,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           map[string]any    `json:"body,omitempty"`
	Params         map[string]any    `json:"params,omitempty"`
	ExpectedStatus int               `json:"expectedStatus"`
}

// TestConfig는 k6 실행 파라미터입니다 (run.sh 환경변수에 매핑).
type TestConfig struct {
	Rate         int    `json:"rate"`
	Pattern      string `json:"pattern"`
	AbnormalRate int    `json:"abnormalRate"`
	// rampup
	Rampup   string `json:"rampup"`
	Sustain  string `json:"sustain"`
	Rampdown string `json:"rampdown"`
	// step
	Steps        int    `json:"steps"`
	StepDuration string `json:"stepDuration"`
	// spike
	Baseline      int    `json:"baseline"`
	Warmup        string `json:"warmup"`
	SpikeDuration string `json:"spikeDuration"`
	Cooldown      string `json:"cooldown"`
}

// AppStore는 모든 핸들러가 공유하는 인메모리 상태입니다.
type AppStore struct {
	mu sync.RWMutex

	// 세션 스토어: sessionID → Session
	sessions sync.Map

	// k6 프로세스 상태
	testStatus TestStatus
	k6Cmd      *exec.Cmd
	k6Cancel   context.CancelFunc

	// 테스트 설정
	testConfig TestConfig

	// SSE 로그 클라이언트: chan string → 등록 여부
	logClients map[chan string]struct{}

	// 최근 로그 링 버퍼 (최대 500줄)
	logRing []string
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// NewAppStore는 기본값으로 초기화된 AppStore를 반환합니다.
func NewAppStore() *AppStore {
	return &AppStore{
		testStatus: StatusIdle,
		testConfig: TestConfig{
			Rate:          56,
			Pattern:       "rampup",
			AbnormalRate:  0,
			Rampup:        "2m",
			Sustain:       "3m",
			Rampdown:      "1m",
			Steps:         4,
			StepDuration:  "1m",
			Baseline:      5,
			Warmup:        "30s",
			SpikeDuration: "3m",
			Cooldown:      "30s",
		},
		logClients: make(map[chan string]struct{}),
		logRing:    make([]string, 0, 500),
	}
}

// GetTestStatus는 현재 테스트 상태를 반환합니다 (읽기 잠금).
func (s *AppStore) GetTestStatus() TestStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.testStatus
}

// SetTestStatus는 테스트 상태를 설정합니다.
func (s *AppStore) SetTestStatus(status TestStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.testStatus = status
}

// GetTestConfig는 현재 테스트 설정을 반환합니다.
func (s *AppStore) GetTestConfig() TestConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.testConfig
}

// SetTestConfig는 테스트 설정을 업데이트합니다.
func (s *AppStore) SetTestConfig(cfg TestConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.testConfig = cfg
}

// AddLogLine은 로그 라인을 링 버퍼에 추가하고 모든 SSE 클라이언트에 브로드캐스트합니다.
func (s *AppStore) AddLogLine(line string) {
	// ANSI 이스케이프 코드 제거
	line = ansiRe.ReplaceAllString(line, "")
	// \r 제거: k6 progress 표시는 \r로 덮어씀 → 마지막 세그먼트만 사용
	if idx := strings.LastIndex(line, "\r"); idx >= 0 {
		line = line[idx+1:]
	}
	line = strings.TrimRight(line, " \t")
	if line == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 링 버퍼: 최대 500줄
	if len(s.logRing) >= 500 {
		s.logRing = s.logRing[1:]
	}
	s.logRing = append(s.logRing, line)

	// 모든 SSE 클라이언트에 비블로킹 전송
	for ch := range s.logClients {
		select {
		case ch <- line:
		default:
			// 버퍼 가득 찬 클라이언트는 스킵
		}
	}
}

// RegisterLogClient는 새 SSE 클라이언트 채널을 등록하고 기존 로그 버퍼를 반환합니다.
func (s *AppStore) RegisterLogClient(ch chan string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logClients[ch] = struct{}{}
	// 현재 링 버퍼 복사 반환
	buf := make([]string, len(s.logRing))
	copy(buf, s.logRing)
	return buf
}

// UnregisterLogClient는 SSE 클라이언트 채널을 제거합니다.
func (s *AppStore) UnregisterLogClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.logClients, ch)
}

// BroadcastDone은 테스트 종료를 모든 클라이언트에 알립니다.
func (s *AppStore) BroadcastDone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.logClients {
		select {
		case ch <- "__DONE__":
		default:
		}
	}
}

// ClearLogs는 로그 버퍼를 초기화합니다.
func (s *AppStore) ClearLogs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logRing = s.logRing[:0]
}

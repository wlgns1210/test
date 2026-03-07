package main

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed templates
var templateFS embed.FS

var (
	globalStore *AppStore
	projectRoot string
	grafanaURL  string
	templates   *template.Template
)

// sessionKey는 컨텍스트 키 타입입니다.
type contextKey string

const sessionContextKey contextKey = "session"

func withSession(ctx context.Context, sess *Session) context.Context {
	return context.WithValue(ctx, sessionContextKey, sess)
}

func sessionFromContext(ctx context.Context) *Session {
	v := ctx.Value(sessionContextKey)
	if v == nil {
		return nil
	}
	return v.(*Session)
}

// detectPublicIP는 EC2 IMDSv2를 통해 퍼블릭 IP를 감지합니다.
// EC2가 아니거나 실패 시 빈 문자열을 반환합니다.
func detectPublicIP() string {
	client := &http.Client{Timeout: 2 * time.Second}

	// IMDSv2: 토큰 발급
	tokenReq, _ := http.NewRequest("PUT", "http://169.254.169.254/latest/api/token", nil)
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	tokenResp, err := client.Do(tokenReq)
	if err == nil {
		defer tokenResp.Body.Close()
		token, _ := io.ReadAll(tokenResp.Body)

		ipReq, _ := http.NewRequest("GET", "http://169.254.169.254/latest/meta-data/public-ipv4", nil)
		ipReq.Header.Set("X-aws-ec2-metadata-token", strings.TrimSpace(string(token)))
		ipResp, err := client.Do(ipReq)
		if err == nil {
			defer ipResp.Body.Close()
			ip, _ := io.ReadAll(ipResp.Body)
			if v := strings.TrimSpace(string(ip)); v != "" {
				return v
			}
		}
	}

	// IMDSv1 폴백
	resp, err := client.Get("http://169.254.169.254/latest/meta-data/public-ipv4")
	if err == nil {
		defer resp.Body.Close()
		ip, _ := io.ReadAll(resp.Body)
		if v := strings.TrimSpace(string(ip)); v != "" {
			return v
		}
	}

	return ""
}

// findProjectRoot는 run.sh가 존재하는 디렉토리를 탐색하여 프로젝트 루트를 반환합니다.
// go run 실행 시 exe 경로가 임시 디렉토리가 되므로, cwd 기반으로 탐색합니다.
func findProjectRoot() string {
	// 1순위: 환경변수
	if dir := os.Getenv("LOADTEST_PROJECT_DIR"); dir != "" {
		return dir
	}

	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}

	// 2순위: cwd 및 상위 디렉토리에서 run.sh 탐색
	candidates := []string{cwd, filepath.Dir(cwd)}

	// exe 경로도 후보에 추가 (컴파일된 바이너리 실행 시)
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, exeDir, filepath.Dir(exeDir))
	}

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "run.sh")); err == nil {
			return dir
		}
	}

	// 최후 수단: cwd 상위 디렉토리
	return filepath.Dir(cwd)
}

func main() {
	globalStore = NewAppStore()

	// 프로젝트 루트 결정 (run.sh 위치 기준)
	projectRoot = findProjectRoot()
	log.Printf("Project root: %s", projectRoot)

	// Grafana URL (환경변수 GRAFANA_URL 우선, 없으면 EC2 퍼블릭 IP 자동 감지)
	grafanaURL = os.Getenv("GRAFANA_URL")
	if grafanaURL == "" {
		if ip := detectPublicIP(); ip != "" {
			grafanaURL = "http://" + ip
		} else {
			grafanaURL = "http://localhost"
		}
	}
	log.Printf("Grafana URL: %s", grafanaURL)

	// 템플릿 로드
	var err error
	templates, err = template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatalf("Template parse error: %v", err)
	}

	// 라우터
	mux := http.NewServeMux()

	// 공개 라우트
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /login", showLogin)
	mux.HandleFunc("POST /login", handleLogin(globalStore))
	mux.HandleFunc("POST /logout", handleLogout(globalStore))

	// 사용자 라우트 (user 이상)
	mux.HandleFunc("GET /user", requireRole("user", showUserPage))
	mux.HandleFunc("GET /api/endpoint", requireRole("user", getEndpoint))
	mux.HandleFunc("POST /api/endpoint", requireRole("user", saveEndpoint))

	// 어드민 라우트
	mux.HandleFunc("GET /admin", requireRole("admin", showAdminPage))
	mux.HandleFunc("GET /api/test/config", requireRole("admin", getTestConfig))
	mux.HandleFunc("POST /api/test/config", requireRole("admin", saveTestConfig))
	mux.HandleFunc("POST /api/test/start", requireRole("admin", startTest))
	mux.HandleFunc("POST /api/test/stop", requireRole("admin", stopTest))
	mux.HandleFunc("GET /api/test/status", requireRole("admin", getTestStatus))
	mux.HandleFunc("GET /api/test/logs", requireRole("admin", streamLogs))
	mux.HandleFunc("GET /api/metrics", requireRole("admin", getMetrics))

	port := os.Getenv("LOADTEST_PORT")
	if port == "" {
		port = "8888"
	}

	log.Printf("Load Test Platform UI running at http://localhost:%s", port)
	log.Printf("  User login  : http://localhost:%s/login", port)
	log.Printf("  Admin login : http://localhost:%s/login (admin credentials)", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// ─── 페이지 핸들러 ──────────────────────────────────────────────────

func showLogin(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Error": r.URL.Query().Get("error") == "1",
	}
	renderTemplate(w, "login.html", data)
}

func showUserPage(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r.Context())

	// 현재 설정된 엔드포인트 추출
	endpoint := ""
	data, err := os.ReadFile(apisFilePath())
	if err == nil && len(data) > 0 {
		var entries []APIEntry
		if json.Unmarshal(data, &entries) == nil && len(entries) > 0 {
			endpoint = extractEndpoint(entries[0].URL)
		}
	}

	renderTemplate(w, "user.html", map[string]any{
		"Username": sess.Username,
		"Role":     sess.Role,
		"Endpoint": endpoint,
	})
}

func showAdminPage(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r.Context())

	// apis.json 로드
	var entries []APIEntry
	data, err := os.ReadFile(apisFilePath())
	if err == nil {
		_ = json.Unmarshal(data, &entries)
	}

	// 엔드포인트 추출
	endpoint := ""
	if len(entries) > 0 {
		endpoint = extractEndpoint(entries[0].URL)
	}

	renderTemplate(w, "admin.html", map[string]any{
		"Username":   sess.Username,
		"APIs":       entries,
		"Config":     globalStore.GetTestConfig(),
		"Endpoint":   endpoint,
		"GrafanaURL": grafanaURL,
	})
}

// renderTemplate은 지정된 템플릿을 렌더링합니다.
func renderTemplate(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template error [%s]: %v", name, err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

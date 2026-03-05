package main

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed templates
var templateFS embed.FS

var (
	globalStore *AppStore
	projectRoot string
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

func main() {
	globalStore = NewAppStore()

	// 프로젝트 루트 결정
	projectRoot = os.Getenv("LOADTEST_PROJECT_DIR")
	if projectRoot == "" {
		// web/ 바이너리 위치에서 상위 디렉토리
		exe, err := os.Executable()
		if err == nil {
			projectRoot = filepath.Dir(filepath.Dir(exe))
		} else {
			projectRoot = ".."
		}
	}
	log.Printf("Project root: %s", projectRoot)

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
		"Username": sess.Username,
		"APIs":     entries,
		"Config":   globalStore.GetTestConfig(),
		"Endpoint": endpoint,
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

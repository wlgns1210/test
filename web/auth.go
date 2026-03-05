package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"time"
)

// 자격증명 로드 (환경변수 우선, 없으면 기본값)
func getCredentials() (userID, userPW, adminID, adminPW string) {
	userID = getEnv("LOADTEST_USER", "user")
	userPW = getEnv("LOADTEST_USER_PASS", "user123")
	adminID = getEnv("LOADTEST_ADMIN", "admin")
	adminPW = getEnv("LOADTEST_ADMIN_PASS", "admin123")
	return
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newSessionID는 crypto/rand 기반 32바이트 hex 세션 ID를 생성합니다.
func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// handleLogin은 POST /login을 처리합니다.
func handleLogin(store *AppStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")

		userID, userPW, adminID, adminPW := getCredentials()

		var role string
		switch {
		case username == adminID && password == adminPW:
			role = "admin"
		case username == userID && password == userPW:
			role = "user"
		default:
			// 로그인 실패: 에러 쿼리 파라미터와 함께 리다이렉트
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		// 세션 생성
		sid, err := newSessionID()
		if err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		store.sessions.Store(sid, &Session{
			Username:  username,
			Role:      role,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		})

		// 쿠키 설정
		http.SetCookie(w, &http.Cookie{
			Name:     "lt_session",
			Value:    sid,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400,
		})

		// 역할에 따라 리다이렉트
		if role == "admin" {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/user", http.StatusSeeOther)
		}
	}
}

// handleLogout은 POST /logout을 처리합니다.
func handleLogout(store *AppStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("lt_session"); err == nil {
			store.sessions.Delete(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:   "lt_session",
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// getSession은 요청에서 세션을 조회합니다. 세션이 없거나 만료되면 nil 반환.
func getSession(r *http.Request, store *AppStore) *Session {
	cookie, err := r.Cookie("lt_session")
	if err != nil {
		return nil
	}
	val, ok := store.sessions.Load(cookie.Value)
	if !ok {
		return nil
	}
	sess := val.(*Session)
	if time.Now().After(sess.ExpiresAt) {
		store.sessions.Delete(cookie.Value)
		return nil
	}
	return sess
}

// requireRole은 역할 기반 인증 미들웨어입니다.
// minRole이 "user"이면 user와 admin 모두 허용, "admin"이면 admin만 허용.
func requireRole(minRole string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := getSession(r, globalStore)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// admin은 모든 역할 허용, user는 user 역할만 허용
		if minRole == "admin" && sess.Role != "admin" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// 세션을 요청 컨텍스트에 넣어 하위 핸들러에서 사용
		r = r.WithContext(withSession(r.Context(), sess))
		next(w, r)
	}
}

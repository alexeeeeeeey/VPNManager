package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "vpn_manager_session"
const sessionTTL = 12 * time.Hour

type sessionCheckKey struct{}

func authentication(cfg config) func(http.Handler) http.Handler {
	var mu sync.Mutex
	sessions := make(map[string]time.Time)
	var lastFailure time.Time
	userHash := sha256.Sum256([]byte(cfg.Username))
	passHash := sha256.Sum256([]byte(cfg.Password))
	cookieName := func(r *http.Request) string {
		if r.TLS == nil {
			return sessionCookie + "_http"
		}
		return sessionCookie
	}
	token := func(r *http.Request) string {
		cookie, err := r.Cookie(cookieName(r))
		if err != nil || len(cookie.Value) != 64 {
			return ""
		}
		return cookie.Value
	}
	setCookie := func(w http.ResponseWriter, r *http.Request, value string, age int) {
		http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: value, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: age})
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/favicon.svg" {
				next.ServeHTTP(w, r)
				return
			}
			if cfg.Development {
				if r.URL.Path == "/login" || r.URL.Path == "/logout" {
					http.Redirect(w, r, "/", http.StatusSeeOther)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/login" || r.URL.Path == "/logout" {
				w.Header().Set("Cache-Control", "no-store")
				if r.URL.Path == "/login" && r.Method == http.MethodGet {
					loginPage(w, http.StatusOK, "")
					return
				}
				if r.Method != http.MethodPost {
					w.Header().Set("Allow", "POST")
					http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if !sameOrigin(r) {
					http.Error(w, "Cross-site request", http.StatusForbidden)
					return
				}
				if r.URL.Path == "/logout" {
					mu.Lock()
					delete(sessions, token(r))
					mu.Unlock()
					setCookie(w, r, "", -1)
					http.Redirect(w, r, "/login", http.StatusSeeOther)
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, 4096)
				if err := r.ParseForm(); err != nil {
					loginPage(w, http.StatusBadRequest, "Некорректные данные формы.")
					return
				}
				mu.Lock()
				defer mu.Unlock()
				now := time.Now()
				if now.Sub(lastFailure) < time.Second {
					w.Header().Set("Retry-After", "1")
					loginPage(w, http.StatusTooManyRequests, "Повторите попытку через секунду.")
					return
				}
				user := sha256.Sum256([]byte(r.PostForm.Get("username")))
				pass := sha256.Sum256([]byte(r.PostForm.Get("password")))
				if subtle.ConstantTimeCompare(userHash[:], user[:])&subtle.ConstantTimeCompare(passHash[:], pass[:]) != 1 {
					lastFailure = now
					loginPage(w, http.StatusUnauthorized, "Неверное имя пользователя или пароль.")
					return
				}
				delete(sessions, token(r))
				var oldest string
				for key, expires := range sessions {
					if !now.Before(expires) {
						delete(sessions, key)
					} else if oldest == "" || expires.Before(sessions[oldest]) {
						oldest = key
					}
				}
				if len(sessions) >= 16 {
					delete(sessions, oldest)
				}
				var secret [32]byte
				if _, err := rand.Read(secret[:]); err != nil {
					http.Error(w, "Session unavailable", http.StatusInternalServerError)
					return
				}
				key := hex.EncodeToString(secret[:])
				sessions[key] = now.Add(sessionTTL)
				setCookie(w, r, key, int(sessionTTL/time.Second))
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			key := token(r)
			valid := func() bool {
				mu.Lock()
				defer mu.Unlock()
				return time.Now().Before(sessions[key])
			}
			if valid() {
				if r.URL.Path == "/api/live" {
					r = r.WithContext(context.WithValue(r.Context(), sessionCheckKey{}, valid))
				}
				next.ServeHTTP(w, r)
			} else if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"Требуется вход"}`)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
		})
	}
}

func loginPage(w http.ResponseWriter, status int, message string) {
	const themeScript = `try{const old=localStorage.getItem('vpn-manager-accent');const color=localStorage.getItem('vpn-manager-theme')||(old==='#baf5a7'?'':old);if(/^#[0-9a-f]{6}$/i.test(color||''))document.documentElement.style.setProperty('--accent',color)}catch{}`
	hash := sha256.Sum256([]byte(themeScript))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; script-src 'sha256-"+base64.StdEncoding.EncodeToString(hash[:])+"'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" type="image/svg+xml" href="/favicon.svg"><title>Manager</title><script>`+themeScript+`</script>
<style>
:root{color-scheme:dark;--accent:#ffffff;--surface:color-mix(in srgb,#070707,var(--accent) 10%);--edge:color-mix(in srgb,#161616,var(--accent) 17%);--muted:color-mix(in srgb,#a3a3a3,var(--accent) 22%);--background:color-mix(in srgb,#040404,var(--accent) 7%)}
*{box-sizing:border-box}body{margin:0;min-height:100dvh;display:grid;place-items:center;padding:1rem;background:var(--background);color:#eef3ef;font:1rem Inter,"Segoe UI",system-ui,sans-serif}
main{width:min(100%,26rem);padding:clamp(1.25rem,5vw,2rem);border:1px solid var(--edge);border-radius:14px;background:var(--surface)}
.brand{margin:0 0 .55rem;color:var(--accent);font-size:.75rem;font-weight:750;letter-spacing:.1em}h1{margin:0 0 1.5rem;font-size:2rem;letter-spacing:-.04em}
label{display:grid;gap:.45rem;margin:1rem 0;color:var(--muted);font-size:.875rem}
input,button{width:100%;border:1px solid var(--edge);border-radius:8px;padding:.65rem .8rem;font:inherit}input{background:var(--background);color:#eef3ef}
button{background:var(--accent);color:#151515;border-color:transparent;font-weight:650;cursor:pointer}button:hover{filter:brightness(1.06)}
:focus-visible{outline:2px solid var(--accent);outline-offset:3px}.error{color:#ffb2a9;background:#3b2523;padding:.65rem .8rem;border-radius:8px}.error:empty{display:none}
</style><main><p class="brand">VPN MANAGER</p><h1>Вход</h1><p class="error" role="alert">`+message+`</p><form action="/login" method="post" autocomplete="on"><label>Имя пользователя<input name="username" autocomplete="username" autocapitalize="none" required></label><label>Пароль<input name="password" type="password" autocomplete="current-password" required></label><button type="submit">Войти</button></form></main></html>`)
}

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const (
	cookieName    = "cs2panel_session"
	sessionMaxAge = 24 * time.Hour
)

type Auth struct {
	password string
	sessions map[string]time.Time
	mu       sync.Mutex
}

func New(password string) *Auth {
	return &Auth{
		password: password,
		sessions: make(map[string]time.Time),
	}
}

func (a *Auth) CheckPassword(input string) bool {
	return subtle.ConstantTimeCompare([]byte(a.password), []byte(input)) == 1
}

func (a *Auth) CreateSession() string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	a.mu.Lock()
	a.sessions[token] = time.Now()
	a.mu.Unlock()

	return token
}

func (a *Auth) ValidateSession(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	created, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Since(created) > sessionMaxAge {
		delete(a.sessions, token)
		return false
	}
	return true
}

func (a *Auth) DeleteSession(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	password := r.FormValue("password")
	if !a.CheckPassword(password) {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}

	token := a.CreateSession()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionMaxAge.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   cookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Middleware protects routes behind authentication.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !a.ValidateSession(c.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"

	"github.com/tuffrabit/gorchestrator/internal/auth"
)

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if u, _ := s.auth.Authenticate(r); u != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	data := map[string]any{
		"Mode":  s.cfg.Auth.Mode,
		"Error": r.URL.Query().Get("error"),
		"Next":  r.URL.Query().Get("next"),
	}
	if err := render(w, "login.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth.Mode != "local" && s.cfg.Auth.Mode != "disabled" {
		http.Error(w, "local auth disabled", http.StatusBadRequest)
		return
	}
	if err := parseRequestForm(r); err != nil {
		http.Redirect(w, r, "/login?error=bad_form", http.StatusFound)
		return
	}
	user, pass := r.FormValue("username"), r.FormValue("password")
	u, err := s.auth.LoginLocal(user, pass)
	if err != nil {
		_ = s.eng.Audit().Record(nil, "login_failed", "user", user, map[string]any{"reason": err.Error()})
		http.Redirect(w, r, "/login?error=invalid", http.StatusFound)
		return
	}
	if err := s.auth.CreateSession(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	_ = s.eng.Audit().Record(&u.ID, "login", "user", u.Email, map[string]any{"mode": "local"})
	next := r.FormValue("next")
	if next == "" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth.Mode != "oidc" {
		http.Error(w, "oidc not enabled", http.StatusBadRequest)
		return
	}
	state, err := randomState()
	if err != nil {
		http.Error(w, "state error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "gorch_oidc_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	urlStr, err := s.auth.OIDCStartURL(r, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, urlStr, http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth.Mode != "oidc" {
		http.Error(w, "oidc not enabled", http.StatusBadRequest)
		return
	}
	stateCookie, err := r.Cookie("gorch_oidc_state")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		http.Redirect(w, r, "/login?error=state", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "gorch_oidc_state", Value: "", Path: "/", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login?error=code", http.StatusFound)
		return
	}
	u, err := s.auth.HandleOIDCCallback(r.Context(), code, r.URL.Query().Get("state"))
	if err != nil {
		_ = s.eng.Audit().Record(nil, "login_failed", "user", "", map[string]any{"reason": err.Error(), "mode": "oidc"})
		http.Redirect(w, r, "/login?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	if err := s.auth.CreateSession(w, r, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	_ = s.eng.Audit().Record(&u.ID, "login", "user", u.Email, map[string]any{"mode": "oidc"})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if u := auth.UserFromContext(r.Context()); u != nil {
		_ = s.eng.Audit().Record(&u.ID, "logout", "user", u.Email, nil)
	}
	s.auth.ClearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

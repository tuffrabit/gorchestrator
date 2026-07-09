// Package auth implements sessions, local/OIDC login, and role middleware.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

// Role hierarchy: admin > member > viewer
const (
	RoleAdmin  = sqlite.RoleAdmin
	RoleMember = sqlite.RoleMember
	RoleViewer = sqlite.RoleViewer
)

const sessionCookie = "gorch_session"
const csrfCookie = "gorch_csrf"
const csrfHeader = "X-CSRF-Token"

type ctxKey int

const userCtxKey ctxKey = 1

// User is the authenticated principal attached to the request context.
type User struct {
	ID          int64
	Email       string
	DisplayName string
	Role        string
}

// Service owns auth configuration and session handling.
type Service struct {
	eng *orchestrator.Engine
	cfg *config.Config
	// oidc fields set when mode=oidc
	oidc *oidcProvider
}

// New creates the auth service and ensures bootstrap local user when needed.
func New(eng *orchestrator.Engine, cfg *config.Config) (*Service, error) {
	s := &Service{eng: eng, cfg: cfg}
	switch cfg.Auth.Mode {
	case "local":
		if err := s.ensureLocalUser(); err != nil {
			return nil, err
		}
	case "oidc":
		if cfg.Auth.OIDC.IssuerURL == "" || cfg.Auth.OIDC.ClientID == "" {
			return nil, fmt.Errorf("auth.mode=oidc requires oidc.issuer_url and oidc.client_id")
		}
		p, err := newOIDCProvider(context.Background(), cfg)
		if err != nil {
			return nil, fmt.Errorf("init oidc: %w", err)
		}
		s.oidc = p
	case "disabled":
		// test-only: middleware injects a synthetic admin
	default:
		return nil, fmt.Errorf("unknown auth.mode %q", cfg.Auth.Mode)
	}
	return s, nil
}

func (s *Service) ensureLocalUser() error {
	pass := osGetenv(s.cfg.Auth.LocalPasswordEnv)
	if pass == "" {
		// Allow empty in tests; generate a random password and log once.
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		pass = base64.RawURLEncoding.EncodeToString(b)
		log.Printf("auth: %s not set; generated local password for %q (shown once): %s",
			s.cfg.Auth.LocalPasswordEnv, s.cfg.Auth.LocalUsername, pass)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	email := s.cfg.Auth.LocalUsername
	if !strings.Contains(email, "@") {
		email = email + "@localhost"
	}
	u, err := s.eng.Users().GetByEmail(email)
	if err != nil {
		return err
	}
	hashStr := string(hash)
	if u == nil {
		_, err = s.eng.Users().Create(email, s.cfg.Auth.LocalUsername, RoleAdmin, nil, &hashStr)
		return err
	}
	// Always re-hash from env so local password tracks GORCH_LOCAL_PASSWORD.
	return s.eng.Users().UpdatePasswordHash(u.ID, hashStr)
}

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(userCtxKey).(*User)
	return u
}

// Authenticate resolves the session cookie to a user without enforcing roles.
func (s *Service) Authenticate(r *http.Request) (*User, error) {
	return s.authenticate(r)
}

// Require enforces a minimum role for JSON API routes (401/403 JSON).
func (s *Service) Require(minRole string, next http.Handler) http.Handler {
	return s.require(minRole, next, true)
}

// RequireHTML enforces a minimum role for browser routes (redirect to login).
func (s *Service) RequireHTML(minRole string, next http.Handler) http.Handler {
	return s.require(minRole, next, false)
}

func (s *Service) require(minRole string, next http.Handler, jsonAPI bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil || u == nil {
			if s.cfg.Auth.Mode == "disabled" {
				u = &User{ID: 0, Email: "disabled@localhost", DisplayName: "test", Role: RoleAdmin}
			} else {
				if jsonAPI {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
				return
			}
		}
		if !roleAtLeast(u.Role, minRole) {
			if jsonAPI {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// CSRF for state-changing cookie auth (skip GET/HEAD/OPTIONS).
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !s.checkCSRF(r) && s.cfg.Auth.Mode != "disabled" {
				if jsonAPI {
					writeJSONError(w, http.StatusForbidden, "csrf validation failed")
					return
				}
				http.Error(w, "csrf validation failed", http.StatusForbidden)
				return
			}
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) authenticate(r *http.Request) (*User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	sess, err := s.eng.Sessions().Get(c.Value)
	if err != nil || sess == nil {
		return nil, nil
	}
	if sess.IsExpired() {
		_ = s.eng.Sessions().Delete(sess.ID)
		return nil, nil
	}
	dbUser, err := s.eng.Users().Get(sess.UserID)
	if err != nil || dbUser == nil {
		return nil, nil
	}
	return &User{
		ID:          dbUser.ID,
		Email:       dbUser.Email,
		DisplayName: dbUser.DisplayName,
		Role:        dbUser.Role,
	}, nil
}

// CreateSession issues a session cookie for the user.
func (s *Service) CreateSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	ttl := s.cfg.Auth.SessionTTLDur
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	sess, err := s.eng.Sessions().Create(userID, ttl)
	if err != nil {
		return err
	}
	secure := strings.HasPrefix(s.cfg.Server.PublicBaseURL, "https://")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().UTC().Add(ttl),
	})
	// CSRF double-submit cookie
	token, err := randomToken(32)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().UTC().Add(ttl),
	})
	_ = s.eng.Users().TouchLogin(userID)
	return nil
}

// ClearSession destroys the session cookie.
func (s *Service) ClearSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.eng.Sessions().Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: "", Path: "/", MaxAge: -1})
}

// LoginLocal validates username/password for local mode.
func (s *Service) LoginLocal(username, password string) (*sqlite.User, error) {
	email := username
	if !strings.Contains(email, "@") {
		email = email + "@localhost"
	}
	u, err := s.eng.Users().GetByEmail(email)
	if err != nil || u == nil {
		// also try raw username as email
		u, err = s.eng.Users().GetByEmail(username)
		if err != nil || u == nil {
			return nil, fmt.Errorf("invalid credentials")
		}
	}
	if !u.PasswordHash.Valid {
		return nil, fmt.Errorf("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash.String), []byte(password)); err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}
	return u, nil
}

// CSRFToken returns the CSRF token from the request cookie (for templates).
func CSRFToken(r *http.Request) string {
	c, err := r.Cookie(csrfCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Service) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	// Header (HTMX / fetch) or form field
	tok := r.Header.Get(csrfHeader)
	if tok == "" {
		tok = r.FormValue("csrf_token")
	}
	return tok != "" && tok == c.Value
}

// OIDCStartURL returns the IdP redirect URL, or error if OIDC not configured.
func (s *Service) OIDCStartURL(r *http.Request, state string) (string, error) {
	if s.oidc == nil {
		return "", fmt.Errorf("oidc not configured")
	}
	return s.oidc.authCodeURL(state), nil
}

// HandleOIDCCallback exchanges the code and upserts the user.
func (s *Service) HandleOIDCCallback(ctx context.Context, code, state string) (*sqlite.User, error) {
	if s.oidc == nil {
		return nil, fmt.Errorf("oidc not configured")
	}
	return s.oidc.exchange(ctx, s, code)
}

func roleAtLeast(have, need string) bool {
	rank := map[string]int{RoleViewer: 1, RoleMember: 2, RoleAdmin: 3}
	return rank[have] >= rank[need]
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + jsonQuote(msg) + `}`))
}

func jsonQuote(s string) string {
	b, _ := jsonMarshal(s)
	return string(b)
}

func jsonMarshal(v any) ([]byte, error) {
	// local to avoid import cycle with encoding/json name clash
	return marshalJSON(v)
}

func osGetenv(k string) string {
	return getenv(k)
}

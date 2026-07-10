// Package server provides the HTTP API and dashboard surface for `gorchestrator serve`.
package server

import (
	"log"
	"net/http"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/auth"
	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

// Server holds HTTP dependencies.
type Server struct {
	eng  *orchestrator.Engine
	cfg  *config.Config
	auth *auth.Service
	mux  *http.ServeMux
}

// New constructs the HTTP server and mounts routes.
func New(eng *orchestrator.Engine, cfg *config.Config) (*Server, error) {
	authSvc, err := auth.New(eng, cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{
		eng:  eng,
		cfg:  cfg,
		auth: authSvc,
		mux:  http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Handler returns the root handler with middleware.
func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.logMiddleware(s.mux))
}

func (s *Server) routes() {
	// Auth pages/API
	s.mux.HandleFunc("GET /login", s.handleLoginPage)
	s.mux.HandleFunc("POST /auth/local", s.handleLocalLogin)
	s.mux.HandleFunc("GET /auth/oidc/start", s.handleOIDCStart)
	s.mux.HandleFunc("GET /auth/callback", s.handleOIDCCallback)
	s.mux.HandleFunc("POST /logout", s.handleLogout)

	// External webhook trigger (token auth, not session cookie)
	s.mux.HandleFunc("POST /hooks/issues", s.handleWebhookIssue)

	// API
	s.mux.Handle("POST /api/issues", s.auth.Require(auth.RoleMember, http.HandlerFunc(s.handleSubmitIssue)))
	s.mux.Handle("GET /api/issues", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleListIssues)))
	s.mux.Handle("GET /api/issues/{id}", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleGetIssue)))
	s.mux.Handle("DELETE /api/issues/{id}", s.auth.Require(auth.RoleMember, http.HandlerFunc(s.handleDeleteIssue)))
	s.mux.Handle("GET /api/issues/{id}/artifacts/{path...}", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleGetArtifact)))
	s.mux.Handle("GET /api/issues/{id}/workspace.zip", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleWorkspaceZip)))
	s.mux.Handle("POST /api/issues/{id}/decisions", s.auth.Require(auth.RoleMember, http.HandlerFunc(s.handleDecide)))
	s.mux.Handle("GET /api/events", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleSSE)))
	s.mux.Handle("GET /api/projects", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleListProjects)))
	s.mux.Handle("GET /api/notifications", s.auth.Require(auth.RoleViewer, http.HandlerFunc(s.handleListNotifications)))

	// Dashboard (HTML)
	s.mux.Handle("GET /{$}", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handleFeed)))
	s.mux.Handle("GET /issues/{id}", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handleFeedIssue)))
	s.mux.Handle("GET /notifications", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handleNotificationsPage)))
	s.mux.Handle("GET /partials/issues", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handlePartialsIssues)))
	s.mux.Handle("GET /partials/issues/{id}", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handlePartialIssue)))
	s.mux.Handle("DELETE /partials/issues/{id}", s.auth.RequireHTML(auth.RoleMember, http.HandlerFunc(s.handlePartialDeleteIssue)))
	s.mux.Handle("GET /partials/issues/{id}/drawer", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handlePartialDrawer)))
	s.mux.Handle("GET /partials/issues/{id}/workspace-file", s.auth.RequireHTML(auth.RoleViewer, http.HandlerFunc(s.handlePartialWorkspaceFile)))
	s.mux.Handle("GET /partials/submit", s.auth.RequireHTML(auth.RoleMember, http.HandlerFunc(s.handlePartialSubmit)))
	s.mux.Handle("GET /partials/submit/flavors", s.auth.RequireHTML(auth.RoleMember, http.HandlerFunc(s.handlePartialSubmitFlavors)))
	s.mux.Handle("POST /partials/issues/{id}/decisions", s.auth.RequireHTML(auth.RoleMember, http.HandlerFunc(s.handlePartialDecide)))
	s.mux.Handle("POST /partials/submit", s.auth.RequireHTML(auth.RoleMember, http.HandlerFunc(s.handlePartialSubmitPost)))

	// Static assets
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(staticFS())))
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher for SSE.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

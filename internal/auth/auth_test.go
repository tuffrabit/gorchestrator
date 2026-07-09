package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
)

func testConfig(tmp string) *config.Config {
	return &config.Config{
		StorageRoot: tmp,
		DBPath:      filepath.Join(tmp, "gorchestrator.db"),
		DefaultModel: config.ModelConfig{
			Provider: "dryrun", Model: "dryrun", Timeout: "30s", TimeoutDur: 30 * time.Second,
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileConfig{MaxBytes: 64 * 1024, MaxLines: 2000},
		},
		Server: config.ServerConfig{
			Listen: "127.0.0.1:8080", PublicBaseURL: "http://127.0.0.1:8080",
			MaxConcurrentIssues: 1, ShutdownTimeoutDur: 5 * time.Second,
		},
		Auth: config.AuthConfig{
			Mode:             "local",
			LocalUsername:    "admin",
			LocalPasswordEnv: "GORCH_TEST_LOCAL_PASSWORD",
			SessionTTLDur:    24 * time.Hour,
		},
	}
}

func TestLocalLoginAndSession(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("GORCH_TEST_LOCAL_PASSWORD", "test-password-123")
	t.Cleanup(func() { os.Unsetenv("GORCH_TEST_LOCAL_PASSWORD") })

	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	svc, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}

	u, err := svc.LoginLocal("admin", "test-password-123")
	if err != nil {
		t.Fatalf("LoginLocal: %v", err)
	}
	if u.Role != sqlite.RoleAdmin {
		t.Fatalf("role = %q, want admin", u.Role)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := svc.CreateSession(rec, req, u.ID); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/issues", nil)
	for _, c := range cookies {
		req2.AddCookie(c)
	}
	got, err := svc.Authenticate(req2)
	if err != nil || got == nil {
		t.Fatalf("Authenticate: %v user=%v", err, got)
	}
	if got.Email != "admin@localhost" {
		t.Fatalf("email = %q", got.Email)
	}
}

func TestViewerForbiddenOnMemberRoute(t *testing.T) {
	tmp := t.TempDir()
	os.Setenv("GORCH_TEST_LOCAL_PASSWORD", "test-password-123")
	t.Cleanup(func() { os.Unsetenv("GORCH_TEST_LOCAL_PASSWORD") })

	cfg := testConfig(tmp)
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	// Create a viewer user directly.
	hash := "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy" // bcrypt of "password" often used in tests — we'll set via Create
	// Use LoginLocal path: create viewer with known hash via repo after bootstrap.
	svc, err := New(eng, cfg)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	_ = hash

	viewerEmail := "viewer@localhost"
	// Create with same password as bootstrap by hashing through LoginLocal path:
	// insert viewer manually after hashing with bcrypt in LoginLocal verify — use Create with password from bootstrap user.
	admin, _ := eng.Users().GetByEmail("admin@localhost")
	if admin == nil {
		t.Fatal("bootstrap admin missing")
	}
	ph := admin.PasswordHash.String
	_, err = eng.Users().Create(viewerEmail, "viewer", sqlite.RoleViewer, nil, &ph)
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	vu, err := eng.Users().GetByEmail(viewerEmail)
	if err != nil || vu == nil {
		t.Fatal("viewer missing")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := svc.CreateSession(rec, req, vu.ID); err != nil {
		t.Fatalf("session: %v", err)
	}

	called := false
	h := svc.Require(RoleMember, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req2 := httptest.NewRequest(http.MethodPost, "/api/issues", nil)
	for _, c := range rec.Result().Cookies() {
		req2.AddCookie(c)
	}
	// CSRF: disabled mode only skips; local mode needs matching tokens.
	// Set both cookie and header.
	var csrf string
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookie {
			csrf = c.Value
		}
	}
	req2.Header.Set(csrfHeader, csrf)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec2.Code)
	}
	if called {
		t.Fatal("handler should not run for viewer")
	}
}

package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// llamaSwapStub fakes the llama-swap management surface. Warmup requests
// "load" the requested model; /unload clears all models after unloadClearsIn
// subsequent /running polls (0 = immediately, negative = never).
type llamaSwapStub struct {
	mu             sync.Mutex
	running        []string
	unloadClearsIn int
	pendingClears  int
	// noLoadOnWarmup simulates a warmup that completes without the model
	// becoming resident (verification must catch it).
	noLoadOnWarmup bool
	requests       []string
	warmups        []map[string]any
}

func newLlamaSwapStub() *llamaSwapStub {
	return &llamaSwapStub{unloadClearsIn: 0}
}

func (s *llamaSwapStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.requests = append(s.requests, "chat")
		s.warmups = append(s.warmups, body)
		if m, _ := body["model"].(string); m != "" && !s.noLoadOnWarmup {
			// A load adds the model alongside anything already resident (a
			// sideloaded model survives a main-model load).
			present := false
			for _, r := range s.running {
				if r == m {
					present = true
					break
				}
			}
			if !present {
				s.running = append(s.running, m)
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	})
	mux.HandleFunc("GET /running", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, "running")
		running := append([]string(nil), s.running...)
		s.mu.Unlock()
		models := make([]map[string]string, 0, len(running))
		for _, m := range running {
			models = append(models, map[string]string{"model": m})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"running": models})
	})
	unload := func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, "unload")
		switch {
		case s.unloadClearsIn < 0:
			// Never clears: simulates a stuck unload.
		case s.unloadClearsIn == 0:
			s.running = nil
		default:
			s.pendingClears = s.unloadClearsIn
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
	mux.HandleFunc("POST /api/models/unload", unload)
	mux.HandleFunc("POST /unload", unload) // legacy path, used by fallback test
	// Per-model unload (sideloaded-model keep lists): removes only that model.
	mux.HandleFunc("POST /api/models/unload/{model}", func(w http.ResponseWriter, r *http.Request) {
		model := r.PathValue("model")
		s.mu.Lock()
		s.requests = append(s.requests, "unload:"+model)
		kept := s.running[:0]
		for _, m := range s.running {
			if m != model {
				kept = append(kept, m)
			}
		}
		s.running = kept
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// tickPoll records a /running poll and clears models once the post-unload
// countdown reaches zero (negative countdown = never clears).
func (s *llamaSwapStub) tickPoll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingClears > 0 {
		s.pendingClears--
		if s.pendingClears == 0 {
			s.running = nil
		}
	}
}

func (s *llamaSwapStub) requestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func newStubServer(t *testing.T, stub *llamaSwapStub) *httptest.Server {
	t.Helper()
	// Wrap the running handler so polls drive the unload countdown.
	mux := stub.handler().(*http.ServeMux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/running" {
			stub.tickPoll()
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureLoaded_WarmupShapeAndVerify(t *testing.T) {
	stub := newLlamaSwapStub()
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)

	if err := ctl.EnsureLoaded(context.Background(), "big-moe", time.Minute); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	if len(stub.warmups) != 1 {
		t.Fatalf("warmup calls = %d, want 1", len(stub.warmups))
	}
	w := stub.warmups[0]
	if w["model"] != "big-moe" {
		t.Fatalf("warmup model = %v, want big-moe", w["model"])
	}
	if w["max_tokens"] != float64(1) {
		t.Fatalf("warmup max_tokens = %v, want 1", w["max_tokens"])
	}
	msgs, ok := w["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("warmup messages = %v, want one message", w["messages"])
	}
	msg, _ := msgs[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "ping" {
		t.Fatalf("warmup message = %v, want user/ping", msg)
	}

	log := stub.requestLog()
	if len(log) != 2 || log[0] != "chat" || log[1] != "running" {
		t.Fatalf("request log = %v, want [chat running]", log)
	}
}

func TestEnsureLoaded_WarmupOKButNotRunning(t *testing.T) {
	stub := newLlamaSwapStub()
	stub.noLoadOnWarmup = true
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)

	err := ctl.EnsureLoaded(context.Background(), "ghost-model", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("EnsureLoaded = %v, want not-running error", err)
	}
}

func TestUnloadAll_PollsUntilEmpty(t *testing.T) {
	stub := newLlamaSwapStub()
	stub.running = []string{"big-moe"}
	stub.unloadClearsIn = 2 // empty on the second poll after unload
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)
	ctl.pollInterval = time.Millisecond

	if err := ctl.UnloadAll(context.Background()); err != nil {
		t.Fatalf("UnloadAll: %v", err)
	}
	log := stub.requestLog()
	if len(log) < 3 || log[0] != "unload" {
		t.Fatalf("request log = %v, want unload then running polls", log)
	}
	for _, entry := range log[1:] {
		if entry != "running" {
			t.Fatalf("request log = %v, want only running polls after unload", log)
		}
	}
}

func TestUnloadAll_PollDeadlineExpires(t *testing.T) {
	stub := newLlamaSwapStub()
	stub.running = []string{"big-moe"}
	stub.unloadClearsIn = -1 // never clears
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)
	ctl.pollInterval = time.Millisecond
	ctl.unloadDeadline = 50 * time.Millisecond

	err := ctl.UnloadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("UnloadAll = %v, want still-running deadline error", err)
	}
}

func TestUnloadAll_KeepsSideloadedModels(t *testing.T) {
	stub := newLlamaSwapStub()
	stub.running = []string{"small-sideload", "big-moe"}
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)
	ctl.pollInterval = time.Millisecond

	if err := ctl.UnloadAll(context.Background(), "small-sideload"); err != nil {
		t.Fatalf("UnloadAll: %v", err)
	}

	// Selective path: list running, per-model unload of the non-kept model,
	// then a poll confirming only the kept model remains. Never a bare unload.
	want := []string{"running", "unload:big-moe", "running"}
	if got := stub.requestLog(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("request log = %v, want %v", got, want)
	}
	stub.mu.Lock()
	running := append([]string(nil), stub.running...)
	stub.mu.Unlock()
	if len(running) != 1 || running[0] != "small-sideload" {
		t.Fatalf("running after selective unload = %v, want [small-sideload]", running)
	}
}

func TestUnloadAll_KeepFallsBackToUnloadAllOnOldServer(t *testing.T) {
	// Old llama-swap without the per-model endpoint: 405 on
	// /api/models/unload/<model> must fall back to unload-all.
	var mu sync.Mutex
	running := []string{"small-sideload", "big-moe"}
	var allHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/models/unload/{model}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("POST /api/models/unload", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		allHit = true
		running = nil
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /running", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		models := make([]map[string]string, 0, len(running))
		for _, m := range running {
			models = append(models, map[string]string{"model": m})
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"running": models})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctl := newLlamaSwapController(srv.URL)
	ctl.pollInterval = time.Millisecond

	if err := ctl.UnloadAll(context.Background(), "small-sideload"); err != nil {
		t.Fatalf("UnloadAll: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !allHit {
		t.Fatal("unload-all fallback was not used after per-model 405")
	}
}

func TestUnloadAll_AlreadyUnloaded(t *testing.T) {
	stub := newLlamaSwapStub() // nothing running
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)

	if err := ctl.UnloadAll(context.Background()); err != nil {
		t.Fatalf("UnloadAll with nothing running: %v", err)
	}
	log := stub.requestLog()
	if len(log) != 2 || log[0] != "unload" || log[1] != "running" {
		t.Fatalf("request log = %v, want [unload running]", log)
	}
}

// Old llama-swap builds only expose POST /unload (current: /api/models/unload).
// A 405/404 on the current path must fall back to the legacy one.
func TestUnloadAll_FallsBackToLegacyPath(t *testing.T) {
	var legacyHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/models/unload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("POST /unload", func(w http.ResponseWriter, r *http.Request) {
		legacyHit = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /running", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"running":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctl := newLlamaSwapController(srv.URL)

	if err := ctl.UnloadAll(context.Background()); err != nil {
		t.Fatalf("UnloadAll: %v", err)
	}
	if !legacyHit {
		t.Fatal("legacy /unload path was not used after 405")
	}
}

func TestUnloadAll_NoSupportedEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/models/unload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /unload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctl := newLlamaSwapController(srv.URL)

	err := ctl.UnloadAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no supported endpoint") {
		t.Fatalf("UnloadAll = %v, want no-supported-endpoint error", err)
	}
}

func TestEnsureLoaded_AlreadyLoadedIsIdempotent(t *testing.T) {
	stub := newLlamaSwapStub()
	stub.running = []string{"big-moe"}
	srv := newStubServer(t, stub)
	ctl := newLlamaSwapController(srv.URL)

	// Crash recovery may leave the model loaded; EnsureLoaded must still
	// succeed (the warmup completes instantly against a resident model).
	if err := ctl.EnsureLoaded(context.Background(), "big-moe", time.Minute); err != nil {
		t.Fatalf("EnsureLoaded already-loaded: %v", err)
	}
}

func TestEnsureLoaded_WarmupHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)
	ctl := newLlamaSwapController(srv.URL)

	err := ctl.EnsureLoaded(context.Background(), "big-moe", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("EnsureLoaded = %v, want status 500 error", err)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestMergeAgentSingleShotTriState(t *testing.T) {
	// nil overlay inherits base.
	base := AgentConfig{SingleShot: boolPtr(true)}
	out := MergeAgent(base, AgentConfig{})
	if out.SingleShot == nil || !*out.SingleShot {
		t.Fatalf("nil overlay should inherit true, got %v", out.SingleShot)
	}

	// Explicit false overrides a true base (tri-state).
	out = MergeAgent(base, AgentConfig{SingleShot: boolPtr(false)})
	if out.SingleShot == nil || *out.SingleShot {
		t.Fatalf("false overlay should override true base, got %v", out.SingleShot)
	}

	// Merge deep-copies: mutating the overlay pointer must not affect the result.
	ov := AgentConfig{SingleShot: boolPtr(true)}
	out = MergeAgent(AgentConfig{}, ov)
	*ov.SingleShot = false
	if !*out.SingleShot {
		t.Fatal("merged SingleShot must be a deep copy")
	}

	// Untouched by default: both nil.
	out = MergeAgent(AgentConfig{}, AgentConfig{})
	if out.SingleShot != nil {
		t.Fatalf("nil + nil should stay nil, got %v", *out.SingleShot)
	}
}

func TestMergeAgentSingleShotContextBytes(t *testing.T) {
	base := AgentConfig{SingleShotContextBytes: 32768}

	out := MergeAgent(base, AgentConfig{})
	if out.SingleShotContextBytes != 32768 {
		t.Fatalf("zero overlay should inherit, got %d", out.SingleShotContextBytes)
	}

	out = MergeAgent(base, AgentConfig{SingleShotContextBytes: 16384})
	if out.SingleShotContextBytes != 16384 {
		t.Fatalf("positive overlay should override, got %d", out.SingleShotContextBytes)
	}
}

func TestMergeAgentContextFiles(t *testing.T) {
	base := AgentConfig{ContextFiles: []string{"a.go", "b.go"}}

	out := MergeAgent(base, AgentConfig{})
	if len(out.ContextFiles) != 2 {
		t.Fatalf("empty overlay should inherit, got %v", out.ContextFiles)
	}

	out = MergeAgent(base, AgentConfig{ContextFiles: []string{"c.go"}})
	if len(out.ContextFiles) != 1 || out.ContextFiles[0] != "c.go" {
		t.Fatalf("non-empty overlay should replace, got %v", out.ContextFiles)
	}

	// Replace copies the slice.
	ov := AgentConfig{ContextFiles: []string{"c.go"}}
	out = MergeAgent(AgentConfig{}, ov)
	ov.ContextFiles[0] = "mutated.go"
	if out.ContextFiles[0] != "c.go" {
		t.Fatal("merged ContextFiles must be a copy")
	}
}

func loadFromString(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadFrom(path)
}

func TestLoadRejectsBadFlavorTimeout(t *testing.T) {
	_, err := loadFromString(t, `
projects:
  proj:
    agents:
      researcher:
        flavors:
          slow:
            model:
              timeout: 6hours
`)
	if err == nil || !strings.Contains(err.Error(), "projects.proj.agents.researcher.flavors.slow") {
		t.Fatalf("want flavor-path timeout error, got %v", err)
	}
}

func TestLoadRejectsBadGlobalAgentTimeout(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  planner:
    model:
      timeout: ten-minutes
`)
	if err == nil || !strings.Contains(err.Error(), "agents.planner") {
		t.Fatalf("want agents.planner timeout error, got %v", err)
	}
}

func TestLoadRejectsNegativeSingleShotContextBytes(t *testing.T) {
	_, err := loadFromString(t, `
projects:
  proj:
    agents:
      planner:
        flavors:
          slow:
            single_shot_context_bytes: -1
`)
	if err == nil || !strings.Contains(err.Error(), "single_shot_context_bytes") {
		t.Fatalf("want single_shot_context_bytes error, got %v", err)
	}
}

func TestLoadAcceptsSingleShotFlavor(t *testing.T) {
	cfg, err := loadFromString(t, `
projects:
  proj:
    agents:
      researcher:
        default: slow
        flavors:
          slow:
            single_shot: true
            single_shot_context_bytes: 32768
            context_files: [main.go, internal/x.go]
            model:
              provider: openai
              model: deepseek-v4-flash
              base_url: http://127.0.0.1:8080/v1
              timeout: 24h
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fl := cfg.Projects["proj"].Agents["researcher"].Flavors["slow"]
	if fl.SingleShot == nil || !*fl.SingleShot {
		t.Fatalf("single_shot not loaded: %+v", fl)
	}
	if fl.SingleShotContextBytes != 32768 {
		t.Fatalf("single_shot_context_bytes = %d", fl.SingleShotContextBytes)
	}
	if len(fl.ContextFiles) != 2 || fl.ContextFiles[0] != "main.go" {
		t.Fatalf("context_files = %v", fl.ContextFiles)
	}
	if fl.Model.Timeout != "24h" {
		t.Fatalf("timeout = %q", fl.Model.Timeout)
	}
}

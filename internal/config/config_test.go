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

func TestMergeAgentSideloadTriState(t *testing.T) {
	// nil overlay inherits base.
	base := AgentConfig{Sideload: boolPtr(true)}
	out := MergeAgent(base, AgentConfig{})
	if out.Sideload == nil || !*out.Sideload {
		t.Fatalf("nil overlay should inherit true, got %v", out.Sideload)
	}

	// Explicit false overrides a true base (tri-state).
	out = MergeAgent(base, AgentConfig{Sideload: boolPtr(false)})
	if out.Sideload == nil || *out.Sideload {
		t.Fatalf("false overlay should override true base, got %v", out.Sideload)
	}

	// Untouched by default: both nil.
	out = MergeAgent(AgentConfig{}, AgentConfig{})
	if out.Sideload != nil {
		t.Fatalf("nil + nil should stay nil, got %v", *out.Sideload)
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

func TestLoadRejectsImplementerSingleShot(t *testing.T) {
	_, err := loadFromString(t, `
projects:
  proj:
    agents:
      implementer:
        flavors:
          bad:
            single_shot: true
`)
	if err == nil || !strings.Contains(err.Error(), "single_shot is not supported for the implementer") {
		t.Fatalf("want implementer single_shot error, got %v", err)
	}
}

func TestLoadAcceptsKnownAdjudicators(t *testing.T) {
	for _, name := range []string{"null", "self", "human"} {
		cfg, err := loadFromString(t, "agents:\n  planner:\n    adjudicator: \""+name+"\"\n")
		if err != nil {
			t.Fatalf("adjudicator %q: load: %v", name, err)
		}
		if cfg.Agents["planner"].Adjudicator != name {
			t.Fatalf("adjudicator = %q, want %q", cfg.Agents["planner"].Adjudicator, name)
		}
	}
	// Unset inherits the built-in default, which is the human gate.
	cfg, err := loadFromString(t, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Agent("planner").Adjudicator; got != "human" {
		t.Fatalf("default adjudicator = %q, want human", got)
	}
}

func TestLoadRejectsUnknownAdjudicator(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  researcher:
    adjudicator: selff
`)
	if err == nil || !strings.Contains(err.Error(), `agents.researcher: unknown adjudicator "selff"`) {
		t.Fatalf("want agents.researcher adjudicator error, got %v", err)
	}

	_, err = loadFromString(t, `
projects:
  proj:
    agents:
      planner:
        flavors:
          fast:
            adjudicator: selff
`)
	if err == nil || !strings.Contains(err.Error(), `projects.proj.agents.planner.flavors.fast: unknown adjudicator "selff"`) {
		t.Fatalf("want flavor-path adjudicator error, got %v", err)
	}
}

func TestDefaultPromptAccessors(t *testing.T) {
	for _, agentType := range []string{"researcher", "planner", "implementer"} {
		if DefaultSystemPrompt(agentType) == "" {
			t.Fatalf("DefaultSystemPrompt(%q) is empty", agentType)
		}
	}
	if DefaultSystemPrompt("nope") != "" {
		t.Fatal("unknown agent type should have no default prompt")
	}
	if DefaultSingleShotPrompt("researcher") == "" || DefaultSingleShotPrompt("planner") == "" {
		t.Fatal("researcher and planner need single-shot defaults")
	}
	if DefaultSingleShotPrompt("implementer") != "" {
		t.Fatal("implementer must not have a single-shot default")
	}
	// Single-shot prompts must not reference tools that don't exist there.
	for _, agentType := range []string{"researcher", "planner"} {
		p := DefaultSingleShotPrompt(agentType)
		for _, toolName := range []string{"write_output", "read_file", "finish_task", "grep_search"} {
			if strings.Contains(p, toolName) {
				t.Fatalf("single-shot %s prompt references tool %q", agentType, toolName)
			}
		}
	}
}

func TestLoadAcceptsInferenceBlock(t *testing.T) {
	cfg, err := loadFromString(t, `
inference:
  type: llama-swap
  base_url: http://192.168.1.152:8080
  mode: exclusive
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Inference.Type != "llama-swap" || cfg.Inference.BaseURL != "http://192.168.1.152:8080" || cfg.Inference.Mode != "exclusive" {
		t.Fatalf("inference = %+v", cfg.Inference)
	}

	// Mode is optional: load/unload without the exclusive lock.
	cfg, err = loadFromString(t, `
inference:
  type: llama-swap
  base_url: http://192.168.1.152:8080
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Inference.Mode != "" {
		t.Fatalf("inference.mode = %q, want empty", cfg.Inference.Mode)
	}
}

func TestLoadAbsentInferenceBlockAccepted(t *testing.T) {
	cfg, err := loadFromString(t, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Inference.Type != "" || cfg.Inference.BaseURL != "" || cfg.Inference.Mode != "" {
		t.Fatalf("inference = %+v, want zero value", cfg.Inference)
	}
}

func TestLoadRejectsBadInference(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown type",
			yaml: "inference:\n  type: vllm\n  base_url: http://h:8080\n",
			want: `inference.type: unknown type "vllm"`,
		},
		{
			name: "missing base_url",
			yaml: "inference:\n  type: llama-swap\n",
			want: "inference.base_url is required",
		},
		{
			name: "unparseable base_url",
			yaml: "inference:\n  type: llama-swap\n  base_url: not-a-url\n",
			want: "inference.base_url: invalid URL",
		},
		{
			name: "unknown mode",
			yaml: "inference:\n  type: llama-swap\n  base_url: http://h:8080\n  mode: shared\n",
			want: `inference.mode: unknown mode "shared"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFromString(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFromRejectsUnknownKeys(t *testing.T) {
	cases := map[string]string{
		"top-level": "stoage_root: /tmp/x\n",
		"nested under project (mis-indented project)": "projects:\n  bunny:\n    source_path: /tmp/bunny\n    gorchestrator:\n      source_path: /tmp/other\n",
		"agent field (stale token_budget)":            "agents:\n  researcher:\n    token_budget: 1000\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFrom(writeConfig(t, body)); err == nil {
				t.Fatal("expected unknown-key error, got nil")
			} else if !strings.Contains(err.Error(), "not found") {
				t.Fatalf("expected strict-decode error naming the field, got: %v", err)
			}
		})
	}
}

func TestLoadFromValidConfigStillLoads(t *testing.T) {
	cfg, err := LoadFrom(writeConfig(t, "projects:\n  bunny:\n    git:\n      repo_url: /tmp/bunny\n      base_branch: main\n"))
	if err != nil {
		t.Fatalf("valid config should load: %v", err)
	}
	if cfg.Projects["bunny"].Git == nil || cfg.Projects["bunny"].Git.RepoURL != "/tmp/bunny" {
		t.Fatalf("project not parsed: %+v", cfg.Projects["bunny"])
	}
}

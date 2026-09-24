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

func TestMergeAgentSystemPromptNeverInvented(t *testing.T) {
	// Neither base (generic defaults) nor an overlay without a prompt may
	// produce one: validation guarantees the user wrote it.
	out := MergeAgent(AgentConfig{SystemPrompt: "base"}, AgentConfig{})
	if out.SystemPrompt != "base" {
		t.Fatalf("empty overlay should inherit, got %q", out.SystemPrompt)
	}
	out = MergeAgent(AgentConfig{}, AgentConfig{SystemPrompt: "overlay"})
	if out.SystemPrompt != "overlay" {
		t.Fatalf("overlay should override, got %q", out.SystemPrompt)
	}
	out = MergeAgent(AgentConfig{}, AgentConfig{})
	if out.SystemPrompt != "" {
		t.Fatalf("merge must not invent a prompt, got %q", out.SystemPrompt)
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

func TestHasEditingTool(t *testing.T) {
	// Empty allowlist = full core list = has editing tools.
	if !(AgentConfig{}).HasEditingTool() {
		t.Fatal("empty Tools should mean the full core list (has editing tools)")
	}
	if (AgentConfig{Tools: []string{"read_file", "list_directory", "grep_search", "write_output"}}).HasEditingTool() {
		t.Fatal("read-only allowlist should not have editing tools")
	}
	for _, tool := range []string{"write_file", "update_file", "run_test"} {
		if !(AgentConfig{Tools: []string{tool}}).HasEditingTool() {
			t.Fatalf("%s should count as an editing tool", tool)
		}
	}
	if (AgentConfig{Tools: []string{"read_file", "write_file"}}).HasEditingTool() != true {
		t.Fatal("mixed list with write_file should have editing tools")
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

func TestLoadRejectsProjectAgentBlock(t *testing.T) {
	_, err := loadFromString(t, `
projects:
  proj:
    source_path: /tmp/proj
    agents:
      researcher:
        flavors:
          slow: {}
`)
	if err == nil || !strings.Contains(err.Error(), "projects.proj.agents was removed") {
		t.Fatalf("want friendly projects.proj.agents error, got %v", err)
	}
}

func TestLoadRejectsMissingSystemPrompt(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  coder:
    model:
      provider: openai
      model: o3-mini
`)
	if err == nil || !strings.Contains(err.Error(), "agents.coder: system_prompt is required") {
		t.Fatalf("want system_prompt required error, got %v", err)
	}
}

func TestLoadRejectsSystemPromptAppend(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  coder:
    system_prompt: "You are a coder."
    system_prompt_append: "extra"
`)
	if err == nil || !strings.Contains(err.Error(), "system_prompt_append") {
		t.Fatalf("want system_prompt_append rejected, got %v", err)
	}
}

func TestLoadRejectsUnknownTool(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  coder:
    system_prompt: "You are a coder."
    tools: [read_file, run_shell]
`)
	if err == nil || !strings.Contains(err.Error(), `agents.coder: unknown tool "run_shell"`) {
		t.Fatalf("want unknown tool error, got %v", err)
	}
}

func TestLoadRejectsUnknownMCPServer(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  coder:
    system_prompt: "You are a coder."
    mcp_servers: [nope]
`)
	if err == nil || !strings.Contains(err.Error(), `agents.coder: unknown mcp server "nope"`) {
		t.Fatalf("want unknown mcp server error, got %v", err)
	}

	// "any" and every configured server are both legal.
	cfg, err := loadFromString(t, `
mcp_servers:
  - name: internal-api
    command: ["./bin/srv"]
agents:
  coder:
    system_prompt: "You are a coder."
    mcp_servers: ["*"]
  other:
    system_prompt: "You are other."
    mcp_servers: [internal-api]
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Agents["coder"].MCPServers; len(got) != 1 || got[0] != "*" {
		t.Fatalf("mcp_servers = %v", got)
	}
}

func TestLoadRejectsInvalidAgentID(t *testing.T) {
	cases := map[string]string{
		"uppercase":    "agents:\n  Coder:\n    system_prompt: x\n",
		"leading dash": "agents:\n  -coder:\n    system_prompt: x\n",
		"too long":     "agents:\n  " + strings.Repeat("a", 33) + ":\n    system_prompt: x\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadFromString(t, body); err == nil || !strings.Contains(err.Error(), "invalid agent id") {
				t.Fatalf("want invalid agent id error, got %v", err)
			}
		})
	}
}

func TestLoadAcceptsArbitraryAgentIDs(t *testing.T) {
	// A one-agent config loads.
	cfg, err := loadFromString(t, `
agents:
  coder:
    system_prompt: "You are an implementer."
`)
	if err != nil {
		t.Fatalf("load one-agent: %v", err)
	}
	if ids := cfg.AgentIDs(); len(ids) != 1 || ids[0] != "coder" {
		t.Fatalf("AgentIDs = %v", ids)
	}

	// A two-agent config with arbitrary slugs loads; ids sort for UI/API.
	cfg, err = loadFromString(t, `
agents:
  review_1:
    system_prompt: "You are a reviewer."
    tools: [read_file, write_output]
  scout:
    system_prompt: "You are a scout."
    single_shot: true
`)
	if err != nil {
		t.Fatalf("load two-agent: %v", err)
	}
	if ids := cfg.AgentIDs(); len(ids) != 2 || ids[0] != "review_1" || ids[1] != "scout" {
		t.Fatalf("AgentIDs = %v, want [review_1 scout]", ids)
	}
	scout, ok := cfg.Agent("scout")
	if !ok || scout.SystemPrompt != "You are a scout." {
		t.Fatalf("scout not merged: ok=%v %+v", ok, scout)
	}
	if scout.SingleShot == nil || !*scout.SingleShot {
		t.Fatalf("single_shot not loaded: %+v", scout)
	}
	if !scout.HasEditingTool() {
		t.Fatalf("scout has no tools: should default to the full core list")
	}
	if _, ok := cfg.Agent("planner"); ok {
		t.Fatalf("planner must not exist without an agents: entry")
	}
	if _, err := cfg.AgentMust("planner"); err == nil || !strings.Contains(err.Error(), `agent "planner" is not configured`) {
		t.Fatalf("AgentMust(planner) = %v, want not-configured error", err)
	}
}

func TestLoadDefaultFlowValidation(t *testing.T) {
	ok := "projects:\n  proj:\n    source_path: /tmp/proj\n    default_flow: [scout, coder]\n"

	if _, err := loadFromString(t, "agents:\n  scout:\n    system_prompt: x\n  coder:\n    system_prompt: y\n"+ok); err != nil {
		t.Fatalf("valid default_flow: %v", err)
	}
	cfg, _ := loadFromString(t, "agents:\n  scout:\n    system_prompt: x\n  coder:\n    system_prompt: y\n"+ok)
	if got := cfg.Projects["proj"].DefaultFlow; len(got) != 2 || got[0] != "scout" || got[1] != "coder" {
		t.Fatalf("DefaultFlow = %v", got)
	}

	// Unknown agent id in default_flow.
	_, err := loadFromString(t, "agents:\n  scout:\n    system_prompt: x\n"+"projects:\n  proj:\n    default_flow: [scout, ghost]\n")
	if err == nil || !strings.Contains(err.Error(), "projects.proj.default_flow: agent \"ghost\" is not configured") {
		t.Fatalf("want unknown default_flow agent error, got %v", err)
	}

	// Empty list.
	_, err = loadFromString(t, "agents:\n  scout:\n    system_prompt: x\n"+"projects:\n  proj:\n    default_flow: []\n")
	if err == nil || !strings.Contains(err.Error(), "default_flow: list must not be empty") {
		t.Fatalf("want empty default_flow error, got %v", err)
	}

	// Too many agents (9).
	_, err = loadFromString(t,
		"agents:\n"+
			"  a1:\n    system_prompt: x\n  a2:\n    system_prompt: x\n  a3:\n    system_prompt: x\n  a4:\n    system_prompt: x\n  a5:\n    system_prompt: x\n  a6:\n    system_prompt: x\n  a7:\n    system_prompt: x\n  a8:\n    system_prompt: x\n  a9:\n    system_prompt: x\n"+
			"projects:\n  proj:\n    default_flow: [a1, a2, a3, a4, a5, a6, a7, a8, a9]\n")
	if err == nil || !strings.Contains(err.Error(), "default_flow: too many agents (max 8)") {
		t.Fatalf("want too-many default_flow error, got %v", err)
	}
}

func TestLoadRejectsBadGlobalAgentTimeout(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  planner:
    system_prompt: "You are a planner."
    model:
      timeout: ten-minutes
`)
	if err == nil || !strings.Contains(err.Error(), "agents.planner") || !strings.Contains(err.Error(), "model.timeout") {
		t.Fatalf("want agents.planner timeout error, got %v", err)
	}
}

func TestLoadRejectsNegativeSingleShotContextBytes(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  scout:
    system_prompt: "You are a scout."
    single_shot: true
    single_shot_context_bytes: -1
`)
	if err == nil || !strings.Contains(err.Error(), "single_shot_context_bytes") {
		t.Fatalf("want single_shot_context_bytes error, got %v", err)
	}
}

func TestLoadAcceptsSingleShotAgent(t *testing.T) {
	// single_shot is legal for ANY agent now (no implementer ban).
	cfg, err := loadFromString(t, `
agents:
  implementer:
    system_prompt: "You are an implementer."
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
	ac := cfg.Agents["implementer"]
	if ac.SingleShot == nil || !*ac.SingleShot {
		t.Fatalf("single_shot not loaded: %+v", ac)
	}
	if ac.SingleShotContextBytes != 32768 {
		t.Fatalf("single_shot_context_bytes = %d", ac.SingleShotContextBytes)
	}
	if len(ac.ContextFiles) != 2 || ac.ContextFiles[0] != "main.go" {
		t.Fatalf("context_files = %v", ac.ContextFiles)
	}
	if ac.Model.Timeout != "24h" {
		t.Fatalf("timeout = %q", ac.Model.Timeout)
	}
}

func TestLoadAcceptsKnownAdjudicators(t *testing.T) {
	for _, name := range []string{"null", "self", "human"} {
		cfg, err := loadFromString(t, "agents:\n  planner:\n    system_prompt: x\n    adjudicator: \""+name+"\"\n")
		if err != nil {
			t.Fatalf("adjudicator %q: load: %v", name, err)
		}
		if cfg.Agents["planner"].Adjudicator != name {
			t.Fatalf("adjudicator = %q, want %q", cfg.Agents["planner"].Adjudicator, name)
		}
	}
	// Unset inherits the generic default, which is the human gate.
	cfg, err := loadFromString(t, "agents:\n  coder:\n    system_prompt: x\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	merged, ok := cfg.Agent("coder")
	if !ok || merged.Adjudicator != "human" {
		t.Fatalf("default adjudicator = %+v ok=%v, want human", merged, ok)
	}
	if merged.MaxAttempts != 3 || merged.Loops != 1 || merged.Rubric == "" {
		t.Fatalf("generic defaults not merged: %+v", merged)
	}
	if merged.Model.Provider != "openai" || merged.Model.Model != "gpt-4o-mini" {
		t.Fatalf("default_model not inherited: %+v", merged.Model)
	}
}

func TestLoadRejectsUnknownAdjudicator(t *testing.T) {
	_, err := loadFromString(t, `
agents:
  researcher:
    system_prompt: "You are a researcher."
    adjudicator: selff
`)
	if err == nil || !strings.Contains(err.Error(), `agents.researcher: unknown adjudicator "selff"`) {
		t.Fatalf("want agents.researcher adjudicator error, got %v", err)
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
		"agent field (stale token_budget)":            "agents:\n  researcher:\n    system_prompt: x\n    token_budget: 1000\n",
		"agent field (removed system_prompt_append)":  "agents:\n  researcher:\n    system_prompt: x\n    system_prompt_append: y\n",
		"project field (removed guardrails)":          "projects:\n  bunny:\n    guardrails:\n      effort_gate_min: high\n",
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
	cfg, err := LoadFrom(writeConfig(t, `
agents:
  researcher:
    system_prompt: "You are a Researcher agent."
    tools: [read_file, list_directory, grep_search, write_output]
  coder:
    system_prompt: "You are an implementer."
projects:
  bunny:
    source_path: /tmp/bunny
    default_flow: [researcher, coder]
    git:
      repo_url: /tmp/bunny
      base_branch: main
`))
	if err != nil {
		t.Fatalf("valid config should load: %v", err)
	}
	if cfg.Projects["bunny"].Git == nil || cfg.Projects["bunny"].Git.RepoURL != "/tmp/bunny" {
		t.Fatalf("project not parsed: %+v", cfg.Projects["bunny"])
	}
	if got := cfg.Projects["bunny"].DefaultFlow; len(got) != 2 || got[0] != "researcher" || got[1] != "coder" {
		t.Fatalf("default_flow = %v", got)
	}
}

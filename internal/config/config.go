package config

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ModelConfig describes the default LLM model to use.
type ModelConfig struct {
	Provider   string        `yaml:"provider"`
	Model      string        `yaml:"model"`
	APIKeyEnv  string        `yaml:"api_key_env"`
	BaseURL    string        `yaml:"base_url"`
	Timeout    string        `yaml:"timeout"`
	TimeoutDur time.Duration `yaml:"-"`
}

// ReadFileConfig configures the read_file tool.
type ReadFileConfig struct {
	MaxBytes int `yaml:"max_bytes"`
	MaxLines int `yaml:"max_lines"`
}

// ToolsConfig configures the core toolset.
type ToolsConfig struct {
	ReadFile ReadFileConfig `yaml:"read_file"`
}

// AdapterConfig declares an external adapter by name and manifest path.
type AdapterConfig struct {
	Name         string `yaml:"name"`
	ManifestPath string `yaml:"manifest_path"`
}

// AgentConfig configures one user-defined agent id under the top-level agents:
// block. Agent ids are arbitrary user-chosen slugs (see validateAgents); there
// are no built-in prompts.
//
// Merge layers (see Config.Agent): generic defaults (default_model, human
// adjudicator, attempt caps) → global agents.<id>.
type AgentConfig struct {
	// ID is the agent's key under agents: in config. It is populated by the
	// accessors (Agent/AgentMust), not read from YAML.
	ID           string      `yaml:"-" json:"id,omitempty"`
	Model        ModelConfig `yaml:"model" json:"model,omitempty"`
	Temperature  *float64    `yaml:"temperature" json:"temperature,omitempty"`
	MaxTokens    int         `yaml:"max_tokens" json:"max_tokens,omitempty"`
	SystemPrompt string      `yaml:"system_prompt" json:"system_prompt,omitempty"` // required; no built-in default
	Tools        []string    `yaml:"tools" json:"tools,omitempty"`                 // optional allowlist; empty = full core tool list
	MCPServers   []string    `yaml:"mcp_servers" json:"mcp_servers,omitempty"`     // per-agent MCP server allowlist; "*" = every configured server
	Adjudicator  string      `yaml:"adjudicator" json:"adjudicator,omitempty"`
	MaxAttempts  int         `yaml:"max_attempts" json:"max_attempts,omitempty"`
	Loops        int         `yaml:"loops" json:"loops,omitempty"`
	Rubric       string      `yaml:"rubric" json:"rubric,omitempty"`
	// SingleShot bypasses the tool-call loop: one GenerateContent call, the reply
	// text is the phase output. Pointer for tri-state merge (nil = inherit).
	SingleShot *bool `yaml:"single_shot" json:"single_shot,omitempty"`
	// Sideload exempts this agent's model from inference lifecycle control:
	// the phase skips the exclusive-mode lock, the explicit load, and the
	// unload, and the model is protected from other phases' evictions. For a
	// small always-resident model that coexists with the swapped main model.
	// Pointer for tri-state merge (nil = inherit).
	Sideload *bool `yaml:"sideload" json:"sideload,omitempty"`
	// SingleShotContextBytes caps the repo digest pre-stuffed into a single-shot
	// prompt; 0 = no digest unless ContextFiles is set (then a 64 KiB default).
	SingleShotContextBytes int `yaml:"single_shot_context_bytes" json:"single_shot_context_bytes,omitempty"`
	// ContextFiles pins exact repo-relative paths for pre-stuffing instead of the
	// auto digest (still bounded by SingleShotContextBytes).
	ContextFiles []string `yaml:"context_files" json:"context_files,omitempty"`
}

// ProjectGitConfig is git workspace settings for a project (YAML + synced config_json).
type ProjectGitConfig struct {
	RepoURL     string         `yaml:"repo_url" json:"repo_url,omitempty"`
	BaseBranch  string         `yaml:"base_branch" json:"base_branch,omitempty"`
	Push        bool           `yaml:"push" json:"push,omitempty"`
	CreatePR    bool           `yaml:"create_pr" json:"create_pr,omitempty"`
	AuthorName  string         `yaml:"author_name" json:"author_name,omitempty"`
	AuthorEmail string         `yaml:"author_email" json:"author_email,omitempty"`
	Auth        ProjectGitAuth `yaml:"auth" json:"auth,omitempty"`
}

// ProjectGitAuth selects credential mode (credentials live in the environment).
type ProjectGitAuth struct {
	Type       string `yaml:"type" json:"type,omitempty"`
	SSHKeyPath string `yaml:"ssh_key_path" json:"ssh_key_path,omitempty"`
	TokenEnv   string `yaml:"token_env" json:"token_env,omitempty"`
	GHProfile  string `yaml:"gh_profile" json:"gh_profile,omitempty"`
}

// ProjectTestConfig is the immutable run_test command block for a project.
type ProjectTestConfig struct {
	Command    string   `yaml:"command" json:"command,omitempty"`
	Timeout    string   `yaml:"timeout" json:"timeout,omitempty"`
	Image      string   `yaml:"image" json:"image,omitempty"`
	CPU        string   `yaml:"cpu" json:"cpu,omitempty"`
	Memory     string   `yaml:"memory" json:"memory,omitempty"`
	SecretsEnv []string `yaml:"secrets_env" json:"secrets_env,omitempty"`
	Runtime    string   `yaml:"runtime" json:"runtime,omitempty"`
}

// ProjectConfig is one entry under the top-level projects: map (YAML source of truth).
type ProjectConfig struct {
	SourcePath    string             `yaml:"source_path" json:"source_path,omitempty"`
	Git           *ProjectGitConfig  `yaml:"git" json:"git,omitempty"`
	Test          *ProjectTestConfig `yaml:"test" json:"test,omitempty"`
	TrustExternal bool               `yaml:"trust_external" json:"trust_external,omitempty"`
	// DefaultFlow is the ordered list of agent ids used for webhook/adapter/CLI
	// submits that carry no flow; it also prefills the web submit form.
	DefaultFlow []string `yaml:"default_flow" json:"default_flow,omitempty"`
}

// InferenceConfig configures explicit model-lifecycle control of a
// model-swapping inference server. When the block is absent, behavior is
// unchanged: the server swaps models implicitly per request.
type InferenceConfig struct {
	Type string `yaml:"type"` // llama-swap (only supported value)
	// BaseURL is the management base (chat API at <base>/v1).
	BaseURL string `yaml:"base_url"`
	// Mode: "" | exclusive. exclusive serializes ALL phases against this
	// server via a process-wide keyed lock.
	Mode string `yaml:"mode"`
}

// ServerConfig configures the serve daemon HTTP surface and worker pool.
type ServerConfig struct {
	Listen              string        `yaml:"listen"`
	MaxConcurrentIssues int           `yaml:"max_concurrent_issues"`
	ShutdownTimeout     string        `yaml:"shutdown_timeout"`
	ShutdownTimeoutDur  time.Duration `yaml:"-"`
	PublicBaseURL       string        `yaml:"public_base_url"`
}

// OIDCConfig configures OpenID Connect authentication.
type OIDCConfig struct {
	IssuerURL       string   `yaml:"issuer_url"`
	ClientID        string   `yaml:"client_id"`
	ClientSecretEnv string   `yaml:"client_secret_env"`
	Scopes          []string `yaml:"scopes"`
}

// AuthConfig configures authentication for the serve daemon.
type AuthConfig struct {
	Mode                 string        `yaml:"mode"` // local | oidc | disabled (tests only)
	LocalUsername        string        `yaml:"local_username"`
	LocalPasswordEnv     string        `yaml:"local_password_env"`
	OIDC                 OIDCConfig    `yaml:"oidc"`
	BootstrapAdminEmails []string      `yaml:"bootstrap_admin_emails"`
	SessionTTL           string        `yaml:"session_ttl"`
	SessionTTLDur        time.Duration `yaml:"-"`
}

// NotificationsConfig configures notification sinks for the serve daemon.
type NotificationsConfig struct {
	// Adapters lists adapter names (from top-level adapters) that implement
	// the notification port. Console is always enabled when serve runs.
	Adapters []string `yaml:"adapters"`
}

// MCPToolConstraint is a simple call-time restriction on an MCP tool argument.
// Unknown Type values are rejected at config load (fail closed).
type MCPToolConstraint struct {
	// Type: arg_prefix | arg_deny_substring | url_allowlist
	Type   string   `yaml:"type" json:"type"`
	Arg    string   `yaml:"arg" json:"arg"`                           // argument name in the tool args object
	Prefix string   `yaml:"prefix,omitempty" json:"prefix,omitempty"` // for arg_prefix
	Values []string `yaml:"values,omitempty" json:"values,omitempty"` // deny substrings (arg_deny_substring)
	Hosts  []string `yaml:"hosts,omitempty" json:"hosts,omitempty"`   // allowed hosts for url_allowlist
}

// MCPToolGrant allowlists one tool from an MCP server (optional constraints).
type MCPToolGrant struct {
	Name        string              `yaml:"name" json:"name"`
	Constraints []MCPToolConstraint `yaml:"constraints,omitempty" json:"constraints,omitempty"`
}

// MCPServerConfig declares an MCP server (stdio transport).
// Empty Tools = Phase 4 compat (all tools from the server when agent lists the server).
// Non-empty Tools = only listed tool names are exposed; others never advertised.
type MCPServerConfig struct {
	Name    string         `yaml:"name"`
	Command []string       `yaml:"command"` // binary + optional fixed args prefix
	Args    []string       `yaml:"args"`
	Env     []string       `yaml:"env"` // host env var NAMES to pass through
	Tools   []MCPToolGrant `yaml:"tools,omitempty"`
}

// WebhookTriggerConfig configures the built-in HTTP webhook trigger.
type WebhookTriggerConfig struct {
	Enabled  bool   `yaml:"enabled"`
	TokenEnv string `yaml:"token_env"` // env var holding shared secret
}

// TriggersConfig configures external and built-in issue sources.
type TriggersConfig struct {
	Webhook       WebhookTriggerConfig `yaml:"webhook"`
	Adapters      []string             `yaml:"adapters"`       // adapter names with port: trigger
	TrustExternal bool                 `yaml:"trust_external"` // skip forced human implementer gate
}

// StorageBackendConfig selects filesystem vs adapter-backed StoragePort.
type StorageBackendConfig struct {
	Backend     string `yaml:"backend"`      // fs | adapter
	AdapterName string `yaml:"adapter_name"` // when backend=adapter
}

// Config is the top-level user configuration.
// ProviderBudgetConfig is a session/context-window token gate for one LLM provider.
// Applied per agent phase run (fresh counter each phase). Zero TokenBudget = unlimited.
type ProviderBudgetConfig struct {
	TokenBudget int `yaml:"token_budget" json:"token_budget,omitempty"`
	// WarnPct is the fraction (1–100) at which to notify once per phase attempt. Default 80 when budget > 0.
	WarnPct int `yaml:"warn_pct" json:"warn_pct,omitempty"`
}

// EscalationRule is one YAML-configured threshold → notification rule.
type EscalationRule struct {
	Name      string `yaml:"name"`
	When      string `yaml:"when"`      // consecutive_failures | budget_exceeded | sandbox_refused | phase_failed
	Project   string `yaml:"project"`   // registered name or "*" for all
	Threshold int    `yaml:"threshold"` // e.g. consecutive count; default 1
	Notify    string `yaml:"notify"`    // admin | console
}

// EscalationConfig holds YAML-only escalation rules (admin page is read-only).
type EscalationConfig struct {
	Rules []EscalationRule `yaml:"rules"`
}

// Config is the process configuration (YAML source of truth).
type Config struct {
	StorageRoot   string                          `yaml:"storage_root"`
	DBPath        string                          `yaml:"db_path"`
	DefaultModel  ModelConfig                     `yaml:"default_model"`
	Tools         ToolsConfig                     `yaml:"tools"`
	Adapters      []AdapterConfig                 `yaml:"adapters"`
	Agents        map[string]AgentConfig          `yaml:"agents"`
	Projects      map[string]ProjectConfig        `yaml:"projects"`
	Providers     map[string]ProviderBudgetConfig `yaml:"providers"`
	MCPServers    []MCPServerConfig               `yaml:"mcp_servers"`
	Triggers      TriggersConfig                  `yaml:"triggers"`
	Storage       StorageBackendConfig            `yaml:"storage"`
	Inference     InferenceConfig                 `yaml:"inference"`
	Server        ServerConfig                    `yaml:"server"`
	Auth          AuthConfig                      `yaml:"auth"`
	Notifications NotificationsConfig             `yaml:"notifications"`
	Escalation    EscalationConfig                `yaml:"escalation"`
}

// HomeDir returns the user's home directory.
func HomeDir() (string, error) {
	return os.UserHomeDir()
}

// Load reads the config from ~/.config/gorchestrator/config.yaml.
// Missing values are defaulted where possible.
func Load() (*Config, error) {
	home, err := HomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "gorchestrator", "config.yaml")
	return LoadFrom(path)
}

// LoadFrom reads the config from the specified path and applies defaults.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Friendly pre-pass: flag the removed project-level agents block before the
	// strict decoder rejects it with an opaque unknown-field error.
	var probe struct {
		Projects map[string]struct {
			Agents any `yaml:"agents"`
		} `yaml:"projects"`
	}
	if perr := yaml.Unmarshal(data, &probe); perr == nil {
		for name, pc := range probe.Projects {
			if pc.Agents != nil {
				return nil, fmt.Errorf("projects.%s.agents was removed; agent ids are now global under top-level agents: and flows are chosen per issue", name)
			}
		}
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // fail on unknown keys instead of silently dropping them
	if err := dec.Decode(&cfg); err != nil && err != io.EOF {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	home, err := HomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home dir: %w", err)
	}

	if cfg.StorageRoot == "" {
		cfg.StorageRoot = filepath.Join(home, ".config", "gorchestrator", "storage")
	}
	cfg.StorageRoot = expandTilde(cfg.StorageRoot, home)

	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(home, ".config", "gorchestrator", "gorchestrator.db")
	}
	cfg.DBPath = expandTilde(cfg.DBPath, home)

	if cfg.DefaultModel.Provider == "" {
		cfg.DefaultModel.Provider = "openai"
	}
	if cfg.DefaultModel.Model == "" {
		cfg.DefaultModel.Model = "gpt-4o-mini"
	}
	if cfg.DefaultModel.APIKeyEnv == "" {
		cfg.DefaultModel.APIKeyEnv = "OPENAI_API_KEY"
	}
	if cfg.DefaultModel.Timeout == "" {
		cfg.DefaultModel.Timeout = "60s"
	}
	cfg.DefaultModel.TimeoutDur, err = time.ParseDuration(cfg.DefaultModel.Timeout)
	if err != nil {
		return nil, fmt.Errorf("parse default_model.timeout: %w", err)
	}

	if cfg.Tools.ReadFile.MaxBytes == 0 {
		cfg.Tools.ReadFile.MaxBytes = 64 * 1024
	}
	if cfg.Tools.ReadFile.MaxLines == 0 {
		cfg.Tools.ReadFile.MaxLines = 2000
	}

	if err := applyServerDefaults(&cfg); err != nil {
		return nil, err
	}
	if err := applyAuthDefaults(&cfg); err != nil {
		return nil, err
	}
	if err := validateAgents(&cfg); err != nil {
		return nil, err
	}
	if err := normalizeProjects(&cfg, home); err != nil {
		return nil, err
	}
	normalizeProviders(&cfg)
	if err := validateMCPServers(&cfg); err != nil {
		return nil, err
	}
	if err := validateEscalation(&cfg); err != nil {
		return nil, err
	}
	if err := validateInference(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validateInference rejects malformed inference blocks. An absent block is
// valid and means no lifecycle control (implicit swap-on-request).
func validateInference(cfg *Config) error {
	inf := &cfg.Inference
	if inf.Type == "" {
		return nil
	}
	if inf.Type != "llama-swap" {
		return fmt.Errorf("inference.type: unknown type %q (want llama-swap)", inf.Type)
	}
	if inf.BaseURL == "" {
		return fmt.Errorf("inference.base_url is required when inference.type is set")
	}
	u, err := url.Parse(inf.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("inference.base_url: invalid URL %q", inf.BaseURL)
	}
	switch inf.Mode {
	case "", "exclusive":
	default:
		return fmt.Errorf("inference.mode: unknown mode %q (want exclusive or empty)", inf.Mode)
	}
	return nil
}

func normalizeProjects(cfg *Config, home string) error {
	if cfg.Projects == nil {
		cfg.Projects = map[string]ProjectConfig{}
		return nil
	}
	for name, pc := range cfg.Projects {
		if name == "" {
			return fmt.Errorf("projects: empty project name is not allowed")
		}
		if pc.SourcePath != "" {
			pc.SourcePath = expandTilde(pc.SourcePath, home)
		}
		if pc.DefaultFlow != nil {
			if len(pc.DefaultFlow) == 0 {
				return fmt.Errorf("projects.%s.default_flow: list must not be empty", name)
			}
			if len(pc.DefaultFlow) > 8 {
				return fmt.Errorf("projects.%s.default_flow: too many agents (max 8), got %d", name, len(pc.DefaultFlow))
			}
			for _, id := range pc.DefaultFlow {
				if _, ok := cfg.Agents[id]; !ok {
					return fmt.Errorf("projects.%s.default_flow: agent %q is not configured under agents:", name, id)
				}
			}
		}
		cfg.Projects[name] = pc
	}
	return nil
}

var agentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// validateAgents rejects malformed global agent entries that would otherwise
// be silently degraded at runtime (e.g. a typo'd model.timeout falling back
// to 60s in modelTimeout, or an unknown tool name).
func validateAgents(cfg *Config) error {
	core := make(map[string]struct{}, len(KnownToolNames))
	for _, name := range KnownToolNames {
		core[name] = struct{}{}
	}
	mcp := make(map[string]struct{}, len(cfg.MCPServers))
	for _, s := range cfg.MCPServers {
		mcp[s.Name] = struct{}{}
	}
	seen := map[string]string{} // lowercased id → id (catch case-only duplicates)
	for id, ac := range cfg.Agents {
		if !agentIDPattern.MatchString(id) {
			return fmt.Errorf("agents.%s: invalid agent id (want ^[a-z0-9][a-z0-9_-]{0,31}$, lowercase)", id)
		}
		if other, dup := seen[strings.ToLower(id)]; dup {
			return fmt.Errorf("agents.%s: duplicate of %q (ids differ only by case)", id, other)
		}
		seen[strings.ToLower(id)] = id
		if strings.TrimSpace(ac.SystemPrompt) == "" {
			return fmt.Errorf("agents.%s: system_prompt is required", id)
		}
		for _, name := range ac.Tools {
			if _, ok := core[name]; !ok {
				return fmt.Errorf("agents.%s: unknown tool %q", id, name)
			}
		}
		for _, name := range ac.MCPServers {
			if name == "*" {
				continue
			}
			if _, ok := mcp[name]; !ok {
				return fmt.Errorf("agents.%s: unknown mcp server %q (declare it under mcp_servers:, or use \"*\")", id, name)
			}
		}
		if ac.Model.Timeout != "" {
			if _, err := time.ParseDuration(ac.Model.Timeout); err != nil {
				return fmt.Errorf("agents.%s: parse model.timeout: %w", id, err)
			}
		}
		if ac.Adjudicator != "" {
			switch ac.Adjudicator {
			case "null", "self", "human":
			default:
				return fmt.Errorf("agents.%s: unknown adjudicator %q (want null|self|human)", id, ac.Adjudicator)
			}
		}
		if ac.SingleShotContextBytes < 0 {
			return fmt.Errorf("agents.%s: single_shot_context_bytes must be >= 0, got %d", id, ac.SingleShotContextBytes)
		}
	}
	return nil
}

func validateMCPServers(cfg *Config) error {
	for i, s := range cfg.MCPServers {
		if s.Name == "" {
			return fmt.Errorf("mcp_servers[%d]: name is required", i)
		}
		if len(s.Command) == 0 {
			return fmt.Errorf("mcp_servers[%d] (%s): command is required", i, s.Name)
		}
		seen := map[string]struct{}{}
		for j, t := range s.Tools {
			if t.Name == "" {
				return fmt.Errorf("mcp_servers[%d] (%s).tools[%d]: name is required", i, s.Name, j)
			}
			if _, ok := seen[t.Name]; ok {
				return fmt.Errorf("mcp_servers[%d] (%s).tools: duplicate tool %q", i, s.Name, t.Name)
			}
			seen[t.Name] = struct{}{}
			for k, c := range t.Constraints {
				switch c.Type {
				case "arg_prefix":
					if c.Arg == "" || c.Prefix == "" {
						return fmt.Errorf("mcp_servers[%d] (%s).tools[%s].constraints[%d]: arg_prefix requires arg and prefix", i, s.Name, t.Name, k)
					}
				case "arg_deny_substring":
					if c.Arg == "" || len(c.Values) == 0 {
						return fmt.Errorf("mcp_servers[%d] (%s).tools[%s].constraints[%d]: arg_deny_substring requires arg and values", i, s.Name, t.Name, k)
					}
				case "url_allowlist":
					if c.Arg == "" || len(c.Hosts) == 0 {
						return fmt.Errorf("mcp_servers[%d] (%s).tools[%s].constraints[%d]: url_allowlist requires arg and hosts", i, s.Name, t.Name, k)
					}
				default:
					return fmt.Errorf("mcp_servers[%d] (%s).tools[%s].constraints[%d]: unknown type %q (want arg_prefix|arg_deny_substring|url_allowlist)", i, s.Name, t.Name, k, c.Type)
				}
			}
		}
	}
	return nil
}

func validateEscalation(cfg *Config) error {
	for i := range cfg.Escalation.Rules {
		r := &cfg.Escalation.Rules[i]
		if r.Name == "" {
			return fmt.Errorf("escalation.rules[%d]: name is required", i)
		}
		switch r.When {
		case "consecutive_failures", "budget_exceeded", "sandbox_refused", "phase_failed":
		default:
			return fmt.Errorf("escalation.rules[%d] (%s): unknown when %q", i, r.Name, r.When)
		}
		if r.Threshold <= 0 {
			r.Threshold = 1
		}
		if r.Notify == "" {
			r.Notify = "admin"
		}
		switch r.Notify {
		case "admin", "console":
		default:
			return fmt.Errorf("escalation.rules[%d] (%s): notify must be admin|console", i, r.Name)
		}
		if r.Project == "" {
			r.Project = "*"
		}
	}
	return nil
}

func normalizeProviders(cfg *Config) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderBudgetConfig{}
		return
	}
	for name, p := range cfg.Providers {
		if p.TokenBudget > 0 && p.WarnPct <= 0 {
			p.WarnPct = 80
		}
		if p.WarnPct > 100 {
			p.WarnPct = 100
		}
		cfg.Providers[name] = p
	}
}

// ProviderBudget returns the session budget for a provider name (case-sensitive as configured).
// Missing provider or zero budget → unlimited (ok=false or TokenBudget==0).
func (c *Config) ProviderBudget(provider string) (ProviderBudgetConfig, bool) {
	if c == nil || c.Providers == nil || provider == "" {
		return ProviderBudgetConfig{}, false
	}
	p, ok := c.Providers[provider]
	return p, ok
}

func applyServerDefaults(cfg *Config) error {
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8080"
	}
	if cfg.Server.MaxConcurrentIssues <= 0 {
		cfg.Server.MaxConcurrentIssues = 2
	}
	if cfg.Server.ShutdownTimeout == "" {
		cfg.Server.ShutdownTimeout = "30s"
	}
	d, err := time.ParseDuration(cfg.Server.ShutdownTimeout)
	if err != nil {
		return fmt.Errorf("parse server.shutdown_timeout: %w", err)
	}
	cfg.Server.ShutdownTimeoutDur = d
	if cfg.Server.PublicBaseURL == "" {
		cfg.Server.PublicBaseURL = "http://" + cfg.Server.Listen
	}
	return nil
}

func applyAuthDefaults(cfg *Config) error {
	if cfg.Auth.Mode == "" {
		cfg.Auth.Mode = "local"
	}
	switch cfg.Auth.Mode {
	case "local", "oidc", "disabled":
	default:
		return fmt.Errorf("auth.mode must be local, oidc, or disabled; got %q", cfg.Auth.Mode)
	}
	if cfg.Auth.LocalUsername == "" {
		cfg.Auth.LocalUsername = "admin"
	}
	if cfg.Auth.LocalPasswordEnv == "" {
		cfg.Auth.LocalPasswordEnv = "GORCH_LOCAL_PASSWORD"
	}
	if cfg.Auth.OIDC.ClientSecretEnv == "" {
		cfg.Auth.OIDC.ClientSecretEnv = "GORCH_OIDC_CLIENT_SECRET"
	}
	if len(cfg.Auth.OIDC.Scopes) == 0 {
		cfg.Auth.OIDC.Scopes = []string{"openid", "profile", "email"}
	}
	if cfg.Auth.SessionTTL == "" {
		cfg.Auth.SessionTTL = "168h"
	}
	d, err := time.ParseDuration(cfg.Auth.SessionTTL)
	if err != nil {
		return fmt.Errorf("parse auth.session_ttl: %w", err)
	}
	cfg.Auth.SessionTTLDur = d
	return nil
}

func expandTilde(path, home string) string {
	if path == "~" {
		return home
	}
	if len(path) > 2 && path[:2] == "~/" {
		return filepath.Join(home, path[2:])
	}
	return path
}

// KnownToolNames is the canonical set of core tool names. An agent's Tools
// allowlist may only name tools from this set (config validation fails
// closed on anything else). internal/tools uses this list so the two never
// drift.
var KnownToolNames = []string{
	"read_file",
	"list_directory",
	"grep_search",
	"write_output",
	"write_file",
	"update_file",
	"run_test",
}

var editingToolNames = map[string]struct{}{
	"write_file":  {},
	"update_file": {},
	"run_test":    {},
}

// HasEditingTool reports whether the agent's effective tool list includes a
// file-mutating tool. An empty Tools list means the full core list, which
// includes the editing tools.
func (a AgentConfig) HasEditingTool() bool {
	if len(a.Tools) == 0 {
		return true
	}
	for _, name := range a.Tools {
		if _, ok := editingToolNames[name]; ok {
			return true
		}
	}
	return false
}

// Agent returns the config for a configured agent id. ok=false when the id
// is not defined under agents:. The returned config carries ID set to the
// agent's key.
func (c *Config) Agent(id string) (AgentConfig, bool) {
	ovr, ok := c.Agents[id]
	if !ok {
		return AgentConfig{}, false
	}
	merged := MergeAgent(defaultAgentConfig(c.DefaultModel), ovr)
	merged.ID = id
	return merged, true
}

// AgentMust is the strict form used by the pipeline.
func (c *Config) AgentMust(id string) (AgentConfig, error) {
	ac, ok := c.Agent(id)
	if !ok {
		return AgentConfig{}, fmt.Errorf("agent %q is not configured under agents: in config", id)
	}
	return ac, nil
}

// AgentIDs returns sorted configured ids (for UI/API).
func (c *Config) AgentIDs() []string {
	ids := make([]string, 0, len(c.Agents))
	for id := range c.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// MergeAgent overlays non-zero fields from overlay onto base.
// Since SystemPrompt has no default, the merge never invents one — config
// validation guarantees it exists on the merged result.
func MergeAgent(base, overlay AgentConfig) AgentConfig {
	out := base
	if overlay.Model.Provider != "" {
		out.Model.Provider = overlay.Model.Provider
	}
	if overlay.Model.Model != "" {
		out.Model.Model = overlay.Model.Model
	}
	if overlay.Model.APIKeyEnv != "" {
		out.Model.APIKeyEnv = overlay.Model.APIKeyEnv
	}
	if overlay.Model.BaseURL != "" {
		out.Model.BaseURL = overlay.Model.BaseURL
	}
	if overlay.Model.Timeout != "" {
		out.Model.Timeout = overlay.Model.Timeout
	}
	if overlay.SystemPrompt != "" {
		out.SystemPrompt = overlay.SystemPrompt
	}
	if overlay.Temperature != nil {
		t := *overlay.Temperature
		out.Temperature = &t
	}
	if overlay.MaxTokens > 0 {
		out.MaxTokens = overlay.MaxTokens
	}
	if len(overlay.Tools) > 0 {
		out.Tools = append([]string(nil), overlay.Tools...)
	}
	if len(overlay.MCPServers) > 0 {
		out.MCPServers = append([]string(nil), overlay.MCPServers...)
	}
	if overlay.Adjudicator != "" {
		out.Adjudicator = overlay.Adjudicator
	}
	if overlay.MaxAttempts > 0 {
		out.MaxAttempts = overlay.MaxAttempts
	}
	if overlay.Loops > 0 {
		out.Loops = overlay.Loops
	}
	if overlay.Rubric != "" {
		out.Rubric = overlay.Rubric
	}
	if overlay.SingleShot != nil {
		s := *overlay.SingleShot
		out.SingleShot = &s
	}
	if overlay.Sideload != nil {
		s := *overlay.Sideload
		out.Sideload = &s
	}
	if overlay.SingleShotContextBytes > 0 {
		out.SingleShotContextBytes = overlay.SingleShotContextBytes
	}
	if len(overlay.ContextFiles) > 0 {
		out.ContextFiles = append([]string(nil), overlay.ContextFiles...)
	}
	return out
}

func defaultAgentConfig(defaultModel ModelConfig) AgentConfig {
	return AgentConfig{
		Model: ModelConfig{
			Provider:  defaultModel.Provider,
			Model:     defaultModel.Model,
			APIKeyEnv: defaultModel.APIKeyEnv,
			BaseURL:   defaultModel.BaseURL,
			Timeout:   defaultModel.Timeout,
		},
		Adjudicator: "human",
		MaxAttempts: 3,
		Loops:       1,
		Rubric:      "The output is complete, accurate, and ready for the next step.",
	}
}

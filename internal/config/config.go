package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// AgentConfig overrides the orchestrator's defaults for a specific agent type.
// Supported keys: "researcher", "planner", "implementer".
//
// Merge layers (see Config.Agent): built-in defaults → default_model →
// agents.<type> YAML → per-issue overrides (applied by the orchestrator).
type AgentConfig struct {
	Model              ModelConfig `yaml:"model"`
	Temperature        *float64    `yaml:"temperature"`
	MaxTokens          int         `yaml:"max_tokens"`
	SystemPrompt       string      `yaml:"system_prompt"`        // full override
	SystemPromptAppend string      `yaml:"system_prompt_append"` // appended after base/override
	Tools              []string    `yaml:"tools"`                // core tool name allowlist; empty = all for type
	MCPServers         []string    `yaml:"mcp_servers"`          // per-agent MCP server allowlist
	TokenBudget        int         `yaml:"token_budget"`         // stored only; enforced in Phase 5
	Adjudicator        string      `yaml:"adjudicator"`
	MaxAttempts        int         `yaml:"max_attempts"`
	Loops              int         `yaml:"loops"`
	Rubric             string      `yaml:"rubric"`
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
	IssuerURL      string   `yaml:"issuer_url"`
	ClientID       string   `yaml:"client_id"`
	ClientSecretEnv string  `yaml:"client_secret_env"`
	Scopes         []string `yaml:"scopes"`
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

// MCPServerConfig declares an MCP server (stdio transport).
type MCPServerConfig struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"` // binary + optional fixed args prefix
	Args    []string `yaml:"args"`
	Env     []string `yaml:"env"` // host env var NAMES to pass through
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
type Config struct {
	StorageRoot   string                 `yaml:"storage_root"`
	DBPath        string                 `yaml:"db_path"`
	DefaultModel  ModelConfig            `yaml:"default_model"`
	Tools         ToolsConfig            `yaml:"tools"`
	Adapters      []AdapterConfig        `yaml:"adapters"`
	Agents        map[string]AgentConfig `yaml:"agents"`
	MCPServers    []MCPServerConfig      `yaml:"mcp_servers"`
	Triggers      TriggersConfig         `yaml:"triggers"`
	Storage       StorageBackendConfig   `yaml:"storage"`
	Server        ServerConfig           `yaml:"server"`
	Auth          AuthConfig             `yaml:"auth"`
	Notifications NotificationsConfig    `yaml:"notifications"`
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

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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

	return &cfg, nil
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

// Agent returns the configuration for the named agent type, merging user
// overrides with built-in defaults and global default_model. Unknown agent
// names receive the defaults.
func (c *Config) Agent(name string) AgentConfig {
	def := defaultAgentConfig(name, c.DefaultModel)
	ovr, ok := c.Agents[name]
	if !ok {
		return def
	}
	if ovr.Model.Provider != "" {
		def.Model.Provider = ovr.Model.Provider
	}
	if ovr.Model.Model != "" {
		def.Model.Model = ovr.Model.Model
	}
	if ovr.Model.APIKeyEnv != "" {
		def.Model.APIKeyEnv = ovr.Model.APIKeyEnv
	}
	if ovr.Model.BaseURL != "" {
		def.Model.BaseURL = ovr.Model.BaseURL
	}
	if ovr.Model.Timeout != "" {
		def.Model.Timeout = ovr.Model.Timeout
	}
	if ovr.SystemPrompt != "" {
		def.SystemPrompt = ovr.SystemPrompt
	}
	if ovr.SystemPromptAppend != "" {
		def.SystemPromptAppend = ovr.SystemPromptAppend
	}
	// Apply append after merge so full override + append both work.
	if def.SystemPromptAppend != "" {
		def.SystemPrompt = def.SystemPrompt + "\n\n" + def.SystemPromptAppend
	}
	if ovr.Temperature != nil {
		t := *ovr.Temperature
		def.Temperature = &t
	}
	if ovr.MaxTokens > 0 {
		def.MaxTokens = ovr.MaxTokens
	}
	if len(ovr.Tools) > 0 {
		def.Tools = append([]string(nil), ovr.Tools...)
	}
	if len(ovr.MCPServers) > 0 {
		def.MCPServers = append([]string(nil), ovr.MCPServers...)
	}
	if ovr.TokenBudget > 0 {
		def.TokenBudget = ovr.TokenBudget
	}
	if ovr.Adjudicator != "" {
		def.Adjudicator = ovr.Adjudicator
	}
	if ovr.MaxAttempts > 0 {
		def.MaxAttempts = ovr.MaxAttempts
	}
	if ovr.Loops > 0 {
		def.Loops = ovr.Loops
	}
	if ovr.Rubric != "" {
		def.Rubric = ovr.Rubric
	}
	return def
}

func defaultAgentConfig(name string, defaultModel ModelConfig) AgentConfig {
	cfg := AgentConfig{
		Model: ModelConfig{
			Provider:  defaultModel.Provider,
			Model:     defaultModel.Model,
			APIKeyEnv: defaultModel.APIKeyEnv,
			BaseURL:   defaultModel.BaseURL,
			Timeout:   defaultModel.Timeout,
		},
		Adjudicator: "self",
		MaxAttempts: 3,
		Loops:       1,
		Rubric:      "The output is complete, accurate, and ready for the next phase.",
	}
	switch name {
	case "researcher":
		cfg.SystemPrompt = defaultResearcherPrompt()
	case "planner":
		cfg.SystemPrompt = defaultPlannerPrompt()
	case "implementer":
		cfg.SystemPrompt = defaultImplementerPrompt()
	}
	return cfg
}

func defaultResearcherPrompt() string {
	return `You are a Researcher agent. Investigate the issue, read the project source snapshot, and produce concise findings in the designated output file.

Core tools:
- read_file: read source files (whole-file or surgical line range)
- list_directory: explore the source tree
- grep_search: locate relevant code, then use surgical read_file
- write_output: write your final findings to the orchestrator-designated output file

Rules:
1. Gather context from the allowed paths.
2. Write your findings using write_output.
3. Be concise and actionable for the Planner.
4. When finished, call finish_task with done=true and a brief rationale evaluating your work against the rubric.`
}

func defaultPlannerPrompt() string {
	return `You are a Planner agent. Read the issue and the Researcher's findings, then produce a concrete implementation plan in the designated output file.

Core tools:
- read_file: read source files and previous phase outputs
- list_directory: explore the source tree
- grep_search: locate relevant code
- write_output: write the implementation plan

Rules:
1. Base the plan on the issue and the accepted research output.
2. Include specific files to change and tests to add.
3. Write the plan using write_output.
4. When finished, call finish_task with done=true and a brief rationale evaluating the plan.`
}

func defaultImplementerPrompt() string {
	return `You are an Implementer agent. Read the issue, the accepted research findings, and the accepted plan, then edit the workspace to implement the changes.

Core tools:
- read_file: read files in the workspace or source snapshot
- list_directory: explore the workspace
- grep_search: locate relevant code
- write_file: create new files in the workspace
- update_file: overwrite existing files in the workspace
- run_test: run the project's immutable tests in a container sandbox

Rules:
1. Edit only within the implementer's workspace.
2. Write clean, testable code matching the existing style.
3. Use run_test for test-and-fix when available.
4. When finished, call finish_task with done=true and a brief rationale evaluating the implementation.`
}

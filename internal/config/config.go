package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// Merge layers (see Config.Agent / orchestrator cast): built-in defaults →
// default_model → global agents.<type> → project flavor → frozen issue cast name.
type AgentConfig struct {
	Model              ModelConfig `yaml:"model" json:"model,omitempty"`
	Temperature        *float64    `yaml:"temperature" json:"temperature,omitempty"`
	MaxTokens          int         `yaml:"max_tokens" json:"max_tokens,omitempty"`
	SystemPrompt       string      `yaml:"system_prompt" json:"system_prompt,omitempty"`               // full override
	SystemPromptAppend string      `yaml:"system_prompt_append" json:"system_prompt_append,omitempty"` // appended after base/override
	Tools              []string    `yaml:"tools" json:"tools,omitempty"`                               // core tool name allowlist; empty = all for type
	MCPServers         []string    `yaml:"mcp_servers" json:"mcp_servers,omitempty"`                   // per-agent MCP server allowlist
	TokenBudget        int         `yaml:"token_budget" json:"token_budget,omitempty"`                 // stored only; enforced in Phase 5
	Adjudicator        string      `yaml:"adjudicator" json:"adjudicator,omitempty"`
	MaxAttempts        int         `yaml:"max_attempts" json:"max_attempts,omitempty"`
	Loops              int         `yaml:"loops" json:"loops,omitempty"`
	Rubric             string      `yaml:"rubric" json:"rubric,omitempty"`
}

// CoreAgentTypes are the only agent type keys allowed under projects.*.agents.
var CoreAgentTypes = []string{"researcher", "planner", "implementer"}

// ProjectAgentConfig is the per-type flavor catalog under projects.<name>.agents.<type>.
type ProjectAgentConfig struct {
	Default string                 `yaml:"default" json:"default,omitempty"`
	Flavors map[string]AgentConfig `yaml:"flavors" json:"flavors,omitempty"`
}

// ProjectGitConfig is git workspace settings for a project (YAML + synced config_json).
type ProjectGitConfig struct {
	RepoURL     string          `yaml:"repo_url" json:"repo_url,omitempty"`
	BaseBranch  string          `yaml:"base_branch" json:"base_branch,omitempty"`
	Push        bool            `yaml:"push" json:"push,omitempty"`
	CreatePR    bool            `yaml:"create_pr" json:"create_pr,omitempty"`
	AuthorName  string          `yaml:"author_name" json:"author_name,omitempty"`
	AuthorEmail string          `yaml:"author_email" json:"author_email,omitempty"`
	Auth        ProjectGitAuth  `yaml:"auth" json:"auth,omitempty"`
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
	SourcePath    string                        `yaml:"source_path" json:"source_path,omitempty"`
	Git           *ProjectGitConfig             `yaml:"git" json:"git,omitempty"`
	Test          *ProjectTestConfig            `yaml:"test" json:"test,omitempty"`
	TrustExternal bool                          `yaml:"trust_external" json:"trust_external,omitempty"`
	Agents        map[string]ProjectAgentConfig `yaml:"agents" json:"agents,omitempty"`
}

// AgentFlavorInfo is the UI/API catalog for one core agent type on a project.
type AgentFlavorInfo struct {
	Default string   `json:"default,omitempty"`
	Flavors []string `json:"flavors"` // names only; empty when no flavors
}

// FlavorCatalog returns per-type flavor names + defaults for submit UI.
// Types with zero or one flavor still appear so the client can hide pickers.
func (p ProjectConfig) FlavorCatalog() map[string]AgentFlavorInfo {
	out := make(map[string]AgentFlavorInfo, len(CoreAgentTypes))
	for _, typ := range CoreAgentTypes {
		info := AgentFlavorInfo{Flavors: []string{}}
		ac, ok := p.Agents[typ]
		if !ok || len(ac.Flavors) == 0 {
			out[typ] = info
			continue
		}
		names := make([]string, 0, len(ac.Flavors))
		for name := range ac.Flavors {
			names = append(names, name)
		}
		// stable order for templates/tests
		sort.Strings(names)
		info.Flavors = names
		info.Default = ac.Default
		if info.Default == "" && len(names) == 1 {
			info.Default = names[0]
		}
		out[typ] = info
	}
	return out
}

// ResolveCast validates and fills agent flavor names for submit.
// requested may be nil or partial; missing keys use project default when flavors exist.
// Empty cast when the project has no flavors for a type.
func (p ProjectConfig) ResolveCast(requested map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, typ := range CoreAgentTypes {
		want, hasWant := requested[typ]
		ac, hasAgents := p.Agents[typ]
		if !hasAgents || len(ac.Flavors) == 0 {
			if hasWant && want != "" {
				return nil, fmt.Errorf("project has no %s flavors; cannot select %q", typ, want)
			}
			continue
		}
		name := want
		if name == "" {
			name = ac.Default
			if name == "" && len(ac.Flavors) == 1 {
				for k := range ac.Flavors {
					name = k
				}
			}
		}
		if name == "" {
			return nil, fmt.Errorf("project agents.%s requires a default or submit choice", typ)
		}
		if _, ok := ac.Flavors[name]; !ok {
			return nil, fmt.Errorf("unknown %s flavor %q", typ, name)
		}
		out[typ] = name
	}
	// Reject unknown keys in requested.
	for k := range requested {
		switch k {
		case "researcher", "planner", "implementer":
		default:
			return nil, fmt.Errorf("unknown agent type %q in agent_flavors", k)
		}
	}
	return out, nil
}

// FlavorOverlay returns the AgentConfig overlay for a frozen cast name.
// empty name with no flavors → nil overlay (ok). missing named flavor → error.
func (p ProjectConfig) FlavorOverlay(agentType, flavorName string) (AgentConfig, bool, error) {
	ac, ok := p.Agents[agentType]
	if !ok || len(ac.Flavors) == 0 {
		if flavorName != "" {
			return AgentConfig{}, false, fmt.Errorf("cast names %s flavor %q but project has no flavors for that type", agentType, flavorName)
		}
		return AgentConfig{}, false, nil
	}
	name := flavorName
	if name == "" {
		name = ac.Default
		if name == "" && len(ac.Flavors) == 1 {
			for k := range ac.Flavors {
				name = k
			}
		}
	}
	if name == "" {
		return AgentConfig{}, false, fmt.Errorf("no %s flavor selected and no project default", agentType)
	}
	overlay, ok := ac.Flavors[name]
	if !ok {
		return AgentConfig{}, false, fmt.Errorf("cast names %s flavor %q which is not defined on the project", agentType, name)
	}
	return overlay, true, nil
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
	StorageRoot   string                   `yaml:"storage_root"`
	DBPath        string                   `yaml:"db_path"`
	DefaultModel  ModelConfig              `yaml:"default_model"`
	Tools         ToolsConfig              `yaml:"tools"`
	Adapters      []AdapterConfig          `yaml:"adapters"`
	Agents        map[string]AgentConfig   `yaml:"agents"`
	Projects      map[string]ProjectConfig `yaml:"projects"`
	MCPServers    []MCPServerConfig        `yaml:"mcp_servers"`
	Triggers      TriggersConfig           `yaml:"triggers"`
	Storage       StorageBackendConfig     `yaml:"storage"`
	Server        ServerConfig             `yaml:"server"`
	Auth          AuthConfig               `yaml:"auth"`
	Notifications NotificationsConfig      `yaml:"notifications"`
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
	if err := normalizeProjects(&cfg, home); err != nil {
		return nil, err
	}

	return &cfg, nil
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
		for agentType := range pc.Agents {
			switch agentType {
			case "researcher", "planner", "implementer":
			default:
				return fmt.Errorf("projects.%s.agents: unknown agent type %q (want researcher|planner|implementer)", name, agentType)
			}
			ac := pc.Agents[agentType]
			if ac.Default != "" && len(ac.Flavors) > 0 {
				if _, ok := ac.Flavors[ac.Default]; !ok {
					return fmt.Errorf("projects.%s.agents.%s: default %q is not a defined flavor", name, agentType, ac.Default)
				}
			}
		}
		cfg.Projects[name] = pc
	}
	return nil
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
// names receive the defaults. Project flavor / issue cast layers are applied
// by the orchestrator on top of this result.
func (c *Config) Agent(name string) AgentConfig {
	def := defaultAgentConfig(name, c.DefaultModel)
	ovr, ok := c.Agents[name]
	if !ok {
		return def
	}
	return MergeAgent(def, ovr)
}

// MergeAgent overlays non-zero fields from overlay onto base.
// SystemPromptAppend is baked into SystemPrompt at the end of the merge.
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
	if overlay.SystemPromptAppend != "" {
		out.SystemPromptAppend = overlay.SystemPromptAppend
	}
	// Apply append after merge so full override + append both work.
	if out.SystemPromptAppend != "" {
		out.SystemPrompt = out.SystemPrompt + "\n\n" + out.SystemPromptAppend
		out.SystemPromptAppend = ""
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
	if overlay.TokenBudget > 0 {
		out.TokenBudget = overlay.TokenBudget
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
	return out
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

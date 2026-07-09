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
type AgentConfig struct {
	Model         ModelConfig `yaml:"model"`
	SystemPrompt  string      `yaml:"system_prompt"`
	Adjudicator   string      `yaml:"adjudicator"`
	MaxAttempts   int         `yaml:"max_attempts"`
	Loops         int         `yaml:"loops"`
	Rubric        string      `yaml:"rubric"`
}

// Config is the top-level user configuration.
type Config struct {
	StorageRoot  string                  `yaml:"storage_root"`
	DBPath       string                  `yaml:"db_path"`
	DefaultModel ModelConfig             `yaml:"default_model"`
	Tools        ToolsConfig             `yaml:"tools"`
	Adapters     []AdapterConfig         `yaml:"adapters"`
	Agents       map[string]AgentConfig  `yaml:"agents"`
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

	return &cfg, nil
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

Rules:
1. Edit only within the implementer's workspace.
2. Write clean, testable code matching the existing style.
3. When finished, call finish_task with done=true and a brief rationale evaluating the implementation.`
}

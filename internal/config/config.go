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

// Config is the top-level user configuration.
type Config struct {
	StorageRoot  string            `yaml:"storage_root"`
	DBPath       string            `yaml:"db_path"`
	DefaultModel ModelConfig       `yaml:"default_model"`
	Tools        ToolsConfig       `yaml:"tools"`
	Adapters     []AdapterConfig   `yaml:"adapters"`
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

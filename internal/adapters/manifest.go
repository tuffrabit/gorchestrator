package adapters

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manifest describes an external adapter binary.
type Manifest struct {
	Name         string   `yaml:"name"`
	Version      string   `yaml:"version"`
	Protocol     string   `yaml:"protocol"`
	Port         string   `yaml:"port"`
	Binary       string   `yaml:"binary"`
	Capabilities []string `yaml:"capabilities"`
}

// LoadManifest reads a manifest from path and verifies its binary.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Binary == "" {
		return nil, fmt.Errorf("manifest %s missing binary field", path)
	}
	if !filepath.IsAbs(m.Binary) {
		m.Binary = filepath.Join(filepath.Dir(path), m.Binary)
	}
	if err := verifyExecutable(m.Binary); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return &m, nil
}

// verifyExecutable reports whether path is a regular executable file.
func verifyExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("binary %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("binary %s is a directory", path)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("binary %s is not executable", path)
	}
	return nil
}

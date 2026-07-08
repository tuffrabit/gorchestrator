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
	Capabilities []string `yaml:"capabilities"`
	Binary       string   `yaml:"-"`
}

// LoadManifest reads a manifest from path.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	m.Binary = filepath.Join(filepath.Dir(path), m.Name)
	if _, err := os.Stat(m.Binary); err != nil {
		return nil, fmt.Errorf("manifest binary missing: %s", m.Binary)
	}
	return &m, nil
}

// Discovery scans dir for adapter manifests.
func Discovery(dir string) ([]*Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var manifests []*Manifest
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".yaml" && filepath.Ext(e.Name()) != ".yml" {
			continue
		}
		m, err := LoadManifest(filepath.Join(dir, e.Name()))
		if err != nil {
			// Skip invalid manifests rather than failing discovery.
			continue
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

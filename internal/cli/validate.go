package cli

import (
	"flag"
	"fmt"
	"sort"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

// Validate loads the config and reports validity without starting anything.
func Validate(fs *flag.FlagSet, args []string) error {
	configPath := fs.String("config", "", "path to config yaml (default: ~/.config/gorchestrator/config.yaml)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.LoadFrom(*configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	names := make([]string, 0, len(cfg.Projects))
	for name := range cfg.Projects {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Printf("config OK: %d project(s)", len(names))
	for _, name := range names {
		fmt.Printf(" %s", name)
	}
	fmt.Println()
	return nil
}

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

// Run executes the `run` subcommand.
func Run(fs *flag.FlagSet, args []string) error {
	issue := fs.String("issue", "", "issue title/body")
	project := fs.String("project", "", "project name (must be declared under projects: in config YAML)")
	dryRun := fs.Bool("dry-run", false, "use the dry-run LLM adapter")
	configPath := fs.String("config", "", "path to config yaml (default: ~/.config/gorchestrator/config.yaml)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *issue == "" || *project == "" {
		return fmt.Errorf("--issue and --project are required")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nreceived interrupt, shutting down...")
		cancel()
	}()

	opts := orchestrator.RunOptions{
		ProjectName: *project,
		IssueTitle:  *issue,
		DryRun:      *dryRun,
	}

	return orchestrator.Run(ctx, cfg, opts)
}

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

// Resume executes the `resume` subcommand.
func Resume(fs *flag.FlagSet, args []string) error {
	project := fs.String("project", "", "project name")
	issueStr := fs.String("issue", "", "issue id")
	decision := fs.String("decision", "", "pass | fail | retry")
	feedback := fs.String("feedback", "", "human adjudicator feedback")
	configPath := fs.String("config", "", "path to config yaml (default: ~/.config/gorchestrator/config.yaml)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *project == "" || *issueStr == "" {
		return fmt.Errorf("--project and --issue are required")
	}

	issueID, err := strconv.ParseInt(*issueStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid issue id: %w", err)
	}

	var cfg *config.Config
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

	if *decision == "retry" && *feedback == "" {
		fmt.Fprintln(os.Stderr, "warning: retry without --feedback discards the most valuable signal from the human gate")
	}

	opts := orchestrator.ResumeOptions{
		ProjectName: *project,
		IssueID:     issueID,
		Decision:    *decision,
		Feedback:    *feedback,
	}

	return orchestrator.Resume(ctx, cfg, opts)
}

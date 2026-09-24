package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

// multiFlag collects repeated -attach values.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// Run executes the `run` subcommand.
func Run(fs *flag.FlagSet, args []string) error {
	issue := fs.String("issue", "", "issue title (short label)")
	body := fs.String("body", "", "optional issue description (longer context for agents)")
	bodyFile := fs.String("body-file", "", "optional path to a file whose contents are the description")
	var attach multiFlag
	fs.Var(&attach, "attach", "optional text attachment file (repeatable; extension must be text-like)")
	project := fs.String("project", "", "project name (must be declared under projects: in config YAML)")
	dryRun := fs.Bool("dry-run", false, "use the dry-run LLM adapter")
	dependsOn := fs.String("depends-on", "", "comma-separated IDs of issues that must be done before this one is claimable")
	flow := fs.String("flow", "", "ordered agent flow for this issue (comma-separated agent ids, e.g. 'scout,fixer'); empty = project default_flow")
	configPath := fs.String("config", "", "path to config yaml (default: ~/.config/gorchestrator/config.yaml)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *issue == "" || *project == "" {
		return fmt.Errorf("--issue and --project are required")
	}

	deps, err := parseDependsOnFlag(*dependsOn)
	if err != nil {
		return err
	}

	flowIDs, err := parseFlowFlag(*flow)
	if err != nil {
		return err
	}

	description := *body
	if *bodyFile != "" {
		data, err := os.ReadFile(*bodyFile)
		if err != nil {
			return fmt.Errorf("read --body-file: %w", err)
		}
		description = string(data)
	}
	var attachments []orchestrator.AttachmentFile
	for _, p := range attach {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read attachment %s: %w", p, err)
		}
		attachments = append(attachments, orchestrator.AttachmentFile{
			Name: filepath.Base(p),
			Data: data,
		})
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

	opts := orchestrator.RunOptions{
		ProjectName: *project,
		IssueTitle:  *issue,
		Description: description,
		Attachments: attachments,
		DryRun:      *dryRun,
		DependsOn:   deps,
		Flow:        flowIDs,
	}

	return orchestrator.Run(ctx, cfg, opts)
}

// parseFlowFlag parses an ordered agent flow ("scout,fixer").
func parseFlowFlag(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("--flow: empty agent id in %q", raw)
		}
		out = append(out, part)
	}
	return out, nil
}

// parseDependsOnFlag parses a comma-separated issue ID list ("12,14").
func parseDependsOnFlag(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("--depends-on: invalid issue id %q", part)
		}
		out = append(out, id)
	}
	return out, nil
}

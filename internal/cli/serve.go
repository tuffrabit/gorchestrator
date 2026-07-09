package cli

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/daemon"
	"github.com/tuffrabit/gorchestrator/internal/notify"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/server"
)

// Serve runs the long-lived daemon: recovery, workers, and HTTP surface.
func Serve(fs *flag.FlagSet, args []string) error {
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

	// Apply server/auth defaults when loading raw configs that skipped them.
	// LoadFrom already applies them; this is defensive for tests that build Config by hand.
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8080"
	}
	if cfg.Server.MaxConcurrentIssues <= 0 {
		cfg.Server.MaxConcurrentIssues = 2
	}
	if cfg.Server.ShutdownTimeoutDur == 0 {
		if cfg.Server.ShutdownTimeout == "" {
			cfg.Server.ShutdownTimeout = "30s"
		}
		cfg.Server.ShutdownTimeoutDur, _ = time.ParseDuration(cfg.Server.ShutdownTimeout)
	}

	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		return fmt.Errorf("init engine: %w", err)
	}
	defer eng.Close()

	dispatcher, adapterSinks, err := notify.BuildDispatcher(eng.Notifications(), cfg)
	if err != nil {
		return fmt.Errorf("init notifications: %w", err)
	}
	eng.SetNotifier(dispatcher)
	defer func() {
		for _, s := range adapterSinks {
			_ = s.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := daemon.New(eng, cfg)
	if err := d.Start(ctx); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	srv, err := server.New(eng, cfg)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("serve: listening on %s (auth.mode=%s)", cfg.Server.Listen, cfg.Auth.Mode)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("serve: received %v, shutting down...", sig)
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	}

	shutdownTimeout := cfg.Server.ShutdownTimeoutDur
	if shutdownTimeout <= 0 {
		shutdownTimeout = 30 * time.Second
	}

	// Stop HTTP first so no new submits land.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("serve: http shutdown: %v", err)
	}

	// Then drain workers.
	d.Shutdown(shutdownTimeout)
	cancel()
	return nil
}

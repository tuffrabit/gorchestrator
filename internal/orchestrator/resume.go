package orchestrator

import (
	"context"
	"fmt"

	"github.com/tuffrabit/gorchestrator/internal/config"
)

// Resume continues an existing issue using the engine.
func Resume(ctx context.Context, cfg *config.Config, opts ResumeOptions) error {
	eng, err := NewEngine(cfg)
	if err != nil {
		return fmt.Errorf("init engine: %w", err)
	}
	defer eng.Close()

	return eng.Resume(ctx, opts)
}

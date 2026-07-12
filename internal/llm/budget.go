package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"google.golang.org/adk/v2/model"
)

// ErrBudgetExceeded is returned before a model call when the session token ceiling is hit.
var ErrBudgetExceeded = errors.New("budget_exceeded")

// BudgetConfig configures a per-session token gate around a model.LLM.
type BudgetConfig struct {
	// Ceiling is the max total tokens for this phase session. <=0 means unlimited (no wrap needed).
	Ceiling int
	// Spent is the rehydrated total from prior usage events in this phase.
	Spent int
	// WarnPct is 1–100; 0 disables warning callbacks.
	WarnPct int
	// OnWarn is called at most once when spent crosses WarnPct of Ceiling.
	OnWarn func(spent, ceiling int)
}

// BudgetLLM wraps a model.LLM and enforces a session token ceiling before each GenerateContent.
type BudgetLLM struct {
	inner   model.LLM
	ceiling int
	warnPct int
	onWarn  func(spent, ceiling int)

	mu        sync.Mutex
	spent     int
	warned    bool
	callCount int
}

// WrapBudget returns inner unchanged when ceiling <= 0; otherwise a BudgetLLM.
func WrapBudget(inner model.LLM, cfg BudgetConfig) model.LLM {
	if inner == nil || cfg.Ceiling <= 0 {
		return inner
	}
	return &BudgetLLM{
		inner:   inner,
		ceiling: cfg.Ceiling,
		spent:   cfg.Spent,
		warnPct: cfg.WarnPct,
		onWarn:  cfg.OnWarn,
	}
}

// Name implements model.LLM.
func (b *BudgetLLM) Name() string {
	return b.inner.Name()
}

// Spent returns tokens counted so far in this wrapper (including rehydrate).
func (b *BudgetLLM) Spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Ceiling returns the configured token ceiling.
func (b *BudgetLLM) Ceiling() int {
	return b.ceiling
}

// GenerateContent implements model.LLM. Checks the ceiling before the call and
// accumulates UsageMetadata after each successful response chunk.
func (b *BudgetLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		b.mu.Lock()
		if b.spent >= b.ceiling {
			spent, ceiling := b.spent, b.ceiling
			b.mu.Unlock()
			yield(nil, fmt.Errorf("%w: spent %d >= ceiling %d", ErrBudgetExceeded, spent, ceiling))
			return
		}
		b.mu.Unlock()

		for resp, err := range b.inner.GenerateContent(ctx, req, stream) {
			if err != nil {
				yield(nil, err)
				return
			}
			if resp != nil && resp.UsageMetadata != nil {
				add := int(resp.UsageMetadata.TotalTokenCount)
				if add <= 0 {
					add = int(resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.CandidatesTokenCount)
				}
				if add > 0 {
					b.addSpent(add)
				}
			}
			if !yield(resp, nil) {
				return
			}
		}
	}
}

func (b *BudgetLLM) addSpent(n int) {
	b.mu.Lock()
	b.spent += n
	b.callCount++
	shouldWarn := false
	spent, ceiling := b.spent, b.ceiling
	if b.warnPct > 0 && !b.warned && b.ceiling > 0 && b.onWarn != nil {
		threshold := b.ceiling * b.warnPct / 100
		if b.spent >= threshold {
			b.warned = true
			shouldWarn = true
		}
	}
	onWarn := b.onWarn
	b.mu.Unlock()
	if shouldWarn && onWarn != nil {
		onWarn(spent, ceiling)
	}
}
func IsBudgetExceeded(err error) bool {
	return errors.Is(err, ErrBudgetExceeded)
}

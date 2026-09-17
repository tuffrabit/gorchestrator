package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
)

// runSingleShot executes a phase as a single no-tools GenerateContent call and
// treats the reply text as the phase output. It mirrors runAgentLoop's return
// contract (output, done, rationale, effort, tokens, err) so runPhase's
// attempt/adjudication machinery applies unchanged. done is always true: there
// is no finish_task, and with the human-gate default the boundary pauses for a
// human decision regardless (under an explicit `self` opt-in it passes; for
// the planner the missing effort tag defaults to high, forcing the human gate
// before implementation).
func (e *Engine) runSingleShot(ctx context.Context, llmModel adkmodel.LLM, phase string, cfg config.AgentConfig, userContent *genai.Content, outputPath, eventsPath string, attempt, loop int) ([]byte, bool, string, string, int, error) {
	instruction := resolveSingleShotPrompt(phase, cfg)
	if instruction == "" {
		return nil, false, "", "", 0, fmt.Errorf("single-shot is not supported for phase %q", phase)
	}
	req := &adkmodel.LLMRequest{
		Contents: []*genai.Content{userContent},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(instruction, genai.RoleUser),
		},
	}

	var text string
	tokens := 0
	for resp, err := range llmModel.GenerateContent(ctx, req, false) {
		if err != nil {
			if llm.IsBudgetExceeded(err) {
				return nil, false, "", "", tokens, err
			}
			return nil, false, "", "", 0, fmt.Errorf("loop %d: %w", loop, err)
		}
		if resp == nil {
			continue
		}
		if resp.UsageMetadata != nil {
			tokens = int(resp.UsageMetadata.TotalTokenCount)
			if tokens <= 0 {
				tokens = int(resp.UsageMetadata.PromptTokenCount + resp.UsageMetadata.CandidatesTokenCount)
			}
		}
		if resp.Content != nil {
			for _, p := range resp.Content.Parts {
				if p != nil {
					text += p.Text
				}
			}
		}
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, "", "", tokens, fmt.Errorf("loop %d produced empty output", loop)
	}
	if err := e.store.Write(ctx, outputPath, []byte(text)); err != nil {
		return nil, false, "", "", tokens, fmt.Errorf("write single-shot output: %w", err)
	}

	// Same event shapes as runAgentLoop so budget rehydration
	// (sumUsageFromEvents) and the dashboard keep working.
	if tokens > 0 {
		recordEvent(ctx, e.store, eventsPath, eventRecord{
			Type:      "usage",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Attempt:   attempt,
			Loop:      loop,
			Tokens:    tokens,
		})
	}
	recordEvent(ctx, e.store, eventsPath, eventRecord{
		Type:      "model_turn",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Attempt:   attempt,
		Loop:      loop,
		Role:      genai.RoleModel,
		Content:   cappedText(text),
	})

	return []byte(text), true, "", "", tokens, nil
}

// resolveSingleShotPrompt picks the system instruction for a single-shot phase.
// The merged cfg.SystemPrompt always contains the built-in tool-loop default
// (defaultAgentConfig), so compare against it: an exact match means "no user
// override"; a default+"\n\n"+suffix prefix means only system_prompt_append was
// set (MergeAgent bakes appends with exactly "\n\n"); anything else is a full
// user override honored as-is. Returns "" for phases without a single-shot
// default (implementation).
func resolveSingleShotPrompt(phase string, cfg config.AgentConfig) string {
	agentType := phaseAgentType(phase)
	base := config.DefaultSingleShotPrompt(agentType)
	if base == "" {
		return ""
	}
	def := config.DefaultSystemPrompt(agentType)
	switch {
	case cfg.SystemPrompt == "" || cfg.SystemPrompt == def:
		return base
	case def != "" && strings.HasPrefix(cfg.SystemPrompt, def+"\n\n"):
		return base + strings.TrimPrefix(cfg.SystemPrompt, def)
	default:
		return cfg.SystemPrompt
	}
}

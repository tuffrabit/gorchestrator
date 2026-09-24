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

// runSingleShot executes a single-shot step as one no-tools GenerateContent
// call and treats the reply text as the step output. It mirrors runAgentLoop's
// return contract (output, done, rationale, tokens, err) so runPhase's
// attempt/adjudication machinery applies unchanged. done is always true: there
// is no finish_task, and under the human-gate default the boundary pauses for
// a human decision regardless.
func (e *Engine) runSingleShot(ctx context.Context, llmModel adkmodel.LLM, cfg config.AgentConfig, userContent *genai.Content, outputPath, eventsPath string, attempt, loop int) ([]byte, bool, string, int, error) {
	instruction := cfg.SystemPrompt
	if instruction == "" {
		return nil, false, "", 0, fmt.Errorf("agent %q has no system_prompt (required)", cfg.ID)
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
			recordEvent(ctx, e.store, eventsPath, eventRecord{
				Type:      "loop_error",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Attempt:   attempt,
				Loop:      loop,
				Error:     err.Error(),
			})
			if llm.IsBudgetExceeded(err) {
				return nil, false, "", tokens, err
			}
			return nil, false, "", 0, fmt.Errorf("loop %d: %w", loop, err)
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
		return nil, false, "", tokens, fmt.Errorf("loop %d produced empty output", loop)
	}
	if err := e.store.Write(ctx, outputPath, []byte(text)); err != nil {
		return nil, false, "", tokens, fmt.Errorf("write single-shot output: %w", err)
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

	return []byte(text), true, "", tokens, nil
}

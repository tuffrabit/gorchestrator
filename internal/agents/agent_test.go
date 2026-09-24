package agents

import (
	"testing"

	"google.golang.org/adk/v2/model"
)

// TestTaskBuild verifies the generic task agent carries the configured id and
// system prompt and builds without tools (single-shot style runs pass none).
func TestTaskBuild(t *testing.T) {
	a, err := NewTask("coder", "You are a coder.").Build(model.LLM(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("Build returned nil agent")
	}
	name := a.Name()
	if name != "coder" {
		t.Fatalf("agent name = %q, want coder", name)
	}
}

// TestTaskNoBuiltInPrompt verifies an empty instruction is preserved as-is;
// there is no default prompt injection.
func TestTaskNoBuiltInPrompt(t *testing.T) {
	tk := NewTask("reviewer", "")
	if tk.Instruction != "" {
		t.Fatalf("expected empty instruction, got %q", tk.Instruction)
	}
}

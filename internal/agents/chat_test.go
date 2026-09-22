package agents

import (
	"strings"
	"testing"
)

func TestChatInstructionWithTools(t *testing.T) {
	c := NewChat("researcher", "system")
	out := c.instruction([]string{"read_file", "list_directory", "grep_search"})
	for _, n := range []string{"read_file", "list_directory", "grep_search"} {
		if !strings.Contains(out, n) {
			t.Fatalf("instruction with tools missing tool name %q:\n%s", n, out)
		}
	}
	if !strings.Contains(out, "Never write a tool call as text") {
		t.Fatalf("instruction missing no-imitation rule:\n%s", out)
	}
	if strings.Contains(out, "never invent tool calls") {
		t.Fatalf("tool-bearing instruction must not carry the no-tools wording:\n%s", out)
	}
}

func TestChatInstructionNoTools(t *testing.T) {
	c := NewChat("planner", "system")
	out := c.instruction(nil)
	if !strings.Contains(out, "cannot inspect files") || !strings.Contains(out, "never invent tool calls") {
		t.Fatalf("tool-less instruction must forbid inventing tool calls:\n%s", out)
	}
	if strings.Contains(out, "You have access to these read-only tools") {
		t.Fatalf("tool-less instruction must not claim tool access:\n%s", out)
	}
}

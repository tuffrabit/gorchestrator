package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func TestPersistIssueContext_DescriptionAndAttachments(t *testing.T) {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	eng, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()
	ctx := context.Background()

	issue, err := eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Fix login",
		Description: "Users cannot sign in after SSO change.\n\nPlease check redirect URI.",
		Attachments: []AttachmentFile{
			{Name: "notes.md", Data: []byte("# repro\nsteps here\n")},
			{Name: "error.log", Data: []byte("ERROR boom\n")},
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("SubmitIssue: %v", err)
	}

	got, err := eng.Issues().Get(issue.ID)
	if err != nil || got == nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.Description == "" || !strings.Contains(got.Description, "SSO") {
		t.Fatalf("sqlite description = %q", got.Description)
	}

	mdKey := storage.IssueMarkdownPath(issue.ProjectID, issue.ID)
	md, err := eng.Store().Read(ctx, mdKey)
	if err != nil {
		t.Fatalf("read issue.md: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "# Fix login") || !strings.Contains(s, "SSO") {
		t.Fatalf("issue.md missing title/description: %s", s)
	}
	if !strings.Contains(s, "attachments/notes.md") || !strings.Contains(s, "attachments/error.log") {
		t.Fatalf("issue.md missing attachment list: %s", s)
	}

	// FS attachments
	for _, name := range []string{"notes.md", "error.log"} {
		data, err := eng.Store().Read(ctx, storage.AttachmentPath(issue.ProjectID, issue.ID, name))
		if err != nil || len(data) == 0 {
			t.Fatalf("attachment %s: %v", name, err)
		}
	}

	// Agent input includes description + paths
	input, err := eng.buildBaseInput(ctx, issue.ProjectID, issue.ID, "research", issue.Title, got.Description)
	if err != nil {
		t.Fatalf("buildBaseInput: %v", err)
	}
	if !strings.Contains(input, "Fix login") || !strings.Contains(input, "SSO") {
		t.Fatalf("base input missing title/desc: %s", input)
	}
	if !strings.Contains(input, "attachments/notes.md") {
		t.Fatalf("base input missing attachment path: %s", input)
	}

	// Reject binary-ish extension
	_, err = eng.SubmitIssue(ctx, RunOptions{
		ProjectName: "acme",
		IssueTitle:  "Bad attach",
		Attachments: []AttachmentFile{{Name: "photo.png", Data: []byte{0x89, 0x50}}},
		DryRun:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "extension") && !strings.Contains(err.Error(), "text-like") {
		t.Fatalf("expected extension rejection, got %v", err)
	}
}

func TestSanitizeAttachmentName(t *testing.T) {
	name, err := sanitizeAttachmentName(`..\..\evil.md`)
	if err != nil {
		t.Fatal(err)
	}
	if name != "evil.md" {
		t.Fatalf("got %q", name)
	}
	if !isAllowedTextAttachment("x.md") {
		t.Fatal("md allowed")
	}
	if isAllowedTextAttachment("x.exe") {
		t.Fatal("exe not allowed")
	}
}

func TestFormatIssueMarkdown(t *testing.T) {
	md := formatIssueMarkdown("T", "Body", []string{"a.txt"})
	if !strings.Contains(md, "# T") || !strings.Contains(md, "Body") || !strings.Contains(md, "attachments/a.txt") {
		t.Fatalf("%s", md)
	}
}

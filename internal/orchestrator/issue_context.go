package orchestrator

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// Limits for optional issue description and text attachments.
const (
	maxDescriptionBytes = 256 * 1024
	maxAttachmentBytes  = 1 * 1024 * 1024
	maxAttachments      = 20
)

// textAttachmentExts are allowed at upload time by extension only.
var textAttachmentExts = map[string]struct{}{
	".txt": {}, ".md": {}, ".markdown": {}, ".html": {}, ".htm": {},
	".json": {}, ".jsonl": {}, ".yaml": {}, ".yml": {}, ".xml": {},
	".csv": {}, ".tsv": {}, ".log": {}, ".toml": {}, ".ini": {},
	".cfg": {}, ".conf": {}, ".rst": {}, ".tex": {}, ".diff": {},
	".patch": {}, ".go": {}, ".py": {}, ".js": {}, ".ts": {},
	".tsx": {}, ".jsx": {}, ".css": {}, ".sql": {}, ".sh": {},
	".bash": {}, ".env": {}, ".properties": {},
}

// AttachmentFile is one user-uploaded context file for a new issue.
type AttachmentFile struct {
	// Name is the original client filename (basename used after sanitization).
	Name string
	Data []byte
}

// persistIssueContext writes issue.md + attachments and sets the SQLite
// description column. Both stores are updated in one flow so they stay aligned:
// filesystem is written first, then SQLite. Failures after partial FS writes are
// returned to the caller (submit fails).
func (e *Engine) persistIssueContext(ctx context.Context, issue *sqlite.Issue, title, description string, attachments []AttachmentFile) error {
	if issue == nil {
		return fmt.Errorf("issue is nil")
	}
	description = strings.TrimSpace(description)
	if len(description) > maxDescriptionBytes {
		return fmt.Errorf("description exceeds %d bytes", maxDescriptionBytes)
	}
	if len(attachments) > maxAttachments {
		return fmt.Errorf("at most %d attachments allowed", maxAttachments)
	}

	savedNames, err := e.writeAttachments(ctx, issue.ProjectID, issue.ID, attachments)
	if err != nil {
		return err
	}

	md := formatIssueMarkdown(title, description, savedNames)
	if err := e.store.Write(ctx, storage.IssueMarkdownPath(issue.ProjectID, issue.ID), []byte(md)); err != nil {
		return fmt.Errorf("write issue.md: %w", err)
	}

	if err := e.issues.SetDescription(issue.ID, description); err != nil {
		return fmt.Errorf("set description: %w", err)
	}
	issue.Description = description
	return nil
}

func (e *Engine) writeAttachments(ctx context.Context, projectID, issueID int64, files []AttachmentFile) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	dir := storage.AttachmentsDir(projectID, issueID)
	if err := e.store.Mkdir(ctx, dir); err != nil {
		return nil, fmt.Errorf("mkdir attachments: %w", err)
	}
	used := map[string]int{}
	var names []string
	for _, f := range files {
		if len(f.Data) > maxAttachmentBytes {
			return nil, fmt.Errorf("attachment %q exceeds %d bytes", f.Name, maxAttachmentBytes)
		}
		base, err := sanitizeAttachmentName(f.Name)
		if err != nil {
			return nil, err
		}
		if !isAllowedTextAttachment(base) {
			return nil, fmt.Errorf("attachment %q: only text-like extensions are allowed", f.Name)
		}
		// Deduplicate names within the batch and against collisions.
		n := used[strings.ToLower(base)]
		used[strings.ToLower(base)] = n + 1
		final := base
		if n > 0 {
			ext := path.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			final = fmt.Sprintf("%s_%d%s", stem, n, ext)
		}
		key := storage.AttachmentPath(projectID, issueID, final)
		if err := e.store.Write(ctx, key, f.Data); err != nil {
			return nil, fmt.Errorf("write attachment %s: %w", final, err)
		}
		names = append(names, final)
	}
	return names, nil
}

func formatIssueMarkdown(title, description string, attachmentNames []string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n")
	if description != "" {
		b.WriteString("\n")
		b.WriteString(description)
		if !strings.HasSuffix(description, "\n") {
			b.WriteString("\n")
		}
	}
	if len(attachmentNames) > 0 {
		b.WriteString("\n## Attachments\n\n")
		for _, name := range attachmentNames {
			fmt.Fprintf(&b, "- `attachments/%s`\n", name)
		}
	}
	return b.String()
}

// listAttachmentNames returns basenames under attachments/ (empty if none).
func (e *Engine) listAttachmentNames(ctx context.Context, projectID, issueID int64) ([]string, error) {
	dir := storage.AttachmentsDir(projectID, issueID)
	exists, err := e.store.Exists(ctx, dir)
	if err != nil || !exists {
		return nil, err
	}
	entries, err := e.store.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range entries {
		if ent.IsDir {
			continue
		}
		names = append(names, ent.Name)
	}
	return names, nil
}

func sanitizeAttachmentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	// Strip any path components from client-supplied names.
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("attachment name is empty")
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0:
			continue
		case unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "", fmt.Errorf("attachment name is empty")
	}
	if err := storage.ValidateRelativePath(out); err != nil {
		return "", fmt.Errorf("attachment name: %w", err)
	}
	// Basename only — no nested paths.
	if strings.Contains(out, "/") {
		return "", fmt.Errorf("attachment name must be a single path segment")
	}
	return out, nil
}

func isAllowedTextAttachment(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	if ext == "" {
		return false
	}
	_, ok := textAttachmentExts[ext]
	return ok
}

// buildIssueUserInput builds the user-message prefix for agent runs:
// title, optional description (inline), and attachment paths for tools.
func buildIssueUserInput(title, description string, attachmentNames []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Issue: %s", title)
	if description != "" {
		b.WriteString("\n\n")
		b.WriteString(description)
	}
	if len(attachmentNames) > 0 {
		b.WriteString("\n\nAttachments (paths relative to the issue root; use read_file / list_directory):\n")
		for _, name := range attachmentNames {
			fmt.Fprintf(&b, "- attachments/%s\n", name)
		}
	}
	return b.String()
}

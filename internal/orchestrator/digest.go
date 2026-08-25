package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"github.com/tuffrabit/gorchestrator/internal/tools"
)

// defaultContextFilesBudget bounds context_files stuffing when
// single_shot_context_bytes is unset.
const defaultContextFilesBudget = 64 * 1024

// maxDigestListingBytes caps the file-listing preamble so huge repos cannot
// blow the prompt budget on paths alone.
const maxDigestListingBytes = 8 * 1024

// buildRepoDigest renders repo context for a single-shot phase: a file listing
// of the source snapshot plus file contents up to the byte budget. Returns ""
// when no digest is configured (no budget and no context_files) or the source
// snapshot does not exist.
func (e *Engine) buildRepoDigest(ctx context.Context, projectID, issueID int64, cfg config.AgentConfig) (string, error) {
	budget := cfg.SingleShotContextBytes
	if budget <= 0 {
		if len(cfg.ContextFiles) == 0 {
			return "", nil
		}
		budget = defaultContextFilesBudget
	}

	srcPrefix := storage.SourcePath(projectID, issueID)
	if exists, _ := e.store.Exists(ctx, srcPrefix); !exists {
		return "", nil
	}
	matcher := e.snapshotGitignore(ctx, srcPrefix)

	explicit := len(cfg.ContextFiles) > 0
	var files []string
	if explicit {
		files = append(files, cfg.ContextFiles...)
	} else {
		keys, err := listRecursive(ctx, e.store, srcPrefix)
		if err != nil {
			return "", fmt.Errorf("list source snapshot: %w", err)
		}
		for _, key := range keys {
			rel := strings.TrimPrefix(key, srcPrefix+"/")
			if rel == ".gitignore" {
				continue
			}
			if matcher.MatchFile(rel) {
				continue
			}
			files = append(files, rel)
		}
		sort.Strings(files)
	}
	if len(files) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("## Repository context (source snapshot)\n\n")
	b.WriteString("Only the files below exist in the provided context. Files present in the snapshot:\n\n")
	listed := 0
	for _, f := range files {
		line := "- " + f + "\n"
		if b.Len()+len(line) > maxDigestListingBytes {
			fmt.Fprintf(&b, "- ... (%d more files not listed)\n", len(files)-listed)
			break
		}
		b.WriteString(line)
		listed++
	}
	b.WriteString("\nFile contents (truncated to fit the context budget):\n")

	used := 0
	omitted := 0
	for _, rel := range files {
		var section string
		data, err := e.store.Read(ctx, srcPrefix+"/"+rel)
		switch {
		case err != nil && explicit:
			section = fmt.Sprintf("\n## %s\n\n(not found in source snapshot)\n", rel)
		case err != nil:
			omitted++
			continue
		case !looksMostlyText(data):
			omitted++
			continue
		default:
			section = fmt.Sprintf("\n## %s\n```\n%s\n```\n", rel, data)
		}
		if used+len(section) > budget {
			omitted++
			continue
		}
		b.WriteString(section)
		used += len(section)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n... %d file(s) omitted (binary, unreadable, or context budget exhausted)\n", omitted)
	}
	return b.String(), nil
}

// snapshotGitignore loads the source snapshot's .gitignore from storage.
// A nil matcher (no file, parse error) matches nothing.
func (e *Engine) snapshotGitignore(ctx context.Context, srcPrefix string) *tools.GitignoreMatcher {
	data, err := e.store.Read(ctx, srcPrefix+"/.gitignore")
	if err != nil || len(data) == 0 {
		return nil
	}
	m, err := tools.ParseGitignore(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return m
}

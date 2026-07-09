package server

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"strings"

	"github.com/yuin/goldmark"

	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

func (s *Server) drawerContent(r *http.Request, view *orchestrator.IssueView, tab string) (string, template.HTML, error) {
	if view == nil || view.Issue == nil {
		return "", "", fmt.Errorf("no issue")
	}
	issue := view.Issue
	phase := issue.CurrentPhase
	projectID := issue.ProjectID
	issueID := issue.ID
	ctx := r.Context()

	switch tab {
	case "result":
		key := storage.ResultPath(projectID, issueID, phase)
		data, err := s.eng.Store().Read(ctx, key)
		if err != nil {
			return "(no result.json yet)", "", nil
		}
		return string(data), "", nil
	case "output":
		// Prefer latest_output from result.json
		key := storage.ResultPath(projectID, issueID, phase)
		data, err := s.eng.Store().Read(ctx, key)
		if err != nil {
			return "(no output)", "", nil
		}
		// crude extract of latest_output path
		outPath := storage.AttemptOutputPath(projectID, issueID, phase, max(view.Attempt, 1))
		if bytes.Contains(data, []byte(`"latest_output"`)) {
			// try read result for pointer
			res, _ := readLatestOutput(ctx, s.eng, projectID, issueID, phase)
			if res != "" {
				outPath = res
			}
		}
		out, err := s.eng.Store().Read(ctx, outPath)
		if err != nil {
			return "(no output yet)", "", nil
		}
		if strings.HasSuffix(outPath, ".md") {
			var buf bytes.Buffer
			if err := goldmark.Convert(out, &buf); err == nil {
				return "", template.HTML(buf.String()), nil
			}
		}
		return string(out), "", nil
	case "activity", "events":
		key := storage.EventsPath(projectID, issueID, phase)
		data, err := s.eng.Store().Read(ctx, key)
		if err != nil {
			return "(no events yet)", "", nil
		}
		// cap huge payloads
		if len(data) > 256*1024 {
			data = append(data[:256*1024], []byte("\n... [truncated]")...)
		}
		return string(data), "", nil
	case "diff":
		ws := storage.WorkspacePath(projectID, issueID)
		exists, _ := s.eng.Store().Exists(ctx, ws)
		if !exists {
			return "(no workspace — diff unavailable)", "", nil
		}
		src := storage.SourcePath(projectID, issueID)
		diff, err := unifiedDiff(ctx, s.eng.Store(), src, ws)
		if err != nil {
			return err.Error(), "", nil
		}
		if diff == "" {
			return "(no differences)", "", nil
		}
		if len(diff) > 256*1024 {
			diff = diff[:256*1024] + "\n... [truncated]"
		}
		return diff, "", nil
	default:
		return "", "", fmt.Errorf("unknown tab %q", tab)
	}
}

func readLatestOutput(ctx context.Context, eng *orchestrator.Engine, projectID, issueID int64, phase string) (string, error) {
	data, err := eng.Store().Read(ctx, storage.ResultPath(projectID, issueID, phase))
	if err != nil {
		return "", err
	}
	// minimal parse without importing full PhaseResult from unexported path — use json
	var m map[string]any
	if err := jsonUnmarshal(data, &m); err != nil {
		return "", err
	}
	if v, ok := m["latest_output"].(string); ok {
		return v, nil
	}
	return "", nil
}

func unifiedDiff(ctx context.Context, store storage.Port, srcRoot, wsRoot string) (string, error) {
	srcFiles, _ := listAllFiles(ctx, store, srcRoot)
	wsFiles, err := listAllFiles(ctx, store, wsRoot)
	if err != nil {
		return "", err
	}
	srcSet := map[string]struct{}{}
	for _, f := range srcFiles {
		rel := strings.TrimPrefix(strings.TrimPrefix(f, srcRoot), "/")
		srcSet[rel] = struct{}{}
	}
	var b strings.Builder
	seen := map[string]struct{}{}
	for _, f := range wsFiles {
		rel := strings.TrimPrefix(strings.TrimPrefix(f, wsRoot), "/")
		seen[rel] = struct{}{}
		wsData, err := store.Read(ctx, f)
		if err != nil {
			continue
		}
		srcPath := path.Join(srcRoot, rel)
		srcData, err := store.Read(ctx, srcPath)
		if err != nil {
			b.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", rel))
			writeLineDiff(&b, nil, strings.Split(string(wsData), "\n"))
			continue
		}
		if string(srcData) == string(wsData) {
			continue
		}
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", rel, rel))
		writeLineDiff(&b, strings.Split(string(srcData), "\n"), strings.Split(string(wsData), "\n"))
	}
	for rel := range srcSet {
		if _, ok := seen[rel]; ok {
			continue
		}
		srcData, err := store.Read(ctx, path.Join(srcRoot, rel))
		if err != nil {
			continue
		}
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ /dev/null\n", rel))
		writeLineDiff(&b, strings.Split(string(srcData), "\n"), nil)
	}
	return b.String(), nil
}

func writeLineDiff(b *strings.Builder, a, c []string) {
	// Simple line-oriented diff (not LCS) for MVP readability.
	max := len(a)
	if len(c) > max {
		max = len(c)
	}
	for i := 0; i < max; i++ {
		var al, cl string
		if i < len(a) {
			al = a[i]
		}
		if i < len(c) {
			cl = c[i]
		}
		if al == cl {
			b.WriteString(" " + al + "\n")
			continue
		}
		if i < len(a) {
			b.WriteString("-" + al + "\n")
		}
		if i < len(c) {
			b.WriteString("+" + cl + "\n")
		}
	}
}

func listAllFiles(ctx context.Context, store storage.Port, root string) ([]string, error) {
	exists, err := store.Exists(ctx, root)
	if err != nil || !exists {
		return nil, err
	}
	return listRecursive(ctx, store, root)
}

func listRecursive(ctx context.Context, store storage.Port, key string) ([]string, error) {
	entries, err := store.List(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		child := path.Join(key, e.Name)
		if e.IsDir {
			sub, err := listRecursive(ctx, store, child)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		out = append(out, child)
	}
	return out, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func jsonUnmarshal(data []byte, v any) error {
	return jsonUnmarshalImpl(data, v)
}

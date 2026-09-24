package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

const drawerPayloadCap = 256 * 1024

// drawerMarkdown renders agent output.md for the artifact drawer.
// WithUnsafe keeps intentional raw HTML in free-form agent artifacts (HTML
// pages, embedded tables, etc.); goldmark's default strips those to comments.
var drawerMarkdown = goldmark.New(
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

// stepKeyIn reports whether key is one of the issue's frozen step keys.
func stepKeyIn(steps []sqlite.Step, key string) bool {
	return stepIndexOf(steps, key) >= 0
}

// stepIndexOf returns the 0-based position of key in steps, or -1.
func stepIndexOf(steps []sqlite.Step, key string) int {
	for i, st := range steps {
		if st.Key == key {
			return i
		}
	}
	return -1
}

// drawerPayload is one rendered drawer tab: raw text (Content) or rendered
// HTML (ContentHTML), plus — for the activity tab — the events as a single
// JSON array (EventsJSON) for the collapsible tree view.
type drawerPayload struct {
	Content     string
	ContentHTML template.HTML
	EventsJSON  string
	Truncated   bool
}

func (s *Server) drawerContent(r *http.Request, view *orchestrator.IssueView, tab, stepKey string) (drawerPayload, error) {
	if view == nil || view.Issue == nil {
		return drawerPayload{}, fmt.Errorf("no issue")
	}
	issue := view.Issue
	ctx := r.Context()
	steps, err := s.eng.StepsForIssue(issue)
	if err != nil || len(steps) == 0 {
		return drawerPayload{}, fmt.Errorf("resolve issue flow: %w", err)
	}
	if tab == "workspace" {
		html, werr := s.renderWorkspaceTree(ctx, view)
		if werr != nil {
			return drawerPayload{Content: werr.Error()}, nil
		}
		return drawerPayload{ContentHTML: html}, nil
	}
	if !stepKeyIn(steps, stepKey) {
		if stepKeyIn(steps, issue.CurrentPhase) {
			stepKey = issue.CurrentPhase
		} else if tab == "output" {
			stepKey = steps[len(steps)-1].Key
		} else {
			stepKey = steps[0].Key
		}
	}
	idx := stepIndexOf(steps, stepKey)
	step := steps[idx]
	isFinal := idx == len(steps)-1

	switch tab {
	case "result":
		data, rerr := s.eng.Store().Read(ctx, storage.ResultPath(issue.ProjectID, issue.ID, step.Key))
		if rerr != nil {
			return drawerPayload{Content: "(no result.json yet)"}, nil
		}
		return drawerPayload{Content: string(data)}, nil
	case "output":
		if isFinal && s.workspaceStepDone(ctx, issue, stepKey) {
			html, herr := s.renderWorkspaceTree(ctx, view)
			if herr != nil {
				return drawerPayload{Content: herr.Error()}, nil
			}
			return drawerPayload{ContentHTML: html}, nil
		}
		content, contentHTML, oerr := s.stepOutput(ctx, view, step)
		return drawerPayload{Content: content, ContentHTML: contentHTML}, oerr
	case "activity", "events":
		data, aerr := s.eng.Store().Read(ctx, storage.EventsPath(issue.ProjectID, issue.ID, step.Key))
		if aerr != nil {
			return drawerPayload{Content: "(no events yet)"}, nil
		}
		truncated := false
		if len(data) > drawerPayloadCap {
			data = data[:drawerPayloadCap]
			// Keep only complete lines: a mid-line cut leaves an invalid
			// trailing JSON record, which would forfeit the tree view for
			// exactly the large workspace logs that need it most.
			if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
				data = data[:i]
			}
			truncated = true
		}
		eventsJSON := eventsToJSONArray(data)
		content := string(data)
		if truncated {
			content += "\n... [truncated]"
		}
		return drawerPayload{Content: content, EventsJSON: eventsJSON, Truncated: truncated}, nil
	default:
		return drawerPayload{}, fmt.Errorf("unknown tab %q", tab)
	}
}

// eventsToJSONArray converts events.jsonl bytes into a single JSON array
// string for the tree viewer. Returns "" when the payload is empty or any
// line is not valid JSON — callers then fall back to the raw text view.
func eventsToJSONArray(data []byte) string {
	lines := bytes.Split(data, []byte("\n"))
	raws := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return ""
		}
		raws = append(raws, json.RawMessage(line))
	}
	if len(raws) == 0 {
		return ""
	}
	out, err := json.Marshal(raws)
	if err != nil {
		return ""
	}
	return string(out)
}

func (s *Server) stepOutput(ctx context.Context, view *orchestrator.IssueView, step sqlite.Step) (string, template.HTML, error) {
	issue := view.Issue
	projectID := issue.ProjectID
	issueID := issue.ID

	data, err := s.eng.Store().Read(ctx, storage.ResultPath(projectID, issueID, step.Key))
	if err != nil {
		return "(no output)", "", nil
	}
	// Always resolve against *this* step's result — not the issue's current-step attempt.
	outPath := storage.AttemptOutputPath(projectID, issueID, step.Key, 1)
	if res, err := readStepResultMeta(data); err == nil {
		if res.Attempt > 0 {
			outPath = storage.AttemptOutputPath(projectID, issueID, step.Key, res.Attempt)
		}
		if res.LatestOutput != "" {
			outPath = res.LatestOutput
		}
	}
	out, err := s.eng.Store().Read(ctx, outPath)
	if err != nil {
		return "(no output yet)", "", nil
	}
	if strings.HasSuffix(outPath, ".md") {
		var buf bytes.Buffer
		if err := drawerMarkdown.Convert(out, &buf); err == nil {
			return "", template.HTML(buf.String()), nil
		}
	}
	return string(out), "", nil
}

type stepResultMeta struct {
	Status       string `json:"status"`
	Attempt      int    `json:"attempt"`
	LatestOutput string `json:"latest_output"`
}

func readStepResultMeta(data []byte) (stepResultMeta, error) {
	var m stepResultMeta
	if err := jsonUnmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

// workspaceStepDone reports whether the given step finished successfully
// (result.json status == "done"). The final step of an issue's flow owns the
// workspace, so the workspace download and the workspace output tab gate on
// the final step.
func (s *Server) workspaceStepDone(ctx context.Context, issue *sqlite.Issue, stepKey string) bool {
	steps, err := s.eng.StepsForIssue(issue)
	if err != nil {
		return false
	}
	idx := stepIndexOf(steps, stepKey)
	if idx < 0 {
		idx = len(steps) - 1
	}
	data, err := s.eng.Store().Read(ctx, storage.ResultPath(issue.ProjectID, issue.ID, steps[idx].Key))
	if err != nil {
		return false
	}
	meta, err := readStepResultMeta(data)
	if err != nil {
		return false
	}
	return meta.Status == "done"
}

// workspaceNode is one entry in the implementer workspace tree.
type workspaceNode struct {
	Name     string
	RelPath  string // workspace-relative path for files; empty for dirs
	IsDir    bool
	Changed  bool // differs from source (files only)
	Children []workspaceNode
}

func (s *Server) renderWorkspaceTree(ctx context.Context, view *orchestrator.IssueView) (template.HTML, error) {
	issue := view.Issue
	// New layout first, legacy fallback for old issues (engine-level resolve).
	ws, err := s.eng.WorkspaceKey(ctx, issue)
	if err != nil {
		return "", err
	}
	exists, err := s.eng.Store().Exists(ctx, ws)
	if err != nil {
		return "", err
	}
	if !exists {
		return template.HTML(`<p class="card-meta">(no workspace yet)</p>`), nil
	}

	files, err := listAllFiles(ctx, s.eng.Store(), ws)
	if err != nil {
		return "", err
	}
	srcRoot := storage.SourcePath(issue.ProjectID, issue.ID)
	changed := map[string]bool{}
	for _, f := range files {
		rel := strings.TrimPrefix(strings.TrimPrefix(f, ws), "/")
		if rel == "" {
			continue
		}
		changed[rel] = fileDiffers(ctx, s.eng.Store(), srcRoot, ws, rel)
	}
	tree := buildWorkspaceTree(files, ws, changed)
	steps, _ := s.eng.StepsForIssue(issue)
	var lastKey string
	if len(steps) > 0 {
		lastKey = steps[len(steps)-1].Key
	}
	canDownload := s.workspaceStepDone(ctx, issue, lastKey)

	var b strings.Builder
	b.WriteString(`<div class="ws-browser">`)
	if canDownload {
		fmt.Fprintf(&b, `<div class="ws-toolbar"><a class="btn btn-primary" href="/api/issues/%d/workspace.zip">Download workspace (.zip)</a></div>`, issue.ID)
	} else {
		b.WriteString(`<div class="ws-toolbar"><span class="card-meta">Download available when the workspace step is done.</span></div>`)
	}
	if len(tree) == 0 {
		b.WriteString(`<p class="card-meta">(workspace is empty)</p>`)
	} else {
		b.WriteString(`<ul class="ws-tree" role="tree">`)
		writeWorkspaceNodes(&b, issue.ID, tree)
		b.WriteString(`</ul>`)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String()), nil
}

func buildWorkspaceTree(files []string, wsRoot string, changed map[string]bool) []workspaceNode {
	type mut struct {
		dirs  map[string]*mut
		files map[string]bool // name -> changed
	}
	root := &mut{dirs: map[string]*mut{}, files: map[string]bool{}}

	for _, f := range files {
		rel := strings.TrimPrefix(strings.TrimPrefix(f, wsRoot), "/")
		if rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		cur := root
		for i, p := range parts {
			if i == len(parts)-1 {
				cur.files[p] = changed[rel]
				continue
			}
			if cur.dirs[p] == nil {
				cur.dirs[p] = &mut{dirs: map[string]*mut{}, files: map[string]bool{}}
			}
			cur = cur.dirs[p]
		}
	}

	var convert func(m *mut, prefix string) []workspaceNode
	convert = func(m *mut, prefix string) []workspaceNode {
		var nodes []workspaceNode
		dirNames := make([]string, 0, len(m.dirs))
		for name := range m.dirs {
			dirNames = append(dirNames, name)
		}
		sort.Strings(dirNames)
		for _, name := range dirNames {
			childPrefix := name
			if prefix != "" {
				childPrefix = prefix + "/" + name
			}
			nodes = append(nodes, workspaceNode{
				Name:     name,
				IsDir:    true,
				Children: convert(m.dirs[name], childPrefix),
			})
		}
		fileNames := make([]string, 0, len(m.files))
		for name := range m.files {
			fileNames = append(fileNames, name)
		}
		sort.Strings(fileNames)
		for _, name := range fileNames {
			rel := name
			if prefix != "" {
				rel = prefix + "/" + name
			}
			nodes = append(nodes, workspaceNode{
				Name:    name,
				RelPath: rel,
				IsDir:   false,
				Changed: m.files[name],
			})
		}
		return nodes
	}

	return convert(root, "")
}

func writeWorkspaceNodes(b *strings.Builder, issueID int64, nodes []workspaceNode) {
	for _, n := range nodes {
		if n.IsDir {
			fmt.Fprintf(b, `<li class="ws-dir" role="treeitem" aria-expanded="true">`)
			fmt.Fprintf(b, `<span class="ws-dir-label">%s/</span>`, html.EscapeString(n.Name))
			if len(n.Children) > 0 {
				b.WriteString(`<ul class="ws-tree" role="group">`)
				writeWorkspaceNodes(b, issueID, n.Children)
				b.WriteString(`</ul>`)
			}
			b.WriteString(`</li>`)
			continue
		}
		changedClass := ""
		changedMark := ""
		if n.Changed {
			changedClass = " ws-changed"
			changedMark = ` <span class="ws-changed-mark" title="differs from source">•</span>`
		}
		// Lazy-load per-file diff on first expand (details toggle).
		fmt.Fprintf(b, `<li class="ws-file%s" role="treeitem">`, changedClass)
		fmt.Fprintf(b, `<details class="ws-file-details" data-path="%s" hx-get="/partials/issues/%d/workspace-file?path=%s" hx-trigger="toggle once" hx-target="find .ws-file-diff" hx-swap="innerHTML">`,
			html.EscapeString(n.RelPath),
			issueID,
			url.QueryEscape(n.RelPath),
		)
		fmt.Fprintf(b, `<summary><code class="ws-file-name">%s</code>%s</summary>`, html.EscapeString(n.Name), changedMark)
		b.WriteString(`<div class="ws-file-diff"><span class="card-meta">Expand to load diff…</span></div>`)
		b.WriteString(`</details></li>`)
	}
}

func fileDiffers(ctx context.Context, store storage.Port, srcRoot, wsRoot, rel string) bool {
	wsData, err := store.Read(ctx, path.Join(wsRoot, rel))
	if err != nil {
		return false
	}
	srcData, err := store.Read(ctx, path.Join(srcRoot, rel))
	if err != nil {
		return true // new file
	}
	return !bytes.Equal(srcData, wsData)
}

// singleFileDiff returns a unified-style diff for one workspace-relative path.
func singleFileDiff(ctx context.Context, store storage.Port, srcRoot, wsRoot, rel string) (string, error) {
	if err := storage.ValidateRelativePath(rel); err != nil {
		return "", err
	}
	wsPath := path.Join(wsRoot, rel)
	srcPath := path.Join(srcRoot, rel)

	wsData, wsErr := store.Read(ctx, wsPath)
	srcData, srcErr := store.Read(ctx, srcPath)

	var b strings.Builder
	switch {
	case wsErr != nil && srcErr != nil:
		return "", fmt.Errorf("file not found: %s", rel)
	case wsErr != nil:
		// Deleted relative to source (shouldn't appear in workspace tree, but handle).
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ /dev/null\n", rel))
		writeLineDiff(&b, strings.Split(string(srcData), "\n"), nil)
	case srcErr != nil:
		b.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", rel))
		writeLineDiff(&b, nil, strings.Split(string(wsData), "\n"))
	case bytes.Equal(srcData, wsData):
		return "(no differences — identical to source)", nil
	default:
		b.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", rel, rel))
		writeLineDiff(&b, strings.Split(string(srcData), "\n"), strings.Split(string(wsData), "\n"))
	}
	out := b.String()
	if len(out) > drawerPayloadCap {
		out = out[:drawerPayloadCap] + "\n... [truncated]"
	}
	return out, nil
}

func writeLineDiff(b *strings.Builder, a, c []string) {
	// Simple line-oriented diff (not LCS) for MVP readability.
	maxN := len(a)
	if len(c) > maxN {
		maxN = len(c)
	}
	for i := 0; i < maxN; i++ {
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

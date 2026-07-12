package server

import (
	"bytes"
	"context"
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
	"github.com/tuffrabit/gorchestrator/internal/storage"
)

const (
	phaseResearch        = "research"
	phasePlan            = "plan"
	phaseImplementation  = "implementation"
	drawerPayloadCap     = 256 * 1024
)

// drawerMarkdown renders agent output.md for the artifact drawer.
// WithUnsafe keeps intentional raw HTML in free-form agent artifacts (HTML
// pages, embedded tables, etc.); goldmark's default strips those to comments.
var drawerMarkdown = goldmark.New(
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

var knownPhases = []string{phaseResearch, phasePlan, phaseImplementation}

func normalizePhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case phaseResearch, "researcher":
		return phaseResearch
	case phasePlan, "planner":
		return phasePlan
	case phaseImplementation, "implementer", "impl":
		return phaseImplementation
	default:
		return ""
	}
}

func phaseLabel(phase string) string {
	switch phase {
	case phaseResearch:
		return "Research"
	case phasePlan:
		return "Plan"
	case phaseImplementation:
		return "Implementation"
	default:
		return phase
	}
}

func phaseAgent(phase string) string {
	switch phase {
	case phaseResearch:
		return "researcher"
	case phasePlan:
		return "planner"
	case phaseImplementation:
		return "implementer"
	default:
		return phase
	}
}

func (s *Server) drawerContent(r *http.Request, view *orchestrator.IssueView, tab, phase string) (string, template.HTML, error) {
	if view == nil || view.Issue == nil {
		return "", "", fmt.Errorf("no issue")
	}
	issue := view.Issue
	if phase == "" {
		phase = issue.CurrentPhase
	}
	phase = normalizePhase(phase)
	if phase == "" {
		phase = normalizePhase(issue.CurrentPhase)
	}
	if phase == "" {
		phase = phaseResearch
	}

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
		if phase == phaseImplementation {
			html, err := s.renderWorkspaceTree(ctx, view)
			if err != nil {
				return err.Error(), "", nil
			}
			return "", html, nil
		}
		return s.drawerPhaseOutput(ctx, view, phase)
	case "activity", "events":
		key := storage.EventsPath(projectID, issueID, phase)
		data, err := s.eng.Store().Read(ctx, key)
		if err != nil {
			return "(no events yet)", "", nil
		}
		if len(data) > drawerPayloadCap {
			data = append(data[:drawerPayloadCap], []byte("\n... [truncated]")...)
		}
		return string(data), "", nil
	default:
		return "", "", fmt.Errorf("unknown tab %q", tab)
	}
}

func (s *Server) drawerPhaseOutput(ctx context.Context, view *orchestrator.IssueView, phase string) (string, template.HTML, error) {
	issue := view.Issue
	projectID := issue.ProjectID
	issueID := issue.ID

	key := storage.ResultPath(projectID, issueID, phase)
	data, err := s.eng.Store().Read(ctx, key)
	if err != nil {
		return "(no output)", "", nil
	}
	// Always resolve against *this* phase's result — not the issue's current-phase attempt.
	outPath := storage.AttemptOutputPath(projectID, issueID, phase, 1)
	if res, err := readPhaseResultMeta(data); err == nil {
		if res.Attempt > 0 {
			outPath = storage.AttemptOutputPath(projectID, issueID, phase, res.Attempt)
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

type phaseResultMeta struct {
	Status       string `json:"status"`
	Attempt      int    `json:"attempt"`
	LatestOutput string `json:"latest_output"`
}

func readPhaseResultMeta(data []byte) (phaseResultMeta, error) {
	var m phaseResultMeta
	if err := jsonUnmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

// implementationDone reports whether the implementer phase finished successfully.
func (s *Server) implementationDone(ctx context.Context, projectID, issueID int64) bool {
	data, err := s.eng.Store().Read(ctx, storage.ResultPath(projectID, issueID, phaseImplementation))
	if err != nil {
		return false
	}
	meta, err := readPhaseResultMeta(data)
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
	ws := storage.WorkspacePath(issue.ProjectID, issue.ID)
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
	canDownload := s.implementationDone(ctx, issue.ProjectID, issue.ID)

	var b strings.Builder
	b.WriteString(`<div class="ws-browser">`)
	if canDownload {
		fmt.Fprintf(&b, `<div class="ws-toolbar"><a class="btn btn-primary" href="/api/issues/%d/workspace.zip">Download workspace (.zip)</a></div>`, issue.ID)
	} else {
		b.WriteString(`<div class="ws-toolbar"><span class="card-meta">Download available when implementation is done.</span></div>`)
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

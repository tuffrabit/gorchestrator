// Package web embeds dashboard templates and static assets.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"strconv"
	"sync"
)

//go:embed templates static
var content embed.FS

var (
	tmplOnce sync.Once
	tmpl     *template.Template
	tmplErr  error
)

// Templates parses and caches all HTML templates.
func Templates() (*template.Template, error) {
	tmplOnce.Do(func() {
		funcMap := template.FuncMap{
			"statusClass": statusClass,
			"statusLabel": statusLabel,
			"expandCard":  expandCard,
			"cardCtx":     cardCtx,
			"jsInt":       jsInt,
		}
		t := template.New("root").Funcs(funcMap)
		t, tmplErr = t.ParseFS(content, "templates/*.html", "templates/partials/*.html")
		tmpl = t
	})
	return tmpl, tmplErr
}

// Static returns the static asset filesystem rooted at static/.
func Static() fs.FS {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		return content
	}
	return sub
}

func statusClass(status string) string {
	switch status {
	case "queued":
		return "status-queued"
	case "in_progress":
		return "status-in-progress"
	case "waiting_human":
		return "status-waiting-human"
	case "failed":
		return "status-failed"
	case "done":
		return "status-done"
	case "cancelled":
		return "status-cancelled"
	case "stopped":
		return "status-stopped"
	default:
		return "status-queued"
	}
}

func statusLabel(status string) string {
	switch status {
	case "queued":
		return "waiting its turn"
	case "in_progress":
		return "active"
	case "waiting_human":
		return "needs human"
	case "failed":
		return "failed"
	case "done":
		return "done"
	case "cancelled":
		return "cancelled"
	case "stopped":
		return "stopped"
	default:
		return status
	}
}

// jsInt renders an integer as a bare JavaScript literal for use inside inline
// event handlers. html/template's contextual escaper pads plain ints in JS
// context (onclick="f( 1 )") and quotes string ones (onclick="f(\"1\")"), so
// templates that generate numeric call arguments use this helper instead. The
// value is formatted with strconv, so the output is always a numeric literal.
func jsInt(v any) template.JS {
	switch n := v.(type) {
	case int:
		return template.JS(strconv.Itoa(n))
	case int32:
		return template.JS(strconv.FormatInt(int64(n), 10))
	case int64:
		return template.JS(strconv.FormatInt(n, 10))
	case float64:
		return template.JS(strconv.FormatInt(int64(n), 10))
	case nil:
		return template.JS("0")
	default:
		return template.JS("0")
	}
}

// expandCard builds the data map for issue_card_inner from an IssueView.
func expandCard(issue any, csrf string, canWrite bool, expanded bool) map[string]any {
	return map[string]any{
		"Issue":    issue,
		"CSRF":     csrf,
		"CanWrite": canWrite,
		"Expanded": expanded,
	}
}

// cardCtx is used by list partials: cardCtx $root $issue expanded
func cardCtx(root map[string]any, issue any, expanded bool) map[string]any {
	csrf, _ := root["CSRF"].(string)
	canWrite, _ := root["CanWrite"].(bool)
	return expandCard(issue, csrf, canWrite, expanded)
}

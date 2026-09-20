package web

import (
	"bytes"
	"os"
	"regexp"
	"testing"
)

func readWebFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func hasPattern(t *testing.T, label string, pattern string, data []byte) {
	t.Helper()
	if !regexp.MustCompile(pattern).Match(data) {
		t.Errorf("%s does not match required pattern: %s", label, pattern)
	}
}

func hasLiteral(t *testing.T, label string, needle string, data []byte) {
	t.Helper()
	if !bytes.Contains(data, []byte(needle)) {
		t.Errorf("%s does not contain required literal: %q", label, needle)
	}
}

func TestResponsiveCSSHasRequiredPatterns(t *testing.T) {
	css := readWebFile(t, "static/css/app.css")

	patterns := []string{
		`(?m)@media\s*\(max-width:\s*768px\)`,
		`(?m)@media\s*\(max-width:\s*480px\)`,
		`(?m)#topnav-toggle:checked\s*~\s*\.topnav\s+\.topnav-links\s*\{[^}]*display\s*:\s*flex`,
		`(?m)\.topnav-toggle-label\s*\{[^}]*display\s*:\s*inline-flex`,
		`(?m)\.filters\s*\{[^}]*grid-template-columns\s*:\s*1fr`,
		`(?m)\.phase-strip\s*\{[^}]*overflow-x\s*:\s*auto`,
		`(?m)\.drawer\s*\{[^}]*--drawer-width\s*:\s*100%`,
		`(?m)\.table-wrap\s*\{[^}]*overflow-x\s*:\s*auto`,
		`(?m)min-height:\s*44px`,
		`(?m)min-height:\s*48px`,
		`(?m)font-size:\s*1rem`,
	}

	for _, pattern := range patterns {
		hasPattern(t, "app.css", pattern, css)
	}
}

func TestLayoutHasMobileTopNavToggle(t *testing.T) {
	layout := readWebFile(t, "templates/layout.html")

	needles := []string{
		`id="topnav-toggle"`,
		`for="topnav-toggle"`,
		`class="topnav-toggle-label`,
		`id="topnav-links"`,
		`class="topnav-links`,
	}

	for _, needle := range needles {
		hasLiteral(t, "layout.html", needle, layout)
	}
}

func TestFeedFiltersUseResponsiveFilterFormWithoutInlineStyle(t *testing.T) {
	feed := readWebFile(t, "templates/feed.html")

	hasLiteral(t, "feed.html", `class="filters`, feed)

	// The filter form may have htmx attributes, but must not use inline styles.
	if regexp.MustCompile(`(?m)<form[^>]*class="filters"[^>]*style=`).Match(feed) {
		t.Errorf("feed.html filter form should not contain inline style")
	}
}

func TestAdminTablesUseScrollWrapper(t *testing.T) {
	files := []string{
		"templates/permissions.html",
		"templates/escalation.html",
		"templates/notifications.html",
	}

	for _, file := range files {
		data := readWebFile(t, file)

		if !bytes.Contains(data, []byte("<table")) {
			continue
		}

		hasLiteral(t, file, `class="table-wrap`, data)
	}
}

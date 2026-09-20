package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSubmitDrawerFormDoesNotCloseDrawerOnNestedHTMXRequests guards against
// the bug where the project select's HTMX GET (flavors) fired an afterRequest
// event that bubbled up to the submit form's inline hx-on::after-request
// handler, closing the drawer before the user could pick a project.
func TestSubmitDrawerFormDoesNotCloseDrawerOnNestedHTMXRequests(t *testing.T) {
	const path = "templates/partials/drawer_submit.html"

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	html := string(data)

	formRE := regexp.MustCompile(`(?s)<form[^>]*hx-post="/partials/submit"[^>]*>`)
	formTag := formRE.FindString(html)

	if formTag == "" {
		t.Fatalf("expected submit form with hx-post=\"/partials/submit\" in %s", path)
	}

	if strings.Contains(formTag, "hx-on::after-request") {
		t.Errorf(
			"submit form must not contain hx-on::after-request because HTMX afterRequest events from nested selects/inputs can bubble and close the drawer. Found in form tag:\n%s",
			formTag,
		)
	}

	if strings.Contains(formTag, "closeDrawer") {
		t.Errorf(
			"submit form tag should not call closeDrawer inline. Use the app.js afterRequest listener for actual issue submission. Found in form tag:\n%s",
			formTag,
		)
	}

	requiredAttributes := []string{
		`hx-post="/partials/submit"`,
		`hx-target="#issue-feed"`,
		`hx-swap="afterbegin settle:0s"`,
		`hx-encoding="multipart/form-data"`,
		`enctype="multipart/form-data"`,
	}

	for _, attr := range requiredAttributes {
		if !strings.Contains(formTag, attr) {
			t.Errorf("expected submit form tag to contain %s, got:\n%s", attr, formTag)
		}
	}
}

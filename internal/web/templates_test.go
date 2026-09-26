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

// TestChatPendingBubbleStopsTypingAnimation guards the chat drawer bug where
// every chat-msg-pending bubble blinked the typing ellipsis forever. A pending
// assistant row stays status="pending" while the turn appends thought/tool
// rows and later reply segments after it, so the animation must be scoped to
// the thread's newest bubble only.
func TestChatPendingBubbleStopsTypingAnimation(t *testing.T) {
	cssPath := "static/css/app.css"
	cssData, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read %s: %v", cssPath, err)
	}
	css := string(cssData)

	guard := regexp.MustCompile(`(?s)\.chat-msg-pending:not\(:last-child\)[^{]*\{[^}]*animation:\s*none`)
	if !guard.MatchString(css) {
		t.Errorf("expected %s to stop the typing animation on superseded pending bubbles (rule .chat-msg-pending:not(:last-child) ... animation: none)", cssPath)
	}

	// The ellipsis itself must still animate for the newest pending bubble.
	if !regexp.MustCompile(`(?s)\.chat-typing i\s*\{[^}]*animation:\s*chat-typing-blink`).MatchString(css) {
		t.Errorf("expected %s to keep the chat-typing-blink animation on .chat-typing i", cssPath)
	}

	const tplPath = "templates/partials/chat_thread.html"
	tplData, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read %s: %v", tplPath, err)
	}
	tpl := string(tplData)

	if !strings.Contains(tpl, `class="chat-msg chat-msg-assistant chat-msg-pending"`) {
		t.Fatalf("expected pending assistant bubble markup in %s", tplPath)
	}
	// The pending bubble must sit directly in .chat-messages (no extra wrapper),
	// otherwise the :last-child guard above would not match it.
	if !regexp.MustCompile(`(?s)<div class="chat-messages">.*?{{range \.Messages}}`).MatchString(tpl) {
		t.Errorf("pending bubbles must be rendered as direct children of .chat-messages in %s", tplPath)
	}
}

// TestChatToolCallRendersAsOneBox guards the chat drawer bug where one tool
// call produced two boxes: a call box from the FunctionCall event and a second
// box from the FunctionResponse event. One call must render one <details>,
// with its arguments and its result as two parts inside it.
func TestChatToolCallRendersAsOneBox(t *testing.T) {
	const tplPath = "templates/partials/chat_thread.html"
	data, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read %s: %v", tplPath, err)
	}
	tpl := string(data)

	if got := strings.Count(tpl, `<details class="chat-msg chat-msg-tool`); got != 1 {
		t.Errorf("%s renders %d tool <details>; a tool call must render exactly one box", tplPath, got)
	}
	if got := strings.Count(tpl, `{{template "chat_tool_part"`); got != 2 {
		t.Errorf("%s invokes chat_tool_part %d times, want the call arguments and the result inside the same box", tplPath, got)
	}
	// Each part owns its own JSON tree scope so Expand/Raw do not leak from
	// the arguments tree into the result tree (or between two calls).
	if !strings.Contains(tpl, `class="chat-tool-part{{if .JSON}} json-tree-scope{{end}}"`) {
		t.Errorf("%s must scope each tool part's JSON tree to that part", tplPath)
	}
}

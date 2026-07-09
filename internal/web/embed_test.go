package web_test

import (
	"testing"
	"github.com/tuffrabit/gorchestrator/internal/web"
)

func TestTemplatesParse(t *testing.T) {
	tmpl, err := web.Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"feed.html", "login.html", "notifications.html", "partials/issue_card.html", "partials/issue_list.html", "partials/drawer_submit.html", "partials/drawer_artifact.html", "issue_card_inner"} {
		if tmpl.Lookup(name) == nil {
			t.Errorf("missing template %s", name)
		}
	}
}

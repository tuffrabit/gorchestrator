package trigger

import "testing"

func TestIsExternal(t *testing.T) {
	cases := map[string]bool{
		"":          false,
		"manual":    false,
		"cli":       false,
		"api":       false,
		"dashboard": false,
		"webhook":   true,
		"github":    true,
		"jira":      true,
	}
	for src, want := range cases {
		if got := IsExternal(src); got != want {
			t.Errorf("IsExternal(%q)=%v want %v", src, got, want)
		}
	}
}

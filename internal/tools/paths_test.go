package tools

import "testing"

func TestResolveAllowedPath_IssueRelative(t *testing.T) {
	issue := "projects/2/issues/2"
	source := "projects/2/issues/2/source"
	allow := []string{issue, source}

	cases := []struct {
		name    string
		in      string
		base    string
		want    string
		wantOK  bool
	}{
		{name: "empty uses base", in: "", base: issue, want: issue, wantOK: true},
		{name: "dot uses base", in: ".", base: issue, want: issue, wantOK: true},
		{name: "slash uses base", in: "/", base: issue, want: issue, wantOK: true},
		{name: "short source", in: "source", base: issue, want: source, wantOK: true},
		{name: "source file short", in: "source/index.html", base: issue, want: source + "/index.html", wantOK: true},
		{name: "full source key", in: source, base: issue, want: source, wantOK: true},
		{name: "full file key", in: source + "/main.go", base: issue, want: source + "/main.go", wantOK: true},
		{name: "research attempts short", in: "research/attempts", base: issue, want: issue + "/research/attempts", wantOK: true},
		{name: "attempts under issue", in: "attempts", base: issue, want: issue + "/attempts", wantOK: true},
		{name: "outside allowlist", in: "projects/9/issues/9", base: issue, wantOK: false},
		{name: "traversal rejected", in: "../secret", base: issue, wantOK: false},
		{name: "traversal nested", in: "source/../../etc", base: issue, wantOK: false},
		{name: "no base empty fails without open allowlist", in: "", base: "", wantOK: false},
		{name: "open allowlist root", in: ".", base: "", want: "", wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			al := allow
			if tc.name == "open allowlist root" {
				al = []string{""}
			}
			if tc.name == "no base empty fails without open allowlist" {
				al = allow
			}
			got, ok := resolveAllowedPath(tc.in, al, tc.base)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.wantOK, got)
			}
			if tc.wantOK && got != tc.want {
				t.Fatalf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveAllowedPath_ImplementerWorkspaceBase(t *testing.T) {
	issue := "projects/1/issues/1"
	source := "projects/1/issues/1/source"
	ws := "projects/1/issues/1/implementation/workspace"
	allow := []string{issue, source, ws}

	got, ok := resolveAllowedPath("main.go", allow, ws)
	if !ok || got != ws+"/main.go" {
		t.Fatalf("main.go → %q ok=%v, want workspace-relative", got, ok)
	}
	// Short "source" should resolve under issue, not workspace/source.
	got, ok = resolveAllowedPath("source", allow, ws)
	if !ok || got != source {
		t.Fatalf("source → %q ok=%v, want %q", got, ok, source)
	}
	got, ok = resolveAllowedPath(".", allow, ws)
	if !ok || got != ws {
		t.Fatalf(". → %q ok=%v, want workspace", got, ok)
	}
}

func TestResolveAllowedPath_WriteWorkspaceOnly(t *testing.T) {
	ws := "projects/1/issues/1/implementation/workspace"
	got, ok := resolveAllowedPath("pkg/a.go", []string{ws}, ws)
	if !ok || got != ws+"/pkg/a.go" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	_, ok = resolveAllowedPath("../source/x", []string{ws}, ws)
	if ok {
		t.Fatal("expected traversal reject")
	}
	// Full key outside workspace allowlist rejected.
	_, ok = resolveAllowedPath("projects/1/issues/1/source/x", []string{ws}, ws)
	if ok {
		t.Fatal("expected source path rejected when only workspace allowlisted")
	}
}

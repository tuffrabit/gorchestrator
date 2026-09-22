package storage

import "testing"

func TestValidateRelativePath(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"research/result.json", false},
		{"../etc/passwd", true},
		{"/etc/passwd", true},
		{"", true},
		{"a/../../b", true},
		{"plan/attempts/1/output.md", false},
	}
	for _, tc := range cases {
		err := ValidateRelativePath(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("%q: want error", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%q: %v", tc.in, err)
		}
	}
}

func TestChatSourcePath(t *testing.T) {
	if got := ChatSourcePath(7); got != "projects/7/chat/source" {
		t.Fatalf("ChatSourcePath(7) = %q, want projects/7/chat/source", got)
	}
	// The project-level chat worktree must never collide with per-issue
	// source or workspace paths.
	if SourcePath(7, 3) == ChatSourcePath(7) || WorkspacePath(7, 3) == ChatSourcePath(7) {
		t.Fatal("ChatSourcePath collides with issue paths")
	}
}

func TestJoinContained(t *testing.T) {
	key, err := JoinContained("projects/1/issues/2", "research/result.json")
	if err != nil {
		t.Fatal(err)
	}
	if key != "projects/1/issues/2/research/result.json" {
		t.Fatalf("key = %q", key)
	}
	if _, err := JoinContained("projects/1/issues/2", "../3/secret"); err == nil {
		t.Fatal("expected escape rejection")
	}
}

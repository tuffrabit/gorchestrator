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

func TestWorkspacePaths(t *testing.T) {
	// The issue-level workspace is shared by every editing step.
	if got := WorkspacePath(7, 3); got != "projects/7/issues/3/workspace" {
		t.Fatalf("WorkspacePath(7, 3) = %q, want projects/7/issues/3/workspace", got)
	}
	// Legacy issues kept their workspace under the implementation phase dir.
	if got := LegacyWorkspacePath(7, 3); got != "projects/7/issues/3/implementation/workspace" {
		t.Fatalf("LegacyWorkspacePath(7, 3) = %q, want projects/7/issues/3/implementation/workspace", got)
	}
	if WorkspacePath(7, 3) == LegacyWorkspacePath(7, 3) {
		t.Fatal("WorkspacePath and LegacyWorkspacePath must not collide")
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

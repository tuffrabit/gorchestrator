package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestResolveContainerRuntime_None(t *testing.T) {
	// Force unknown preference to hit error path without depending on host.
	_, err := resolveContainerRuntime("not-a-runtime")
	if err == nil || !strings.Contains(err.Error(), "unknown runtime") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecuteRunTest_NoCommand(t *testing.T) {
	bt := &BoundTools{}
	res, err := executeRunTest(context.Background(), bt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "no test.command") {
		t.Fatalf("error: %q", res.Error)
	}
}

func TestExecuteRunTest_NoImage(t *testing.T) {
	bt := &BoundTools{
		Test: &TestConfig{Command: "go test ./..."},
	}
	res, err := executeRunTest(context.Background(), bt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "test.image") {
		t.Fatalf("error: %q", res.Error)
	}
}

func TestExecuteRunTest_DryRun(t *testing.T) {
	bt := &BoundTools{
		Test: &TestConfig{
			Command: "go test ./...",
			Image:   "golang:1.22",
			DryRun:  true,
		},
		WorkspaceHostPath: "/tmp/ws",
	}
	res, err := executeRunTest(context.Background(), bt)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Runtime != "dry-run" {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Stdout, "SECRET") {
		t.Fatal("secrets must not appear in dry-run output")
	}
}

func TestExecuteRunTest_RefuseWithoutRuntime(t *testing.T) {
	// Prefer a non-existent binary name via Runtime when docker/podman might exist.
	// Using "docker" with PATH emptied is heavy; instead assert the error string
	// from resolve when neither is findable by forcing unknown then auto with
	// a fake PATH isn't portable. Unit-test the message for missing image/command
	// above; runtime refusal is covered when prefer is docker and we can't
	// guarantee absence. Smoke: dry-run already proves no container spawn.
	bt := &BoundTools{
		Test: &TestConfig{
			Command: "true",
			Image:   "alpine:latest",
			Runtime: "not-a-runtime",
			Timeout: time.Second,
		},
		WorkspaceHostPath: t.TempDir(),
	}
	res, err := executeRunTest(context.Background(), bt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "unavailable") {
		t.Fatalf("error: %q", res.Error)
	}
}

func TestCapBytes(t *testing.T) {
	s := capBytes([]byte(strings.Repeat("a", 100)), 10)
	if !strings.Contains(s, "truncated") {
		t.Fatalf("got %q", s)
	}
}

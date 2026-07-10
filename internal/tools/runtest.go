package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	defaultTestStdoutCap = 64 * 1024
	defaultTestStderrCap = 64 * 1024
	defaultTestCPU       = "1"
	defaultTestMemory    = "512m"
	defaultTestTimeout   = 60 * time.Second
)

// TestConfig is the project's immutable test command configuration.
// Secrets are injected from host env by name only — never into task.json.
type TestConfig struct {
	Command    string        // required
	Image      string        // required when Command is set
	Timeout    time.Duration // default 60s
	CPU        string        // default "1"
	Memory     string        // default "512m"
	SecretsEnv []string      // host env var names to inject
	Runtime    string        // auto | docker | podman
	DryRun     bool          // stub success without container
}

// RunTestArgs is empty — the agent cannot change the command.
type RunTestArgs struct{}

// RunTestResult is returned to the model (size-capped, no secret values).
type RunTestResult struct {
	ExitCode     int    `json:"exit_code"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	TimedOut     bool   `json:"timed_out"`
	Error        string `json:"error,omitempty"`
	SecretsCount int    `json:"secrets_injected"`
	Runtime      string `json:"runtime,omitempty"`
}

func newRunTestTool(bt *BoundTools) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "run_test",
		Description: "Run the project's pre-configured test command inside a container sandbox (no network, workspace-only mount, CPU/memory/time limits). The command is immutable and cannot be changed by the agent. Returns size-capped stdout/stderr.",
	}, func(ctx agent.Context, _ RunTestArgs) (RunTestResult, error) {
		return executeRunTest(ctx, bt)
	})
}

func executeRunTest(ctx context.Context, bt *BoundTools) (RunTestResult, error) {
	cfg := bt.Test
	if cfg == nil || strings.TrimSpace(cfg.Command) == "" {
		return RunTestResult{Error: "run_test unavailable: no test.command configured for this project"}, nil
	}
	if cfg.DryRun {
		return RunTestResult{
			ExitCode: 0,
			Stdout:   "dry-run: run_test stub success (no container)",
			Runtime:  "dry-run",
		}, nil
	}
	if strings.TrimSpace(cfg.Image) == "" {
		return RunTestResult{Error: "run_test unavailable: test.image is required when test.command is set"}, nil
	}
	if bt.WorkspaceHostPath == "" {
		return RunTestResult{Error: "run_test unavailable: workspace host path not set"}, nil
	}

	runtime, err := resolveContainerRuntime(cfg.Runtime)
	if err != nil {
		return RunTestResult{Error: err.Error()}, nil
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}
	cpu := cfg.CPU
	if cpu == "" {
		cpu = defaultTestCPU
	}
	mem := cfg.Memory
	if mem == "" {
		mem = defaultTestMemory
	}

	// Build secret env pairs without logging values.
	var envArgs []string
	secretsCount := 0
	for _, name := range cfg.SecretsEnv {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		envArgs = append(envArgs, "-e", name+"="+val)
		secretsCount++
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"run", "--rm",
		"--network=none",
		"--cpus", cpu,
		"--memory", mem,
		"-v", bt.WorkspaceHostPath + ":/workspace:rw",
		"-w", "/workspace",
	}
	args = append(args, envArgs...)
	args = append(args, cfg.Image, "sh", "-c", cfg.Command)

	cmd := exec.CommandContext(runCtx, runtime, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	result := RunTestResult{
		SecretsCount: secretsCount,
		Runtime:      runtime,
		Stdout:       capBytes(stdout.Bytes(), defaultTestStdoutCap),
		Stderr:       capBytes(stderr.Bytes(), defaultTestStderrCap),
	}

	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		result.Error = fmt.Sprintf("run_test timed out after %s", timeout)
		return result, nil
	}
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			result.ExitCode = ee.ExitCode()
		} else {
			result.ExitCode = -1
			result.Error = runErr.Error()
		}
		return result, nil
	}
	result.ExitCode = 0
	return result, nil
}

func resolveContainerRuntime(prefer string) (string, error) {
	prefer = strings.ToLower(strings.TrimSpace(prefer))
	switch prefer {
	case "docker":
		if _, err := exec.LookPath("docker"); err != nil {
			return "", fmt.Errorf("run_test unavailable: docker not found on PATH")
		}
		return "docker", nil
	case "podman":
		if _, err := exec.LookPath("podman"); err != nil {
			return "", fmt.Errorf("run_test unavailable: podman not found on PATH")
		}
		return "podman", nil
	case "", "auto":
		if _, err := exec.LookPath("docker"); err == nil {
			return "docker", nil
		}
		if _, err := exec.LookPath("podman"); err == nil {
			return "podman", nil
		}
		return "", fmt.Errorf("run_test unavailable: no container runtime (docker/podman)")
	default:
		return "", fmt.Errorf("run_test unavailable: unknown runtime %q", prefer)
	}
}

func capBytes(b []byte, max int) string {
	if max <= 0 || len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + fmt.Sprintf("\n…[truncated, total %d bytes]", len(b))
}

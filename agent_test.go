package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubCLI writes a tiny executable named name into dir and returns its
// path. The stub echoes its arguments and exits with code.
func stubCLI(t *testing.T, dir, name, body string, code int) string {
	t.Helper()
	script := "#!/bin/sh\n" + body + "\n"
	if code != 0 {
		script += "exit " + strconv.Itoa(code) + "\n"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentRunnerWithStubs(t *testing.T) {
	ctx := context.Background()
	binDir := t.TempDir()
	stubCLI(t, binDir, "codex", "echo codex-got: \"$@\"", 0)
	stubCLI(t, binDir, "claude", "echo claude-got: \"$@\"", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	runner := ExecAgentRunner{Timeout: time.Minute}
	out, err := runner.Run(ctx, "codex", "do the thing", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "codex-got: exec do the thing") {
		t.Fatalf("codex output = %q", out)
	}
	out, err = runner.Run(ctx, "claude", "plan it", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "claude-got: -p plan it") {
		t.Fatalf("claude output = %q", out)
	}
}

func TestAgentRunnerFailure(t *testing.T) {
	ctx := context.Background()
	binDir := t.TempDir()
	stubCLI(t, binDir, "codex", "echo boom >&2", 1)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	runner := ExecAgentRunner{Timeout: time.Minute}
	_, err := runner.Run(ctx, "codex", "fail", t.TempDir())
	if err == nil {
		t.Fatal("expected an error from a failing agent")
	}
	if !strings.Contains(err.Error(), "agent codex") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentRunnerUnknownCLI(t *testing.T) {
	ctx := context.Background()
	runner := ExecAgentRunner{Timeout: time.Minute}
	if _, err := runner.Run(ctx, "not-a-cli", "x", t.TempDir()); err == nil {
		t.Fatal("expected an error for an unknown agent CLI")
	}
}

func TestAgentRunnerMissingBinary(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PATH", t.TempDir()) // empty PATH: no codex binary
	runner := ExecAgentRunner{Timeout: time.Minute}
	_, err := runner.Run(ctx, "codex", "x", t.TempDir())
	if err == nil {
		t.Fatal("expected an error when the agent binary is missing")
	}
}

func TestPrompts(t *testing.T) {
	issue, err := ParseIssue([]byte(sampleIssue))
	if err != nil {
		t.Fatal(err)
	}
	plan := PlanPrompt(issue)
	for _, want := range []string{"tasks.md", "Acceptance criteria", "Verify:", "Never read .env"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan prompt missing %q", want)
		}
	}
	tasks := BuildPlan(issue, mustLoadRealConfig(t))
	gen := GeneratePrompt(tasks[0], issue)
	for _, want := range []string{"test-first", "Never read .env", "Never modify .github/", "Never commit"} {
		if !strings.Contains(gen, want) {
			t.Fatalf("generate prompt missing %q", want)
		}
	}
}

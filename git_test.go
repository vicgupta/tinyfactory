package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestRepo(t *testing.T) (repoDir, bareDir string) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	bareDir = filepath.Join(base, "origin.git")
	repoDir = filepath.Join(base, "repo")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@local", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@local")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(base, "init", "--bare", bareDir)
	run(base, "init", repoDir)
	run(repoDir, "config", "user.name", "test")
	run(repoDir, "config", "user.email", "test@local")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repoDir, "add", "-A")
	run(repoDir, "commit", "-m", "initial")
	run(repoDir, "remote", "add", "origin", bareDir)
	run(repoDir, "push", "-u", "origin", "HEAD")
	return repoDir, bareDir
}

func TestGitWorktreeCommitPush(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initTestRepo(t)
	git := ExecGitRunner{}
	worktree := filepath.Join(t.TempDir(), "wt")

	branch, err := git.CurrentBranch(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := git.RevParse(ctx, repoDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Fatalf("HEAD sha = %q, want 40 hex chars", sha)
	}

	if err := git.CreateWorktree(ctx, repoDir, worktree, "tinyfactory/test-item", branch); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := git.StatusPorcelain(ctx, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(ChangedPaths(status)) != 1 || ChangedPaths(status)[0] != "feature.txt" {
		t.Fatalf("status = %v, want [feature.txt]", status)
	}
	if err := git.CommitAll(ctx, worktree, "add feature"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	status, err = git.StatusPorcelain(ctx, worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 0 {
		t.Fatalf("status after commit = %v, want clean", status)
	}
	if err := git.Push(ctx, worktree, "origin", "tinyfactory/test-item"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// The branch now exists on the remote.
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "origin", "tinyfactory/test-item")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "tinyfactory/test-item") {
		t.Fatalf("remote does not have the branch: %s", out)
	}
}

func TestGitProtectedPaths(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initTestRepo(t)
	git := ExecGitRunner{}

	// Commit a protected file first: the guard is about tracked files an
	// agent edits, and `git checkout --` only reverts tracked files.
	ciPath := filepath.Join(repoDir, ".github", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(ciPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ciPath, []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "git", "add", "-A")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "add ci")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}

	worktree := filepath.Join(t.TempDir(), "wt")
	branch, err := git.CurrentBranch(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.CreateWorktree(ctx, repoDir, worktree, "tinyfactory/protected", branch); err != nil {
		t.Fatal(err)
	}
	// Simulate an agent editing a protected file and adding a normal one.
	wtCI := filepath.Join(worktree, ".github", "workflows", "ci.yml")
	if err := os.WriteFile(wtCI, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "ok.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := git.StatusPorcelain(ctx, worktree)
	if err != nil {
		t.Fatal(err)
	}
	changed := ChangedPaths(status)
	var protected []string
	for _, p := range changed {
		if IsProtectedPath(p, []string{".github/", "evals/"}) {
			protected = append(protected, p)
		}
	}
	if len(protected) != 1 || protected[0] != ".github/workflows/ci.yml" {
		t.Fatalf("protected = %v, want [.github/workflows/ci.yml]", protected)
	}
	// Revert the protected path; the normal file must survive.
	if err := git.CheckoutPaths(ctx, worktree, protected); err != nil {
		t.Fatalf("CheckoutPaths: %v", err)
	}
	data, err := os.ReadFile(wtCI)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "name: ci\n" {
		t.Fatalf("protected file was not reverted, got %q", data)
	}
	if _, err := os.Stat(filepath.Join(worktree, "ok.txt")); err != nil {
		t.Fatal("normal file should survive the revert")
	}
}

func TestChangedPaths(t *testing.T) {
	lines := []string{
		" M README.md",
		"?? new.txt",
		"A  added.go",
		"R  old.go -> new.go",
		" D deleted.txt",
	}
	got := ChangedPaths(lines)
	want := []string{"README.md", "new.txt", "added.go", "new.go", "deleted.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ChangedPaths = %v, want %v", got, want)
	}
}

func TestIsProtectedPath(t *testing.T) {
	protected := []string{".github/", "evals/", "factory-flow.yaml"}
	cases := map[string]bool{
		".github/workflows/ci.yml": true,
		".github/CODEOWNERS":       true,
		"evals/suite.yaml":         true,
		"factory-flow.yaml":        true,
		".githubx/file":            false,
		"src/main.go":              false,
		"evalsx/a":                 false,
	}
	for path, want := range cases {
		if got := IsProtectedPath(path, protected); got != want {
			t.Fatalf("IsProtectedPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Fix the login redirect": "fix-the-login-redirect",
		"  Spaces & CAPS!! ":     "spaces-caps",
		"":                       "item",
		"!!!":                    "item",
		"a-very-long-title-that-keeps-going-and-going-and-going-past-forty-chars": "a-very-long-title-that-keeps-going-and-g",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

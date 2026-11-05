package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPreview(t *testing.T) {
	issue, err := ParseIssue([]byte(sampleIssue))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Preview(issue, mustLoadRealConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"spec gate: PASS",
		"tasks (mechanical breakdown",
		"After login, the browser shows the dashboard.",
		"verify: go test ./...",
		"validate-diff: go vet ./...",
		"run-tests: go test -count=1 ./...",
		"verify-behavior",
		"merge commit (never squash)",
		"send-backs: max 2",
		"stop after 2 consecutive failures",
		"model routing: plan=claude, generate=codex",
		"caps: NOT SET",
		"estimated cost: unknown",
		"preview made no changes",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("preview missing %q\n--- report ---\n%s", want, report)
		}
	}
}

func TestPreviewSpecGateFailure(t *testing.T) {
	issue, err := ParseIssue([]byte("# Vague idea\n\n## Outcome\nDo a thing.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(issue, mustLoadRealConfig(t)); err == nil {
		t.Fatal("Preview: expected a spec-gate error, got nil")
	} else if !strings.Contains(err.Error(), "spec gate failed") {
		t.Fatalf("Preview error = %v, want a spec-gate error", err)
	}
}

func TestParseTasksMDRoundTrip(t *testing.T) {
	issue, err := ParseIssue([]byte(sampleIssue))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustLoadRealConfig(t)
	want := BuildPlan(issue, cfg)
	got, err := ParseTasksMD(RenderTasksMD(issue, want))
	if err != nil {
		t.Fatalf("ParseTasksMD(RenderTasksMD(...)): %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d tasks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Title != want[i].Title || got[i].Verify != want[i].Verify {
			t.Errorf("task %d = %+v, want %+v", i+1, got[i], want[i])
		}
		if len(got[i].AcceptanceCriteria) != len(want[i].AcceptanceCriteria) {
			t.Errorf("task %d criteria = %v, want %v", i+1, got[i].AcceptanceCriteria, want[i].AcceptanceCriteria)
		}
	}
}

func TestParseTasksMDErrors(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{"no tasks", "# Tasks\n\nNothing here.\n", "no tasks found"},
		{"no criteria", "Task 1: Do it\nSome body.\nVerify: `go test ./...`\n", "missing acceptance criteria"},
		{"no verify", "Task 1: Do it\nAcceptance criteria:\n- it works\n", "missing Verify command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTasksMD(tc.md); err == nil {
				t.Fatal("expected an error, got nil")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func writeIssueFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issue.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// liveRunner returns a Runner with stubbed git/agent and the given store.
func liveRunner(t *testing.T, cfg *Config, store *Store) *Runner {
	t.Helper()
	return &Runner{
		Config: cfg,
		Git:    ExecGitRunner{},
		Agent:  ExecAgentRunner{Timeout: time.Minute},
		Store:  store,
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	r := liveRunner(t, mustLoadRealConfig(t), NewStore(stateDir))
	r.DryRun = true
	if err := r.Run(ctx, writeIssueFile(t, sampleIssue)); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	// Nothing was written: no state.json, no events.jsonl.
	if _, err := os.Stat(filepath.Join(stateDir, "state.json")); !os.IsNotExist(err) {
		t.Error("dry run wrote state.json")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Error("dry run wrote events.jsonl")
	}
}

func TestRunDryRunSpecGateFailure(t *testing.T) {
	ctx := context.Background()
	r := liveRunner(t, mustLoadRealConfig(t), NewStore(t.TempDir()))
	r.DryRun = true
	err := r.Run(ctx, writeIssueFile(t, "# Vague\n\n## Outcome\nDo a thing.\n"))
	if err == nil {
		t.Fatal("expected a spec-gate error, got nil")
	}
	if !strings.Contains(err.Error(), "spec gate failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunLiveRefusesWithoutCaps(t *testing.T) {
	ctx := context.Background()
	// The real config has all three caps null: a live run must refuse
	// before any write, even with a repo and token present.
	r := liveRunner(t, mustLoadRealConfig(t), NewStore(t.TempDir()))
	r.Repo = "octocat/hello-world"
	r.GitHub = NewGitHubClient("https://api.github.com", "unused")
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected a caps error, got nil")
	}
	if !strings.Contains(err.Error(), "caps not set") {
		t.Fatalf("error = %v, want a caps error", err)
	}
}

func TestRunLiveRefusesWithoutRepo(t *testing.T) {
	ctx := context.Background()
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	r := liveRunner(t, cfg, NewStore(t.TempDir()))
	r.GitHub = NewGitHubClient("https://api.github.com", "unused")
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected a no-repo error, got nil")
	}
	if !strings.Contains(err.Error(), "--repo owner/name") {
		t.Fatalf("error = %v, want a --repo error", err)
	}
}

func TestRunLiveRefusesWithoutToken(t *testing.T) {
	ctx := context.Background()
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	r := liveRunner(t, cfg, NewStore(t.TempDir()))
	r.Repo = "octocat/hello-world"
	// GitHub client deliberately nil: no token.
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected a no-token error, got nil")
	}
	if !strings.Contains(err.Error(), "TINYFACTORY_TOKEN") {
		t.Fatalf("error = %v, want a token error", err)
	}
}

func TestRunLiveRefusesWithoutRepoDir(t *testing.T) {
	ctx := context.Background()
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	r := liveRunner(t, cfg, NewStore(t.TempDir()))
	r.Repo = "octocat/hello-world"
	r.GitHub = NewGitHubClient("https://api.github.com", "unused")
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected a no-repo-dir error, got nil")
	}
	if !strings.Contains(err.Error(), "--repo-dir") {
		t.Fatalf("error = %v, want a --repo-dir error", err)
	}
}

func TestRunLiveRefusesWrongOrigin(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initTestRepo(t)
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	r := liveRunner(t, cfg, NewStore(t.TempDir()))
	r.Repo = "octocat/hello-world"
	r.RepoDir = repoDir
	r.GitHub = NewGitHubClient("https://api.github.com", "unused")
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected an origin-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "does not look like octocat/hello-world") {
		t.Fatalf("error = %v, want an origin-mismatch error", err)
	}
	// No state was written: the guard failed before any write.
	if _, statErr := os.Stat(filepath.Join(r.Store.Dir, "state.json")); !os.IsNotExist(statErr) {
		t.Error("guard failure wrote state.json")
	}
}

func TestRunLiveSpecGateSendsBack(t *testing.T) {
	ctx := context.Background()
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	store := NewStore(t.TempDir())
	// A real local clone whose origin genuinely points at the target
	// repo, so the guards pass and the run reaches the spec gate.
	repoDir, _ := initGoRepo(t, "octocat", "hello-world")
	r := liveRunner(t, cfg, store)
	r.Repo = "octocat/hello-world"
	r.RepoDir = repoDir
	r.GitHub = NewGitHubClient("https://api.github.com", "unused")
	err := r.Run(ctx, writeIssueFile(t, "# Vague\n\n## Outcome\nDo a thing.\n"))
	if err == nil {
		t.Fatal("expected a spec-gate error, got nil")
	}
	if !strings.Contains(err.Error(), "spec gate failed") {
		t.Fatalf("error = %v", err)
	}
	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Status != "sent_back" || st.Stage != StageIntake {
		t.Fatalf("state = %+v, want sent_back/intake", st)
	}
	if st.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0 (a spec-gate send-back is the issue's fault)", st.ConsecutiveFailures)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Event == "item_sent_back" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want an item_sent_back event", events)
	}
}

func TestRunStopsAfterConsecutiveFailures(t *testing.T) {
	ctx := context.Background()
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	store := NewStore(t.TempDir())
	if err := store.SaveState(&State{
		Issue:               "old item",
		Stage:               StageFailed,
		Status:              "failed",
		ConsecutiveFailures: 2,
		StartedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	r := liveRunner(t, cfg, store)
	r.Repo = "octocat/hello-world"
	r.RepoDir, _ = initGoRepo(t, "octocat", "hello-world")
	r.GitHub = NewGitHubClient("https://api.github.com", "unused")
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected a stopped-loop error, got nil")
	}
	if !strings.Contains(err.Error(), "loop stopped after 2 consecutive failures") || !strings.Contains(err.Error(), "tinyfactory reset") {
		t.Fatalf("error = %v, want the stopped-loop error with the reset hint", err)
	}
}

// setCaps sets the three live caps on a config loaded from the real file.
func setCaps(cfg *Config, perRun, perDay float64, merges int) {
	cfg.Caps.CostUSDPerRun = &perRun
	cfg.Caps.CostUSDPerDay = &perDay
	cfg.Caps.MergesPerDay = &merges
}

func TestGuardScanAndRevert(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initTestRepo(t)
	git := ExecGitRunner{}

	// Commit a protected file first so the guard sees a tracked change.
	ciPath := filepath.Join(repoDir, ".github", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(ciPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ciPath, []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, repoDir, "add", "-A")
	runGitIn(t, repoDir, "commit", "-m", "add ci")

	branch, err := git.CurrentBranch(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if err := git.CreateWorktree(ctx, repoDir, wt, "tinyfactory/guard", branch); err != nil {
		t.Fatal(err)
	}
	// Agent misbehavior: edit the protected CI file, create an untracked
	// .env, create an untracked file under evals/, and add a normal file.
	if err := os.WriteFile(filepath.Join(wt, ".github", "workflows", "ci.yml"), []byte("weakened\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "evals"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "evals", "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "ok.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &Runner{Config: mustLoadRealConfig(t), Git: git}
	violations, err := r.guardScan(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".github/workflows/ci.yml", ".env", "evals/"}
	if strings.Join(violations, ",") != strings.Join(want, ",") {
		t.Fatalf("guardScan = %v, want %v", violations, want)
	}
	reverted, err := r.revertViolations(ctx, wt, violations)
	if err != nil {
		t.Fatalf("revertViolations: %v", err)
	}
	if len(reverted) != 3 {
		t.Fatalf("reverted = %v, want 3 paths", reverted)
	}
	data, err := os.ReadFile(filepath.Join(wt, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "name: ci\n" {
		t.Fatalf("CI file was not reverted, got %q", data)
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Error(".env should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(wt, "evals", "new.txt")); !os.IsNotExist(err) {
		t.Error("evals/new.txt should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(wt, "ok.txt")); err != nil {
		t.Error("the normal file should survive the revert")
	}
}

func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// --- end-to-end live run against stub agents + a fake GitHub ---

// initGoRepo builds a minimal Go module repo with a bare remote whose
// path contains owner/name, so the origin check passes locally.
func initGoRepo(t *testing.T, owner, name string) (repoDir, bareDir string) {
	t.Helper()
	base := t.TempDir()
	bareDir = filepath.Join(base, owner, name+".git")
	if err := os.MkdirAll(filepath.Dir(bareDir), 0o755); err != nil {
		t.Fatal(err)
	}
	repoDir = filepath.Join(base, "clone")
	runGitIn(t, base, "init", "--bare", bareDir)
	runGitIn(t, base, "init", "-b", "main", repoDir)
	runGitIn(t, repoDir, "config", "user.name", "test")
	runGitIn(t, repoDir, "config", "user.email", "test@local")
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/demo\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, repoDir, "add", "-A")
	runGitIn(t, repoDir, "commit", "-m", "initial")
	runGitIn(t, repoDir, "remote", "add", "origin", bareDir)
	runGitIn(t, repoDir, "push", "-u", "origin", "HEAD")
	return repoDir, bareDir
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
}

// fakeGitHubE2E serves the four endpoints the live run uses, always
// green, and records every request in order.
func fakeGitHubE2E(t *testing.T, allowMergeCommit bool) (*httptest.Server, *[]recordedRequest, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var log []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		log = append(log, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octocat/hello-world":
			writeJSON(t, w, http.StatusOK, map[string]any{
				"name": "hello-world", "full_name": "octocat/hello-world",
				"default_branch": "main", "private": false,
				"allow_merge_commit": allowMergeCommit,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octocat/hello-world/pulls":
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			head, _ := payload["head"].(string)
			writeJSON(t, w, http.StatusCreated, map[string]any{
				"number": 7, "html_url": "https://github.com/octocat/hello-world/pull/7",
				"state": "open", "merged": false,
				"head": map[string]any{"sha": strings.Repeat("a", 40), "ref": head},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octocat/hello-world/pulls/7/merge":
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["merge_method"] != "merge" {
				writeJSON(t, w, http.StatusMethodNotAllowed, map[string]any{"message": "only merge commits"})
				return
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"merged": true})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/octocat/hello-world/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs"):
			writeJSON(t, w, http.StatusOK, map[string]any{
				"total_count": 1,
				"check_runs": []map[string]any{
					{"name": "ci", "status": "completed", "conclusion": "success"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &log, &mu
}

func TestRunLiveEndToEnd(t *testing.T) {
	ctx := context.Background()
	repoDir, bareDir := initGoRepo(t, "octocat", "hello-world")

	// Stub agents on PATH: claude writes tasks.md, codex writes a file.
	binDir := t.TempDir()
	stubCLI(t, binDir, "claude", "printf '# Tasks — demo\\n\\nTask 1: Add feature\\nImplement the feature.\\nAcceptance criteria:\\n- feature exists\\nVerify: `go test ./...`\\n' > tasks.md", 0)
	stubCLI(t, binDir, "codex", "printf 'package main\\n\\nfunc Feature() string { return \"x\" }\\n' > feature.go", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	srv, reqLog, mu := fakeGitHubE2E(t, true)
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	store := NewStore(t.TempDir())
	r := &Runner{
		Config:        cfg,
		Git:           ExecGitRunner{},
		Agent:         ExecAgentRunner{Timeout: time.Minute},
		GitHub:        NewGitHubClient(srv.URL, "test-token"),
		Store:         store,
		Repo:          "octocat/hello-world",
		RepoDir:       repoDir,
		CIWait:        30 * time.Second,
		CIPoll:        100 * time.Millisecond,
		MainCheckWait: 30 * time.Second,
		EvalTimeout:   2 * time.Minute,
	}
	if err := r.Run(ctx, writeIssueFile(t, sampleIssue)); err != nil {
		t.Fatalf("live run: %v", err)
	}

	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Status != "done" || st.Stage != StageLearn {
		t.Fatalf("state = %+v, want done/learn", st)
	}
	if st.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0", st.ConsecutiveFailures)
	}
	if st.PRNumber != 7 {
		t.Fatalf("PRNumber = %d, want 7", st.PRNumber)
	}
	events, err := store.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Event)
	}
	for _, want := range []string{"spec_gate_passed", "plan_ready", "pr_opened", "pr_merged", "task_merged", "item_completed"} {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("events %v missing %q", kinds, want)
		}
	}

	// The task branch was pushed to the bare remote; main was NOT moved
	// by the simulated API merge (the fake server only pretends).
	mu.Lock()
	var paths []string
	for _, req := range *reqLog {
		paths = append(paths, req.Method+" "+req.Path)
	}
	mu.Unlock()
	wantPaths := []string{
		"GET /repos/octocat/hello-world",
		"POST /repos/octocat/hello-world/pulls",
		"GET /repos/octocat/hello-world/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/check-runs",
		"POST /repos/octocat/hello-world/pulls/7/merge",
	}
	for i, want := range wantPaths {
		if i >= len(paths) || paths[i] != want {
			t.Errorf("request %d = %q, want %q (full log: %v)", i, firstOrEmpty(paths, i), want, paths)
		}
	}
	mergeBody := ""
	mu.Lock()
	for _, req := range *reqLog {
		if req.Method == "POST" && strings.HasSuffix(req.Path, "/merge") {
			mergeBody = req.Body
		}
	}
	mu.Unlock()
	if !strings.Contains(mergeBody, `"merge_method":"merge"`) {
		t.Errorf("merge request body = %s, want merge_method merge", mergeBody)
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", bareDir, "tinyfactory/fix-the-login-redirect-task-1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "tinyfactory/fix-the-login-redirect-task-1") {
		t.Fatalf("task branch was not pushed to the bare remote: %s", out)
	}
	// The worktree is gone after a successful task.
	if entries, _ := os.ReadDir(filepath.Join(store.Dir, "worktrees")); len(entries) != 0 {
		t.Errorf("worktrees dir not empty after success: %v", entries)
	}
}

func firstOrEmpty(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func TestRunLiveMergeBlockedUpfront(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initGoRepo(t, "octocat", "hello-world")
	binDir := t.TempDir()
	stubCLI(t, binDir, "claude", "echo unused", 0)
	stubCLI(t, binDir, "codex", "echo unused", 0)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	srv, _, _ := fakeGitHubE2E(t, false) // repo disallows merge commits
	cfg := mustLoadRealConfig(t)
	setCaps(cfg, 5, 20, 5)
	store := NewStore(t.TempDir())
	r := &Runner{
		Config:  cfg,
		Git:     ExecGitRunner{},
		Agent:   ExecAgentRunner{Timeout: time.Minute},
		GitHub:  NewGitHubClient(srv.URL, "test-token"),
		Store:   store,
		Repo:    "octocat/hello-world",
		RepoDir: repoDir,
	}
	err := r.Run(ctx, writeIssueFile(t, sampleIssue))
	if err == nil {
		t.Fatal("expected a blocked error, got nil")
	}
	if !strings.Contains(err.Error(), "disallows merge commits") {
		t.Fatalf("error = %v, want the merge-blocked error", err)
	}
	st, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.Status != "blocked" {
		t.Fatalf("state status = %q, want blocked", st.Status)
	}
	// No worktree was created: the block happened before any git writes.
	if entries, _ := os.ReadDir(filepath.Join(store.Dir, "worktrees")); len(entries) != 0 {
		t.Errorf("worktrees dir not empty after upfront block: %v", entries)
	}
}

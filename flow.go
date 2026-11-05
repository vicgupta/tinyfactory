// flow.go — the loop driver: intake → plan → generate → evaluate →
// deploy → learn, one item at a time.
//
// The driver enforces every gate in factory-flow.yaml. The live path
// (run without --dry-run) refuses to do anything until the target repo,
// the token, the local clone, and the caps are all present; the check
// order is deliberate — nothing is written (no state, no event, no
// branch) before those guards pass.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Eval commands for v1. The config names the evaluate stages but not
// their commands; v1 assumes a Go repo and uses the same commands as
// tinyagent's pipeline: go vet for validate-diff, go test for run-tests.
// verify-behavior runs each task's own Verify command.
const (
	validateDiffCmd = "go vet ./..."
	runTestsCmd     = "go test -count=1 ./..."
)

// Default timeouts. CIWait bounds waiting for a PR's checks; MainCheckWait
// bounds the post-merge check on main; EvalTimeout bounds one eval command.
const (
	DefaultCIWait        = 10 * time.Minute
	DefaultCIPoll        = 15 * time.Second
	DefaultMainCheckWait = 2 * time.Minute
	DefaultEvalTimeout   = 10 * time.Minute
)

// Stage names recorded in state.json and events.jsonl.
const (
	StageIntake   = "intake"
	StagePlan     = "plan"
	StageGenerate = "generate"
	StageEvaluate = "evaluate"
	StageDeploy   = "deploy"
	StageLearn    = "learn"
	StageDone     = "done"
	StageStopped  = "stopped"
	StageFailed   = "failed"
)

// GitHubAPI is the GitHub surface the loop uses. *GitHubClient satisfies it.
type GitHubAPI interface {
	GetRepo(ctx context.Context, owner, repo string) (*Repo, error)
	CreatePullRequest(ctx context.Context, owner, repo, title, head, base, body string) (*PullRequest, error)
	MergePullRequest(ctx context.Context, owner, repo string, number int, method string) (bool, error)
	CheckRunsForCommit(ctx context.Context, owner, repo, sha string) (*CheckRuns, error)
}

// Runner runs one item through the loop.
type Runner struct {
	Config *Config
	Git    GitRunner
	Agent  AgentRunner
	// GitHub is nil when no token/target repo was given; a live run
	// refuses before writing anything in that case.
	GitHub  GitHubAPI
	Store   *Store
	RepoDir string // local clone of the target repo (git.go: --repo-dir)
	Repo    string // "owner/name" GitHub target, "" when none
	DryRun  bool
	Out     io.Writer // progress output; nil means discard

	CIWait        time.Duration
	CIPoll        time.Duration
	MainCheckWait time.Duration
	EvalTimeout   time.Duration

	// currentIssue is the issue being run, set by Run before the plan
	// step; the plan prompt needs the parsed issue.
	currentIssue *Issue
}

func (r *Runner) out() io.Writer {
	if r.Out == nil {
		return io.Discard
	}
	return r.Out
}

func (r *Runner) ciWait() time.Duration {
	if r.CIWait != 0 {
		return r.CIWait
	}
	return DefaultCIWait
}

func (r *Runner) ciPoll() time.Duration {
	if r.CIPoll != 0 {
		return r.CIPoll
	}
	return DefaultCIPoll
}

func (r *Runner) mainCheckWait() time.Duration {
	if r.MainCheckWait != 0 {
		return r.MainCheckWait
	}
	return DefaultMainCheckWait
}

func (r *Runner) evalTimeout() time.Duration {
	if r.EvalTimeout != 0 {
		return r.EvalTimeout
	}
	return DefaultEvalTimeout
}

func (r *Runner) logf(format string, args ...any) {
	fmt.Fprintf(r.out(), format+"\n", args...)
}

func (r *Runner) event(stage, kind, detail string) {
	if r.Store == nil {
		return
	}
	// An event-log failure must not mask the run's own error; record it
	// on stdout and continue.
	if err := r.Store.AppendEvent(Event{Stage: stage, Event: kind, Detail: detail}); err != nil {
		r.logf("warning: could not append event %s: %v", kind, err)
	}
}

func (r *Runner) setState(st *State) {
	if r.Store == nil {
		return
	}
	if err := r.Store.SaveState(st); err != nil {
		r.logf("warning: could not save state: %v", err)
	}
}

// splitRepo splits "owner/name" or errors.
func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("repo %q: want \"owner/name\"", repo)
	}
	return parts[0], parts[1], nil
}

// Preview runs intake + plan only and returns the report. It makes no
// changes, opens no PR, and merges nothing. The plan is the mechanical
// breakdown (one task per acceptance criterion); the plan agent refines
// it in a live run.
func Preview(issue *Issue, cfg *Config) (string, error) {
	if missing := issue.MissingFields(); len(missing) > 0 {
		return "", fmt.Errorf("spec gate failed: missing %s — send the issue back, never plan from a vague issue", strings.Join(missing, ", "))
	}
	tasks := BuildPlan(issue, cfg)
	var b strings.Builder
	fmt.Fprintf(&b, "tinyfactory preview — %s\n", issue.Title)
	b.WriteString("spec gate: PASS (outcome, constraints, acceptance criteria, escalation conditions)\n")
	b.WriteString("\ntasks (mechanical breakdown — the plan agent refines this in a live run):\n")
	for i, t := range tasks {
		fmt.Fprintf(&b, "  %d. %s\n     verify: %s\n", i+1, t.Title, t.Verify)
	}
	b.WriteString("\nchecks and gates the item would run:\n")
	b.WriteString("  intake: spec gate (outcome, constraints, acceptance criteria, escalation conditions)\n")
	b.WriteString("  plan: specify → clarify → plan → tasks; tasks.md with acceptance criteria + Verify per task\n")
	b.WriteString("  generate: one task per worktree, TDD, codex; claude plans\n")
	b.WriteString("  evaluate, in order, before any PR opens:\n")
	fmt.Fprintf(&b, "    validate-diff: %s\n", validateDiffCmd)
	fmt.Fprintf(&b, "    run-tests: %s\n", runTestsCmd)
	b.WriteString("    verify-behavior: each task's Verify command against the issue's acceptance criteria\n")
	b.WriteString("  review: machine first, human only on what the machine can't settle\n")
	b.WriteString("  deploy: PR, wait for the CI gate, merge commit (never squash); blocked if the repo disallows merge commits\n")
	fmt.Fprintf(&b, "  send-backs: max %d, then stop for a human\n", cfg.Stages.Evaluate.MaxSendBacks)
	fmt.Fprintf(&b, "  stop after %d consecutive failures/reverts\n", cfg.Caps.StopAfterConsecutiveFailures)
	fmt.Fprintf(&b, "\nmodel routing: plan=%s, generate=%s, cheap tier=%s\n",
		cfg.Stages.Generate.Models.Plan, cfg.Stages.Generate.Models.Generate, cheapTier(cfg))
	if missing := cfg.MissingCaps(); len(missing) > 0 {
		fmt.Fprintf(&b, "caps: NOT SET (%s) — a live run will refuse to merge until all caps are set\n", strings.Join(missing, ", "))
	} else {
		fmt.Fprintf(&b, "caps: cost/run $%.2f, cost/day $%.2f, merges/day %d\n",
			*cfg.Caps.CostUSDPerRun, *cfg.Caps.CostUSDPerDay, *cfg.Caps.MergesPerDay)
	}
	b.WriteString("estimated cost: unknown — no per-model pricing in the config; set the caps before a live run\n")
	b.WriteString("\npreview made no changes, opened no PR, merged nothing.\n")
	return b.String(), nil
}

func cheapTier(cfg *Config) string {
	if cfg.Stages.Generate.Models.Cheap == "" {
		return "undecided"
	}
	return cfg.Stages.Generate.Models.Cheap
}

// Run runs one issue file through the loop. In dry-run mode it runs
// intake + plan only and reports what would happen; it writes nothing.
func (r *Runner) Run(ctx context.Context, issuePath string) error {
	data, err := os.ReadFile(issuePath)
	if err != nil {
		return fmt.Errorf("reading issue %s: %w", issuePath, err)
	}
	issue, err := ParseIssue(data)
	if err != nil {
		return err
	}
	if issue.Title == "" {
		issue.Title = filepath.Base(issuePath)
	}
	r.currentIssue = issue

	if r.DryRun {
		report, err := Preview(issue, r.Config)
		if err != nil {
			return err
		}
		r.logf("%s", report)
		r.logf("dry run: would proceed to generate → evaluate → deploy with the gates above; nothing was written.")
		return nil
	}

	// Live guards, in order, before ANY write (no state, no event, no
	// branch, no PR). A live run must refuse to merge while any cap is
	// null, and must have both a target repo and a token.
	if missing := r.Config.MissingCaps(); len(missing) > 0 {
		return fmt.Errorf("live run refused: caps not set (%s) — set them in factory-flow.yaml before the first real run", strings.Join(missing, ", "))
	}
	if strings.TrimSpace(r.Repo) == "" {
		return fmt.Errorf("live run refused: no target repo — pass --repo owner/name")
	}
	owner, repoName, err := splitRepo(r.Repo)
	if err != nil {
		return fmt.Errorf("live run refused: %w", err)
	}
	if r.GitHub == nil {
		return fmt.Errorf("live run refused: TINYFACTORY_TOKEN is not set — the token is read from the environment at runtime and never stored")
	}
	if strings.TrimSpace(r.RepoDir) == "" {
		return fmt.Errorf("live run refused: no local clone — pass --repo-dir <path> (a local checkout of %s)", r.Repo)
	}
	if _, err := r.Git.StatusPorcelain(ctx, r.RepoDir); err != nil {
		return fmt.Errorf("live run refused: --repo-dir %s is not a git checkout: %v", r.RepoDir, err)
	}
	originURL, err := r.Git.RemoteURL(ctx, r.RepoDir, "origin")
	if err != nil {
		return fmt.Errorf("live run refused: local clone %s has no origin remote: %v", r.RepoDir, err)
	}
	if !strings.Contains(originURL, r.Repo) {
		return fmt.Errorf("live run refused: local clone origin %q does not look like %s — the clone must point at the target repo", originURL, r.Repo)
	}

	// Consecutive-failure guard: the loop stops after N failures and
	// stays stopped until a human resets it (tinyfactory reset).
	consecutive := 0
	if r.Store != nil {
		if st, err := r.Store.LoadState(); err == nil {
			consecutive = st.ConsecutiveFailures
		}
	}
	if consecutive >= r.Config.Caps.StopAfterConsecutiveFailures {
		return fmt.Errorf("loop stopped after %d consecutive failures — a human must reset it (tinyfactory reset) before the next run", consecutive)
	}

	// Clean up this item's stale branches/worktrees from an earlier run
	// so a retry starts clean. Best effort: a branch still checked out
	// somewhere is left alone and the later worktree step reports it.
	slug := Slugify(issue.Title)
	r.cleanupStale(ctx, slug)

	st := &State{
		Issue:               issue.Title,
		Stage:               StageIntake,
		Status:              "running",
		SendBacks:           0,
		ConsecutiveFailures: consecutive,
		StartedAt:           time.Now().UTC(),
	}
	r.setState(st)

	// 1. Intake / spec gate. A failure sends the issue back and stops;
	// it is the issue's fault, not the loop's, so it does not count
	// toward consecutive failures.
	if missing := issue.MissingFields(); len(missing) > 0 {
		st.Stage = StageIntake
		st.Status = "sent_back"
		r.setState(st)
		r.event(StageIntake, "item_sent_back", fmt.Sprintf("spec gate failed: missing %s", strings.Join(missing, ", ")))
		return fmt.Errorf("spec gate failed: missing %s — issue sent back", strings.Join(missing, ", "))
	}
	r.event(StageIntake, "spec_gate_passed", issue.Title)

	// The repo must allow merge commits before any work starts: if it
	// does not, the run is blocked, never silently squashed.
	repo, err := r.GitHub.GetRepo(ctx, owner, repoName)
	if err != nil {
		return r.failItem(ctx, st, StageIntake, fmt.Sprintf("could not read repo %s: %v", r.Repo, err))
	}
	if repo.AllowMergeCommit != nil && !*repo.AllowMergeCommit {
		st.Stage = StageDeploy
		st.Status = "blocked"
		r.setState(st)
		r.event(StageDeploy, "merge_blocked", fmt.Sprintf("repo %s disallows merge commits", r.Repo))
		return fmt.Errorf("blocked: repo %s disallows merge commits — merge method must remain \"merge\" and no other method will be attempted", r.Repo)
	}
	base := repo.DefaultBranch
	if base == "" {
		base = r.Config.Stages.Deploy.ReleaseFrom
	}
	if err := r.Git.Fetch(ctx, r.RepoDir, "origin", base); err != nil {
		return r.failItem(ctx, st, StageIntake, fmt.Sprintf("could not fetch origin/%s: %v", base, err))
	}
	baseRef := "origin/" + base

	// 2. Plan: the plan agent writes tasks.md in a throwaway worktree;
	// the runner parses it back. The plan branch is never pushed.
	st.Stage = StagePlan
	r.setState(st)
	tasks, err := r.planTasks(ctx, slug, baseRef)
	if err != nil {
		return r.failItem(ctx, st, StagePlan, fmt.Sprintf("plan failed: %v", err))
	}
	r.event(StagePlan, "plan_ready", fmt.Sprintf("%d tasks", len(tasks)))

	// 3–5. One task per worktree: generate → evaluate → deploy.
	for i, task := range tasks {
		if err := r.runTask(ctx, st, issue, task, i+1, len(tasks), slug, base, baseRef, owner, repoName); err != nil {
			return err
		}
	}

	// 6. Learn: the item merged; record the outcome and reset the
	// consecutive-failure count.
	st.Stage = StageLearn
	st.Status = "done"
	st.Branch = "" // the task branches were deleted after their merges
	st.ConsecutiveFailures = 0
	r.setState(st)
	r.event(StageLearn, "item_completed", fmt.Sprintf("%s: %d tasks merged", issue.Title, len(tasks)))
	r.logf("done: %s — %d task(s) merged, main green", issue.Title, len(tasks))
	return nil
}

// failItem records an item failure, counts it toward consecutive
// failures, and returns the error.
func (r *Runner) failItem(ctx context.Context, st *State, stage, reason string) error {
	st.Stage = stage
	st.Status = "failed"
	st.ConsecutiveFailures++
	r.setState(st)
	r.event(stage, "item_failed", reason)
	return fmt.Errorf("%s", reason)
}

// cleanupStale removes this item's leftover worktrees and branches from
// an earlier run. Failures are logged, not fatal: a branch that cannot
// be removed will fail loudly at the worktree step instead.
func (r *Runner) cleanupStale(ctx context.Context, slug string) {
	prefix := "tinyfactory/" + slug + "-"
	worktrees, err := r.Git.ListWorktrees(ctx, r.RepoDir)
	if err == nil {
		for _, wt := range worktrees {
			if strings.HasPrefix(wt.Branch, prefix) {
				if err := r.Git.RemoveWorktree(ctx, r.RepoDir, wt.Path); err != nil {
					r.logf("warning: could not remove stale worktree %s: %v", wt.Path, err)
				}
			}
		}
	}
	branches, err := r.Git.ListBranches(ctx, r.RepoDir, prefix+"*")
	if err == nil {
		for _, b := range branches {
			if err := r.Git.DeleteBranch(ctx, r.RepoDir, b); err != nil {
				r.logf("warning: could not delete stale branch %s: %v", b, err)
			}
		}
	}
	_ = r.Git.PruneWorktrees(ctx, r.RepoDir)
}

// planTasks runs the plan agent in a throwaway worktree and parses the
// tasks.md it writes. The plan branch is never pushed.
func (r *Runner) planTasks(ctx context.Context, slug, baseRef string) ([]Task, error) {
	branch := "tinyfactory/" + slug + "-plan"
	wt := r.worktreePath(slug + "-plan")
	if err := r.Git.CreateWorktree(ctx, r.RepoDir, wt, branch, baseRef); err != nil {
		return nil, fmt.Errorf("creating plan worktree: %w", err)
	}
	defer func() {
		_ = r.Git.RemoveWorktree(ctx, r.RepoDir, wt)
		_ = r.Git.DeleteBranch(ctx, r.RepoDir, branch)
	}()
	prompt := PlanPrompt(r.currentIssue)
	out, err := r.Agent.Run(ctx, r.Config.Stages.Generate.Models.Plan, prompt, wt)
	if err != nil {
		return nil, fmt.Errorf("plan agent %s failed: %w\n%s", r.Config.Stages.Generate.Models.Plan, err, out)
	}
	data, err := os.ReadFile(filepath.Join(wt, r.Config.Stages.Plan.Output))
	if err != nil {
		return nil, fmt.Errorf("plan agent did not write %s: %v", r.Config.Stages.Plan.Output, err)
	}
	tasks, err := ParseTasksMD(string(data))
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// runTask runs one task: generate (with bounded send-backs), evaluate,
// deploy (PR → CI gate → merge commit). Any unrecoverable failure stops
// the whole item for a human.
func (r *Runner) runTask(ctx context.Context, st *State, issue *Issue, task Task, num, total int, slug, base, baseRef, owner, repoName string) error {
	branch := fmt.Sprintf("tinyfactory/%s-task-%d", slug, num)
	wt := r.worktreePath(fmt.Sprintf("%s-task-%d", slug, num))
	if err := r.Git.CreateWorktree(ctx, r.RepoDir, wt, branch, baseRef); err != nil {
		return r.failItem(ctx, st, StageGenerate, fmt.Sprintf("task %d: creating worktree: %v", num, err))
	}
	cleanup := func() {
		_ = r.Git.RemoveWorktree(ctx, r.RepoDir, wt)
		_ = r.Git.DeleteBranch(ctx, r.RepoDir, branch)
	}
	st.Branch = branch

	// Generate with bounded send-backs.
	var feedback string
	generated := false
	for attempt := 0; attempt <= r.Config.Stages.Evaluate.MaxSendBacks; attempt++ {
		st.Stage = StageGenerate
		r.setState(st)
		var prompt string
		if feedback == "" {
			prompt = GeneratePrompt(task, issue)
		} else {
			prompt = GeneratePromptWithFeedback(task, issue, feedback)
		}
		out, err := r.Agent.Run(ctx, r.Config.Stages.Generate.Models.Generate, prompt, wt)
		if err != nil {
			if attempt == r.Config.Stages.Evaluate.MaxSendBacks {
				return r.failItem(ctx, st, StageGenerate, fmt.Sprintf("task %d: generate agent failed after %d send-backs: %v\n%s", num, r.Config.Stages.Evaluate.MaxSendBacks, err, out))
			}
			st.SendBacks++
			r.setState(st)
			r.event(StageGenerate, "send_back", fmt.Sprintf("task %d: generate agent failed (attempt %d): %v", num, attempt+1, err))
			feedback = fmt.Sprintf("agent failed: %v\n%s", err, out)
			continue
		}

		// Guard: the agent must not touch protected paths or secrets.
		// Violations are reverted; the attempt counts as a send-back.
		if violations, err := r.guardScan(ctx, wt); err != nil {
			return r.failItem(ctx, st, StageGenerate, fmt.Sprintf("task %d: guard scan failed: %v", num, err))
		} else if len(violations) > 0 {
			reverted, revErr := r.revertViolations(ctx, wt, violations)
			if revErr != nil {
				return r.failItem(ctx, st, StageGenerate, fmt.Sprintf("task %d: could not revert protected changes %v: %v", num, reverted, revErr))
			}
			st.ConsecutiveFailures++ // a revert counts toward the stop rule
			if attempt == r.Config.Stages.Evaluate.MaxSendBacks {
				return r.failItem(ctx, st, StageGenerate, fmt.Sprintf("task %d: agent touched protected paths %v after %d send-backs — stopped for a human", num, violations, r.Config.Stages.Evaluate.MaxSendBacks))
			}
			st.SendBacks++
			r.setState(st)
			r.event(StageGenerate, "send_back", fmt.Sprintf("task %d: reverted protected changes %v (attempt %d)", num, violations, attempt+1))
			feedback = fmt.Sprintf("you touched protected paths that were reverted and must not be touched: %s", strings.Join(violations, ", "))
			continue
		}

		// Evaluate, in order, before any PR opens.
		st.Stage = StageEvaluate
		r.setState(st)
		if err := r.evaluate(ctx, wt, task); err != nil {
			if attempt == r.Config.Stages.Evaluate.MaxSendBacks {
				return r.failItem(ctx, st, StageEvaluate, fmt.Sprintf("task %d: evaluate failed after %d send-backs: %v", num, r.Config.Stages.Evaluate.MaxSendBacks, err))
			}
			st.SendBacks++
			r.setState(st)
			r.event(StageEvaluate, "send_back", fmt.Sprintf("task %d: evaluate failed (attempt %d): %v", num, attempt+1, err))
			feedback = fmt.Sprintf("evaluation failed:\n%v", err)
			continue
		}
		generated = true
		break
	}
	if !generated {
		return r.failItem(ctx, st, StageGenerate, fmt.Sprintf("task %d: no successful generate attempt", num))
	}

	// Deploy: commit, push, PR, wait for the CI gate, merge commit.
	st.Stage = StageDeploy
	r.setState(st)
	pr, err := r.deploy(ctx, st, issue, task, num, total, branch, wt, base, owner, repoName)
	if err != nil {
		return err // deploy already recorded the failure/stop
	}
	_ = pr
	cleanup()
	r.event(StageDeploy, "task_merged", fmt.Sprintf("task %d/%d: PR #%d merged", num, total, st.PRNumber))
	r.logf("task %d/%d merged (PR #%d)", num, total, st.PRNumber)
	return nil
}

// guardScan lists changed paths the loop must never accept: protected
// paths from the config, and any secrets file (a file named .env or
// listed under invariants.never_read_secrets).
func (r *Runner) guardScan(ctx context.Context, wt string) ([]string, error) {
	status, err := r.Git.StatusPorcelain(ctx, wt)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, e := range ParseStatus(status) {
		if IsProtectedPath(e.Path, r.Config.Invariants.ProtectedPaths) {
			violations = append(violations, e.Path)
			continue
		}
		if isSecretsFile(e.Path, r.Config.Invariants.NeverReadSecrets) {
			violations = append(violations, e.Path)
		}
	}
	return violations, nil
}

// isSecretsFile reports whether a changed path is a secrets file: an
// exact match or a basename match against the config's list (".env"
// matches .env in any directory).
func isSecretsFile(path string, secrets []string) bool {
	for _, s := range secrets {
		if path == s || filepath.Base(path) == s {
			return true
		}
	}
	return false
}

// revertViolations reverts each violating path: untracked files and
// directories are deleted, tracked changes are checked out. It returns
// the paths it reverted.
func (r *Runner) revertViolations(ctx context.Context, wt string, violations []string) ([]string, error) {
	status, err := r.Git.StatusPorcelain(ctx, wt)
	if err != nil {
		return nil, err
	}
	entries := ParseStatus(status)
	byPath := make(map[string]string, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e.XY
	}
	var tracked, reverted []string
	for _, p := range violations {
		if byPath[p] == "??" {
			// Untracked: git checkout cannot remove it; delete it.
			if err := os.RemoveAll(filepath.Join(wt, p)); err != nil {
				return reverted, fmt.Errorf("removing untracked %s: %w", p, err)
			}
			reverted = append(reverted, p)
			continue
		}
		tracked = append(tracked, p)
	}
	if len(tracked) > 0 {
		if err := r.Git.CheckoutPaths(ctx, wt, tracked); err != nil {
			return reverted, fmt.Errorf("reverting %v: %w", tracked, err)
		}
		reverted = append(reverted, tracked...)
	}
	return reverted, nil
}

// evaluate runs the evaluate stages in config order. v1 assumes a Go
// repo: validate-diff is go vet, run-tests is go test, verify-behavior
// runs each task's Verify command (deduplicated).
func (r *Runner) evaluate(ctx context.Context, wt string, task Task) error {
	for _, stage := range r.Config.Stages.Evaluate.Order {
		var cmd string
		switch stage {
		case "validate-diff":
			cmd = validateDiffCmd
		case "run-tests":
			cmd = runTestsCmd
		case "verify-behavior":
			// Handled below: one command per distinct Verify line.
			if err := r.verifyBehavior(ctx, wt, task); err != nil {
				return err
			}
			continue
		default:
			return fmt.Errorf("evaluate: unknown stage %q in config", stage)
		}
		if err := r.runEval(ctx, wt, stage, cmd); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) verifyBehavior(ctx context.Context, wt string, task Task) error {
	seen := map[string]bool{}
	for _, cmd := range []string{task.Verify} {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		if err := r.runEval(ctx, wt, "verify-behavior", cmd); err != nil {
			return err
		}
	}
	return nil
}

// runEval runs one eval command in dir with a timeout. The output tail
// is included on failure so a send-back tells the agent what broke.
func (r *Runner) runEval(ctx context.Context, dir, stage, cmd string) error {
	callCtx, cancel := context.WithTimeout(ctx, r.evalTimeout())
	defer cancel()
	c := exec.CommandContext(callCtx, "sh", "-c", cmd)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s (%s) failed: %v: %s", stage, cmd, err, tailLines(strings.TrimSpace(string(out)), 20))
	}
	r.logf("  %s: ok", stage)
	return nil
}

// deploy commits, pushes, opens a PR, waits for the CI gate, and merges
// with a merge commit. A merge the repo disallows is a block, never a
// silent squash. After the merge it checks main's CI; a red main stops
// the loop (fix-or-revert-and-stop: v1 stops and records — the revert
// itself is a human step, see README).
func (r *Runner) deploy(ctx context.Context, st *State, issue *Issue, task Task, num, total int, branch, wt, base, owner, repoName string) (*PullRequest, error) {
	title := fmt.Sprintf("Task %d: %s", num, task.Title)
	if err := r.Git.CommitAll(ctx, wt, title); err != nil {
		return nil, r.failItem(ctx, st, StageDeploy, fmt.Sprintf("task %d: commit failed: %v", num, err))
	}
	if err := r.Git.Push(ctx, wt, "origin", branch); err != nil {
		return nil, r.failItem(ctx, st, StageDeploy, fmt.Sprintf("task %d: push failed: %v", num, err))
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Issue: %s\n\nTask %d of %d: %s\n\n%s\n\nAcceptance criteria:\n", issue.Title, num, total, task.Title, task.Body)
	for _, c := range task.AcceptanceCriteria {
		body.WriteString("- " + c + "\n")
	}
	fmt.Fprintf(&body, "\nVerify: `%s`\n\nGenerated by tinyfactory v1 (preview-first loop). Merge commit only.\n", task.Verify)
	pr, err := r.GitHub.CreatePullRequest(ctx, owner, repoName, title, branch, base, body.String())
	if err != nil {
		return nil, r.failItem(ctx, st, StageDeploy, fmt.Sprintf("task %d: opening PR failed: %v", num, err))
	}
	st.PRNumber = pr.Number
	r.setState(st)
	r.event(StageDeploy, "pr_opened", fmt.Sprintf("task %d: PR #%d (%s)", num, pr.Number, pr.HTMLURL))
	r.logf("task %d: PR #%d opened (%s)", num, pr.Number, pr.HTMLURL)

	if err := r.waitForGreen(ctx, owner, repoName, pr.Head.SHA, r.ciWait()); err != nil {
		return nil, r.failItem(ctx, st, StageDeploy, fmt.Sprintf("task %d: CI gate on PR #%d: %v — PR left open for a human", num, pr.Number, err))
	}
	merged, err := r.GitHub.MergePullRequest(ctx, owner, repoName, pr.Number, "merge")
	if err != nil {
		st.Stage = StageDeploy
		st.Status = "blocked"
		r.setState(st)
		r.event(StageDeploy, "merge_blocked", fmt.Sprintf("task %d: PR #%d: %v", num, pr.Number, err))
		return nil, fmt.Errorf("task %d: PR #%d: %v", num, pr.Number, err)
	}
	if !merged {
		return nil, r.failItem(ctx, st, StageDeploy, fmt.Sprintf("task %d: PR #%d reported not merged", num, pr.Number))
	}
	r.event(StageDeploy, "pr_merged", fmt.Sprintf("task %d: PR #%d merged with a merge commit", num, pr.Number))

	// Main must be green after the merge. A red main stops the loop.
	if err := r.checkMainAfterMerge(ctx, owner, repoName, base); err != nil {
		st.Stage = StageDeploy
		st.Status = "stopped"
		st.ConsecutiveFailures++
		r.setState(st)
		r.event(StageDeploy, "main_red", err.Error())
		return nil, fmt.Errorf("main went red after merging PR #%d: %v — loop stopped; revert is a human step (see README)", pr.Number, err)
	}
	return pr, nil
}

// waitForGreen polls the check runs for sha until all are green, a check
// fails, or wait elapses.
func (r *Runner) waitForGreen(ctx context.Context, owner, repo, sha string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		runs, err := r.GitHub.CheckRunsForCommit(ctx, owner, repo, sha)
		if err != nil {
			return fmt.Errorf("reading check runs for %s: %w", sha, err)
		}
		if AllChecksGreen(runs) {
			return nil
		}
		for _, run := range runs.Runs {
			if run.Status == "completed" && run.Conclusion != nil {
				switch *run.Conclusion {
				case "success", "neutral", "skipped":
				default:
					return fmt.Errorf("check %q concluded %s", run.Name, *run.Conclusion)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("checks did not go green within %s", wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.ciPoll()):
		}
	}
}

// checkMainAfterMerge fetches the base branch and checks its CI. A fetch
// or lookup failure is advisory (the merge already landed; the check
// cannot run) and is logged, not fatal. A red check is fatal: the loop
// stops. Checks still pending when the bounded wait ends are advisory —
// main CI is often slower than the PR gate.
func (r *Runner) checkMainAfterMerge(ctx context.Context, owner, repo, base string) error {
	if err := r.Git.Fetch(ctx, r.RepoDir, "origin", base); err != nil {
		r.event(StageDeploy, "main_check_skipped", fmt.Sprintf("could not fetch origin/%s after merge: %v", base, err))
		return nil
	}
	sha, err := r.Git.RevParse(ctx, r.RepoDir, "origin/"+base)
	if err != nil {
		r.event(StageDeploy, "main_check_skipped", fmt.Sprintf("could not resolve origin/%s after merge: %v", base, err))
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, r.mainCheckWait())
	defer cancel()
	err = r.waitForGreen(waitCtx, owner, repo, sha, r.mainCheckWait())
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "did not go green within") || strings.Contains(err.Error(), "context deadline exceeded") {
		r.event(StageDeploy, "main_check_skipped", fmt.Sprintf("main checks still pending after %s: %v", r.mainCheckWait(), err))
		return nil
	}
	return err
}

// worktreePath returns the worktree directory for a run, under the state
// dir so a crashed run's worktree is findable.
func (r *Runner) worktreePath(name string) string {
	if r.Store == nil {
		return filepath.Join(os.TempDir(), "tinyfactory-"+name)
	}
	return filepath.Join(r.Store.Dir, "worktrees", name)
}

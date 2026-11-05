# Skill: factory-flow
> Intent: Run one factory item end to end through intake → plan → generate → evaluate → deploy → learn, with gates that never compress away. This is the operating flow for tinyfactory.

Status: preview draft. Config: `../factory-flow.yaml`. Defaults below follow Vic's standing rules (merge commit everywhere, guardrails on, Codex primary / Claude for plan, fixes via PR). Five decisions from `review-v1-spec.md` are still open (merge method, safeguards as defaults, Q1 split, PR-only fixes, run location + dry-run) — if any flips, edit this file, don't work around it.

**Inputs**
- issue (outcome, constraints, measurable acceptance criteria, escalation conditions)
- repo_path
- state (state.json, events.jsonl)
- caps (per-run and per-day cost/time limits)

**Preview mode**
- Run with `--preview` (or when asked to preview): do the intake and plan steps, then output the task breakdown, the checks and gates the item would run, and the estimated cost. Make no changes, open no PR, merge nothing. Preview is the default for the first cycle of any new repo.

**Steps**

1. Intake / spec gate. The issue must state an outcome, constraints, measurable acceptance criteria, and escalation conditions. If any is missing, stop and send the issue back — never plan from a vague issue. Record the gate result in events.jsonl.
2. Plan. Run specify → clarify → plan → tasks and write `tasks.md`. Every task has acceptance criteria and a `Verify:` line with a single command (same shape as the `plan-tasks` skill). Planning is the only step allowed to be expensive.
3. Generate. One task per worktree/branch, TDD: write the failing behavior case first, then the code. Per-stage models: Claude for the plan step, Codex primary for generate, cheap model where it fits. Never generate from an issue that failed the spec gate.
4. Evaluate, in this order, before any PR opens:
   - validate-diff (vet/lint),
   - run-tests,
   - verify-behavior against the issue's own acceptance criteria, not just the test suite (same shape as the `verify-behavior` skill).
   Then layered review: machine review first, human review only on what the machine can't settle. A failure sends the item back to generate, bounded: 2 send-backs, then stop and flag for a human.
5. Deploy. Open a PR, wait for the CI gate, merge with a merge commit — never squash by default; if the repo disallows merge commits, report blocked, never silently squash. If main goes red, fix-or-revert immediately and stop the loop until main is green again. Release from main, never from a branch.
6. Learn. The observer records the outcome, the cost, and what failed into events.jsonl, and feeds it back into the skills and specs. That is what makes the second hundred items cheaper than the first ten.

**Invariants — never compress these away**
- Evals are locked and sovereign: never edit checks, CI workflow files, or eval suites. The loop must not touch `.github/` or its own pipeline file.
- Cost and time caps per run and per day. Stop the loop after 2 consecutive reverts or failures instead of grinding on.
- Fixes always go through a PR, never direct to main (the revert step only, if at all). A direct push bypasses the CI gate the whole loop depends on.
- Agents never read secrets: `.env` and any file holding a token are off limits. The loop never edits its own CI to weaken a check it relies on.
- One item at a time through the loop. No parallel items until the loop is proven on single items.

**Success check:** the item merged via PR with green CI, behavior verified against its acceptance criteria, outcome and cost logged in events.jsonl, and main green after the merge — or the item stopped at a named gate with the reason recorded.

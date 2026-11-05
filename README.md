# tinyfactory

A single-binary autonomous software factory loop, driven by the companion
config `factory-flow.yaml` and the operating-flow skill
`skills/factory-flow.md`. One item at a time through intake → plan →
generate → evaluate → deploy → learn, with gates that never compress away.

Status: **v1, preview-first.** The loop is built and verified locally with
dry-run/preview and a full simulated live run (stub agents + fake GitHub +
local git repos). No live run has happened yet: there is no target repo, no
token, and the caps in the config are still null.

## Build

Go 1.24, standard library only — no external dependencies, no downloads.

```sh
export PATH=/home/hatch/go-sdk/go/bin:$PATH
export GOTMPDIR=/home/hatch/tmp-go GOCACHE=/home/hatch/.cache/go-build
CGO_ENABLED=0 go build -o tinyfactory .   # static single binary
gofmt -l .          # must be clean
go vet ./...        # must be clean
go test ./...       # must be green
```

## Usage

```sh
# Preview: intake + plan only. Prints the task breakdown, the checks and
# gates the item would run, the model routing, and the estimated cost.
# Makes no changes, opens no PR, merges nothing.
tinyfactory preview --issue issue.md [--config factory-flow.yaml]

# Dry run: preview plus the live guards, with no writes at all.
tinyfactory run --issue issue.md --dry-run

# Live run (see "Before any live run" below).
tinyfactory run --issue issue.md --repo owner/name --repo-dir /path/to/clone \
    [--state-dir .tinyfactory] [--config factory-flow.yaml]

# Reset the consecutive-failure stop so the loop can run again.
# Keeps events.jsonl (the audit trail).
tinyfactory reset [--state-dir .tinyfactory]

tinyfactory version
```

An issue is a markdown file with four required sections (any heading
level 1–3, or `Field: ...` lines): **Outcome**, **Constraints**,
**Acceptance criteria**, **Escalation**. Missing any of them fails the
spec gate and the issue is sent back — never planned from a vague issue.

## The flow and its gates

- **Intake / spec gate.** All four fields required. A failure is recorded
  in `events.jsonl` as `item_sent_back` and stops the run. It does not
  count toward consecutive failures (it is the issue's fault, not the
  loop's).
- **Plan.** The plan agent (Claude) writes `tasks.md` in a throwaway
  worktree; the runner parses it back. Every task needs acceptance
  criteria and a `Verify:` command. The plan branch is never pushed.
- **Generate.** One task per worktree/branch, test-first, Codex. After
  every agent step the loop scans the diff: touching a protected path
  (`.github/`, `evals/`) or a secrets file (`.env`) is reverted on the
  spot and counts as a send-back. A revert counts toward the stop rule.
- **Evaluate, in order, before any PR opens:** `validate-diff`
  (`go vet ./...`), `run-tests` (`go test -count=1 ./...`),
  `verify-behavior` (each task's `Verify` command, against the issue's
  own acceptance criteria). A failure sends the item back to generate;
  max 2 send-backs, then stop for a human.
- **Deploy.** Commit, push, open a PR, wait for the CI gate (default
  10 min), merge with a **merge commit — never squash**. Two checks:
  the repo is read up front (`allow_merge_commit`); if it disallows
  merge commits the run is **blocked**, never silently squashed. A 405
  at merge time is the same block. After the merge the loop fetches
  main and checks its CI; a red main stops the loop
  (`fix-or-revert-and-stop` — see Limitations).
- **Learn.** The outcome is recorded in `events.jsonl`; a completed
  item resets the consecutive-failure count to 0.

State lives in `--state-dir` (default `.tinyfactory`):
`state.json` (current item + stage + send-backs + consecutive failures)
and `events.jsonl` (append-only, timestamped audit trail). Worktrees
live under `<state-dir>/worktrees/` so a crashed run's worktree is
findable; a successful task's worktree and branch are removed.

## Config keys (`factory-flow.yaml`)

The config is the source of truth; the binary validates it at load and
fails loudly on anything that contradicts the flow:

- `loop`: `one_item_at_a_time: true`, `preview_first_cycle: true`
  (preview is the default for the first cycle of any new repo).
- `stages.intake`: spec gate, requires the four fields, `on_missing:
  send_back`.
- `stages.plan`: steps `specify → clarify → plan → tasks`, output
  `tasks.md`, each task requires `acceptance_criteria` +
  `verify_command`.
- `stages.generate`: `one_task_per: worktree`, `tdd: true`, models
  `plan: claude`, `generate: codex` (cheap tier undecided).
- `stages.evaluate`: order `validate-diff, run-tests, verify-behavior`,
  review `machine, human`, `max_send_backs: 2`.
- `stages.deploy`: `via: pr`, `merge: merge-commit`, `on_red_main:
  fix-or-revert-and-stop`, `release_from: main`.
- `stages.learn`: observer on, log `events.jsonl`.
- `caps`: `cost_usd_per_run`, `cost_usd_per_day`, `merges_per_day`
  (all null in the shipped config), `stop_after_consecutive_failures:
  2`.
- `invariants`: `protected_paths` (`.github/`, `evals/`, pipeline
  file), `never_direct_to_main`, `never_read_secrets` (`.env`),
  `never_edit_own_ci`.

The YAML parser (`yaml.go`) is a minimal subset parser for exactly the
constructs in this file (comments, nested maps by indentation, block
sequences, inline flow lists, plain/quoted/bool/int/null scalars). It
errors clearly on anything it does not support (anchors, aliases, flow
maps, block scalars, tabs, duplicate keys).

## Before any live run

A live run refuses before writing anything (no state, no event, no
branch, no PR) unless all of these hold, checked in this order:

1. **Cap numbers set** in `factory-flow.yaml` — `cost_usd_per_run`,
   `cost_usd_per_day`, `merges_per_day` are all null in the shipped
   config. A live run must refuse to merge while any cap is null.
2. **Target repo** — `--repo owner/name`.
3. **Token** — `TINYFACTORY_TOKEN` in the environment. Read at runtime,
   never logged, never stored, never passed to an agent. Without it the
   binary runs preview/dry-run only.
4. **Local clone** — `--repo-dir <path>`: a local checkout of the
   target repo (the binary does not clone; the operator clones once).
   Its `origin` remote must point at the target repo (checked by URL).
5. **Consecutive-failure stop clear** — after 2 consecutive failures
   the loop stays stopped until a human runs `tinyfactory reset`.
6. **Merge commits allowed** — checked up front via the repo's
   `allow_merge_commit` setting.
7. **Agent CLIs installed** — `codex` (generate) and `claude` (plan),
   invoked as `codex exec <prompt>` and `claude -p <prompt>`. Verify
   these non-interactive forms against the installed CLI versions
   before a live run; the prompts forbid reading secrets and touching
   protected paths.

## Limitations of v1 (named, not silent)

- **Eval assumes a Go repo.** `validate-diff` is `go vet ./...` and
  `run-tests` is `go test -count=1 ./...`; the config names the stages
  but not their commands. A non-Go target repo fails evaluation.
- **Estimated cost is "unknown" in preview.** No per-model pricing
  lives in the config, so preview reports the routing and the caps and
  says the dollar estimate is unknown. Set the caps before a live run;
  per-model pricing belongs in a future config addition.
- **Red main: stop, no automatic revert yet.** On a red main after a
  merge, v1 records `main_red`, stops the loop, and leaves the revert
  to a human (via a PR — never direct to main). An automatic
  revert-PR is the next step, not this version.
- **The loop cannot detect an agent *reading* `.env`.** The prompts
  forbid it and the guard catches any *change* to a secrets file, but a
  silent read leaves no trace in the worktree.
- **Human review is the PR itself.** V1 merges when the CI gate is
  green; it does not check for an approving review. A review-required
  check belongs in a future version.
- **No `--resume`.** A re-run of the same issue cleans up that item's
  stale `tinyfactory/<slug>-*` worktrees/branches first; anything else
  left behind must be cleaned up by hand.
- **"pipeline file" in `protected_paths`** refers to the factory's own
  pipeline file (`factory-flow.yaml` / `skills/factory-flow.md`). The
  loop only ever writes inside the target repo's worktree, so it cannot
  touch that file; the in-repo guard covers `.github/` and `evals/`.

## Layout

| File | What it is |
| --- | --- |
| `main.go` | CLI: `preview`, `run`, `reset`, `version` |
| `flow.go` | Loop driver + preview report |
| `config.go` | Typed config + validation |
| `yaml.go` | Minimal YAML-subset parser |
| `issue.go` | Issue parsing + spec gate |
| `plan.go` | Task breakdown + `tasks.md` render/parse |
| `state.go` | `state.json` + `events.jsonl` store |
| `git.go` | Git operations (worktrees, branches, guards) |
| `agent.go` | Agent CLI runner + plan/generate prompts |
| `github.go` | GitHub REST client (merge commit only) |
| `factory-flow.yaml` | Companion config, source of truth |
| `skills/factory-flow.md` | Operating-flow skill (human-readable) |
| `review-v1-spec.md` | Critical review the build follows |

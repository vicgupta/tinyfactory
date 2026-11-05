# tinyfactory v1 spec — review (2026-11-03)

Source: Vic's email to himself, "tinyfactory spec (v1 draft)", 2026-11-03T00:05:51Z,
with the Cursor canvas link. Status in the spec: proposal. No build authorized.
Corroboration that he is setting it up: "Demo Tinyfactory Token" created
2026-11-03T00:39:38Z and "AMF Demo Token" 2026-11-03T01:25:25Z (GitHub emails),
34 and 79 minutes after the spec email. Those names match the spec/demo
context, so they read as his — not flagged as unknown tokens.

## Verdict

Coherent spec, and the loop design (state file, events log, redaction, fix-or-
revert on red main) is the strongest part. Two things need a deliberate
decision before any build, because they contradict his own standing rules:
squash-merge (R3) vs his merge-commit-everywhere rule, and dropping AMF's
guardrails (R6) with no replacement. One open question (Q1, coding agent)
has a clear answer from his own recent data.

## 1. R3 squash-merge contradicts the tinyagent merge rule

- Spec R3: "squash-merges once PR checks pass."
- His confirmed rule for tinyagent (MEMORY.md, 2026-10-24, revised ~19:17 EDT):
  merge commit everywhere, never squash by default; if a repo disallows merge
  commits, report blocked rather than silently squashing.
- Either the rule doesn't apply to tinyfactory (say so deliberately), or R3
  should change to merge commit. Don't let it be an accident — squash also
  destroys the per-commit trail the loop's own event log is trying to keep.

## 2. R6 drops AMF's guardrails with nothing in their place

- Spec §10 admits it: the loop can run forever (no cost ceiling), can edit its
  own CI and weaken the check it relies on, and agents run with prompts off
  and can read local files including .env (which holds the PAT).
- AMF, by contrast, refuses unattended changes to .github/ and its pipeline
  file (spec §10 says so itself).
- Recommendation: adopt two of the "optional" Q2 safeguards as v1 defaults,
  not options — (a) protect CI workflow files from loop edits, (b) a daily cap
  on merges/spend. Add a dry-run first cycle before the first real merge.
  Also: stop the loop after K consecutive reverts (suggest K=2), not just mark
  the issue failed and carry on.

## 3. Q1 coding agent — answerable from his own data

- His factory runs right now are overwhelmingly Codex: Cobra digest Nov 3
  (coforge-product) — 14 runs, 12 codex / gpt-6.1-sol, 2 claude-code.
- Counterpoint: Nov 3 digest — Claude Code won a three-agent Android
  build-off against Codex and Antigravity; the spec itself notes Claude is
  strong at planning/docs, Codex at contained changes.
- Recommendation: Codex primary, Claude secondary (the fallback already in
  §5), and use Claude for the plan step specifically — per-stage models, the
  same shape as his client-harness step-3 work. Defer dynamic selection.

## 4. Q3 — fixes should always go through a PR

- Spec §8 step 3 allows the main-CI fix to go "through a PR or directly to
  main". Recommend PR always: a direct-to-main fix bypasses the CI gate the
  whole loop depends on, and breaks the audit trail state.json/events.jsonl
  exist to provide. R6's "fixes may push straight to main" should be narrowed
  to the revert step only, if at all.

## 5. Missing step: verify behaviour before opening the PR

- The loop's only gates before merge are tests + PR CI. Tinyagent's fix-bug
  pipeline (drafted Nov 2) adds a verify-behavior step against a written
  behaviour case before anything is declared done.
- Suggest tinyfactory do the same: the plan step already writes acceptance
  criteria per issue — run them as an explicit check (pass/fail per criterion)
  before the PR opens, and record the result in events.jsonl.

## 6. Q5 language — Go, stdlib only

- Yes. Matches AMF, tinyagent, minifactory-go and ksound; single binary, no
  runtime to install. This is the least controversial open question.

## 7. Q4 where to run — VM/container, not the laptop

- The loop runs until stopped; a laptop sleeps and closes. Long term it wants
  a VM or container. Note: nova.kantsy.com was reimaged Oct 11 (fresh Ubuntu,
  no docker/node/go) and nothing may be installed without his explicit word,
  so this needs a separate go-ahead, not a quiet assumption.

## 8. Lineage — this is the parked AMF merge feature, reborn standalone

- Oct 25 he asked AMF for PR-merge capability (commit merge); that work was
  then stopped/parked. Tinyfactory is that idea plus full autonomy, rebuilt
  standalone. Worth naming explicitly so the AMF decision isn't accidentally
  re-litigated inside tinyfactory, and so the squash-vs-merge-commit question
  (item 1) gets answered once for both.

## Decisions needed from Vic (no build until then)

1. Merge method: squash (spec as written) or merge commit (his standing rule)?
2. Adopt the two Q2 safeguards as v1 defaults (CI protection + daily cap)?
3. Q1: Codex primary / Claude secondary + Claude for plan — agreed?
4. Q3: fixes always via PR?
5. Q4: where does the first real run live, and is a dry-run first cycle OK?

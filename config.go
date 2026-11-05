// config.go — the typed form of factory-flow.yaml, with validation.
//
// The YAML is the source of truth; this file decodes it into structs and
// rejects anything that contradicts the operating flow (wrong stage order,
// squash merge, missing spec-gate fields, guardrails off). A bad config
// fails loudly at load time, never halfway through a run.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the whole factory-flow.yaml document.
type Config struct {
	Version    int
	Loop       LoopConfig
	Stages     StageConfig
	Caps       CapsConfig
	Invariants InvariantsConfig
}

type LoopConfig struct {
	OneItemAtATime    bool
	PreviewFirstCycle bool
}

type StageConfig struct {
	Intake   IntakeStage
	Plan     PlanStage
	Generate GenerateStage
	Evaluate EvaluateStage
	Deploy   DeployStage
	Learn    LearnStage
}

type IntakeStage struct {
	Gate      string
	Requires  []string
	OnMissing string
}

type PlanStage struct {
	Steps        []string
	Output       string
	TaskRequires []string
}

type GenerateStage struct {
	OneTaskPer string
	TDD        bool
	Models     ModelsConfig
}

type ModelsConfig struct {
	Plan     string
	Generate string
	Cheap    string // empty when undecided
}

type EvaluateStage struct {
	Order        []string
	Review       []string
	MaxSendBacks int
}

type DeployStage struct {
	Via         string
	Merge       string
	OnRedMain   string
	ReleaseFrom string
}

type LearnStage struct {
	Observer bool
	Log      string
}

type CapsConfig struct {
	CostUSDPerRun *float64
	CostUSDPerDay *float64
	MergesPerDay  *int
	// StopAfterConsecutiveFailures is the number of consecutive
	// failures/reverts after which the loop stops for a human.
	StopAfterConsecutiveFailures int
}

type InvariantsConfig struct {
	ProtectedPaths    []string
	NeverDirectToMain bool
	NeverReadSecrets  []string
	NeverEditOwnCI    bool
}

// LoadConfig reads and validates the config file at path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	root, err := ParseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	cfg, err := DecodeConfig(root)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// LiveCapsReady reports whether every cap a live run needs is set. A live
// run must refuse to merge while any cap is null.
func (c *Config) LiveCapsReady() bool {
	return len(c.MissingCaps()) == 0
}

// MissingCaps lists the unset caps a live run needs.
func (c *Config) MissingCaps() []string {
	var out []string
	if c.Caps.CostUSDPerRun == nil {
		out = append(out, "caps.cost_usd_per_run")
	}
	if c.Caps.CostUSDPerDay == nil {
		out = append(out, "caps.cost_usd_per_day")
	}
	if c.Caps.MergesPerDay == nil {
		out = append(out, "caps.merges_per_day")
	}
	return out
}

// DecodeConfig converts a parsed YAML document into a Config.
func DecodeConfig(root *Node) (*Config, error) {
	if root == nil || root.Kind != KindMap {
		return nil, fmt.Errorf("document root must be a mapping")
	}
	cfg := &Config{}
	var err error

	version, err := reqInt(root, "version", "version")
	if err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("version: must be 1, got %d", version)
	}
	cfg.Version = version

	loop, err := reqMap(root, "loop")
	if err != nil {
		return nil, err
	}
	if cfg.Loop.OneItemAtATime, err = reqBool(loop, "loop", "one_item_at_a_time"); err != nil {
		return nil, err
	}
	if !cfg.Loop.OneItemAtATime {
		return nil, fmt.Errorf("loop.one_item_at_a_time: must be true, the flow runs one item at a time")
	}
	if cfg.Loop.PreviewFirstCycle, err = reqBool(loop, "loop", "preview_first_cycle"); err != nil {
		return nil, err
	}
	if !cfg.Loop.PreviewFirstCycle {
		return nil, fmt.Errorf("loop.preview_first_cycle: must be true, preview is the default for the first cycle")
	}

	stages, err := reqMap(root, "stages")
	if err != nil {
		return nil, err
	}
	if err := checkStageOrder(stages); err != nil {
		return nil, err
	}
	if err := decodeStages(cfg, stages); err != nil {
		return nil, err
	}

	caps, err := reqMap(root, "caps")
	if err != nil {
		return nil, err
	}
	if err := decodeCaps(cfg, caps); err != nil {
		return nil, err
	}

	inv, err := reqMap(root, "invariants")
	if err != nil {
		return nil, err
	}
	if err := decodeInvariants(cfg, inv); err != nil {
		return nil, err
	}
	return cfg, nil
}

func checkStageOrder(stages *Node) error {
	want := []string{"intake", "plan", "generate", "evaluate", "deploy", "learn"}
	if len(stages.Pairs) != len(want) {
		return fmt.Errorf("stages: must have exactly the stages %s, got %d entries", strings.Join(want, ", "), len(stages.Pairs))
	}
	for i, w := range want {
		if stages.Pairs[i].Key != w {
			return fmt.Errorf("stages: stage order must be %s, got %q at position %d", strings.Join(want, ", "), stages.Pairs[i].Key, i+1)
		}
	}
	return nil
}

func decodeStages(cfg *Config, stages *Node) error {
	var err error

	intake, err := reqMap(stages, "intake")
	if err != nil {
		return err
	}
	if cfg.Stages.Intake.Gate, err = reqString(intake, "stages.intake", "gate"); err != nil {
		return err
	}
	if cfg.Stages.Intake.Gate != "spec" {
		return fmt.Errorf("stages.intake.gate: must be \"spec\", got %q", cfg.Stages.Intake.Gate)
	}
	if cfg.Stages.Intake.Requires, err = reqStringList(intake, "stages.intake", "requires"); err != nil {
		return err
	}
	for _, field := range []string{"outcome", "constraints", "acceptance_criteria", "escalation_conditions"} {
		if !contains(cfg.Stages.Intake.Requires, field) {
			return fmt.Errorf("stages.intake.requires: must include %q", field)
		}
	}
	if cfg.Stages.Intake.OnMissing, err = reqString(intake, "stages.intake", "on_missing"); err != nil {
		return err
	}
	if cfg.Stages.Intake.OnMissing != "send_back" {
		return fmt.Errorf("stages.intake.on_missing: must be \"send_back\", got %q", cfg.Stages.Intake.OnMissing)
	}

	plan, err := reqMap(stages, "plan")
	if err != nil {
		return err
	}
	if cfg.Stages.Plan.Steps, err = reqStringList(plan, "stages.plan", "steps"); err != nil {
		return err
	}
	if strings.Join(cfg.Stages.Plan.Steps, ",") != "specify,clarify,plan,tasks" {
		return fmt.Errorf("stages.plan.steps: must be [specify, clarify, plan, tasks], got %v", cfg.Stages.Plan.Steps)
	}
	if cfg.Stages.Plan.Output, err = reqString(plan, "stages.plan", "output"); err != nil {
		return err
	}
	if cfg.Stages.Plan.Output != "tasks.md" {
		return fmt.Errorf("stages.plan.output: must be \"tasks.md\", got %q", cfg.Stages.Plan.Output)
	}
	if cfg.Stages.Plan.TaskRequires, err = reqStringList(plan, "stages.plan", "task_requires"); err != nil {
		return err
	}
	for _, field := range []string{"acceptance_criteria", "verify_command"} {
		if !contains(cfg.Stages.Plan.TaskRequires, field) {
			return fmt.Errorf("stages.plan.task_requires: must include %q", field)
		}
	}

	gen, err := reqMap(stages, "generate")
	if err != nil {
		return err
	}
	if cfg.Stages.Generate.OneTaskPer, err = reqString(gen, "stages.generate", "one_task_per"); err != nil {
		return err
	}
	if cfg.Stages.Generate.OneTaskPer != "worktree" {
		return fmt.Errorf("stages.generate.one_task_per: must be \"worktree\", got %q", cfg.Stages.Generate.OneTaskPer)
	}
	if cfg.Stages.Generate.TDD, err = reqBool(gen, "stages.generate", "tdd"); err != nil {
		return err
	}
	if !cfg.Stages.Generate.TDD {
		return fmt.Errorf("stages.generate.tdd: must be true, the flow is test-first")
	}
	models, err := reqMap(gen, "models")
	if err != nil {
		return err
	}
	if cfg.Stages.Generate.Models.Plan, err = reqString(models, "stages.generate.models", "plan"); err != nil {
		return err
	}
	if cfg.Stages.Generate.Models.Plan != "claude" {
		return fmt.Errorf("stages.generate.models.plan: must be \"claude\", got %q", cfg.Stages.Generate.Models.Plan)
	}
	if cfg.Stages.Generate.Models.Generate, err = reqString(models, "stages.generate.models", "generate"); err != nil {
		return err
	}
	if cfg.Stages.Generate.Models.Generate != "codex" {
		return fmt.Errorf("stages.generate.models.generate: must be \"codex\", got %q", cfg.Stages.Generate.Models.Generate)
	}
	if cheap := models.Get("cheap"); cheap != nil && !cheap.IsNull() {
		if cfg.Stages.Generate.Models.Cheap, err = cheap.AsString(); err != nil {
			return fmt.Errorf("stages.generate.models.cheap: %v", err)
		}
	}

	eval, err := reqMap(stages, "evaluate")
	if err != nil {
		return err
	}
	if cfg.Stages.Evaluate.Order, err = reqStringList(eval, "stages.evaluate", "order"); err != nil {
		return err
	}
	if strings.Join(cfg.Stages.Evaluate.Order, ",") != "validate-diff,run-tests,verify-behavior" {
		return fmt.Errorf("stages.evaluate.order: must be [validate-diff, run-tests, verify-behavior], got %v", cfg.Stages.Evaluate.Order)
	}
	if cfg.Stages.Evaluate.Review, err = reqStringList(eval, "stages.evaluate", "review"); err != nil {
		return err
	}
	if strings.Join(cfg.Stages.Evaluate.Review, ",") != "machine,human" {
		return fmt.Errorf("stages.evaluate.review: must be [machine, human], got %v", cfg.Stages.Evaluate.Review)
	}
	if cfg.Stages.Evaluate.MaxSendBacks, err = reqInt(eval, "stages.evaluate", "max_send_backs"); err != nil {
		return err
	}
	if cfg.Stages.Evaluate.MaxSendBacks < 1 {
		return fmt.Errorf("stages.evaluate.max_send_backs: must be at least 1, got %d", cfg.Stages.Evaluate.MaxSendBacks)
	}

	deploy, err := reqMap(stages, "deploy")
	if err != nil {
		return err
	}
	if cfg.Stages.Deploy.Via, err = reqString(deploy, "stages.deploy", "via"); err != nil {
		return err
	}
	if cfg.Stages.Deploy.Via != "pr" {
		return fmt.Errorf("stages.deploy.via: must be \"pr\", got %q", cfg.Stages.Deploy.Via)
	}
	if cfg.Stages.Deploy.Merge, err = reqString(deploy, "stages.deploy", "merge"); err != nil {
		return err
	}
	if cfg.Stages.Deploy.Merge != "merge-commit" {
		if cfg.Stages.Deploy.Merge == "squash" {
			return fmt.Errorf("stages.deploy.merge: squash is forbidden by the merge-commit rule, use \"merge-commit\"")
		}
		return fmt.Errorf("stages.deploy.merge: must be \"merge-commit\", got %q", cfg.Stages.Deploy.Merge)
	}
	if cfg.Stages.Deploy.OnRedMain, err = reqString(deploy, "stages.deploy", "on_red_main"); err != nil {
		return err
	}
	if cfg.Stages.Deploy.OnRedMain != "fix-or-revert-and-stop" {
		return fmt.Errorf("stages.deploy.on_red_main: must be \"fix-or-revert-and-stop\", got %q", cfg.Stages.Deploy.OnRedMain)
	}
	if cfg.Stages.Deploy.ReleaseFrom, err = reqString(deploy, "stages.deploy", "release_from"); err != nil {
		return err
	}
	if cfg.Stages.Deploy.ReleaseFrom != "main" {
		return fmt.Errorf("stages.deploy.release_from: must be \"main\", got %q", cfg.Stages.Deploy.ReleaseFrom)
	}

	learn, err := reqMap(stages, "learn")
	if err != nil {
		return err
	}
	if cfg.Stages.Learn.Observer, err = reqBool(learn, "stages.learn", "observer"); err != nil {
		return err
	}
	if !cfg.Stages.Learn.Observer {
		return fmt.Errorf("stages.learn.observer: must be true, the loop learns from every item")
	}
	if cfg.Stages.Learn.Log, err = reqString(learn, "stages.learn", "log"); err != nil {
		return err
	}
	if cfg.Stages.Learn.Log != "events.jsonl" {
		return fmt.Errorf("stages.learn.log: must be \"events.jsonl\", got %q", cfg.Stages.Learn.Log)
	}
	return nil
}

func decodeCaps(cfg *Config, caps *Node) error {
	var err error
	if cfg.Caps.CostUSDPerRun, err = optFloat(caps, "caps", "cost_usd_per_run"); err != nil {
		return err
	}
	if cfg.Caps.CostUSDPerRun != nil && *cfg.Caps.CostUSDPerRun <= 0 {
		return fmt.Errorf("caps.cost_usd_per_run: must be greater than 0, got %v", *cfg.Caps.CostUSDPerRun)
	}
	if cfg.Caps.CostUSDPerDay, err = optFloat(caps, "caps", "cost_usd_per_day"); err != nil {
		return err
	}
	if cfg.Caps.CostUSDPerDay != nil && *cfg.Caps.CostUSDPerDay <= 0 {
		return fmt.Errorf("caps.cost_usd_per_day: must be greater than 0, got %v", *cfg.Caps.CostUSDPerDay)
	}
	if cfg.Caps.MergesPerDay, err = optInt(caps, "caps", "merges_per_day"); err != nil {
		return err
	}
	if cfg.Caps.MergesPerDay != nil && *cfg.Caps.MergesPerDay <= 0 {
		return fmt.Errorf("caps.merges_per_day: must be greater than 0, got %d", *cfg.Caps.MergesPerDay)
	}
	if cfg.Caps.StopAfterConsecutiveFailures, err = reqInt(caps, "caps", "stop_after_consecutive_failures"); err != nil {
		return err
	}
	if cfg.Caps.StopAfterConsecutiveFailures < 1 {
		return fmt.Errorf("caps.stop_after_consecutive_failures: must be at least 1, got %d", cfg.Caps.StopAfterConsecutiveFailures)
	}
	return nil
}

func decodeInvariants(cfg *Config, inv *Node) error {
	var err error
	if cfg.Invariants.ProtectedPaths, err = reqStringList(inv, "invariants", "protected_paths"); err != nil {
		return err
	}
	if len(cfg.Invariants.ProtectedPaths) == 0 {
		return fmt.Errorf("invariants.protected_paths: must list at least one protected path")
	}
	if !contains(cfg.Invariants.ProtectedPaths, ".github/") {
		return fmt.Errorf("invariants.protected_paths: must include \".github/\"")
	}
	if cfg.Invariants.NeverDirectToMain, err = reqBool(inv, "invariants", "never_direct_to_main"); err != nil {
		return err
	}
	if !cfg.Invariants.NeverDirectToMain {
		return fmt.Errorf("invariants.never_direct_to_main: must be true, fixes go through a PR")
	}
	if cfg.Invariants.NeverReadSecrets, err = reqStringList(inv, "invariants", "never_read_secrets"); err != nil {
		return err
	}
	if !contains(cfg.Invariants.NeverReadSecrets, ".env") {
		return fmt.Errorf("invariants.never_read_secrets: must include \".env\"")
	}
	if cfg.Invariants.NeverEditOwnCI, err = reqBool(inv, "invariants", "never_edit_own_ci"); err != nil {
		return err
	}
	if !cfg.Invariants.NeverEditOwnCI {
		return fmt.Errorf("invariants.never_edit_own_ci: must be true, the loop never weakens its own checks")
	}
	return nil
}

// --- node access helpers. Each reports the full config path on failure. ---

func reqMap(parent *Node, key string) (*Node, error) {
	n := parent.Get(key)
	if n == nil {
		return nil, fmt.Errorf("%s: missing required mapping", key)
	}
	if n.Kind != KindMap {
		return nil, fmt.Errorf("%s: must be a mapping, got %s", key, n.Kind)
	}
	return n, nil
}

func reqString(parent *Node, path, key string) (string, error) {
	n := parent.Get(key)
	if n == nil {
		return "", fmt.Errorf("%s.%s: missing required value", path, key)
	}
	v, err := n.AsString()
	if err != nil {
		return "", fmt.Errorf("%s.%s: %v", path, key, err)
	}
	return v, nil
}

func reqBool(parent *Node, path, key string) (bool, error) {
	n := parent.Get(key)
	if n == nil {
		return false, fmt.Errorf("%s.%s: missing required value", path, key)
	}
	v, err := n.AsBool()
	if err != nil {
		return false, fmt.Errorf("%s.%s: %v", path, key, err)
	}
	return v, nil
}

func reqInt(parent *Node, path, key string) (int, error) {
	n := parent.Get(key)
	if n == nil {
		return 0, fmt.Errorf("%s.%s: missing required value", path, key)
	}
	v, err := n.AsInt()
	if err != nil {
		return 0, fmt.Errorf("%s.%s: %v", path, key, err)
	}
	return v, nil
}

func reqStringList(parent *Node, path, key string) ([]string, error) {
	n := parent.Get(key)
	if n == nil {
		return nil, fmt.Errorf("%s.%s: missing required value", path, key)
	}
	v, err := n.AsStringList()
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %v", path, key, err)
	}
	return v, nil
}

// optFloat returns nil for an absent or null value, otherwise the number.
func optFloat(parent *Node, path, key string) (*float64, error) {
	n := parent.Get(key)
	if n == nil || n.IsNull() {
		return nil, nil
	}
	s, err := n.AsString()
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %v", path, key, err)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("%s.%s: expected a number, got %q", path, key, s)
	}
	return &v, nil
}

// optInt returns nil for an absent or null value, otherwise the integer.
func optInt(parent *Node, path, key string) (*int, error) {
	n := parent.Get(key)
	if n == nil || n.IsNull() {
		return nil, nil
	}
	v, err := n.AsInt()
	if err != nil {
		return nil, fmt.Errorf("%s.%s: %v", path, key, err)
	}
	return &v, nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

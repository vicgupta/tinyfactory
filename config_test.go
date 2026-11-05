package main

import (
	"os"
	"strings"
	"testing"
)

func mustLoadRealConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadConfig("factory-flow.yaml")
	if err != nil {
		t.Fatalf("LoadConfig(factory-flow.yaml): %v", err)
	}
	return cfg
}

func TestLoadRealConfig(t *testing.T) {
	cfg := mustLoadRealConfig(t)
	if cfg.Version != 1 {
		t.Fatalf("Version = %d, want 1", cfg.Version)
	}
	if !cfg.Loop.OneItemAtATime || !cfg.Loop.PreviewFirstCycle {
		t.Fatal("loop flags should both be true")
	}
	if cfg.Stages.Deploy.Merge != "merge-commit" {
		t.Fatalf("Deploy.Merge = %q, want merge-commit", cfg.Stages.Deploy.Merge)
	}
	if cfg.Stages.Generate.Models.Plan != "claude" || cfg.Stages.Generate.Models.Generate != "codex" {
		t.Fatalf("models = %+v, want plan=claude generate=codex", cfg.Stages.Generate.Models)
	}
	if cfg.Stages.Evaluate.MaxSendBacks != 2 {
		t.Fatalf("MaxSendBacks = %d, want 2", cfg.Stages.Evaluate.MaxSendBacks)
	}
	if cfg.Caps.StopAfterConsecutiveFailures != 2 {
		t.Fatalf("StopAfterConsecutiveFailures = %d, want 2", cfg.Caps.StopAfterConsecutiveFailures)
	}
	if cfg.LiveCapsReady() {
		t.Fatal("real config has null caps, LiveCapsReady should be false")
	}
	missing := cfg.MissingCaps()
	if strings.Join(missing, ",") != "caps.cost_usd_per_run,caps.cost_usd_per_day,caps.merges_per_day" {
		t.Fatalf("MissingCaps = %v", missing)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig("does-not-exist.yaml"); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestDecodeConfigBad(t *testing.T) {
	base, err := os.ReadFile("factory-flow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		mutate  func(string) string
		wantSub string
	}{
		{
			"squash merge is forbidden",
			func(s string) string { return strings.Replace(s, "merge: merge-commit", "merge: squash", 1) },
			"squash is forbidden",
		},
		{
			"rebase merge is rejected",
			func(s string) string { return strings.Replace(s, "merge: merge-commit", "merge: rebase", 1) },
			"must be \"merge-commit\"",
		},
		{
			"version must be 1",
			func(s string) string { return strings.Replace(s, "version: 1", "version: 2", 1) },
			"version: must be 1",
		},
		{
			"stage order is enforced",
			func(s string) string {
				// Swap the intake and plan key lines; the order check
				// runs before the bodies are decoded.
				s = strings.Replace(s, "  intake:\n", "  __TMP__:\n", 1)
				s = strings.Replace(s, "  plan:\n", "  intake:\n", 1)
				s = strings.Replace(s, "  __TMP__:\n", "  plan:\n", 1)
				return s
			},
			"stage order must be",
		},
		{
			"spec gate must require all four fields",
			func(s string) string { return strings.Replace(s, "      - escalation_conditions\n", "", 1) },
			"must include \"escalation_conditions\"",
		},
		{
			"eval order is enforced",
			func(s string) string {
				return strings.Replace(s, "order: [validate-diff, run-tests, verify-behavior]", "order: [run-tests, validate-diff, verify-behavior]", 1)
			},
			"must be [validate-diff, run-tests, verify-behavior]",
		},
		{
			"guardrail never_direct_to_main must be true",
			func(s string) string {
				return strings.Replace(s, "never_direct_to_main: true", "never_direct_to_main: false", 1)
			},
			"never_direct_to_main: must be true",
		},
		{
			"guardrail never_edit_own_ci must be true",
			func(s string) string {
				return strings.Replace(s, "never_edit_own_ci: true", "never_edit_own_ci: false", 1)
			},
			"never_edit_own_ci: must be true",
		},
		{
			"protected paths must include .github/",
			func(s string) string { return strings.Replace(s, "    - .github/\n", "", 1) },
			"must include \".github/\"",
		},
		{
			"secrets must include .env",
			func(s string) string { return strings.Replace(s, "    - .env\n", "    - .ssh/\n", 1) },
			"must include \".env\"",
		},
		{
			"plan steps are fixed",
			func(s string) string {
				return strings.Replace(s, "steps: [specify, clarify, plan, tasks]", "steps: [specify, plan, tasks]", 1)
			},
			"must be [specify, clarify, plan, tasks]",
		},
		{
			"deploy must go via PR",
			func(s string) string { return strings.Replace(s, "via: pr", "via: push", 1) },
			"must be \"pr\"",
		},
		{
			"stop threshold must be at least 1",
			func(s string) string {
				return strings.Replace(s, "stop_after_consecutive_failures: 2", "stop_after_consecutive_failures: 0", 1)
			},
			"must be at least 1",
		},
		{
			"a negative cap is rejected",
			func(s string) string {
				return strings.Replace(s, "cost_usd_per_run: null", "cost_usd_per_run: -5", 1)
			},
			"must be greater than 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := ParseYAML([]byte(tc.mutate(string(base))))
			if err != nil {
				t.Fatalf("mutated config failed to parse: %v", err)
			}
			_, err = DecodeConfig(root)
			if err == nil {
				t.Fatalf("expected a validation error containing %q, got none", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestDecodeConfigCapsSet(t *testing.T) {
	base, err := os.ReadFile("factory-flow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(base)
	s = strings.Replace(s, "cost_usd_per_run: null", "cost_usd_per_run: 5", 1)
	s = strings.Replace(s, "cost_usd_per_day: null", "cost_usd_per_day: 50", 1)
	s = strings.Replace(s, "merges_per_day: null", "merges_per_day: 10", 1)
	root, err := ParseYAML([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := DecodeConfig(root)
	if err != nil {
		t.Fatalf("config with caps set should validate: %v", err)
	}
	if !cfg.LiveCapsReady() {
		t.Fatal("config with all caps set should be LiveCapsReady")
	}
	if *cfg.Caps.CostUSDPerRun != 5 || *cfg.Caps.CostUSDPerDay != 50 || *cfg.Caps.MergesPerDay != 10 {
		t.Fatalf("caps = %+v", cfg.Caps)
	}
}

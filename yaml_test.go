package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseRealConfig(t *testing.T) {
	data, err := os.ReadFile("factory-flow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	root, err := ParseYAML(data)
	if err != nil {
		t.Fatalf("parsing the real factory-flow.yaml failed: %v", err)
	}
	if root.Kind != KindMap {
		t.Fatalf("root kind = %v, want map", root.Kind)
	}
	v, err := root.Get("version").AsInt()
	if err != nil || v != 1 {
		t.Fatalf("version = %d, err %v, want 1", v, err)
	}
	loop := root.Get("loop")
	if loop == nil {
		t.Fatal("missing loop")
	}
	one, err := loop.Get("one_item_at_a_time").AsBool()
	if err != nil || !one {
		t.Fatalf("loop.one_item_at_a_time = %v, err %v, want true", one, err)
	}
	preview, err := loop.Get("preview_first_cycle").AsBool()
	if err != nil || !preview {
		t.Fatalf("loop.preview_first_cycle = %v, err %v, want true", preview, err)
	}

	stages := root.Get("stages")
	if stages == nil {
		t.Fatal("missing stages")
	}
	wantOrder := []string{"intake", "plan", "generate", "evaluate", "deploy", "learn"}
	if len(stages.Pairs) != len(wantOrder) {
		t.Fatalf("stages has %d entries, want %d", len(stages.Pairs), len(wantOrder))
	}
	for i, want := range wantOrder {
		if stages.Pairs[i].Key != want {
			t.Fatalf("stages[%d] = %q, want %q", i, stages.Pairs[i].Key, want)
		}
	}

	intake := stages.Get("intake")
	requires, err := intake.Get("requires").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(requires, ",") != "outcome,constraints,acceptance_criteria,escalation_conditions" {
		t.Fatalf("intake.requires = %v", requires)
	}

	plan := stages.Get("plan")
	steps, err := plan.Get("steps").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != "specify,clarify,plan,tasks" {
		t.Fatalf("plan.steps = %v", steps)
	}

	gen := stages.Get("generate")
	models := gen.Get("models")
	if got, _ := models.Get("plan").AsString(); got != "claude" {
		t.Fatalf("generate.models.plan = %q, want claude", got)
	}
	if got, _ := models.Get("generate").AsString(); got != "codex" {
		t.Fatalf("generate.models.generate = %q, want codex", got)
	}

	eval := stages.Get("evaluate")
	order, err := eval.Get("order").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "validate-diff,run-tests,verify-behavior" {
		t.Fatalf("evaluate.order = %v", order)
	}
	backs, err := eval.Get("max_send_backs").AsInt()
	if err != nil || backs != 2 {
		t.Fatalf("evaluate.max_send_backs = %d, err %v, want 2", backs, err)
	}

	deploy := stages.Get("deploy")
	if got, _ := deploy.Get("merge").AsString(); got != "merge-commit" {
		t.Fatalf("deploy.merge = %q, want merge-commit", got)
	}

	caps := root.Get("caps")
	if caps == nil {
		t.Fatal("missing caps")
	}
	for _, key := range []string{"cost_usd_per_run", "cost_usd_per_day", "merges_per_day"} {
		if !caps.Get(key).IsNull() {
			t.Fatalf("caps.%s should be null", key)
		}
	}
	stop, err := caps.Get("stop_after_consecutive_failures").AsInt()
	if err != nil || stop != 2 {
		t.Fatalf("caps.stop_after_consecutive_failures = %d, err %v, want 2", stop, err)
	}

	inv := root.Get("invariants")
	paths, err := inv.Get("protected_paths").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != ".github/,evals/,pipeline file" {
		t.Fatalf("invariants.protected_paths = %v", paths)
	}
	secrets, err := inv.Get("never_read_secrets").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0] != ".env" {
		t.Fatalf("invariants.never_read_secrets = %v", secrets)
	}
}

func TestParseScalars(t *testing.T) {
	doc := `
a: 42
b: true
c: false
d: null
e: ~
f:
g: 'single quoted'
h: "double \"quoted\" \n"
i: plain text here
j: [one, two, 'three, four']
k: []
`
	root, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := root.Get("a").AsInt(); v != 42 {
		t.Fatalf("a = %d, want 42", v)
	}
	if v, _ := root.Get("b").AsBool(); !v {
		t.Fatal("b should be true")
	}
	if v, _ := root.Get("c").AsBool(); v {
		t.Fatal("c should be false")
	}
	if !root.Get("d").IsNull() || !root.Get("e").IsNull() || !root.Get("f").IsNull() {
		t.Fatal("d, e, f should be null")
	}
	if v, _ := root.Get("g").AsString(); v != "single quoted" {
		t.Fatalf("g = %q", v)
	}
	if v, _ := root.Get("h").AsString(); v != "double \"quoted\" \n" {
		t.Fatalf("h = %q", v)
	}
	if v, _ := root.Get("i").AsString(); v != "plain text here" {
		t.Fatalf("i = %q", v)
	}
	list, err := root.Get("j").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(list, "|") != "one|two|three, four" {
		t.Fatalf("j = %v", list)
	}
	empty, err := root.Get("k").AsStringList()
	if err != nil || len(empty) != 0 {
		t.Fatalf("k = %v, err %v, want empty list", empty, err)
	}
	// Comments: full-line and trailing.
	doc2 := "# a comment\nx: 1 # trailing\n# another\ny: 2\n"
	root2, err := ParseYAML([]byte(doc2))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := root2.Get("x").AsInt(); v != 1 {
		t.Fatalf("x = %d, want 1", v)
	}
	if v, _ := root2.Get("y").AsInt(); v != 2 {
		t.Fatalf("y = %d, want 2", v)
	}
}

func TestParseNestedMapsAndSequences(t *testing.T) {
	doc := `
outer:
  inner:
    deep: yes
  list:
    - one
    - two
same:
- a
- b
`
	root, err := ParseYAML([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := root.Get("outer").Get("inner").Get("deep").AsString(); got != "yes" {
		t.Fatalf("deep = %q, want yes", got)
	}
	list, err := root.Get("outer").Get("list").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(list, ",") != "one,two" {
		t.Fatalf("list = %v", list)
	}
	// Sequence at the same indent as its key.
	same, err := root.Get("same").AsStringList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(same, ",") != "a,b" {
		t.Fatalf("same = %v", same)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"tabs":             "a:\n\tb: 1\n",
		"duplicate key":    "a: 1\na: 2\n",
		"flow map":         "a: {x: 1}\n",
		"block scalar":     "a: |\n  text\n",
		"anchor":           "a: &x 1\n",
		"alias":            "a: *x\n",
		"map in sequence":  "a:\n  - k: v\n",
		"bad line":         "just some words\n",
		"bad indent":       "a:\n    b: 1\n  c: 2\n",
		"unterminated":     "a: 'open\n",
		"colon in scalar":  "a: one: two\n",
		"unclosed flow":    "a: [one, two\n",
		"sequence in map":  "a: 1\n- x\n",
		"empty sequence":   "a:\n  - \n",
		"bad escape":       "a: \"\\q\"\n",
		"trailing garbage": "a: 'x' junk\n",
		"empty document":   "# only a comment\n",
		"bad boolean":      "a: maybe\n", // parses as a string; AsBool must fail
	}
	for name, doc := range cases {
		if name == "bad boolean" {
			root, err := ParseYAML([]byte(doc))
			if err != nil {
				t.Errorf("%s: unexpected parse error: %v", name, err)
				continue
			}
			if _, err := root.Get("a").AsBool(); err == nil {
				t.Errorf("%s: AsBool should fail on %q", name, "maybe")
			}
			continue
		}
		if _, err := ParseYAML([]byte(doc)); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

const sampleIssue = `# Fix the login redirect

## Outcome
Users land on their dashboard after login instead of a blank page.

## Constraints
- No changes to the auth service.
- Must keep the existing session cookie format.

## Acceptance criteria
- After login, the browser shows the dashboard.
- The session cookie is unchanged.
- A failed login still shows the error page.

## Escalation
If the redirect touches auth, stop and flag for a human.
`

func TestParseIssue(t *testing.T) {
	issue, err := ParseIssue([]byte(sampleIssue))
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "Fix the login redirect" {
		t.Fatalf("Title = %q", issue.Title)
	}
	if !strings.Contains(issue.Outcome, "dashboard after login") {
		t.Fatalf("Outcome = %q", issue.Outcome)
	}
	if !strings.Contains(issue.Constraints, "auth service") {
		t.Fatalf("Constraints = %q", issue.Constraints)
	}
	if len(issue.AcceptanceCriteria) != 3 {
		t.Fatalf("AcceptanceCriteria = %v, want 3", issue.AcceptanceCriteria)
	}
	if issue.AcceptanceCriteria[0] != "After login, the browser shows the dashboard." {
		t.Fatalf("criteria[0] = %q", issue.AcceptanceCriteria[0])
	}
	if !strings.Contains(issue.Escalation, "stop and flag") {
		t.Fatalf("Escalation = %q", issue.Escalation)
	}
	if !issue.GatePassed() {
		t.Fatalf("sample issue should pass the gate, missing %v", issue.MissingFields())
	}
}

func TestParseIssueSingleLineForm(t *testing.T) {
	doc := `Outcome: Ship the fix.
Constraints: None.
Acceptance criteria: The test passes.
Escalation: Ask Vic.
`
	issue, err := ParseIssue([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !issue.GatePassed() {
		t.Fatalf("single-line issue should pass the gate, missing %v", issue.MissingFields())
	}
	if len(issue.AcceptanceCriteria) != 1 || issue.AcceptanceCriteria[0] != "The test passes." {
		t.Fatalf("AcceptanceCriteria = %v", issue.AcceptanceCriteria)
	}
}

func TestParseIssueMissingFields(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want []string
	}{
		{
			"no outcome",
			"## Constraints\nNone.\n\n## Acceptance criteria\n- It works.\n\n## Escalation\nAsk.\n",
			[]string{"outcome"},
		},
		{
			"no criteria",
			"## Outcome\nShip it.\n\n## Constraints\nNone.\n\n## Escalation\nAsk.\n",
			[]string{"acceptance_criteria"},
		},
		{
			"empty file",
			"",
			[]string{"outcome", "constraints", "acceptance_criteria", "escalation_conditions"},
		},
		{
			"only a title",
			"# Do a thing\n",
			[]string{"outcome", "constraints", "acceptance_criteria", "escalation_conditions"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue, err := ParseIssue([]byte(tc.doc))
			if err != nil {
				t.Fatal(err)
			}
			got := issue.MissingFields()
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("MissingFields = %v, want %v", got, tc.want)
			}
			if issue.GatePassed() {
				t.Fatal("issue with missing fields should not pass the gate")
			}
		})
	}
}

func TestParseIssueCriteriaWithoutBullets(t *testing.T) {
	doc := `## Outcome
Ship it.

## Constraints
None.

## Acceptance criteria
The dashboard loads after login and the cookie is unchanged.

## Escalation
Ask.
`
	issue, err := ParseIssue([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(issue.AcceptanceCriteria) != 1 {
		t.Fatalf("AcceptanceCriteria = %v, want 1 paragraph criterion", issue.AcceptanceCriteria)
	}
}

func TestBuildPlan(t *testing.T) {
	issue, err := ParseIssue([]byte(sampleIssue))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustLoadRealConfig(t)
	tasks := BuildPlan(issue, cfg)
	if len(tasks) != 3 {
		t.Fatalf("BuildPlan made %d tasks, want 3", len(tasks))
	}
	for _, task := range tasks {
		if len(task.AcceptanceCriteria) != 1 {
			t.Fatalf("task %q has %d criteria, want 1", task.Title, len(task.AcceptanceCriteria))
		}
		if task.Verify == "" {
			t.Fatalf("task %q has no Verify command", task.Title)
		}
	}
	md := RenderTasksMD(issue, tasks)
	for _, want := range []string{"# Tasks — Fix the login redirect", "Task 1:", "Task 3:", "Acceptance criteria:", "Verify: `go test ./...`", "Mechanical breakdown"} {
		if !strings.Contains(md, want) {
			t.Fatalf("tasks.md missing %q", want)
		}
	}
}

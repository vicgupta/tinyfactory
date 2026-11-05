// agent.go — running the coding agents.
//
// The loop shells out to the configured CLIs: codex for generate, claude
// for plan. The invocations are the CLIs' non-interactive forms
// (codex exec, claude -p); verify them against the installed CLI versions
// before a live run. The binary never passes a token to an agent, and the
// prompts forbid reading secrets.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// AgentRunner runs one agent CLI with a prompt in a directory and returns
// its combined output.
type AgentRunner interface {
	Run(ctx context.Context, cli, prompt, dir string) (string, error)
}

// ExecAgentRunner runs the real CLIs.
type ExecAgentRunner struct {
	// Timeout bounds one agent call; zero means DefaultAgentTimeout.
	Timeout time.Duration
}

// DefaultAgentTimeout bounds a single agent call.
const DefaultAgentTimeout = 30 * time.Minute

// agentArgs maps a CLI name to its non-interactive arguments.
func agentArgs(cli string) ([]string, error) {
	switch cli {
	case "codex":
		return []string{"exec"}, nil
	case "claude":
		return []string{"-p"}, nil
	}
	return nil, fmt.Errorf("unknown agent CLI %q (want \"codex\" or \"claude\")", cli)
}

func (r ExecAgentRunner) Run(ctx context.Context, cli, prompt, dir string) (string, error) {
	args, err := agentArgs(cli)
	if err != nil {
		return "", err
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = DefaultAgentTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, cli, append(args, prompt)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("agent %s: %w: %s", cli, err, tailLines(text, 20))
	}
	return text, nil
}

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// PlanPrompt builds the prompt for the plan agent: produce tasks.md for
// the issue in the plan-tasks skill shape.
func PlanPrompt(issue *Issue) string {
	var b strings.Builder
	b.WriteString("You are the plan agent for tinyfactory. Break the issue below into small, ordered tasks and write tasks.md in the repository root.\n\n")
	fmt.Fprintf(&b, "Issue: %s\n\nOutcome:\n%s\n\nConstraints:\n%s\n\nAcceptance criteria:\n", issue.Title, issue.Outcome, issue.Constraints)
	for _, c := range issue.AcceptanceCriteria {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	fmt.Fprintf(&b, "\nEscalation:\n%s\n\n", issue.Escalation)
	b.WriteString("Write one task per acceptance criterion, in dependency order. Each task has this shape:\n\n")
	b.WriteString("Task N: <short title>\n<what to change>\nAcceptance criteria:\n- <an observable outcome>\nVerify: `<one shell command that checks this task>`\n\n")
	b.WriteString("Every task needs at least one acceptance-criteria bullet and a Verify line with a single command. Do not write any code. Never read .env or any secrets file. Never modify .github/, evals/, or the pipeline file.\n")
	return b.String()
}

// GeneratePrompt builds the prompt for the generate agent: implement one
// task, test-first, in the worktree.
func GeneratePrompt(task Task, issue *Issue) string {
	var b strings.Builder
	b.WriteString("You are the generate agent for tinyfactory. Implement exactly one task, test-first.\n\n")
	fmt.Fprintf(&b, "Issue: %s\n\nTask: %s\n%s\nAcceptance criteria:\n", issue.Title, task.Title, task.Body)
	for _, c := range task.AcceptanceCriteria {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	fmt.Fprintf(&b, "\nVerify: `%s`\n\n", task.Verify)
	b.WriteString("First write a failing behavior case for the acceptance criteria, then write the code that makes it pass. Run the Verify command before you finish. Do not change anything outside this task. Never read .env or any secrets file. Never modify .github/, evals/, or the pipeline file. Never commit; the loop commits.\n")
	return b.String()
}

// GeneratePromptWithFeedback is GeneratePrompt plus the previous attempt's
// failure, so a send-back tells the agent exactly what to fix.
func GeneratePromptWithFeedback(task Task, issue *Issue, feedback string) string {
	prompt := GeneratePrompt(task, issue)
	if strings.TrimSpace(feedback) == "" {
		return prompt
	}
	return prompt + "\nThe previous attempt failed. Fix the failure below; do not start over unless the failure says to.\n\n" + strings.TrimSpace(feedback) + "\n"
}

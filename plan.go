// plan.go — the plan step: turn an issue into tasks.md.
//
// In a live run the plan agent (Claude, per the config) writes the real
// plan. Preview and dry-run have no agent, so BuildPlan makes a mechanical
// breakdown instead: one task per acceptance criterion, each carrying that
// criterion and a Verify command. It is labelled as mechanical wherever it
// is printed or written, and the plan agent refines it in a live run.
package main

import (
	"fmt"
	"regexp"
	"strings"
)

// taskHeadingRe matches the "Task N: <title>" line RenderTasksMD writes.
var taskHeadingRe = regexp.MustCompile(`^Task \d+: (.+)$`)

// Task is one planned unit of work.
type Task struct {
	Title              string
	Body               string
	AcceptanceCriteria []string
	Verify             string
}

// BuildPlan derives tasks from the issue's acceptance criteria.
func BuildPlan(issue *Issue, cfg *Config) []Task {
	tasks := make([]Task, 0, len(issue.AcceptanceCriteria))
	for _, criterion := range issue.AcceptanceCriteria {
		title := criterion
		if len(title) > 80 {
			title = strings.TrimSpace(title[:77]) + "..."
		}
		tasks = append(tasks, Task{
			Title:              title,
			Body:               criterion,
			AcceptanceCriteria: []string{criterion},
			Verify:             "go test ./...",
		})
	}
	return tasks
}

// ParseTasksMD parses tasks.md in the shape RenderTasksMD writes (and
// PlanPrompt asks the plan agent to write): "Task N: <title>" lines, a
// body, an "Acceptance criteria:" section with "- " bullets, and a
// "Verify: `<command>`" line. A task with no acceptance criteria or no
// Verify command is an error: the plan stage must not hand the loop an
// unverifiable task.
func ParseTasksMD(md string) ([]Task, error) {
	var tasks []Task
	var cur *Task
	var section string // "", "acceptance"

	flush := func() {
		if cur != nil {
			tasks = append(tasks, *cur)
			cur = nil
		}
	}
	lines := strings.Split(md, "\n")
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		if m := taskHeadingRe.FindStringSubmatch(line); m != nil {
			flush()
			cur = &Task{Title: strings.TrimSpace(m[1])}
			section = ""
			continue
		}
		if cur == nil {
			continue // title, notes, blank lines before the first task
		}
		switch {
		case line == "Acceptance criteria:":
			section = "acceptance"
		case strings.HasPrefix(line, "Verify:"):
			verify := strings.TrimSpace(strings.TrimPrefix(line, "Verify:"))
			verify = strings.Trim(verify, "`")
			cur.Verify = strings.TrimSpace(verify)
			section = ""
		case section == "acceptance" && strings.HasPrefix(line, "- "):
			cur.AcceptanceCriteria = append(cur.AcceptanceCriteria, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
		case section == "acceptance" && strings.TrimSpace(line) != "":
			// A non-bullet line inside the criteria section ends it;
			// treat it as body text only if it is not a Verify line.
			section = ""
			cur.Body += line + "\n"
		default:
			if strings.TrimSpace(line) != "" {
				cur.Body += line + "\n"
			}
		}
	}
	flush()
	if len(tasks) == 0 {
		return nil, fmt.Errorf("tasks.md: no tasks found (want \"Task N: <title>\" lines)")
	}
	for i := range tasks {
		tasks[i].Body = strings.TrimSpace(tasks[i].Body)
		if len(tasks[i].AcceptanceCriteria) == 0 {
			return nil, fmt.Errorf("tasks.md task %d (%q): missing acceptance criteria", i+1, tasks[i].Title)
		}
		if strings.TrimSpace(tasks[i].Verify) == "" {
			return nil, fmt.Errorf("tasks.md task %d (%q): missing Verify command", i+1, tasks[i].Title)
		}
	}
	return tasks, nil
}
func RenderTasksMD(issue *Issue, tasks []Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Tasks — %s\n\n", issue.Title)
	b.WriteString("> Mechanical breakdown for preview. The plan agent (Claude) refines tasks and Verify commands in a live run.\n\n")
	for i, t := range tasks {
		fmt.Fprintf(&b, "Task %d: %s\n%s\nAcceptance criteria:\n", i+1, t.Title, t.Body)
		for _, c := range t.AcceptanceCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		fmt.Fprintf(&b, "Verify: `%s`\n\n", t.Verify)
	}
	return b.String()
}

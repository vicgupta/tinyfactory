// issue.go — issue files and the spec gate.
//
// An issue is a markdown file. Its four required fields are read from
// sections headed "Outcome", "Constraints", "Acceptance criteria" and
// "Escalation" (any heading level 1-3), or from single lines of the form
// "Outcome: ...". Anything else in the file is ignored. The spec gate
// fails when any field is missing or empty.
package main

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// Issue is one parsed issue file.
type Issue struct {
	Title              string
	Outcome            string
	Constraints        string
	AcceptanceCriteria []string
	Escalation         string
	Raw                string
}

// SpecGateFields are the four fields the spec gate requires, in config
// naming (matching stages.intake.requires).
var SpecGateFields = []string{"outcome", "constraints", "acceptance_criteria", "escalation_conditions"}

var (
	headingRe   = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*$`)
	fieldLineRe = regexp.MustCompile(`^(?i)(outcome|constraints|acceptance criteria|escalation)\s*:\s*(.+)$`)
	bulletRe    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.+)$`)
)

// ParseIssue parses an issue markdown file.
func ParseIssue(data []byte) (*Issue, error) {
	issue := &Issue{Raw: string(data)}
	var section string // "", "outcome", "constraints", "acceptance_criteria", "escalation"
	var sectionLines []string

	flush := func() {
		if section == "" {
			sectionLines = nil
			return
		}
		text := strings.TrimSpace(strings.Join(sectionLines, "\n"))
		switch section {
		case "outcome":
			issue.Outcome = text
		case "constraints":
			issue.Constraints = text
		case "acceptance_criteria":
			issue.AcceptanceCriteria = parseCriteria(sectionLines)
		case "escalation":
			issue.Escalation = text
		}
		sectionLines = nil
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush()
			name := fieldName(m[2])
			if name != "" {
				section = name
			} else {
				section = ""
				if issue.Title == "" && m[1] == "#" {
					issue.Title = strings.TrimSpace(m[2])
				}
			}
			continue
		}
		if section == "" {
			if m := fieldLineRe.FindStringSubmatch(line); m != nil {
				switch fieldName(m[1]) {
				case "outcome":
					issue.Outcome = strings.TrimSpace(m[2])
				case "constraints":
					issue.Constraints = strings.TrimSpace(m[2])
				case "acceptance_criteria":
					issue.AcceptanceCriteria = parseCriteria([]string{strings.TrimSpace(m[2])})
				case "escalation":
					issue.Escalation = strings.TrimSpace(m[2])
				}
			}
			continue
		}
		sectionLines = append(sectionLines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading issue: %w", err)
	}
	flush()
	return issue, nil
}

// fieldName maps a heading or label to a spec-gate field, or "" when it is
// not one of the four fields.
func fieldName(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "outcome":
		return "outcome"
	case "constraints":
		return "constraints"
	case "acceptance criteria", "acceptance_criteria", "acceptance":
		return "acceptance_criteria"
	case "escalation", "escalation conditions", "escalation_conditions":
		return "escalation"
	}
	return ""
}

// parseCriteria turns section lines into criteria: one per bullet, or the
// whole section text as a single criterion when there are no bullets.
func parseCriteria(lines []string) []string {
	var out []string
	var para []string
	flushPara := func() {
		if text := strings.TrimSpace(strings.Join(para, " ")); text != "" {
			out = append(out, text)
		}
		para = nil
	}
	for _, line := range lines {
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			flushPara()
			out = append(out, strings.TrimSpace(m[3]))
			continue
		}
		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}
		para = append(para, strings.TrimSpace(line))
	}
	flushPara()
	return out
}

// MissingFields lists the spec-gate fields that are missing or empty.
func (i *Issue) MissingFields() []string {
	var out []string
	if strings.TrimSpace(i.Outcome) == "" {
		out = append(out, "outcome")
	}
	if strings.TrimSpace(i.Constraints) == "" {
		out = append(out, "constraints")
	}
	if len(i.AcceptanceCriteria) == 0 {
		out = append(out, "acceptance_criteria")
	}
	if strings.TrimSpace(i.Escalation) == "" {
		out = append(out, "escalation_conditions")
	}
	return out
}

// GatePassed reports whether the issue passes the spec gate.
func (i *Issue) GatePassed() bool {
	return len(i.MissingFields()) == 0
}

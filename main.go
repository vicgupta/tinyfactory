// main.go — the tinyfactory CLI.
//
// Commands:
//
//	tinyfactory preview --issue <file.md> [--config <path>]
//	tinyfactory run --issue <file.md> [--repo owner/name] [--repo-dir <path>]
//	    [--state-dir <dir>] [--config <path>] [--dry-run]
//	tinyfactory reset --state-dir <dir>
//	tinyfactory version
//
// The token is read from TINYFACTORY_TOKEN at runtime, only for a live
// run. It is never logged, never stored, and never passed to an agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Version is the binary version; it is also the GitHub User-Agent.
const Version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "preview":
		err = cmdPreview(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "reset":
		err = cmdReset(os.Args[2:])
	case "version":
		fmt.Printf("tinyfactory %s\n", Version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "tinyfactory: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tinyfactory: %v\n", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `tinyfactory %s — one item at a time through intake → plan → generate → evaluate → deploy → learn.

Usage:
  tinyfactory preview --issue <file.md> [--config <path>]
      Intake + plan only. Prints the task breakdown, the checks and gates
      the item would run, the model routing, and the estimated cost.
      Makes no changes, opens no PR, merges nothing.

  tinyfactory run --issue <file.md> [--repo owner/name] [--repo-dir <path>]
      [--state-dir <dir>] [--config <path>] [--dry-run]
      Runs one item through the loop. Without --repo and
      TINYFACTORY_TOKEN the binary runs preview/dry-run only: a live run
      refuses before writing anything when the repo, token, local clone,
      or caps are missing. --dry-run is preview plus the live guards,
      with no writes.

  tinyfactory reset --state-dir <dir>
      Clears the consecutive-failure stop so the loop can run again.
      Keeps events.jsonl (the audit trail).

  tinyfactory version

Flags:
  --config     companion config YAML (default: ./factory-flow.yaml, else
               the one next to this binary)
  --state-dir  state.json + events.jsonl directory (default: .tinyfactory)
  --repo-dir   local checkout of the target repo (live runs only)
`, Version)
}

// resolveConfig finds the config: --config wins; otherwise
// ./factory-flow.yaml, else the file next to the binary.
func resolveConfig(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if _, err := os.Stat("factory-flow.yaml"); err == nil {
		return "factory-flow.yaml", nil
	}
	exe, err := os.Executable()
	if err == nil {
		p := filepath.Join(filepath.Dir(exe), "factory-flow.yaml")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config found: pass --config <path> or run from the tinyfactory directory")
}

func cmdPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	issuePath := fs.String("issue", "", "issue markdown file")
	configPath := fs.String("config", "", "companion config YAML")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *issuePath == "" {
		return fmt.Errorf("preview: --issue <file.md> is required")
	}
	cfgFile, err := resolveConfig(*configPath)
	if err != nil {
		return err
	}
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*issuePath)
	if err != nil {
		return fmt.Errorf("reading issue %s: %w", *issuePath, err)
	}
	issue, err := ParseIssue(data)
	if err != nil {
		return err
	}
	if issue.Title == "" {
		issue.Title = filepath.Base(*issuePath)
	}
	report, err := Preview(issue, cfg)
	if err != nil {
		return err
	}
	fmt.Println(report)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	issuePath := fs.String("issue", "", "issue markdown file")
	configPath := fs.String("config", "", "companion config YAML")
	repo := fs.String("repo", "", "target repo, owner/name (live runs only)")
	repoDir := fs.String("repo-dir", "", "local checkout of the target repo (live runs only)")
	stateDir := fs.String("state-dir", ".tinyfactory", "state.json + events.jsonl directory")
	dryRun := fs.Bool("dry-run", false, "preview plus the live guards; no writes, no PR, no merge")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *issuePath == "" {
		return fmt.Errorf("run: --issue <file.md> is required")
	}
	cfgFile, err := resolveConfig(*configPath)
	if err != nil {
		return err
	}
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		return err
	}
	r := &Runner{
		Config:  cfg,
		Git:     ExecGitRunner{},
		Agent:   ExecAgentRunner{},
		Store:   NewStore(*stateDir),
		Repo:    *repo,
		RepoDir: *repoDir,
		DryRun:  *dryRun,
		Out:     os.Stdout,
	}
	if !*dryRun && *repo != "" {
		// A live run needs the token; without it the Runner refuses
		// before writing anything. The token is never logged.
		if token := os.Getenv("TINYFACTORY_TOKEN"); token != "" {
			r.GitHub = NewGitHubClient("https://api.github.com", token)
		}
	}
	return r.Run(context.Background(), *issuePath)
}

func cmdReset(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateDir := fs.String("state-dir", ".tinyfactory", "state.json + events.jsonl directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p := filepath.Join(*stateDir, "state.json")
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("tinyfactory reset: no state to clear in %s\n", *stateDir)
			return nil
		}
		return fmt.Errorf("reset: %w", err)
	}
	fmt.Printf("tinyfactory reset: cleared %s (events.jsonl kept as the audit trail)\n", p)
	return nil
}

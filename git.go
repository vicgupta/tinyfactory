// git.go — the git operations the loop needs, via the git CLI.
//
// All writes happen on a feature branch inside a worktree; main is never
// touched directly. The only way main changes is the merge API call in
// deploy. The binary never puts a token in a git command (there is no
// clone support; live runs work from a local checkout given with
// --repo-dir).
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitRunner is the git surface the loop uses. ExecGitRunner is the real
// implementation; tests use it against throwaway repos.
type GitRunner interface {
	CreateWorktree(ctx context.Context, repoDir, worktreePath, branch, base string) error
	CommitAll(ctx context.Context, dir, message string) error
	Push(ctx context.Context, dir, remote, branch string) error
	StatusPorcelain(ctx context.Context, dir string) ([]string, error)
	CurrentBranch(ctx context.Context, dir string) (string, error)
	RevParse(ctx context.Context, dir, rev string) (string, error)
	CheckoutPaths(ctx context.Context, dir string, paths []string) error
	RevertCommit(ctx context.Context, dir, sha string) error
	Fetch(ctx context.Context, dir, remote, ref string) error
	RemoteURL(ctx context.Context, dir, remote string) (string, error)
	RemoveWorktree(ctx context.Context, dir, path string) error
	DeleteBranch(ctx context.Context, dir, branch string) error
	PruneWorktrees(ctx context.Context, dir string) error
	ListWorktrees(ctx context.Context, dir string) ([]Worktree, error)
	ListBranches(ctx context.Context, dir, pattern string) ([]string, error)
}

// ExecGitRunner runs git via os/exec.
type ExecGitRunner struct{}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	// Trim trailing whitespace only: `git status --porcelain` encodes
	// meaning in the leading columns (" M" vs "M "), so the leading
	// whitespace of the first line must survive.
	text := strings.TrimRight(string(out), " \t\r\n")
	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (ExecGitRunner) CreateWorktree(ctx context.Context, repoDir, worktreePath, branch, base string) error {
	_, err := runGit(ctx, repoDir, "worktree", "add", worktreePath, "-b", branch, base)
	return err
}

func (ExecGitRunner) CommitAll(ctx context.Context, dir, message string) error {
	if _, err := runGit(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	// Explicit identity: the loop must not depend on the user's git config.
	_, err := runGit(ctx, dir, "-c", "user.name=tinyfactory", "-c", "user.email=tinyfactory@local", "commit", "-m", message)
	return err
}

func (ExecGitRunner) Push(ctx context.Context, dir, remote, branch string) error {
	_, err := runGit(ctx, dir, "push", remote, branch)
	return err
}

func (ExecGitRunner) StatusPorcelain(ctx context.Context, dir string) ([]string, error) {
	out, err := runGit(ctx, dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (ExecGitRunner) CurrentBranch(ctx context.Context, dir string) (string, error) {
	return runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func (ExecGitRunner) RevParse(ctx context.Context, dir, rev string) (string, error) {
	return runGit(ctx, dir, "rev-parse", rev)
}

func (ExecGitRunner) CheckoutPaths(ctx context.Context, dir string, paths []string) error {
	args := append([]string{"checkout", "--"}, paths...)
	_, err := runGit(ctx, dir, args...)
	return err
}

func (ExecGitRunner) RevertCommit(ctx context.Context, dir, sha string) error {
	_, err := runGit(ctx, dir, "-c", "user.name=tinyfactory", "-c", "user.email=tinyfactory@local", "revert", "--no-edit", sha)
	return err
}

// Fetch fetches ref from remote into dir (updates remote-tracking refs).
func (ExecGitRunner) Fetch(ctx context.Context, dir, remote, ref string) error {
	_, err := runGit(ctx, dir, "fetch", remote, ref)
	return err
}

// RemoteURL returns the URL of remote in dir.
func (ExecGitRunner) RemoteURL(ctx context.Context, dir, remote string) (string, error) {
	return runGit(ctx, dir, "remote", "get-url", remote)
}

// RemoveWorktree removes the worktree at path (registered under dir).
func (ExecGitRunner) RemoveWorktree(ctx context.Context, dir, path string) error {
	_, err := runGit(ctx, dir, "worktree", "remove", "--force", path)
	return err
}

// DeleteBranch deletes a local branch in dir. The branch must not be
// checked out in any worktree.
func (ExecGitRunner) DeleteBranch(ctx context.Context, dir, branch string) error {
	_, err := runGit(ctx, dir, "branch", "-D", branch)
	return err
}

// PruneWorktrees drops stale worktree metadata in dir.
func (ExecGitRunner) PruneWorktrees(ctx context.Context, dir string) error {
	_, err := runGit(ctx, dir, "worktree", "prune")
	return err
}

// Worktree is one entry of `git worktree list --porcelain`: the
// worktree's path and the branch checked out there ("" when detached).
type Worktree struct {
	Path   string
	Branch string
}

// ListWorktrees lists the worktrees registered in dir.
func (ExecGitRunner) ListWorktrees(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := runGit(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			wts = append(wts, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case strings.HasPrefix(line, "branch ") && cur != nil:
			cur.Branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
		case strings.TrimSpace(line) == "":
			flush()
		}
	}
	flush()
	return wts, nil
}

// ListBranches lists local branches in dir matching pattern (a glob
// such as "tinyfactory/foo-*").
func (ExecGitRunner) ListBranches(ctx context.Context, dir, pattern string) ([]string, error) {
	out, err := runGit(ctx, dir, "branch", "--format=%(refname:short)", "--list", pattern)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// XY is the two-column status (" M", "??", "R ", ...); Path is the
// (new) path, with rename arrows already resolved.
type StatusEntry struct {
	XY   string
	Path string
}

// ParseStatus parses `git status --porcelain` lines into entries. Lines
// too short to hold a status and a path are skipped.
func ParseStatus(lines []string) []StatusEntry {
	var out []StatusEntry
	for _, line := range lines {
		if len(line) < 4 {
			continue
		}
		paths := ChangedPaths([]string{line})
		if len(paths) == 0 {
			continue
		}
		out = append(out, StatusEntry{XY: line[:2], Path: paths[0]})
	}
	return out
}

// ChangedPaths extracts the file paths from `git status --porcelain`
// lines. Rename entries ("old -> new") yield the new path.
func ChangedPaths(status []string) []string {
	var out []string
	for _, line := range status {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, `"`)
		out = append(out, path)
	}
	return out
}

// IsProtectedPath reports whether path falls under one of the protected
// entries. An entry ending in "/" is a directory prefix; anything else is
// an exact path match.
func IsProtectedPath(path string, protected []string) bool {
	for _, entry := range protected {
		if strings.HasSuffix(entry, "/") {
			if strings.HasPrefix(path, entry) {
				return true
			}
			continue
		}
		if path == entry {
			return true
		}
	}
	return false
}

// Slugify turns an issue title into a safe branch-name fragment.
func Slugify(title string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "item"
	}
	return slug
}

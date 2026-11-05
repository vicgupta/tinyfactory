// Package main implements tinyfactory's GitHub REST client.
//
// The client talks to the GitHub REST API (or any compatible base URL)
// using only the standard library. It performs no retries, touches no
// endpoint other than the four methods declared here, and never includes
// the token in any error string or log output.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GitHubClient is a minimal GitHub REST API client.
type GitHubClient struct {
	BaseURL    string // e.g. "https://api.github.com", no trailing slash
	Token      string // never logged, never included in any error string
	HTTPClient *http.Client
	UserAgent  string
}

// NewGitHubClient returns a client for baseURL authenticated with token.
// The default User-Agent is "tinyfactory/0.1.0" (applied at request time
// when UserAgent is empty). A trailing slash on baseURL is trimmed.
func NewGitHubClient(baseURL, token string) *GitHubClient {
	return &GitHubClient{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		UserAgent: "tinyfactory/0.1.0",
	}
}

// Repo is the subset of a GitHub repository object tinyfactory needs.
// AllowMergeCommit is a pointer because the field is absent from some
// API responses; nil means "unknown", not "allowed".
type Repo struct {
	Name             string `json:"name"`
	FullName         string `json:"full_name"`
	DefaultBranch    string `json:"default_branch"`
	Private          bool   `json:"private"`
	AllowMergeCommit *bool  `json:"allow_merge_commit"`
}

// PullRequest is the subset of a GitHub pull request object tinyfactory needs.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Head    struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

// CheckRun is one check run on a commit.
type CheckRun struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
}

// CheckRuns is the list response for GET .../commits/{sha}/check-runs.
type CheckRuns struct {
	TotalCount int        `json:"total_count"`
	Runs       []CheckRun `json:"check_runs"`
}

func (c *GitHubClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *GitHubClient) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "tinyfactory/0.1.0"
}

// statusError is the error returned for a non-2xx GitHub response. It
// carries the status code and the (already truncated) response body so
// callers can distinguish specific statuses such as 405.
type statusError struct {
	StatusCode int
	Method     string
	Path       string
	Body       []byte
}

func (e *statusError) Error() string {
	return fmt.Sprintf("github %s %s: unexpected status %d: %s", e.Method, e.Path, e.StatusCode, snippet(e.Body))
}

// do performs a single JSON request against the GitHub API and decodes a
// successful (2xx) response body into out (when out is non-nil). On a
// non-2xx response it returns a *statusError carrying the HTTP status
// code, the request method and path, and at most 200 chars of the
// response body. The token is never included in any error.
func (c *GitHubClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github %s %s: marshal request body: %w", method, path, err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("github %s %s: build request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("github %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return fmt.Errorf("github %s %s: read response body: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: respBody}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("github %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// snippet returns at most the first 200 bytes of b as a string.
func snippet(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// GetRepo fetches GET /repos/{owner}/{repo}.
func (c *GitHubClient) GetRepo(ctx context.Context, owner, repo string) (*Repo, error) {
	var r Repo
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	if err := c.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreatePullRequest creates a pull request via POST /repos/{owner}/{repo}/pulls.
// A 422 (e.g. a PR already exists for the head branch) is reported as an
// error carrying the status code and a body snippet.
func (c *GitHubClient) CreatePullRequest(ctx context.Context, owner, repo, title, head, base, body string) (*PullRequest, error) {
	var pr PullRequest
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	payload := map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	if err := c.do(ctx, http.MethodPost, path, payload, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// MergePullRequest merges a pull request with a merge commit via
// POST /repos/{owner}/{repo}/pulls/{number}/merge with
// {"merge_method": "merge"}.
//
// Only the "merge" method is permitted: any other value is rejected
// before any HTTP request is made. A 200/201 response with
// {"merged": true} returns (true, nil). A 405 response means the
// repository disallows merge commits; it is reported as a blocking
// error and never silently falls back to another merge method.
func (c *GitHubClient) MergePullRequest(ctx context.Context, owner, repo string, number int, method string) (bool, error) {
	if method != "merge" {
		return false, fmt.Errorf("merge method %q is forbidden: only \"merge\" (merge commit) is allowed", method)
	}

	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number)
	payload := map[string]string{"merge_method": method}

	var resp struct {
		Merged bool `json:"merged"`
	}
	if err := c.do(ctx, http.MethodPost, path, payload, &resp); err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusMethodNotAllowed {
			return false, fmt.Errorf("github POST %s: merge commits are disallowed on this repository (HTTP 405); the run is blocked — merge method must remain \"merge\" and no other method will be attempted: %s", path, snippet(statusErr.Body))
		}
		return false, err
	}
	return resp.Merged, nil
}

// CheckRunsForCommit fetches GET /repos/{owner}/{repo}/commits/{sha}/check-runs.
func (c *GitHubClient) CheckRunsForCommit(ctx context.Context, owner, repo, sha string) (*CheckRuns, error) {
	var runs CheckRuns
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, sha)
	if err := c.do(ctx, http.MethodGet, path, nil, &runs); err != nil {
		return nil, err
	}
	return &runs, nil
}

// AllChecksGreen reports whether every check run in runs passed.
// It is false when runs is nil, when TotalCount is 0, or when any run
// is not completed or has a conclusion other than success, neutral, or
// skipped. It is true only when every run is completed with a passing
// conclusion.
func AllChecksGreen(runs *CheckRuns) bool {
	if runs == nil || runs.TotalCount == 0 || len(runs.Runs) == 0 {
		return false
	}
	for _, run := range runs.Runs {
		if run.Status != "completed" {
			return false
		}
		if run.Conclusion == nil {
			return false
		}
		switch *run.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return false
		}
	}
	return true
}

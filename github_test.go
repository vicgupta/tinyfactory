package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "test-token-abc123"

func strptr(s string) *string { return &s }

// fakeGitHub spins up an httptest server that records requests and serves
// handler. It asserts the standard GitHub headers on every request and
// fails the test if the token ever appears in a response body (the client
// must never echo it back in an error).
func fakeGitHub(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *int64) {
	t.Helper()
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer "+testToken)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want %q", got, "application/vnd.github+json")
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version header = %q, want %q", got, "2022-11-28")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func newTestClient(srv *httptest.Server) *GitHubClient {
	return NewGitHubClient(srv.URL, testToken)
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return b
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error contains the token: %v", err)
	}
}

func TestNewGitHubClient(t *testing.T) {
	c := NewGitHubClient("https://api.github.com/", testToken)
	if c.BaseURL != "https://api.github.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", c.BaseURL)
	}
	if c.UserAgent != "tinyfactory/0.1.0" {
		t.Errorf("UserAgent = %q, want %q", c.UserAgent, "tinyfactory/0.1.0")
	}
	// Default User-Agent is applied when UserAgent is empty.
	c2 := &GitHubClient{BaseURL: "https://api.github.com", Token: testToken}
	if got := c2.userAgent(); got != "tinyfactory/0.1.0" {
		t.Errorf("userAgent() = %q, want %q", got, "tinyfactory/0.1.0")
	}
}

func TestGetRepoSuccess(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/repos/octocat/hello-world" {
			t.Errorf("path = %s, want /repos/octocat/hello-world", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"name": "hello-world", "full_name": "octocat/hello-world",
			"default_branch": "main", "private": false,
			"allow_merge_commit": true,
		})
	})
	c := newTestClient(srv)
	repo, err := c.GetRepo(context.Background(), "octocat", "hello-world")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if repo.Name != "hello-world" || repo.FullName != "octocat/hello-world" ||
		repo.DefaultBranch != "main" || repo.Private {
		t.Errorf("unexpected repo: %+v", repo)
	}
	if repo.AllowMergeCommit == nil || !*repo.AllowMergeCommit {
		t.Errorf("AllowMergeCommit = %v, want true", repo.AllowMergeCommit)
	}
}

func TestGetRepoMergeCommitDisallowed(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"name": "hello-world", "full_name": "octocat/hello-world",
			"default_branch": "main", "private": false,
			"allow_merge_commit": false,
		})
	})
	c := newTestClient(srv)
	repo, err := c.GetRepo(context.Background(), "octocat", "hello-world")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if repo.AllowMergeCommit == nil || *repo.AllowMergeCommit {
		t.Errorf("AllowMergeCommit = %v, want false (pointer to false)", repo.AllowMergeCommit)
	}
}

func TestGetRepoNotFound(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	})
	c := newTestClient(srv)
	_, err := c.GetRepo(context.Background(), "octocat", "nope")
	if err == nil {
		t.Fatal("GetRepo: expected error for 404, got nil")
	}
	assertNoToken(t, err)
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status 404: %v", err)
	}
	if !strings.Contains(err.Error(), "/repos/octocat/nope") {
		t.Errorf("error should mention method+path: %v", err)
	}
}

func TestCreatePullRequestSuccess(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/repos/octocat/hello-world/pulls" {
			t.Errorf("path = %s, want /repos/octocat/hello-world/pulls", r.URL.Path)
		}
		var payload map[string]string
		if err := json.Unmarshal(readBody(t, r), &payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload["title"] != "Add feature" || payload["head"] != "feature/x" ||
			payload["base"] != "main" || payload["body"] != "Does the thing." {
			t.Errorf("unexpected request body: %v", payload)
		}
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"number": 42, "html_url": "https://github.com/octocat/hello-world/pull/42",
			"state": "open", "merged": false,
			"head": map[string]any{"sha": "abc123", "ref": "feature/x"},
		})
	})
	c := newTestClient(srv)
	pr, err := c.CreatePullRequest(context.Background(), "octocat", "hello-world", "Add feature", "feature/x", "main", "Does the thing.")
	if err != nil {
		t.Fatalf("CreatePullRequest: %v", err)
	}
	if pr.Number != 42 || pr.HTMLURL != "https://github.com/octocat/hello-world/pull/42" ||
		pr.State != "open" || pr.Merged || pr.Head.SHA != "abc123" || pr.Head.Ref != "feature/x" {
		t.Errorf("unexpected PR: %+v", pr)
	}
}

func TestCreatePullRequestUnauthorized(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials"})
	})
	c := newTestClient(srv)
	_, err := c.CreatePullRequest(context.Background(), "octocat", "hello-world", "t", "h", "main", "b")
	if err == nil {
		t.Fatal("CreatePullRequest: expected error for 401, got nil")
	}
	assertNoToken(t, err)
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status 401: %v", err)
	}
}

func TestCreatePullRequestAlreadyExists(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnprocessableEntity, map[string]any{
			"message": "Validation Failed",
			"errors":  []map[string]any{{"message": "A pull request already exists for octocat:feature/x."}},
		})
	})
	c := newTestClient(srv)
	_, err := c.CreatePullRequest(context.Background(), "octocat", "hello-world", "t", "feature/x", "main", "b")
	if err == nil {
		t.Fatal("CreatePullRequest: expected error for 422, got nil")
	}
	assertNoToken(t, err)
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error should mention status 422: %v", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should include body snippet: %v", err)
	}
}

func TestMergePullRequestSuccess(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/repos/octocat/hello-world/pulls/42/merge" {
			t.Errorf("path = %s, want /repos/octocat/hello-world/pulls/42/merge", r.URL.Path)
		}
		var payload map[string]string
		if err := json.Unmarshal(readBody(t, r), &payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload["merge_method"] != "merge" {
			t.Errorf("merge_method = %q, want %q", payload["merge_method"], "merge")
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"merged": true, "message": "Pull Request successfully merged"})
	})
	c := newTestClient(srv)
	merged, err := c.MergePullRequest(context.Background(), "octocat", "hello-world", 42, "merge")
	if err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if !merged {
		t.Error("merged = false, want true")
	}
}

func TestMergePullRequestMethodNotAllowed(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusMethodNotAllowed, map[string]any{"message": "Merge commits are not allowed on this repository."})
	})
	c := newTestClient(srv)
	merged, err := c.MergePullRequest(context.Background(), "octocat", "hello-world", 42, "merge")
	if err == nil {
		t.Fatal("MergePullRequest: expected error for 405, got nil")
	}
	if merged {
		t.Error("merged = true, want false on error")
	}
	assertNoToken(t, err)
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "merge commit") || !strings.Contains(lower, "disallow") {
		t.Errorf("error should say merge commits are disallowed: %v", err)
	}
	if !strings.Contains(lower, "blocked") {
		t.Errorf("error should say the run is blocked: %v", err)
	}
}

func TestMergePullRequestForbiddenMethods(t *testing.T) {
	for _, method := range []string{"squash", "rebase"} {
		t.Run(method, func(t *testing.T) {
			srv, count := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]any{"merged": true})
			})
			c := newTestClient(srv)
			merged, err := c.MergePullRequest(context.Background(), "octocat", "hello-world", 42, method)
			if err == nil {
				t.Fatalf("MergePullRequest(%q): expected error, got nil", method)
			}
			if merged {
				t.Errorf("MergePullRequest(%q): merged = true, want false", method)
			}
			assertNoToken(t, err)
			if !strings.Contains(err.Error(), `"merge"`) {
				t.Errorf("error should name the only allowed method: %v", err)
			}
			if got := atomic.LoadInt64(count); got != 0 {
				t.Errorf("fake server saw %d request(s), want 0: %q must error before any HTTP call", got, method)
			}
		})
	}
}

func TestCheckRunsForCommitSuccess(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/repos/octocat/hello-world/commits/deadbeef/check-runs" {
			t.Errorf("path = %s, want /repos/octocat/hello-world/commits/deadbeef/check-runs", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"total_count": 2,
			"check_runs": []map[string]any{
				{"name": "build", "status": "completed", "conclusion": "success"},
				{"name": "lint", "status": "completed", "conclusion": "neutral"},
			},
		})
	})
	c := newTestClient(srv)
	runs, err := c.CheckRunsForCommit(context.Background(), "octocat", "hello-world", "deadbeef")
	if err != nil {
		t.Fatalf("CheckRunsForCommit: %v", err)
	}
	if runs.TotalCount != 2 || len(runs.Runs) != 2 {
		t.Fatalf("unexpected runs: %+v", runs)
	}
	if runs.Runs[0].Name != "build" || runs.Runs[0].Conclusion == nil || *runs.Runs[0].Conclusion != "success" {
		t.Errorf("unexpected first run: %+v", runs.Runs[0])
	}
	if !AllChecksGreen(runs) {
		t.Error("AllChecksGreen = false for two completed passing runs, want true")
	}
}

func TestCheckRunsForCommitServerError(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	c := newTestClient(srv)
	_, err := c.CheckRunsForCommit(context.Background(), "octocat", "hello-world", "deadbeef")
	if err == nil {
		t.Fatal("CheckRunsForCommit: expected error for 500, got nil")
	}
	assertNoToken(t, err)
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500: %v", err)
	}
}

func TestAllChecksGreen(t *testing.T) {
	cases := []struct {
		name string
		runs *CheckRuns
		want bool
	}{
		{"nil", nil, false},
		{"empty", &CheckRuns{TotalCount: 0, Runs: nil}, false},
		{"all success", &CheckRuns{TotalCount: 2, Runs: []CheckRun{
			{Name: "a", Status: "completed", Conclusion: strptr("success")},
			{Name: "b", Status: "completed", Conclusion: strptr("success")},
		}}, true},
		{"neutral and skipped pass", &CheckRuns{TotalCount: 2, Runs: []CheckRun{
			{Name: "a", Status: "completed", Conclusion: strptr("neutral")},
			{Name: "b", Status: "completed", Conclusion: strptr("skipped")},
		}}, true},
		{"one failure", &CheckRuns{TotalCount: 2, Runs: []CheckRun{
			{Name: "a", Status: "completed", Conclusion: strptr("success")},
			{Name: "b", Status: "completed", Conclusion: strptr("failure")},
		}}, false},
		{"one in progress", &CheckRuns{TotalCount: 2, Runs: []CheckRun{
			{Name: "a", Status: "completed", Conclusion: strptr("success")},
			{Name: "b", Status: "in_progress", Conclusion: nil},
		}}, false},
		{"one queued", &CheckRuns{TotalCount: 1, Runs: []CheckRun{
			{Name: "a", Status: "queued", Conclusion: nil},
		}}, false},
		{"nil conclusion", &CheckRuns{TotalCount: 1, Runs: []CheckRun{
			{Name: "a", Status: "completed", Conclusion: nil},
		}}, false},
		{"cancelled", &CheckRuns{TotalCount: 1, Runs: []CheckRun{
			{Name: "a", Status: "completed", Conclusion: strptr("cancelled")},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllChecksGreen(tc.runs); got != tc.want {
				t.Errorf("AllChecksGreen(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestErrorBodyTruncatedTo200Chars(t *testing.T) {
	long := strings.Repeat("x", 500)
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(long))
	})
	c := newTestClient(srv)
	_, err := c.GetRepo(context.Background(), "octocat", "hello-world")
	if err == nil {
		t.Fatal("GetRepo: expected error for 502, got nil")
	}
	assertNoToken(t, err)
	if strings.Contains(err.Error(), long) {
		t.Error("error contains the full 500-char body; body must be truncated to 200 chars")
	}
	if got := len(err.Error()); got > 400 {
		t.Errorf("error unexpectedly long (%d chars); body snippet should be capped at 200", got)
	}
}

func TestUserAgentHeader(t *testing.T) {
	srv, _ := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "tinyfactory/0.1.0" {
			t.Errorf("User-Agent = %q, want %q", got, "tinyfactory/0.1.0")
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"name": "x"})
	})
	c := newTestClient(srv)
	// Client with an empty UserAgent field still sends the default.
	c.UserAgent = ""
	if _, err := c.GetRepo(context.Background(), "o", "r"); err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
}

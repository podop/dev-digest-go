package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// Wiring tests of the PR leaderboard (plan S18'): the route is registered and the background poll
// starts only with PR_POLL_ENABLED. Contract goldens and the poll's behaviour are W4's.

// FR6, FR3: GET /leaderboard is served by serve, with the empty shape when no PR falls into the
// window (the clock is years after the seeded data), 422 for an unknown period and 404 for a
// repository that does not exist.
func TestLeaderboard_RouteIsWired(t *testing.T) {
	s := startStack(t, spec{now: func() time.Time { return time.Date(2031, 1, 15, 12, 0, 0, 0, time.UTC) }})

	resp := s.get("/leaderboard?period=30d")
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /leaderboard?period=30d = %d %s, want 200", resp.Status, resp.Body)
	}
	body := resp.Obj(t)
	for _, key := range []string{"period", "repos", "summary", "previous_summary", "awards", "rows", "needs_attention", "hot_files", "health_by_size", "activity"} {
		if _, ok := body[key]; !ok {
			t.Errorf("GET /leaderboard?period=30d has no %q key: %s", key, resp.Body)
		}
	}
	if rows, ok := body["rows"].([]any); !ok || len(rows) != 0 {
		t.Errorf("GET /leaderboard?period=30d rows = %v, want an empty list", body["rows"])
	}
	if body["previous_summary"] != nil {
		t.Errorf("GET /leaderboard?period=30d previous_summary = %v, want null", body["previous_summary"])
	}

	if bogus := s.get("/leaderboard?period=bogus"); bogus.Status != http.StatusUnprocessableEntity || bogus.errorCode(t) != "validation_error" {
		t.Errorf("GET /leaderboard?period=bogus = %d %s, want 422 validation_error", bogus.Status, bogus.Body)
	}
	const unknown = "6f1b1c1e-3b0a-4c43-9a43-0c6f3d2a9f11"
	if missing := s.get("/leaderboard?repo=" + unknown); missing.Status != http.StatusNotFound || missing.errorCode(t) != "not_found" {
		t.Errorf("GET /leaderboard?repo=%s = %d %s, want 404 not_found", unknown, missing.Status, missing.Body)
	}
}

// pollSpec returns a stack spec over a database that holds one repository o/r (served by the
// GitHub stub) and no seed, with a GitHub token and PR_POLL_ENABLED as given.
func pollSpec(t *testing.T, enabled bool) (spec, string) {
	t.Helper()
	db := pgtest.Migrated(t)
	ws := scalar[string](t, db.DB, `INSERT INTO workspaces (name) VALUES ('poll') RETURNING id::text`)
	repo := scalar[string](t, db.DB,
		`INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'o', 'r', 'o/r') RETURNING id::text`, ws)
	return spec{
		db:     db,
		noSeed: true,
		// The stub's PR #7 was updated on 2026-10-20: a fixed clock keeps it inside the poller's 62-day window.
		now:    func() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) },
		env:    map[string]string{"GITHUB_TOKEN": "poll-token"},
		config: func(c *config.Config) { c.PRPollEnabled = enabled },
	}, repo
}

func listRequests(s *stack) []ghRequest {
	var out []ghRequest
	for _, r := range s.gh.requests() {
		if r.Method == http.MethodGet && r.Path == "/repos/o/r/pulls" {
			out = append(out, r)
		}
	}
	return out
}

// FR3: with PR_POLL_ENABLED the poller reads every repository once at start, with the stored
// GitHub token, stores its PRs and marks the repository polled.
func TestPoll_StartsWhenEnabled(t *testing.T) {
	sp, repo := pollSpec(t, true)
	s := startStack(t, sp)

	eventually(t, 30*time.Second, "the repository to be marked polled", func() bool {
		return scalar[int](t, s.db.DB, `SELECT count(*) FROM repos WHERE id = ? AND last_polled_at IS NOT NULL`, repo) == 1
	})
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ? AND number = 7`, repo); n != 1 {
		t.Errorf("stored pull requests #7 of the polled repository = %d, want 1", n)
	}
	// FR24: the same round fills the stats of the stored PR with one request that carries the token.
	eventually(t, 30*time.Second, "the diff stats of PR #7 to be filled", func() bool {
		return scalar[int](t, s.db.DB, `SELECT count(*) FROM pull_requests WHERE repo_id = ? AND number = 7 AND additions = 10 AND deletions = 2 AND files_count = 2`, repo) == 1
	})
	var statRequests []ghRequest
	for _, r := range s.gh.requests() {
		if strings.HasPrefix(r.Path, "/repos/o/r/pulls/") {
			statRequests = append(statRequests, r)
		}
	}
	if len(statRequests) != 1 || statRequests[0].Path != "/repos/o/r/pulls/7" || statRequests[0].Auth != "Bearer poll-token" {
		t.Errorf("GitHub requests below /repos/o/r/pulls/ = %+v, want one GET of /pulls/7 with the poll token", statRequests)
	}
	reqs := listRequests(s)
	if len(reqs) == 0 {
		t.Fatalf("the GitHub stub saw no list request for o/r: %v", s.gh.requests())
	}
	if want := "Bearer poll-token"; reqs[0].Auth != want {
		t.Errorf("poll request Authorization = %q, want %q", reqs[0].Auth, want)
	}
	if got := logLines(t, s.logs, "pull request poller started"); len(got) != 1 {
		t.Errorf("log lines %q = %d, want 1", "pull request poller started", len(got))
	}
	if err := s.stop(); err != nil {
		t.Errorf("stop after polling = %v, want nil", err)
	}
}

// FR3: with PR_POLL_ENABLED=false nothing is polled and the start is logged as disabled.
func TestPoll_DisabledStartsNothing(t *testing.T) {
	sp, repo := pollSpec(t, false)
	s := startStack(t, sp)
	if err := s.stop(); err != nil {
		t.Fatalf("stop = %v, want nil", err)
	}

	lines := logLines(t, s.logs, "pull request poller disabled (PR_POLL_ENABLED=false)")
	if len(lines) != 1 {
		t.Errorf("disabled log lines = %d, want 1\nlogs:\n%s", len(lines), s.logs.String())
	}
	if strings.Contains(s.logs.String(), "pull request poller started") {
		t.Errorf("logs say the poller started although it is disabled:\n%s", s.logs.String())
	}
	if reqs := listRequests(s); len(reqs) != 0 {
		t.Errorf("the GitHub stub saw %d list requests with the poller disabled, want 0", len(reqs))
	}
	if n := scalar[int](t, s.db.DB, `SELECT count(*) FROM repos WHERE id = ? AND last_polled_at IS NOT NULL`, repo); n != 0 {
		t.Errorf("repos marked polled = %d with the poller disabled, want 0", n)
	}
}

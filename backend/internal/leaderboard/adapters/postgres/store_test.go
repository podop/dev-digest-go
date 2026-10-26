package postgres_test

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"dev-digest-go/backend/internal/leaderboard/adapters/postgres"
	"dev-digest-go/backend/internal/leaderboard/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ptr[T any](v T) *T { return &v }

var (
	from = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to   = from.Add(14 * 24 * time.Hour)
	day  = 24 * time.Hour
)

// world is a migrated database with two workspaces. Workspace ws holds repoA (acme/api), repoB
// (acme/web) and the agents alpha, beta, doomed (deleted by the fixture); other holds repoX.
// Pull requests (by number, repoA unless noted), window [from, to):
//
//	1  open, draft=false; reviews: alpha before the range, alpha at from, alpha at from+3d,
//	   beta at from+1d, two reviews of the deleted agent, a summary review; runs of every status
//	2  review exactly at `to` (out of the range), nothing else
//	3  merged inside the range, reviewed once before the range (two open criticals, one dismissed)
//	4  opened exactly at `to` and merged one millisecond before `from` (out on both sides)
//	5  opened exactly at `from`, draft, status needs_review
//	6  only a review of kind summary in the range
//	7  merged without merged_at (status merged, NULL)
//	8  closed, reviewed in the range (one open critical)
//
// repoB #1 has a review in the range; repoX #1 (other workspace) too.
type world struct {
	db                       *gorm.DB
	store                    *postgres.Store
	ws, other                string
	repoA, repoB, repoX      string
	pr                       map[int]string // repoA pull requests by number
	prB, prX                 string
	alpha, beta, doomedAgent string
	reviews                  map[string]string // fixture name -> review id
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := pgtest.Migrated(t)
	w := &world{db: d.DB, store: postgres.NewStore(d.DB), pr: map[int]string{}, reviews: map[string]string{}}
	w.ws = w.insert(t, `INSERT INTO workspaces (name) VALUES ('default') RETURNING id`)
	w.other = w.insert(t, `INSERT INTO workspaces (name) VALUES ('other') RETURNING id`)
	w.repoA = w.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name, last_polled_at) VALUES (?, 'acme', 'api', 'acme/api', ?) RETURNING id`, w.ws, from)
	w.repoB = w.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'web', 'acme/web') RETURNING id`, w.ws)
	w.repoX = w.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'zed', 'x', 'zed/x') RETURNING id`, w.other)
	w.alpha = w.agent(t, w.ws, "alpha")
	w.beta = w.agent(t, w.ws, "beta")
	w.doomedAgent = w.agent(t, w.ws, "doomed")
	gamma := w.agent(t, w.other, "gamma")

	opened := from.Add(-20 * day)
	w.pr[1] = w.pull(t, w.repoA, 1, "open", pullSpec{opened: &opened, sha: "head1", reviewedSHA: ptr("old1")})
	w.pr[2] = w.pull(t, w.repoA, 2, "needs_review", pullSpec{})
	w.pr[3] = w.pull(t, w.repoA, 3, "merged", pullSpec{opened: &opened, merged: ptr(from.Add(5 * day)), closed: ptr(from.Add(5 * day))})
	w.pr[4] = w.pull(t, w.repoA, 4, "merged", pullSpec{opened: ptr(to), merged: ptr(from.Add(-time.Millisecond))})
	w.pr[5] = w.pull(t, w.repoA, 5, "needs_review", pullSpec{opened: ptr(from), draft: true, adds: 10, dels: 5, files: 2})
	w.pr[6] = w.pull(t, w.repoA, 6, "reviewed", pullSpec{})
	w.pr[7] = w.pull(t, w.repoA, 7, "merged", pullSpec{})
	w.pr[8] = w.pull(t, w.repoA, 8, "closed", pullSpec{closed: ptr(from.Add(day))})
	w.prB = w.pull(t, w.repoB, 1, "open", pullSpec{})
	w.prX = w.pull(t, w.repoX, 1, "open", pullSpec{})

	// PR 1: the reviews. Findings: severity, kind, state.
	w.review(t, "alphaOld", w.pr[1], &w.alpha, "review", from.Add(-10*day), ptr(40),
		fnd("CRITICAL", "finding", "pending", "old.go"), fnd("CRITICAL", "finding", "pending", "old.go"))
	w.review(t, "alpha1", w.pr[1], &w.alpha, "review", from, ptr(50), fnd("CRITICAL", "finding", "pending", "a.go"))
	w.review(t, "alpha2", w.pr[1], &w.alpha, "review", from.Add(3*day), ptr(80),
		fnd("CRITICAL", "finding", "pending", "a.go"), fnd("CRITICAL", "finding", "accepted", "b.go"),
		fnd("CRITICAL", "finding", "dismissed", "b.go"), fnd("CRITICAL", "finding", "both", "b.go"),
		fnd("WARNING", "finding", "pending", "b.go"))
	w.review(t, "beta", w.pr[1], &w.beta, "review", from.Add(day), nil, fnd("CRITICAL", "secret_leak", "pending", "s.go"))
	w.review(t, "doomed1", w.pr[1], &w.doomedAgent, "review", from.Add(2*day), ptr(30), fnd("CRITICAL", "finding", "pending", "d.go"))
	w.review(t, "doomed2", w.pr[1], &w.doomedAgent, "review", from.Add(2*day+time.Hour), ptr(60), fnd("CRITICAL", "lethal_trifecta", "pending", "d.go"))
	w.review(t, "summary", w.pr[1], &w.alpha, "summary", from.Add(4*day), nil, fnd("CRITICAL", "finding", "pending", "sum.go"))
	w.exec(t, `DELETE FROM agents WHERE id = ?`, w.doomedAgent)

	w.review(t, "mergedBefore", w.pr[3], &w.alpha, "review", from.Add(-3*day), ptr(20),
		fnd("CRITICAL", "finding", "pending", "m.go"), fnd("CRITICAL", "finding", "pending", "m.go"),
		fnd("CRITICAL", "finding", "dismissed", "m.go"))
	w.review(t, "atTo", w.pr[2], &w.alpha, "review", to, ptr(70), fnd("WARNING", "finding", "pending", "t.go"))
	w.review(t, "onlySummary", w.pr[6], &w.alpha, "summary", from.Add(day), nil)
	w.review(t, "closed", w.pr[8], &w.beta, "review", from.Add(2*day), ptr(90), fnd("CRITICAL", "finding", "pending", "c.go"))
	w.review(t, "repoB", w.prB, &w.alpha, "review", from.Add(day), ptr(10), fnd("SUGGESTION", "finding", "pending", "w.go"))
	w.review(t, "repoX", w.prX, &gamma, "review", from.Add(day), ptr(10), fnd("CRITICAL", "finding", "pending", "x.go"))

	// Runs.
	w.run(t, w.ws, w.pr[1], from, "done", "local", ptr("0.250000"))
	w.run(t, w.ws, w.pr[1], from.Add(day), "failed", "ci", nil)
	w.run(t, w.ws, w.pr[1], from.Add(2*day), "running", "local", ptr("1"))
	w.run(t, w.ws, w.pr[1], to, "done", "local", ptr("9"))                          // out: at to
	w.run(t, w.ws, w.pr[1], from.Add(-time.Millisecond), "done", "local", ptr("9")) // out: before from
	w.run(t, w.ws, "", from.Add(day), "done", "ci", ptr("9"))                       // no PR: absent
	w.run(t, w.ws, w.prB, from.Add(day), "cancelled", "local", ptr("0.5"))
	w.run(t, w.other, w.prX, from.Add(day), "done", "local", ptr("7"))
	return w
}

func (w *world) insert(t *testing.T, query string, args ...any) string {
	t.Helper()
	var id string
	if err := w.db.Raw(query, args...).Scan(&id).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	return id
}

func (w *world) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := w.db.Exec(query, args...).Error; err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

func (w *world) agent(t *testing.T, ws, name string) string {
	t.Helper()
	return w.insert(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt) VALUES (?, ?, 'openai', 'gpt', 'p') RETURNING id`, ws, name)
}

type pullSpec struct {
	opened, merged, closed *time.Time
	draft                  bool
	sha                    string
	reviewedSHA            *string
	adds, dels, files      int
}

func (w *world) pull(t *testing.T, repo string, number int, status string, s pullSpec) string {
	t.Helper()
	ws := w.ws
	if repo == w.repoX {
		ws = w.other
	}
	if s.sha == "" {
		s.sha = "sha"
	}
	return w.insert(t, `INSERT INTO pull_requests (workspace_id, repo_id, number, title, author, branch, base, head_sha, last_reviewed_sha,
			additions, deletions, files_count, status, opened_at, updated_at, merged_at, closed_at, draft)
		VALUES (?, ?, ?, ?, 'dev', 'feat', 'main', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		ws, repo, number, "title "+status, s.sha, s.reviewedSHA, s.adds, s.dels, s.files, status, s.opened, s.opened, s.merged, s.closed, s.draft)
}

type finding struct{ severity, kind, state, file string }

func fnd(severity, kind, state, file string) finding { return finding{severity, kind, state, file} }

func (w *world) review(t *testing.T, name, pr string, agent *string, kind string, at time.Time, score *int, fs ...finding) {
	t.Helper()
	ws := w.ws
	if pr == w.prX {
		ws = w.other
	}
	id := w.insert(t, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, score, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		ws, pr, agent, kind, score, at)
	w.reviews[name] = id
	for i, f := range fs {
		var accepted, dismissed *time.Time
		switch f.state {
		case "accepted":
			accepted = &from
		case "dismissed":
			dismissed = &from
		case "both":
			accepted, dismissed = &from, &from
		}
		w.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence,
				kind, accepted_at, dismissed_at)
			VALUES (?, ?, ?, ?, ?, 'bug', ?, 'r', 0.5, ?, ?, ?)`,
			id, f.file, i+1, i+2, f.severity, "t "+f.file, f.kind, accepted, dismissed)
	}
}

func (w *world) run(t *testing.T, ws, pr string, at time.Time, status, source string, cost *string) {
	t.Helper()
	var prID *string
	if pr != "" {
		prID = &pr
	}
	w.exec(t, `INSERT INTO agent_runs (workspace_id, pr_id, ran_at, status, source, cost_usd) VALUES (?, ?, ?, ?, ?, ?::numeric)`,
		ws, prID, at, status, source, cost)
}

func numbers(pulls []domain.PullFact) []string {
	var out []string
	for _, p := range pulls {
		out = append(out, p.Repo+"#"+itoa(p.Number))
	}
	sort.Strings(out)
	return out
}

// win is a window without a previous one over [from, to): the whole range is the current window.
func win(from, to time.Time) domain.Window { return domain.Window{From: from, To: to} }

func itoa(n int) string { return string(rune('0' + n)) }

func TestRepoScope(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	t.Run("all: ordered by full name, workspace only", func(t *testing.T) {
		got, err := w.store.RepoScope(ctx, w.ws, domain.RepoAll)
		if err != nil {
			t.Fatalf("RepoScope(all) error = %v", err)
		}
		want := []domain.RepoRef{
			{ID: w.repoA, Owner: "acme", Name: "api", FullName: "acme/api", LastPolledAt: ptr(from)},
			{ID: w.repoB, Owner: "acme", Name: "web", FullName: "acme/web"},
		}
		if diff := cmp.Diff(want, got, cmpopts.EquateApproxTime(0)); diff != "" {
			t.Errorf("RepoScope(all) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("all: byte order of the full name, independent of the database locale", func(t *testing.T) {
		ws := w.insert(t, `INSERT INTO workspaces (name) VALUES ('collation') RETURNING id`)
		for _, name := range []string{"api", "Zed", "_x", "Beta"} {
			w.exec(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', ?, ?)`, ws, name, "acme/"+name)
		}
		got, err := w.store.RepoScope(ctx, ws, domain.RepoAll)
		if err != nil {
			t.Fatalf("RepoScope(all) error = %v", err)
		}
		var names []string
		for _, r := range got {
			names = append(names, r.FullName)
		}
		want := []string{"acme/Beta", "acme/Zed", "acme/_x", "acme/api"}
		if diff := cmp.Diff(want, names); diff != "" {
			t.Errorf("RepoScope(all) full names mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("all: a workspace without repos is empty, not an error", func(t *testing.T) {
		empty := w.insert(t, `INSERT INTO workspaces (name) VALUES ('empty') RETURNING id`)
		got, err := w.store.RepoScope(ctx, empty, domain.RepoAll)
		if err != nil || len(got) != 0 {
			t.Errorf("RepoScope(all) = %v, %v, want no repos and no error", got, err)
		}
	})
	t.Run("one repo", func(t *testing.T) {
		got, err := w.store.RepoScope(ctx, w.ws, w.repoB)
		if err != nil {
			t.Fatalf("RepoScope(one) error = %v", err)
		}
		want := []domain.RepoRef{{ID: w.repoB, Owner: "acme", Name: "web", FullName: "acme/web"}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("RepoScope(one) mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("not found", func(t *testing.T) {
		for name, repo := range map[string]string{
			"repo of another workspace": w.repoX,
			"unknown id":                "00000000-0000-4000-8000-000000000000",
			"not a uuid":                "../etc/passwd",
			"empty":                     "",
		} {
			if _, err := w.store.RepoScope(ctx, w.ws, repo); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("RepoScope(%s) error = %v, want domain.ErrNotFound", name, err)
			}
		}
	})
	t.Run("removed repo disappears", func(t *testing.T) {
		w.exec(t, `DELETE FROM repos WHERE id = ?`, w.repoB)
		got, err := w.store.RepoScope(ctx, w.ws, domain.RepoAll)
		if err != nil || len(got) != 1 || got[0].ID != w.repoA {
			t.Errorf("RepoScope(all) = %v, %v, want only repoA", got, err)
		}
		if _, err := w.store.RepoScope(ctx, w.ws, w.repoB); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("RepoScope(removed) error = %v, want domain.ErrNotFound", err)
		}
	})
}

// TestRepoScope_CodeHost: a GitLab repository carries provider, host and web_url, a GitHub one
// none of them (FR46); same-named repositories of two hosts stay two repositories, their pull
// requests and hot files told apart by repo id, and `repo` is the short name (SCR6).
func TestRepoScope_CodeHost(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	gl := func(host string, project int, webURL string) string {
		return w.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
			VALUES (?, 'grp/sub', 'api', 'grp/sub/api', 'gitlab', ?, ?, ?) RETURNING id`, w.ws, host, project, webURL)
	}
	one := gl("gitlab.example.com", 1, "https://gitlab.example.com/grp/sub/api")
	two := gl("git.other.org:8443", 2, "https://git.other.org:8443/grp/sub/api")

	t.Run("fields", func(t *testing.T) {
		got, err := w.store.RepoScope(ctx, w.ws, domain.RepoAll)
		if err != nil {
			t.Fatalf("RepoScope(all) error = %v", err)
		}
		byID := map[string]domain.RepoRef{}
		for _, r := range got {
			byID[r.ID] = r
		}
		for id, want := range map[string]domain.RepoRef{
			w.repoA: {Provider: "", Host: "", WebURL: ""},
			one:     {Provider: "gitlab", Host: "gitlab.example.com", WebURL: "https://gitlab.example.com/grp/sub/api"},
			two:     {Provider: "gitlab", Host: "git.other.org:8443", WebURL: "https://git.other.org:8443/grp/sub/api"},
		} {
			r := byID[id]
			if r.Provider != want.Provider || r.Host != want.Host || r.WebURL != want.WebURL {
				t.Errorf("RepoScope(all) repo %s = {%q %q %q}, want {%q %q %q}", id, r.Provider, r.Host, r.WebURL, want.Provider, want.Host, want.WebURL)
			}
		}
		single, err := w.store.RepoScope(ctx, w.ws, two)
		if err != nil || len(single) != 1 || single[0].Host != "git.other.org:8443" || single[0].Name != "api" {
			t.Errorf("RepoScope(one) = %+v, %v, want the repo of host git.other.org:8443", single, err)
		}
	})
	t.Run("same name on two hosts stays distinct", func(t *testing.T) {
		for i, id := range []string{one, two} {
			pr := w.pull(t, id, 1, "open", pullSpec{})
			w.review(t, "gl"+itoa(i), pr, &w.alpha, "review", from.Add(day), ptr(50), fnd("CRITICAL", "finding", "pending", "same.go"))
		}
		got, err := w.store.WindowFacts(ctx, w.ws, []string{one, two}, win(from, to))
		if err != nil {
			t.Fatalf("WindowFacts() error = %v", err)
		}
		repoIDs := map[string]string{}
		for _, p := range got.Pulls {
			repoIDs[p.RepoID] = p.Repo
		}
		if diff := cmp.Diff(map[string]string{one: "api", two: "api"}, repoIDs); diff != "" {
			t.Errorf("WindowFacts() pull repos mismatch (-want +got):\n%s", diff)
		}
		hot := map[string]int{}
		for _, f := range got.HotFiles {
			if f.Path == "same.go" && f.Repo == "api" {
				hot[f.RepoID] = f.Findings
			}
		}
		if diff := cmp.Diff(map[string]int{one: 1, two: 1}, hot); diff != "" {
			t.Errorf("WindowFacts() hot files per repo id mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestWindowFacts_Membership(t *testing.T) {
	w := newWorld(t)
	got, err := w.store.WindowFacts(t.Context(), w.ws, []string{w.repoA}, win(from, to))
	if err != nil {
		t.Fatalf("WindowFacts() error = %v", err)
	}

	t.Run("pulls: review OR merge OR open inside [from, to)", func(t *testing.T) {
		// 1 reviewed; 3 merged inside; 5 opened at from; 8 reviewed. Not 2 (review at to), 4 (opened
		// at to, merged before from), 6 (summary only), 7 (no instants).
		want := []string{"api#1", "api#3", "api#5", "api#8"}
		if diff := cmp.Diff(want, numbers(got.Pulls)); diff != "" {
			t.Errorf("WindowFacts() pulls mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("pull columns", func(t *testing.T) {
		byNumber := map[int]domain.PullFact{}
		for _, p := range got.Pulls {
			byNumber[p.Number] = p
		}
		opened := from.Add(-20 * day)
		want := map[int]domain.PullFact{
			1: {PRID: w.pr[1], RepoID: w.repoA, Repo: "api", Number: 1, Title: "title open", Author: "dev", HeadSHA: "head1",
				Status: "open", LastReviewedSHA: ptr("old1"), OpenedAt: &opened, UpdatedAt: &opened,
				FirstReviewAt: ptr(from.Add(-10 * day)),
				// current reviews, ever: alpha2 (1 open: a.go; accepted, dismissed and both are not
				// open), beta (the open secret_leak), the deleted agent's newest review (1); older
				// reviews and the summary review do not count.
				OpenCritical: 3},
			3: {PRID: w.pr[3], RepoID: w.repoA, Repo: "api", Number: 3, Title: "title merged", Author: "dev", HeadSHA: "sha",
				Status: "merged", OpenedAt: &opened, UpdatedAt: &opened, MergedAt: ptr(from.Add(5 * day)), ClosedAt: ptr(from.Add(5 * day)),
				FirstReviewAt: ptr(from.Add(-3 * day)), OpenCritical: 2}, // a merged PR keeps its open criticals
			5: {PRID: w.pr[5], RepoID: w.repoA, Repo: "api", Number: 5, Title: "title needs_review", Author: "dev", HeadSHA: "sha",
				Additions: 10, Deletions: 5, FilesCount: 2, Status: "needs_review", Draft: true, OpenedAt: ptr(from), UpdatedAt: ptr(from)},
			8: {PRID: w.pr[8], RepoID: w.repoA, Repo: "api", Number: 8, Title: "title closed", Author: "dev", HeadSHA: "sha",
				Status: "closed", ClosedAt: ptr(from.Add(day)), FirstReviewAt: ptr(from.Add(2 * day)), OpenCritical: 1},
		}
		if diff := cmp.Diff(want, byNumber, cmpopts.EquateApproxTime(0)); diff != "" {
			t.Errorf("WindowFacts() pulls mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("reviews: kind review in [from, to), deleted agent is empty id and name", func(t *testing.T) {
		type brief struct {
			Agent, Name string
			Score       *int
			At          time.Time
		}
		have := map[string]brief{}
		byID := map[string]string{}
		for name, id := range w.reviews {
			byID[id] = name
		}
		for _, r := range got.Reviews {
			have[byID[r.ReviewID]] = brief{r.AgentID, r.AgentName, r.Score, r.CreatedAt}
		}
		want := map[string]brief{
			"alpha1":  {w.alpha, "alpha", ptr(50), from},
			"alpha2":  {w.alpha, "alpha", ptr(80), from.Add(3 * day)},
			"beta":    {w.beta, "beta", nil, from.Add(day)},
			"doomed1": {"", "", ptr(30), from.Add(2 * day)},
			"doomed2": {"", "", ptr(60), from.Add(2*day + time.Hour)},
			"closed":  {w.beta, "beta", ptr(90), from.Add(2 * day)},
		}
		if diff := cmp.Diff(want, have, cmpopts.EquateApproxTime(0)); diff != "" {
			t.Errorf("WindowFacts() reviews mismatch (-want +got):\n%s", diff)
		}
		for _, r := range got.Reviews {
			if r.PRID == "" {
				t.Errorf("review %s has no PRID", r.ReviewID)
			}
		}
	})
	t.Run("finding counts: every kind and every triage state of the snapshot reviews, nothing else", func(t *testing.T) {
		byID := map[string]string{}
		for name, id := range w.reviews {
			byID[id] = name
		}
		have := map[string]domain.ReviewCounts{}
		for _, c := range got.Counts {
			c.ReviewID = byID[c.ReviewID]
			have[c.ReviewID] = c
		}
		// With no previous window the range is one window: only the newest review per agent counts
		// (alpha1 and doomed1 are superseded). alphaOld and mergedBefore are before the range,
		// summary has another kind, atTo is out of the range. alpha2 holds, in this order, an open
		// critical, an accepted one, a dismissed one, one accepted and dismissed (dismissed wins,
		// EC7) and an open warning.
		want := map[string]domain.ReviewCounts{
			"alpha2": {
				ReviewID: "alpha2", All: domain.FindingCounts{Critical: 4, Warning: 1},
				Live: domain.FindingCounts{Critical: 2, Warning: 1}, OpenCritical: 1,
			},
			"beta": {
				ReviewID: "beta", All: domain.FindingCounts{Critical: 1}, Live: domain.FindingCounts{Critical: 1}, OpenCritical: 1,
			},
			"doomed2": {
				ReviewID: "doomed2", All: domain.FindingCounts{Critical: 1}, Live: domain.FindingCounts{Critical: 1}, OpenCritical: 1,
			},
			"closed": {
				ReviewID: "closed", All: domain.FindingCounts{Critical: 1}, Live: domain.FindingCounts{Critical: 1}, OpenCritical: 1,
			},
		}
		if diff := cmp.Diff(want, have); diff != "" {
			t.Errorf("WindowFacts() counts per review mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("top findings: the first three of each PR, columns and triage times", func(t *testing.T) {
		byID := map[string]string{}
		for name, id := range w.reviews {
			byID[id] = name
		}
		type brief struct {
			Review, File string
			Line         int
			Severity     string
			Accepted     bool
			Dismissed    bool
		}
		var have []brief
		for _, f := range got.TopFindings {
			if f.FindingID == "" || f.Title != "t "+f.File {
				t.Errorf("WindowFacts() top finding = %+v, want an id and the title %q", f, "t "+f.File)
			}
			have = append(have, brief{byID[f.ReviewID], f.File, f.StartLine, f.Severity, f.AcceptedAt != nil, f.DismissedAt != nil})
		}
		sort.Slice(have, func(i, j int) bool { return have[i].File < have[j].File })
		// PR 1: seven snapshot findings, all CRITICAL but alpha2's accepted, dismissed and
		// warning ones; the open ones rank first by file: a.go (alpha2), d.go (doomed2), s.go
		// (beta). PR 8: its only finding.
		want := []brief{
			{"alpha2", "a.go", 1, "CRITICAL", false, false},
			{"closed", "c.go", 1, "CRITICAL", false, false},
			{"doomed2", "d.go", 1, "CRITICAL", false, false},
			{"beta", "s.go", 1, "CRITICAL", false, false},
		}
		if diff := cmp.Diff(want, have); diff != "" {
			t.Errorf("WindowFacts() top findings mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("runs: any status, PR runs in [from, to) only, summed per PR", func(t *testing.T) {
		if len(got.Runs) != 1 {
			t.Fatalf("WindowFacts() runs = %+v, want one group (PR 1; no previous window)", got.Runs)
		}
		r := got.Runs[0]
		// The three runs of PR 1 in the range (done 0.25, failed without cost, running 1) are one
		// group: first run = from, cost 1.25. The runs outside the range, of the PR without a
		// match and of other repositories are absent.
		if r.PRID != w.pr[1] || !r.RanAt.Equal(from) || r.CostUSD == nil || *r.CostUSD != 1.25 {
			t.Errorf("WindowFacts() run = %+v, want PR 1 at %v with cost 1.25", r, from)
		}
	})
	t.Run("range echo", func(t *testing.T) {
		if !got.From.Equal(from) || !got.To.Equal(to) {
			t.Errorf("WindowFacts() range = [%v, %v), want [%v, %v)", got.From, got.To, from, to)
		}
	})
}

func TestWindowFacts_Bounds(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	// Shifting the range by one millisecond around the instants at `from` and at `to` flips them.
	tests := []struct {
		name     string
		from, to time.Time
		pulls    []string
		reviews  int
		runCost  float64 // the runs of PR 1 are one group; its cost is the sum of the runs in the range
	}{
		{"[from, to)", from, to, []string{"api#1", "api#3", "api#5", "api#8"}, 6, 1.25},
		{"from+1ms drops the review, run and opening at from", from.Add(time.Millisecond), to, []string{"api#1", "api#3", "api#8"}, 5, 1},
		{"to+1ms adds the review and the run at to, and the PR opened at to", from, to.Add(time.Millisecond), []string{"api#1", "api#2", "api#3", "api#4", "api#5", "api#8"}, 7, 10.25},
		{"to-1ms still holds all but the end", from, to.Add(-time.Millisecond), []string{"api#1", "api#3", "api#5", "api#8"}, 6, 1.25},
		{"before from: merged PR 4 only, its run too", from.Add(-day), from, []string{"api#4"}, 0, 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoA}, win(tc.from, tc.to))
			if err != nil {
				t.Fatalf("WindowFacts() error = %v", err)
			}
			if diff := cmp.Diff(tc.pulls, numbers(got.Pulls)); diff != "" {
				t.Errorf("WindowFacts() pulls mismatch (-want +got):\n%s", diff)
			}
			if len(got.Reviews) != tc.reviews {
				t.Errorf("WindowFacts() reviews = %d, want %d", len(got.Reviews), tc.reviews)
			}
			var cost float64
			for _, r := range got.Runs {
				if r.CostUSD != nil {
					cost += *r.CostUSD
				}
			}
			if cost != tc.runCost {
				t.Errorf("WindowFacts() cost of the runs in the range = %v, want %v", cost, tc.runCost)
			}
		})
	}
}

// TestWindowFacts_FindingsOfBothSnapshots: with a previous window the store counts the findings
// of the newest review per (PR, agent) in each half of the range, split at Window.From, and of no
// other review; it ranks the top findings per PR and half. Reviews of deleted agents are one
// agent. Runs are summed per PR and half.
func TestWindowFacts_FindingsOfBothSnapshots(t *testing.T) {
	w := newWorld(t)
	split := from.Add(2 * day) // doomed1 sits exactly on the split: current half
	window := domain.Window{From: split, To: to, HasPrev: true, PrevFrom: from, PrevTo: split}
	got, err := w.store.WindowFacts(t.Context(), w.ws, []string{w.repoA}, window)
	if err != nil {
		t.Fatalf("WindowFacts() error = %v", err)
	}
	if !got.From.Equal(from) || !got.To.Equal(to) {
		t.Errorf("WindowFacts() range = [%v, %v), want [%v, %v)", got.From, got.To, from, to)
	}
	byID := map[string]string{}
	for name, id := range w.reviews {
		byID[id] = name
	}
	counted := map[string]int{}
	for _, c := range got.Counts {
		counted[byID[c.ReviewID]] = c.All.Critical + c.All.Warning + c.All.Suggestion
	}
	// previous half [from, from+2d): alpha1 (alpha), beta; current half: the deleted agent's
	// doomed2 (doomed1 is superseded), alpha2 (5 findings), closed (PR 8).
	want := map[string]int{"alpha1": 1, "beta": 1, "doomed2": 1, "alpha2": 5, "closed": 1}
	if diff := cmp.Diff(want, counted); diff != "" {
		t.Errorf("WindowFacts() findings counted per review mismatch (-want +got):\n%s", diff)
	}
	// Top findings per PR and half: PR 1 previous half has two findings (both kept); its current
	// half has doomed2's d.go and alpha2's five: a.go (open critical) and d.go (open critical)
	// come first, then alpha2's accepted b.go critical (class accepted); the dismissed ones and the
	// warning stay out. PR 8: its one.
	tops := map[string]int{}
	for _, f := range got.TopFindings {
		tops[byID[f.ReviewID]]++
	}
	wantTops := map[string]int{"alpha1": 1, "beta": 1, "doomed2": 1, "alpha2": 2, "closed": 1}
	if diff := cmp.Diff(wantTops, tops); diff != "" {
		t.Errorf("WindowFacts() top findings per review mismatch (-want +got):\n%s", diff)
	}
	// ReadFrom is the previous window's start, so the reviews of both halves are returned.
	if len(got.Reviews) != 6 {
		t.Errorf("WindowFacts() reviews = %d, want the 6 of the range", len(got.Reviews))
	}
	// Runs of PR 1 in the range: previous half = the done run at from and the failed one without
	// cost at from+1d (summed: 0.25, first at from); current half = the running run at from+2d (1).
	type group struct {
		At   time.Time
		Cost *float64
	}
	var runs []group
	for _, r := range got.Runs {
		runs = append(runs, group{r.RanAt, r.CostUSD})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].At.Before(runs[j].At) })
	wantRuns := []group{{from, ptr(0.25)}, {from.Add(2 * day), ptr(1.0)}}
	if diff := cmp.Diff(wantRuns, runs, cmpopts.EquateApproxTime(0)); diff != "" {
		t.Errorf("WindowFacts() runs per half mismatch (-want +got):\n%s", diff)
	}
}

func TestWindowFacts_Isolation(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	t.Run("a repo of another workspace yields nothing", func(t *testing.T) {
		got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoX}, win(from, to))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Pulls)+len(got.Reviews)+len(got.Counts)+len(got.TopFindings)+len(got.HotFiles)+len(got.Runs) != 0 {
			t.Errorf("WindowFacts(ws, repo of other) = %+v, want nothing", got)
		}
	})
	t.Run("the other workspace sees only its own", func(t *testing.T) {
		got, err := w.store.WindowFacts(ctx, w.other, []string{w.repoA, w.repoB, w.repoX}, win(from, to))
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"x#1"}, numbers(got.Pulls)); diff != "" {
			t.Errorf("WindowFacts(other) pulls mismatch (-want +got):\n%s", diff)
		}
		if len(got.Reviews) != 1 || len(got.Counts) != 1 || len(got.TopFindings) != 1 || len(got.Runs) != 1 || got.Runs[0].CostUSD == nil || *got.Runs[0].CostUSD != 7 {
			t.Errorf("WindowFacts(other) = %+v, want one review, one finding and the run of cost 7", got)
		}
	})
	t.Run("only the requested repos", func(t *testing.T) {
		got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoB}, win(from, to))
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"web#1"}, numbers(got.Pulls)); diff != "" {
			t.Errorf("WindowFacts(repoB) pulls mismatch (-want +got):\n%s", diff)
		}
		if len(got.Runs) != 1 || len(got.Reviews) != 1 {
			t.Errorf("WindowFacts(repoB) = %+v, want its one run and one review", got)
		}
	})
	t.Run("no repos", func(t *testing.T) {
		got, err := w.store.WindowFacts(ctx, w.ws, nil, win(from, to))
		if err != nil || len(got.Pulls) != 0 {
			t.Errorf("WindowFacts(no repos) = %+v, %v, want empty", got, err)
		}
	})
}

func TestWindowFacts_DeletedRepoLeavesNoPulls(t *testing.T) {
	w := newWorld(t)
	w.exec(t, `DELETE FROM repos WHERE id = ?`, w.repoA)
	got, err := w.store.WindowFacts(t.Context(), w.ws, []string{w.repoA, w.repoB}, win(from, to))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"web#1"}, numbers(got.Pulls)); diff != "" {
		t.Errorf("WindowFacts() pulls after removing repoA mismatch (-want +got):\n%s", diff)
	}
	if len(got.Reviews) != 1 || len(got.Counts) != 1 || len(got.TopFindings) != 1 || len(got.Runs) != 1 {
		t.Errorf("WindowFacts() = %d reviews, %d counts, %d top findings, %d runs, want only repoB's 1, 1, 1, 1",
			len(got.Reviews), len(got.Counts), len(got.TopFindings), len(got.Runs))
	}
}

func TestWindowFacts_OpenCritical(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	critical := func(f domain.Facts) map[int]int {
		out := map[int]int{}
		for _, p := range f.Pulls {
			out[p.Number] = p.OpenCritical
		}
		return out
	}

	got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoA}, win(from, to))
	if err != nil {
		t.Fatalf("WindowFacts() error = %v", err)
	}
	// PR 1 open (3: alpha2, beta, the deleted agent's newest), PR 3 merged (2), PR 5 draft without
	// reviews (0), PR 8 closed (1). Whatever the status, the current reviews count.
	if diff := cmp.Diff(map[int]int{1: 3, 3: 2, 5: 0, 8: 1}, critical(got)); diff != "" {
		t.Errorf("WindowFacts() open criticals per PR mismatch (-want +got):\n%s", diff)
	}

	t.Run("current reviews are the newest ever, not the newest of the window", func(t *testing.T) {
		// A newer review after the range supersedes alpha2 and turns the count into its own.
		later := w.insert(t, `INSERT INTO reviews (workspace_id, pr_id, agent_id, kind, score, created_at) VALUES (?, ?, ?, 'review', 90, ?) RETURNING id`,
			w.ws, w.pr[1], w.alpha, to.Add(day))
		w.exec(t, `INSERT INTO findings (review_id, file, start_line, end_line, severity, category, title, rationale, confidence)
			VALUES (?, 'n.go', 1, 1, 'WARNING', 'bug', 't', 'r', 0.5)`, later)
		after, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoA}, win(from, to))
		if err != nil {
			t.Fatalf("WindowFacts() error = %v", err)
		}
		if got := critical(after)[1]; got != 2 { // beta and the deleted agent; alpha's newest has none
			t.Errorf("WindowFacts() PR 1 open criticals = %d, want 2", got)
		}
	})
	t.Run("isolation", func(t *testing.T) {
		other, err := w.store.WindowFacts(ctx, w.other, []string{w.repoA, w.repoX}, win(from, to))
		if err != nil {
			t.Fatal(err)
		}
		if len(other.Pulls) != 1 || other.Pulls[0].Repo != "x" || other.Pulls[0].OpenCritical != 1 {
			t.Errorf("WindowFacts(other) pulls = %+v, want only x#1 with 1 open critical", other.Pulls)
		}
	})
}

// TestWindowFacts_TopFindingsOrderAndCap pins plan P13 as the SQL applies it (and
// domain.topFindings repeats it): per PR and snapshot half the first three findings of the
// snapshot reviews by severity, then open before accepted before dismissed (dismissed wins over
// accepted), then file in byte order, start line, id.
func TestWindowFacts_TopFindingsOrderAndCap(t *testing.T) {
	w := newWorld(t)
	pr := w.pull(t, w.repoA, 20, "open", pullSpec{opened: ptr(from.Add(day))})
	// alphaOld is superseded by alphaNew (same PR, same agent): its critical stays out.
	w.review(t, "alphaOld", pr, &w.alpha, "review", from.Add(day), ptr(10), fnd("CRITICAL", "finding", "pending", "old.go"))
	// Lines are the insertion order, 1..6.
	w.review(t, "alphaNew", pr, &w.alpha, "review", from.Add(2*day), ptr(20),
		fnd("WARNING", "finding", "pending", "z.go"),     // 1: WARNING
		fnd("SUGGESTION", "finding", "pending", "a.go"),  // 2: SUGGESTION
		fnd("CRITICAL", "finding", "dismissed", "b.go"),  // 3: CRITICAL, dismissed
		fnd("CRITICAL", "finding", "accepted", "b.go"),   // 4: CRITICAL, accepted
		fnd("CRITICAL", "finding", "pending", "c.go"),    // 5: CRITICAL, open
		fnd("CRITICAL", "finding", "both", "A_upper.go"), // 6: CRITICAL, dismissed (wins over accepted); upper case sorts before lower in byte order
	)
	w.review(t, "betaNew", pr, &w.beta, "review", from.Add(3*day), ptr(30),
		fnd("CRITICAL", "finding", "pending", "c.go"), // 1: CRITICAL, open, same file as alphaNew:5 but line 1
	)

	got, err := w.store.WindowFacts(t.Context(), w.ws, []string{w.repoA}, win(from, to))
	if err != nil {
		t.Fatalf("WindowFacts() error = %v", err)
	}
	byID := map[string]string{}
	for name, id := range w.reviews {
		byID[id] = name
	}
	type brief struct {
		Review, File string
		Line         int
	}
	var have []brief
	for _, f := range got.TopFindings {
		if f.ReviewID == w.reviews["alphaNew"] || f.ReviewID == w.reviews["betaNew"] || f.ReviewID == w.reviews["alphaOld"] {
			have = append(have, brief{byID[f.ReviewID], f.File, f.StartLine})
		}
	}
	sort.Slice(have, func(i, j int) bool {
		if have[i].File != have[j].File {
			return have[i].File < have[j].File
		}
		return have[i].Line < have[j].Line
	})
	// Open criticals first: c.go:1 (betaNew) before c.go:5 (alphaNew); then the accepted critical
	// b.go:4. The dismissed criticals (b.go:3, A_upper.go:6), the warning and the suggestion are
	// beyond the cap of three, and alphaOld's critical is not in the snapshot.
	want := []brief{{"alphaNew", "b.go", 4}, {"betaNew", "c.go", 1}, {"alphaNew", "c.go", 5}}
	if diff := cmp.Diff(want, have); diff != "" {
		t.Errorf("WindowFacts() top findings of PR 20 mismatch (-want +got):\n%s", diff)
	}
	// The same order through the domain, which re-applies it: the same three.
	cur, _ := domain.BuildRows(domain.Window{Key: domain.PeriodWeek, Location: time.UTC, From: from, To: to, Days: 14}, got, to)
	for _, row := range cur {
		if row.PRID != pr {
			continue
		}
		var ids []string
		for _, tf := range row.TopFindings {
			ids = append(ids, tf.File+":"+itoa(tf.StartLine)+":"+tf.State)
		}
		if diff := cmp.Diff([]string{"c.go:1:open", "c.go:5:open", "b.go:4:accepted"}, ids); diff != "" {
			t.Errorf("BuildRows() top findings of PR 20 mismatch (-want +got):\n%s", diff)
		}
	}
}

// TestWindowFacts_HotFiles pins FR14 as the SQL applies it: findings of the current snapshot
// without a dismissed time (accepted ones count) and with a path, per (repository, path), the
// critical count, the distinct PR numbers; the cut keeps at least the first six files and
// every file tied with the sixth on findings, critical, path and repository name.
func TestWindowFacts_HotFiles(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()

	t.Run("counts, exclusions and PR numbers", func(t *testing.T) {
		got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoA}, win(from, to))
		if err != nil {
			t.Fatalf("WindowFacts() error = %v", err)
		}
		// Snapshot findings of repoA without a dismissed time: a.go (alpha2, open critical),
		// b.go (alpha2: the accepted critical and the open warning; the dismissed and the
		// accepted-and-dismissed criticals are out), s.go (beta), d.go (doomed2) of PR 1 and c.go
		// (closed) of PR 8.
		want := []domain.HotFile{
			{Path: "b.go", RepoID: w.repoA, Repo: "api", Findings: 2, Critical: 1, PRs: []int{1}},
			{Path: "a.go", RepoID: w.repoA, Repo: "api", Findings: 1, Critical: 1, PRs: []int{1}},
			{Path: "c.go", RepoID: w.repoA, Repo: "api", Findings: 1, Critical: 1, PRs: []int{8}},
			{Path: "d.go", RepoID: w.repoA, Repo: "api", Findings: 1, Critical: 1, PRs: []int{1}},
			{Path: "s.go", RepoID: w.repoA, Repo: "api", Findings: 1, Critical: 1, PRs: []int{1}},
		}
		if diff := cmp.Diff(want, domain.HotFiles(got.HotFiles)); diff != "" {
			t.Errorf("WindowFacts() hot files mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("only the current half counts", func(t *testing.T) {
		split := from.Add(2 * day)
		window := domain.Window{From: split, To: to, HasPrev: true, PrevFrom: from, PrevTo: split}
		got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoA}, window)
		if err != nil {
			t.Fatalf("WindowFacts() error = %v", err)
		}
		// The previous half (alpha1's a.go, beta's s.go) is not in the hot files.
		var paths []string
		for _, f := range domain.HotFiles(got.HotFiles) {
			paths = append(paths, f.Path)
		}
		if diff := cmp.Diff([]string{"b.go", "a.go", "c.go", "d.go"}, paths); diff != "" {
			t.Errorf("WindowFacts() hot file paths with a previous half mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("cap with ties, empty paths and superseded reviews", func(t *testing.T) {
		repoD := w.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'zed', 'api', 'zed/api') RETURNING id`, w.ws)
		many := func(n int, severity, file string) []finding {
			out := make([]finding, n)
			for i := range out {
				out[i] = fnd(severity, "finding", "pending", file)
			}
			return out
		}
		join := func(parts ...[]finding) []finding {
			var out []finding
			for _, p := range parts {
				out = append(out, p...)
			}
			return out
		}
		pr30 := w.pull(t, w.repoA, 30, "open", pullSpec{})
		pr32 := w.pull(t, w.repoA, 32, "open", pullSpec{})
		pr31 := w.pull(t, repoD, 31, "open", pullSpec{})
		// F1..F6 get 9, 8, 7, 6, 5, 4 findings in repoA PR 30, F1 two more criticals in PR 32;
		// the file "" and dismissed findings are not files; an older review of the same agent is
		// not in the snapshot.
		w.review(t, "hotOld", pr30, &w.alpha, "review", from.Add(day), nil, many(20, "WARNING", "F1")...)
		w.review(t, "hot30", pr30, &w.alpha, "review", from.Add(2*day), nil, join(
			many(9, "WARNING", "F1"), many(8, "WARNING", "F2"), many(7, "WARNING", "F3"), many(6, "WARNING", "F4"),
			many(5, "WARNING", "F5"), many(4, "WARNING", "F6"),
			[]finding{fnd("WARNING", "finding", "dismissed", "F1"), fnd("WARNING", "finding", "dismissed", "F1")},
			[]finding{fnd("WARNING", "finding", "pending", "")},
		)...)
		w.review(t, "hot32", pr32, &w.beta, "review", from.Add(2*day), nil, many(2, "CRITICAL", "F1")...)
		// repoD has the same short name "api" as repoA: its F6 (4 findings, no critical) ties
		// with repoA's F6 on findings, critical, path and repository name.
		w.review(t, "hot31", pr31, &w.alpha, "review", from.Add(2*day), nil, many(4, "WARNING", "F6")...)

		got, err := w.store.WindowFacts(ctx, w.ws, []string{w.repoA, repoD}, win(from, to))
		if err != nil {
			t.Fatalf("WindowFacts() error = %v", err)
		}
		if len(got.HotFiles) != domain.HotFilesLimit+1 {
			t.Errorf("WindowFacts() returned %d hot files, want %d (the first %d and the one tied with the last)",
				len(got.HotFiles), domain.HotFilesLimit+1, domain.HotFilesLimit)
		}
		want := []domain.HotFile{
			{Path: "F1", RepoID: w.repoA, Repo: "api", Findings: 11, Critical: 2, PRs: []int{30, 32}},
			{Path: "F2", RepoID: w.repoA, Repo: "api", Findings: 8, PRs: []int{30}},
			{Path: "F3", RepoID: w.repoA, Repo: "api", Findings: 7, PRs: []int{30}},
			{Path: "F4", RepoID: w.repoA, Repo: "api", Findings: 6, PRs: []int{30}},
			{Path: "F5", RepoID: w.repoA, Repo: "api", Findings: 5, PRs: []int{30}},
			{Path: "F6", RepoID: w.repoA, Repo: "api", Findings: 4, PRs: []int{30}}, // the tie: PR list [30] < [31]
		}
		if diff := cmp.Diff(want, domain.HotFiles(got.HotFiles)); diff != "" {
			t.Errorf("WindowFacts() hot files after the domain's order and cap mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestWindowFacts_RunsPerHalf: the runs of a PR in one half are one group; a group without a
// known cost has none (the domain counts it as 0); runs of two PRs are two groups.
func TestWindowFacts_RunsPerHalf(t *testing.T) {
	w := newWorld(t)
	pr := w.pull(t, w.repoA, 40, "open", pullSpec{})
	w.run(t, w.ws, pr, from.Add(day), "failed", "ci", nil)
	w.run(t, w.ws, pr, from.Add(2*day), "done", "local", nil)
	got, err := w.store.WindowFacts(t.Context(), w.ws, []string{w.repoA}, win(from, to))
	if err != nil {
		t.Fatalf("WindowFacts() error = %v", err)
	}
	var found []domain.RunFact
	for _, r := range got.Runs {
		if r.PRID == pr {
			found = append(found, r)
		}
	}
	if len(found) != 1 || !found[0].RanAt.Equal(from.Add(day)) || found[0].CostUSD != nil {
		t.Errorf("WindowFacts() runs of PR 40 = %+v, want one group at %v without a cost", found, from.Add(day))
	}
	if len(got.Runs) != 2 {
		t.Errorf("WindowFacts() runs = %d groups, want 2 (PR 1 and PR 40)", len(got.Runs))
	}
}

// statements records the SQL a store sends (GORM logger).
type statements struct {
	logger.Interface
	mu  sync.Mutex
	sql []string
}

func (s *statements) LogMode(logger.LogLevel) logger.Interface { return s }

func (s *statements) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	q, _ := fc()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sql = append(s.sql, q)
}

func (s *statements) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sql
	s.sql = nil
	return out
}

func TestStore_SetBasedReadOnlyAndBoundedQueries(t *testing.T) {
	w := newWorld(t)
	rec := &statements{Interface: logger.Discard}
	store := postgres.NewStore(w.db.Session(&gorm.Session{Logger: rec}))

	request := func() []string {
		t.Helper()
		ctx := t.Context()
		repos, err := store.RepoScope(ctx, w.ws, domain.RepoAll)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(repos))
		for i, r := range repos {
			ids[i] = r.ID
		}
		if _, err := store.WindowFacts(ctx, w.ws, ids, win(from, to)); err != nil {
			t.Fatal(err)
		}
		return rec.take()
	}

	before := request()
	// RepoScope 1; WindowFacts: pulls, reviews, runs, open criticals, and one finding statistics
	// statement per shard of repositories (2 repositories: 2 shards).
	if len(before) != 7 {
		t.Errorf("one request sent %d statements, want 7", len(before))
	}
	for _, q := range before {
		head := strings.ToUpper(strings.TrimSpace(q))
		if !strings.HasPrefix(head, "SELECT") && !strings.HasPrefix(head, "WITH") {
			t.Errorf("statement is not a read: %.60s", q)
		}
	}

	// More pull requests, reviews and findings do not add statements (no per-PR loop, no per-finding
	// read). The reads of WindowFacts are sent concurrently; they are one statement each.
	for n := 100; n < 130; n++ {
		pr := w.pull(t, w.repoA, n, "open", pullSpec{opened: ptr(from.Add(day))})
		w.review(t, "bulk"+itoa(n%10)+itoa(n/10%10), pr, &w.alpha, "review", from.Add(day), ptr(50), fnd("CRITICAL", "finding", "pending", "z.go"))
		w.run(t, w.ws, pr, from.Add(day), "done", "local", nil)
	}
	if after := request(); len(after) != len(before) {
		t.Errorf("statements with 30 more PRs = %d, want %d (independent of the PR count)", len(after), len(before))
	}

	// More repositories add finding-statistics shards up to a fixed number: 1 + 4 + 4 = 9 at most.
	for i := range 4 {
		w.insert(t, `INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', ?, ?) RETURNING id`,
			w.ws, "extra"+itoa(i), "acme/extra"+itoa(i))
	}
	if more := request(); len(more) != 9 {
		t.Errorf("statements with 6 repositories = %d, want 9 (the shards are capped)", len(more))
	}
}

func TestStore_JoinsCallersTransaction(t *testing.T) {
	// A repo inserted in an open transaction is visible only to a store call that takes its handle
	// from platform postgres.Conn; a store using its own pool would not see it.
	w := newWorld(t)
	err := platformpg.NewTx(w.db).InTx(t.Context(), func(ctx context.Context) error {
		if err := platformpg.Conn(ctx, w.db).Exec(
			`INSERT INTO repos (workspace_id, owner, name, full_name) VALUES (?, 'acme', 'zzz', 'acme/zzz')`, w.ws).Error; err != nil {
			return err
		}
		got, err := w.store.RepoScope(ctx, w.ws, domain.RepoAll)
		if err != nil {
			return err
		}
		if len(got) != 3 || got[2].FullName != "acme/zzz" {
			t.Errorf("RepoScope() in the transaction = %v, want the uncommitted repo last", got)
		}
		return errors.New("rollback")
	})
	if err == nil || err.Error() != "rollback" {
		t.Fatalf("InTx() error = %v, want the rollback error", err)
	}
	got, err := w.store.RepoScope(t.Context(), w.ws, domain.RepoAll)
	if err != nil || len(got) != 2 {
		t.Errorf("RepoScope() after rollback = %v, %v, want 2 repos", got, err)
	}
}

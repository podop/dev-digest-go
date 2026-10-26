package app_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

const glRepoID = "repo-gl"

var glRepo = domain.Repo{ID: glRepoID, Owner: "acme/team", Name: "api", Provider: domain.ProviderGitLab, Host: "git.corp.example", ProjectID: 42}

// fakeGitLab answers from fields and records every call.
type fakeGitLab struct {
	mu sync.Mutex

	project    domain.GitLabProject
	projectErr error
	listed     []domain.Listed
	listErr    error
	fetched    domain.Fetched
	getErr     error
	issue      domain.Issue
	issueErr   error
	stats      map[int]domain.DiffStats
	statsErr   error

	calls       []string // method names in order
	listPages   [][2]int
	statsCalls  [][]int
	getDeadline time.Duration // time left on the Get context
	getHasDL    bool
	getRepo     domain.Repo
}

func (g *fakeGitLab) record(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, name)
}

func (g *fakeGitLab) Project(_ context.Context, _ domain.Repo) (domain.GitLabProject, error) {
	g.record("Project")
	return g.project, g.projectErr
}

func (g *fakeGitLab) ListPage(_ context.Context, _ domain.Repo, page, perPage int) ([]domain.Listed, bool, error) {
	g.record("ListPage")
	g.listPages = append(g.listPages, [2]int{page, perPage})
	return g.listed, len(g.listed) >= perPage, g.listErr
}

func (g *fakeGitLab) Get(ctx context.Context, repo domain.Repo, _ int) (domain.Fetched, error) {
	g.record("Get")
	dl, ok := ctx.Deadline()
	g.getHasDL, g.getDeadline, g.getRepo = ok, time.Until(dl), repo
	return g.fetched, g.getErr
}

func (g *fakeGitLab) Issue(_ context.Context, _ domain.Repo, _ int) (domain.Issue, error) {
	g.record("Issue")
	return g.issue, g.issueErr
}

func (g *fakeGitLab) Stats(_ context.Context, _ domain.Repo, numbers []int) (map[int]domain.DiffStats, error) {
	g.record("Stats")
	g.statsCalls = append(g.statsCalls, append([]int(nil), numbers...))
	return g.stats, g.statsErr
}

func glStoreAndService(gl *fakeGitLab, gh *fakeGitHub) (*fakeStore, *app.Service) {
	st := newStore()
	st.repos[glRepoID] = glRepo
	svc := app.NewService(st, gh, glDiscardLog(), func() time.Time { return now }, app.WithGitLab(gl))
	return st, svc
}

func glPull(n int, stats domain.DiffStats) domain.Pull {
	p := stored(n, stats)
	p.ID = "gl-pr-" + string(rune('a'+n))
	p.RepoID = glRepoID
	return p
}

func TestListForRepo_gitlabSyncsProjectThenOnePage(t *testing.T) {
	gl := &fakeGitLab{
		project: domain.GitLabProject{ID: 42, FullName: "acme/team/api", Owner: "acme/team", Name: "api"},
		listed:  []domain.Listed{{Number: 3, Title: "mr", HeadSHA: "h", Status: "open"}},
	}
	gh := &fakeGitHub{}
	st, svc := glStoreAndService(gl, gh)

	if _, err := svc.ListForRepo(t.Context(), wsID, glRepoID); err != nil {
		t.Fatalf("ListForRepo() error = %v", err)
	}
	if diff := cmp.Diff([]string{"Project", "ListPage"}, gl.calls); diff != "" {
		t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([][2]int{{1, app.GitLabPageSize}}, gl.listPages); diff != "" || app.GitLabPageSize != 50 {
		t.Errorf("ListPage pages = %v (-want +got):\n%s", gl.listPages, diff)
	}
	if len(st.upserted) != 1 || st.upserted[0].Number != 3 {
		t.Errorf("upserted = %+v, want MR !3", st.upserted)
	}
	if len(st.pathUpdates) != 0 {
		t.Errorf("UpdateRepoPath calls = %d, want 0 for an unchanged path", len(st.pathUpdates))
	}
	if gh.listCalls != 0 || len(gh.fetchCalls) != 0 {
		t.Errorf("GitHub calls = %d list / %v fetch, want none for a GitLab repository", gh.listCalls, gh.fetchCalls)
	}
}

func TestListForRepo_gitlabRenamedProjectUpdatesPath(t *testing.T) {
	moved := domain.GitLabProject{ID: 42, FullName: "acme/platform/api", Owner: "acme/platform", Name: "api"}
	tests := []struct {
		name   string
		heldBy string
		err    error
	}{
		{name: "updated"},
		{name: "collision keeps the path", heldBy: "other-repo"},
		{name: "store failure keeps the path", err: errors.New("db down")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl := &fakeGitLab{project: moved, listed: []domain.Listed{{Number: 1, HeadSHA: "h", Status: "open"}}}
			st, svc := glStoreAndService(gl, &fakeGitHub{})
			st.pathHeldBy, st.pathErr = tc.heldBy, tc.err

			if _, err := svc.ListForRepo(t.Context(), wsID, glRepoID); err != nil {
				t.Fatalf("ListForRepo() error = %v", err)
			}
			if diff := cmp.Diff([]domain.GitLabProject{moved}, st.pathUpdates); diff != "" {
				t.Errorf("UpdateRepoPath calls mismatch (-want +got):\n%s", diff)
			}
			if len(st.upserted) != 1 {
				t.Errorf("upserted = %d, want the sync to go on by project id", len(st.upserted))
			}
		})
	}
}

// FR47: a path collision is logged once, by the service, with both repository ids and no path.
func TestListForRepo_gitlabPathCollisionLogsOneWarnWithBothIDs(t *testing.T) {
	moved := domain.GitLabProject{ID: 42, FullName: "acme/platform/api", Owner: "acme/platform", Name: "api"}
	gl := &fakeGitLab{project: moved, listed: []domain.Listed{{Number: 1, HeadSHA: "h", Status: "open"}}}
	st := newStore()
	st.repos[glRepoID] = glRepo
	st.pathHeldBy = "holder-id"
	logs := &logSink{}
	svc := app.NewService(st, &fakeGitHub{}, slog.New(logs), func() time.Time { return now }, app.WithGitLab(gl))

	if _, err := svc.ListForRepo(t.Context(), wsID, glRepoID); err != nil {
		t.Fatalf("ListForRepo() error = %v", err)
	}
	got := logs.warnAttrs()
	if len(got) != 1 {
		t.Fatalf("WARN lines = %v, want exactly 1", got)
	}
	if got[0]["repo_id"] != glRepoID || got[0]["other_repo_id"] != "holder-id" {
		t.Errorf("WARN = %v, want repo_id %s and other_repo_id holder-id", got[0], glRepoID)
	}
	for k, v := range got[0] {
		if strings.Contains(v, "platform") {
			t.Errorf("WARN attribute %s = %q names the path", k, v)
		}
	}
}

// Security L1: a fetched path that is not a plain project path, or contradicts its own parts, is not
// stored. The stored path stays, one WARN names the ids only, and the list sync goes on.
func TestListForRepo_gitlabRefusesAnImplausibleFetchedPath(t *testing.T) {
	tests := []struct {
		name string
		p    domain.GitLabProject
	}{
		{"dot segments", domain.GitLabProject{ID: 42, FullName: "x/../../../evil", Owner: "x/../../..", Name: "evil"}},
		{"userinfo-like", domain.GitLabProject{ID: 42, FullName: "@evil.example/x", Owner: "@evil.example", Name: "x"}},
		{"only dots", domain.GitLabProject{ID: 42, FullName: "../..", Owner: "..", Name: ".."}},
		{"bidi override", domain.GitLabProject{ID: 42, FullName: "Owner/Name\u202eexe", Owner: "Owner", Name: "Name\u202eexe"}},
		{"namespace contradicts the path", domain.GitLabProject{ID: 42, FullName: "acme/api", Owner: "contradicts", Name: "api"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl := &fakeGitLab{project: tc.p, listed: []domain.Listed{{Number: 1, HeadSHA: "h", Status: "open"}}}
			st := newStore()
			st.repos[glRepoID] = glRepo
			logs := &logSink{}
			svc := app.NewService(st, &fakeGitHub{}, slog.New(logs), func() time.Time { return now }, app.WithGitLab(gl))

			if _, err := svc.ListForRepo(t.Context(), wsID, glRepoID); err != nil {
				t.Fatalf("ListForRepo() error = %v", err)
			}
			if len(st.pathUpdates) != 0 {
				t.Errorf("UpdateRepoPath calls = %v, want none", st.pathUpdates)
			}
			if len(st.upserted) != 1 {
				t.Errorf("upserted = %d, want the sync to go on by project id", len(st.upserted))
			}
			got := logs.warnAttrs()
			if len(got) != 1 || got[0]["repo_id"] != glRepoID || got[0]["project_id"] != "42" {
				t.Fatalf("WARN lines = %v, want one with repo_id %s and project_id 42", got, glRepoID)
			}
			for k, v := range got[0] {
				for _, text := range []string{"evil", "Owner", "contradicts", "acme", "\u202e", ".."} {
					if strings.Contains(v, text) {
						t.Errorf("WARN attribute %s = %q carries path text %q", k, v, text)
					}
				}
			}
		})
	}
}

func TestListForRepo_gitlabFailureServesStored(t *testing.T) {
	tests := []struct {
		name      string
		gl        *fakeGitLab
		wantCalls []string
	}{
		{name: "not connected", gl: &fakeGitLab{projectErr: app.ErrNotConnected}, wantCalls: []string{"Project"}},
		{name: "no token", gl: &fakeGitLab{projectErr: &app.NoTokenError{Conn: "corp"}}, wantCalls: []string{"Project"}},
		{name: "rate limited", gl: &fakeGitLab{projectErr: &app.GitLabError{Status: 429, RateLimited: true}}, wantCalls: []string{"Project"}},
		{name: "project 500 sends no list request", gl: &fakeGitLab{projectErr: &app.GitLabError{Status: 500}}, wantCalls: []string{"Project", "Stats"}},
		{name: "list 500", gl: &fakeGitLab{listErr: &app.GitLabError{Status: 500}, statsErr: errors.New("x")}, wantCalls: []string{"Project", "ListPage", "Stats"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, svc := glStoreAndService(tc.gl, &fakeGitHub{})
			st.pulls = []domain.Pull{glPull(5, domain.DiffStats{})}

			got, err := svc.ListForRepo(t.Context(), wsID, glRepoID)
			if err != nil {
				t.Fatalf("ListForRepo() error = %v, want the stored rows with 200", err)
			}
			if len(got) != 1 || got[0].Pull.Number != 5 {
				t.Errorf("ListForRepo() = %+v, want the stored MR !5", got)
			}
			if diff := cmp.Diff(tc.wantCalls, tc.gl.calls); diff != "" {
				t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
			}
			if len(st.upserted) != 0 {
				t.Errorf("upserted = %+v, want nothing after a failed sync", st.upserted)
			}
		})
	}
}

func TestListForRepo_gitlabBackfillsStatsInOneBatch(t *testing.T) {
	gl := &fakeGitLab{
		project: domain.GitLabProject{ID: 42, FullName: "acme/team/api", Owner: "acme/team", Name: "api"},
		stats: map[int]domain.DiffStats{
			1: {Additions: 5, Deletions: 1, FilesCount: 2},
			2: {}, // an all-zero summary counts as none
			// 3 absent
		},
	}
	st, svc := glStoreAndService(gl, &fakeGitHub{})
	for n := 1; n <= 25; n++ {
		st.pulls = append(st.pulls, glPull(n, domain.DiffStats{}))
	}

	got, err := svc.ListForRepo(t.Context(), wsID, glRepoID)
	if err != nil {
		t.Fatalf("ListForRepo() error = %v", err)
	}
	wantBatch := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if diff := cmp.Diff([][]int{wantBatch}, gl.statsCalls); diff != "" {
		t.Errorf("Stats batches mismatch (-want +got):\n%s", diff)
	}
	if len(st.statsSet) != 1 || st.statsSet[glPull(1, domain.DiffStats{}).ID] != (domain.DiffStats{Additions: 5, Deletions: 1, FilesCount: 2}) {
		t.Errorf("stored stats = %+v, want only MR !1", st.statsSet)
	}
	if got[0].Pull.Additions != 5 || got[1].Pull.Additions != 0 {
		t.Errorf("listed additions = %d, %d, want 5 and 0 (unset)", got[0].Pull.Additions, got[1].Pull.Additions)
	}
}

func TestListForRepo_gitlabStatsFailureIsNonFatal(t *testing.T) {
	gl := &fakeGitLab{statsErr: &app.GitLabError{Status: 502}}
	st, svc := glStoreAndService(gl, &fakeGitHub{})
	st.pulls = []domain.Pull{glPull(1, domain.DiffStats{})}

	if _, err := svc.ListForRepo(t.Context(), wsID, glRepoID); err != nil {
		t.Fatalf("ListForRepo() error = %v, want nil", err)
	}
	if len(gl.statsCalls) != 1 || len(st.statsSet) != 0 {
		t.Errorf("stats calls = %v stored = %v, want one failed batch and nothing stored", gl.statsCalls, st.statsSet)
	}
}

func TestListForRepo_sameNameRepositoriesAreRoutedByProvider(t *testing.T) {
	gl := &fakeGitLab{project: domain.GitLabProject{ID: 42, FullName: "acme/api", Owner: "acme", Name: "api"}}
	gh := &fakeGitHub{}
	st, svc := glStoreAndService(gl, gh)
	st.repos[glRepoID] = domain.Repo{ID: glRepoID, Owner: "acme", Name: "api", Provider: domain.ProviderGitLab, Host: "gitlab.com", ProjectID: 42}

	if _, err := svc.ListForRepo(t.Context(), wsID, repoID); err != nil { // GitHub acme/api
		t.Fatalf("ListForRepo(github) error = %v", err)
	}
	if gh.listCalls != 1 || len(gl.calls) != 0 {
		t.Errorf("after the GitHub repo: github list calls = %d, gitlab calls = %v, want 1 and none", gh.listCalls, gl.calls)
	}
	if _, err := svc.ListForRepo(t.Context(), wsID, glRepoID); err != nil { // GitLab acme/api
		t.Fatalf("ListForRepo(gitlab) error = %v", err)
	}
	if gh.listCalls != 1 || len(gl.calls) == 0 {
		t.Errorf("after the GitLab repo: github list calls = %d, gitlab calls = %v, want 1 and some", gh.listCalls, gl.calls)
	}
}

func TestPoll_gitlab(t *testing.T) {
	project := domain.GitLabProject{ID: 42, FullName: "acme/team/api", Owner: "acme/team", Name: "api"}
	t.Run("success", func(t *testing.T) {
		gl := &fakeGitLab{project: project, listed: []domain.Listed{{Number: 1, HeadSHA: "h"}, {Number: 2, HeadSHA: "h"}}}
		gh := &fakeGitHub{}
		st, svc := glStoreAndService(gl, gh)
		res, err := svc.Poll(t.Context(), wsID, glRepoID)
		if err != nil || res.Synced != 2 {
			t.Fatalf("Poll() = %+v, %v, want 2 synced", res, err)
		}
		if !st.polledAt.Equal(now) || len(st.upserted) != 2 || gh.listCalls != 0 {
			t.Errorf("polledAt = %v upserted = %d github calls = %d, want now, 2 and 0", st.polledAt, len(st.upserted), gh.listCalls)
		}
	})

	tests := []struct {
		name     string
		gl       *fakeGitLab
		wantKind apperr.Kind
		wantCode string
		wantMsg  string
	}{
		{
			name: "not connected", gl: &fakeGitLab{projectErr: app.ErrNotConnected}, wantKind: apperr.KindValidation,
			wantCode: "repo_host_not_connected", wantMsg: "No GitLab connection for git.corp.example. Add one in Settings → GitLab Integration.",
		},
		{
			name: "no token", gl: &fakeGitLab{projectErr: &app.NoTokenError{Conn: "corp"}}, wantKind: apperr.KindConfig,
			wantCode: "config_error", wantMsg: "GitLab connection corp has no token",
		},
		{
			name: "rate limited", gl: &fakeGitLab{project: project, listErr: &app.GitLabError{Status: 429, RateLimited: true}},
			wantKind: apperr.KindExternalService, wantCode: "external_service", wantMsg: "GitLab request failed",
		},
		{
			name: "project failure", gl: &fakeGitLab{projectErr: &app.GitLabError{Status: 500}},
			wantKind: apperr.KindExternalService, wantCode: "external_service", wantMsg: "GitLab request failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, svc := glStoreAndService(tc.gl, &fakeGitHub{})
			_, err := svc.Poll(t.Context(), wsID, glRepoID)
			kind, ae := kindOf(t, err)
			if kind != tc.wantKind || ae.Code != tc.wantCode || ae.Message != tc.wantMsg {
				t.Errorf("Poll() = %v %q %q, want %v %q %q", kind, ae.Code, ae.Message, tc.wantKind, tc.wantCode, tc.wantMsg)
			}
			if !st.polledAt.IsZero() || len(st.upserted) != 0 {
				t.Errorf("polledAt = %v upserted = %d, want nothing stored after a failed poll", st.polledAt, len(st.upserted))
			}
			if tc.name == "not connected" {
				want := map[string]string{"host": "git.corp.example", "repo_id": glRepoID}
				if diff := cmp.Diff(want, ae.Details); diff != "" {
					t.Errorf("Poll() details mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestPoll_gitlabWithoutPortReadsAsNotConnected(t *testing.T) {
	st := newStore()
	st.repos[glRepoID] = glRepo
	gh := &fakeGitHub{}
	_, err := app.NewService(st, gh, glDiscardLog(), nil).Poll(t.Context(), wsID, glRepoID)
	if _, ae := kindOf(t, err); ae.Code != "repo_host_not_connected" {
		t.Errorf("Poll() code = %q, want repo_host_not_connected", ae.Code)
	}
	if gh.listCalls != 0 {
		t.Errorf("GitHub list calls = %d, want 0 (a GitLab repository never reaches GitHub)", gh.listCalls)
	}
}

func glFetched() domain.Fetched {
	return domain.Fetched{
		Listed: domain.Listed{Number: 7, Title: "mr title", Author: "ann", Branch: "f", Base: "main", HeadSHA: "abc", Status: "open"},
		Body:   ptr("Closes #12"),
		Files: []domain.File{
			{Path: "a.go", Additions: 1, Deletions: 2, Patch: ptr("p1")},
			{Path: "a.go", Additions: 3, Patch: ptr("p2")},
			{Path: "b.go", Additions: 4},
		},
		Commits: []domain.Commit{{SHA: "new"}, {SHA: "old"}}, // GitLab: newest first
	}
}

func TestDetail_gitlabFresh(t *testing.T) {
	tests := []struct {
		name      string
		stats     map[int]domain.DiffStats
		statsErr  error
		fetchStat domain.DiffStats
		want      domain.DiffStats
		wantStats bool // Stats is asked
	}{
		{name: "stats from the diff-stats source", stats: map[int]domain.DiffStats{7: {Additions: 20, Deletions: 5, FilesCount: 3}}, want: domain.DiffStats{Additions: 20, Deletions: 5, FilesCount: 3}, wantStats: true},
		{name: "no summary falls back to the files", stats: map[int]domain.DiffStats{}, want: domain.DiffStats{Additions: 8, Deletions: 2, FilesCount: 2}, wantStats: true},
		{name: "source failure falls back to the files", statsErr: errors.New("graphql"), want: domain.DiffStats{Additions: 8, Deletions: 2, FilesCount: 2}, wantStats: true},
		{name: "fetch already carries stats", fetchStat: domain.DiffStats{Additions: 1, Deletions: 1, FilesCount: 1}, want: domain.DiffStats{Additions: 1, Deletions: 1, FilesCount: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := glFetched()
			f.DiffStats = tc.fetchStat
			gl := &fakeGitLab{fetched: f, stats: tc.stats, statsErr: tc.statsErr, issue: domain.Issue{Number: 12, Title: "bug", State: "open"}}
			gh := &fakeGitHub{}
			st, svc := glStoreAndService(gl, gh)
			pr := glPull(7, domain.DiffStats{})
			st.pulls = []domain.Pull{pr}

			got, err := svc.Detail(t.Context(), wsID, pr.ID)
			if err != nil {
				t.Fatalf("Detail() error = %v", err)
			}
			if !got.FromGitHub || got.Pull.ID != pr.ID || got.Pull.Title != "mr title" || got.Pull.Additions != tc.want.Additions || got.Pull.FilesCount != tc.want.FilesCount {
				t.Errorf("Detail() pull = %+v, want the fetched MR with the stored id and stats %+v", got.Pull, tc.want)
			}
			if got.LinkedIssue == nil || got.LinkedIssue.Number != 12 {
				t.Errorf("Detail() linked issue = %+v, want #12", got.LinkedIssue)
			}
			wantFiles := []domain.File{{Path: "a.go", Additions: 4, Deletions: 2, Patch: ptr("p1\np2")}, {Path: "b.go", Additions: 4}}
			if diff := cmp.Diff(wantFiles, st.mirrors[pr.ID].Files); diff != "" {
				t.Errorf("mirrored files mismatch (-want +got):\n%s", diff)
			}
			if m := st.mirrors[pr.ID]; m.DiffStats != tc.want {
				t.Errorf("mirrored stats = %+v, want %+v", m.DiffStats, tc.want)
			}
			if diff := cmp.Diff([]domain.Commit{{SHA: "old"}, {SHA: "new"}}, got.Commits); diff != "" {
				t.Errorf("Detail() commits mismatch (-want +got, oldest first):\n%s", diff)
			}
			if asked := len(gl.statsCalls) > 0; asked != tc.wantStats {
				t.Errorf("Stats asked = %v, want %v", asked, tc.wantStats)
			}
			if !gl.getHasDL || gl.getDeadline <= 0 || gl.getDeadline > app.GitLabDetailTimeout {
				t.Errorf("Get deadline = %v (set %v), want within (0, %v]", gl.getDeadline, gl.getHasDL, app.GitLabDetailTimeout)
			}
			if gl.getRepo != glRepo || gh.listCalls != 0 || len(gh.fetchCalls) != 0 || len(gh.issueCalls) != 0 {
				t.Errorf("Get repo = %+v github calls = %d/%v/%v, want the GitLab repo and no GitHub call", gl.getRepo, gh.listCalls, gh.fetchCalls, gh.issueCalls)
			}
		})
	}
}

func TestDetail_gitlabIssueFailureOmitsIssue(t *testing.T) {
	gl := &fakeGitLab{fetched: glFetched(), stats: map[int]domain.DiffStats{}, issueErr: &app.GitLabError{Status: 404}}
	st, svc := glStoreAndService(gl, &fakeGitHub{})
	pr := glPull(7, domain.DiffStats{})
	st.pulls = []domain.Pull{pr}

	got, err := svc.Detail(t.Context(), wsID, pr.ID)
	if err != nil || got.LinkedIssue != nil || !got.FromGitHub {
		t.Errorf("Detail() = issue %+v fresh %v err %v, want the fresh detail without an issue", got.LinkedIssue, got.FromGitHub, err)
	}
}

func TestDetail_gitlabServesStored(t *testing.T) {
	tests := []struct {
		name      string
		getErr    error
		wantCalls []string
	}{
		{name: "not connected", getErr: app.ErrNotConnected, wantCalls: []string{"Get"}},
		{name: "no token", getErr: &app.NoTokenError{Conn: "corp"}, wantCalls: []string{"Get"}},
		{name: "gitlab 500", getErr: &app.GitLabError{Status: 500}, wantCalls: []string{"Get"}},
		{name: "deadline", getErr: context.DeadlineExceeded, wantCalls: []string{"Get"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gl := &fakeGitLab{fetched: glFetched(), getErr: tc.getErr}
			st, svc := glStoreAndService(gl, &fakeGitHub{})
			pr := glPull(7, domain.DiffStats{Additions: 1})
			pr.Status = "needs_review"
			st.pulls = []domain.Pull{pr}
			st.files[pr.ID] = []domain.File{{Path: "s.go"}}
			st.commits[pr.ID] = []domain.Commit{{SHA: "c"}}

			got, err := svc.Detail(t.Context(), wsID, pr.ID)
			if err != nil {
				t.Fatalf("Detail() error = %v", err)
			}
			if got.FromGitHub || got.LinkedIssue != nil || len(got.Files) != 1 || len(got.Commits) != 1 {
				t.Errorf("Detail() = %+v, want the stored mirror without an issue", got)
			}
			if diff := cmp.Diff(tc.wantCalls, gl.calls); diff != "" {
				t.Errorf("GitLab calls mismatch (-want +got):\n%s", diff)
			}
			if len(st.mirrors) != 0 {
				t.Errorf("mirrors = %v, want nothing mirrored", st.mirrors)
			}
		})
	}
}

func TestComments_gitlabSendsNothing(t *testing.T) {
	gl := &fakeGitLab{}
	gh := &fakeGitHub{comments: []domain.ReviewComment{{ID: 1}}}
	st, svc := glStoreAndService(gl, gh)
	pr := glPull(7, domain.DiffStats{})
	st.pulls = []domain.Pull{pr}

	got, err := svc.Comments(t.Context(), wsID, pr.ID)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Comments() = %#v, %v, want an empty list", got, err)
	}
	if len(gl.calls) != 0 {
		t.Errorf("GitLab calls = %v, want none", gl.calls)
	}

	_, err = svc.PostComment(t.Context(), wsID, pr.ID, app.CommentRequest{Path: "a.go", Line: 1, Body: "x"})
	kind, ae := kindOf(t, err)
	if kind != apperr.KindInvalidInput || ae.Code != "gitlab_unavailable" || ae.Message != "Posting to GitLab merge requests is not available yet." {
		t.Errorf("PostComment() = %v %q %q, want 400 gitlab_unavailable with the not-yet message", kind, ae.Code, ae.Message)
	}
	if len(gh.createCalls) != 0 || len(gl.calls) != 0 {
		t.Errorf("create calls = %d, gitlab calls = %v, want none", len(gh.createCalls), gl.calls)
	}
}

func glDiscardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

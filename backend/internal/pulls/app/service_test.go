package app_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/pulls/app"
	"dev-digest-go/backend/internal/pulls/domain"
)

func ptr[T any](v T) *T { return &v }

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

const (
	wsID   = "ws-1"
	repoID = "repo-1"
)

// fakeStore is an in-memory store holding one repo.
type fakeStore struct {
	mu        sync.Mutex
	repos     map[string]domain.Repo
	pulls     []domain.Pull
	files     map[string][]domain.File
	commits   map[string][]domain.Commit
	rollups   map[string]domain.Rollup
	upserted  []domain.Listed
	statsSet  map[string]domain.DiffStats
	mirrors   map[string]domain.DetailMirror
	polledAt  time.Time
	mirrorErr error
}

func newStore() *fakeStore {
	return &fakeStore{
		repos:    map[string]domain.Repo{repoID: {ID: repoID, Owner: "acme", Name: "api"}},
		files:    map[string][]domain.File{},
		commits:  map[string][]domain.Commit{},
		rollups:  map[string]domain.Rollup{},
		statsSet: map[string]domain.DiffStats{},
		mirrors:  map[string]domain.DetailMirror{},
	}
}

func (s *fakeStore) FindRepo(_ context.Context, ws, id string) (domain.Repo, error) {
	r, ok := s.repos[id]
	if !ok || ws != wsID {
		return domain.Repo{}, domain.ErrRepoNotFound
	}
	return r, nil
}

func (s *fakeStore) MarkPolled(_ context.Context, _, _ string, at time.Time) error {
	s.polledAt = at
	return nil
}

func (s *fakeStore) FindPull(_ context.Context, ws, id string) (domain.Pull, error) {
	for _, p := range s.pulls {
		if p.ID == id && ws == wsID {
			return p, nil
		}
	}
	return domain.Pull{}, domain.ErrPullNotFound
}

func (s *fakeStore) ListPulls(context.Context, string, string) ([]domain.Pull, error) {
	return append([]domain.Pull(nil), s.pulls...), nil
}

func (s *fakeStore) UpsertListed(_ context.Context, _, _ string, l []domain.Listed) error {
	s.upserted = append(s.upserted, l...)
	return nil
}

func (s *fakeStore) SetDiffStats(_ context.Context, id string, st domain.DiffStats) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statsSet[id] = st
	return nil
}

func (s *fakeStore) ReplaceDetail(_ context.Context, id string, m domain.DetailMirror) error {
	if s.mirrorErr != nil {
		return s.mirrorErr
	}
	s.mirrors[id] = m
	return nil
}

func (s *fakeStore) ListFiles(_ context.Context, id string) ([]domain.File, error) {
	return s.files[id], nil
}

func (s *fakeStore) ListCommits(_ context.Context, id string) ([]domain.Commit, error) {
	return s.commits[id], nil
}

func (s *fakeStore) Rollups(_ context.Context, _ string, ids []string) (map[string]domain.Rollup, error) {
	out := map[string]domain.Rollup{}
	for _, id := range ids {
		out[id] = s.rollups[id]
	}
	return out, nil
}

// fakeGitHub answers from fields and counts calls.
type fakeGitHub struct {
	mu          sync.Mutex
	listed      []domain.Listed
	listErr     error
	fetched     map[int]domain.Fetched
	fetchErr    error
	fetchHook   func()
	issue       domain.Issue
	issueErr    error
	comments    []domain.ReviewComment
	commentsErr error
	created     domain.ReviewComment
	createErr   error

	listCalls    int
	fetchCalls   []int
	issueCalls   []int
	createCalls  []domain.CommentInput
	createdOwner string
}

func (g *fakeGitHub) ListPullRequests(context.Context, string, string) ([]domain.Listed, error) {
	g.mu.Lock()
	g.listCalls++
	g.mu.Unlock()
	return g.listed, g.listErr
}

func (g *fakeGitHub) GetPullRequest(_ context.Context, _, _ string, n int) (domain.Fetched, error) {
	g.mu.Lock()
	g.fetchCalls = append(g.fetchCalls, n)
	hook := g.fetchHook
	g.mu.Unlock()
	if hook != nil {
		hook()
	}
	if g.fetchErr != nil {
		return domain.Fetched{}, g.fetchErr
	}
	return g.fetched[n], nil
}

func (g *fakeGitHub) GetIssue(_ context.Context, _, _ string, n int) (domain.Issue, error) {
	g.issueCalls = append(g.issueCalls, n)
	return g.issue, g.issueErr
}

func (g *fakeGitHub) ListReviewComments(context.Context, string, string, int) ([]domain.ReviewComment, error) {
	return g.comments, g.commentsErr
}

func (g *fakeGitHub) CreateReviewComment(_ context.Context, owner, _ string, _ int, in domain.CommentInput) (domain.ReviewComment, error) {
	g.createCalls = append(g.createCalls, in)
	g.createdOwner = owner
	return g.created, g.createErr
}

func newService(st *fakeStore, gh *fakeGitHub) *app.Service {
	return app.NewService(st, gh, slog.New(slog.DiscardHandler), func() time.Time { return now })
}

func stored(n int, stats domain.DiffStats) domain.Pull {
	return domain.Pull{
		ID: "pr-" + string(rune('a'+n)), RepoID: repoID, Number: n, Title: "t", Author: "a", Branch: "b", Base: "main",
		HeadSHA: "head", Status: "open", Additions: stats.Additions, Deletions: stats.Deletions, FilesCount: stats.FilesCount,
	}
}

func kindOf(t *testing.T, err error) (apperr.Kind, *apperr.Error) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v, want *apperr.Error", err)
	}
	return ae.Kind, ae
}

func TestListForRepo_unknownRepo(t *testing.T) {
	_, err := newService(newStore(), &fakeGitHub{}).ListForRepo(t.Context(), wsID, "nope")
	kind, ae := kindOf(t, err)
	if kind != apperr.KindNotFound || ae.Message != "Repo not found" {
		t.Errorf("ListForRepo(unknown) = %v %q, want not_found %q", kind, ae.Message, "Repo not found")
	}
}

func TestListForRepo_noTokenServesStoredRows(t *testing.T) {
	st := newStore()
	st.pulls = []domain.Pull{stored(2, domain.DiffStats{}), stored(1, domain.DiffStats{})}
	gh := &fakeGitHub{listErr: app.ErrNoToken}

	items, err := newService(st, gh).ListForRepo(t.Context(), wsID, repoID)
	if err != nil {
		t.Fatalf("ListForRepo() error = %v", err)
	}
	if len(items) != 2 || items[0].Pull.Number != 2 || items[0].Status != domain.StatusNeedsReview {
		t.Errorf("ListForRepo() = %+v, want the 2 stored rows in store order, needs_review", items)
	}
	if len(st.upserted) != 0 || len(gh.fetchCalls) != 0 {
		t.Errorf("no token: upserts = %d, detail fetches = %v, want none", len(st.upserted), gh.fetchCalls)
	}
}

func TestListForRepo_syncsListedAndKeepsRollups(t *testing.T) {
	st := newStore()
	st.pulls = []domain.Pull{stored(7, domain.DiffStats{Additions: 1})}
	st.rollups[st.pulls[0].ID] = domain.Rollup{Score: ptr(80), LatestReviewIDs: []string{"r1"}}
	gh := &fakeGitHub{listed: []domain.Listed{{Number: 7, Title: "new", Status: "open"}, {Number: 8, Title: "x", Status: "open"}}}

	items, err := newService(st, gh).ListForRepo(t.Context(), wsID, repoID)
	if err != nil {
		t.Fatalf("ListForRepo() error = %v", err)
	}
	if diff := cmp.Diff(gh.listed, st.upserted); diff != "" {
		t.Errorf("upserted mismatch (-want +got):\n%s", diff)
	}
	if got := items[0].Rollup.Score; got == nil || *got != 80 {
		t.Errorf("rollup score = %v, want 80", got)
	}
}

func TestListForRepo_githubFailureServesStoredAndStillBackfills(t *testing.T) {
	st := newStore()
	st.pulls = []domain.Pull{stored(1, domain.DiffStats{})}
	gh := &fakeGitHub{
		listErr: &app.GitHubError{Status: 500, Message: "boom"},
		fetched: map[int]domain.Fetched{1: {DiffStats: domain.DiffStats{Additions: 5, Deletions: 1, FilesCount: 2}}},
	}
	items, err := newService(st, gh).ListForRepo(t.Context(), wsID, repoID)
	if err != nil {
		t.Fatalf("ListForRepo() error = %v, want nil (GitHub failures are swallowed)", err)
	}
	if got := items[0].Pull; got.Additions != 5 || got.FilesCount != 2 {
		t.Errorf("backfilled pull = %+v, want stats 5/1/2", got)
	}
}

func TestListForRepo_backfillLimitAndConcurrency(t *testing.T) {
	st := newStore()
	for n := 1; n <= 12; n++ {
		st.pulls = append(st.pulls, stored(n, domain.DiffStats{}))
	}
	started := make(chan struct{}, 12)
	gate := make(chan struct{})
	var mu sync.Mutex
	inflight, maxInflight := 0, 0
	gh := &fakeGitHub{fetched: map[int]domain.Fetched{}} // a token exists and the list is empty
	gh.fetchHook = func() {
		mu.Lock()
		inflight++
		maxInflight = max(maxInflight, inflight)
		mu.Unlock()
		started <- struct{}{}
		<-gate
		mu.Lock()
		inflight--
		mu.Unlock()
	}
	for n := 1; n <= 12; n++ {
		gh.fetched[n] = domain.Fetched{DiffStats: domain.DiffStats{Additions: n, FilesCount: 1}}
	}

	done := make(chan []app.ListItem)
	go func() {
		items, err := newService(st, gh).ListForRepo(t.Context(), wsID, repoID)
		if err != nil {
			t.Errorf("ListForRepo() error = %v", err)
		}
		done <- items
	}()
	for range app.BackfillConcurrency {
		<-started // the first four fetches are in flight and blocked on the gate
	}
	close(gate)
	items := <-done

	if len(gh.fetchCalls) != app.BackfillLimit {
		t.Errorf("detail fetches = %d, want %d", len(gh.fetchCalls), app.BackfillLimit)
	}
	if maxInflight != app.BackfillConcurrency {
		t.Errorf("max concurrent fetches = %d, want %d", maxInflight, app.BackfillConcurrency)
	}
	filled := 0
	for _, it := range items {
		if it.Pull.FilesCount == 1 {
			filled++
		}
	}
	if filled != app.BackfillLimit || len(st.statsSet) != app.BackfillLimit {
		t.Errorf("filled = %d, stored stats = %d, want %d each", filled, len(st.statsSet), app.BackfillLimit)
	}
}

func TestListForRepo_derivedStatus(t *testing.T) {
	st := newStore()
	reviewed := stored(1, domain.DiffStats{Additions: 1})
	reviewed.LastReviewedSHA = ptr("head")
	reviewed.UpdatedAt = ptr(now.Add(-3 * 24 * time.Hour))
	staleOne := stored(2, domain.DiffStats{Additions: 1})
	staleOne.LastReviewedSHA = ptr("head")
	staleOne.UpdatedAt = ptr(now.Add(-8 * 24 * time.Hour))
	merged := stored(3, domain.DiffStats{Additions: 1})
	merged.Status = "merged"
	st.pulls = []domain.Pull{reviewed, staleOne, merged}

	items, err := newService(st, &fakeGitHub{listErr: app.ErrNoToken}).ListForRepo(t.Context(), wsID, repoID)
	if err != nil {
		t.Fatalf("ListForRepo() error = %v", err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Status)
	}
	if diff := cmp.Diff([]string{"reviewed", "stale", "merged"}, got); diff != "" {
		t.Errorf("statuses mismatch (-want +got):\n%s", diff)
	}
}

func TestPoll(t *testing.T) {
	tests := []struct {
		name       string
		repo       string
		listErr    error
		wantKind   apperr.Kind
		wantMsg    string
		wantSynced int
		wantPolled bool
	}{
		{name: "ok", repo: repoID, wantSynced: 3, wantPolled: true},
		{name: "unknown repo", repo: "nope", wantKind: apperr.KindNotFound, wantMsg: "Repo not found"},
		{name: "no token", repo: repoID, listErr: app.ErrNoToken, wantKind: apperr.KindConfig, wantMsg: "GITHUB_TOKEN is not configured"},
		{name: "github 503", repo: repoID, listErr: &app.GitHubError{Status: 503, Message: "down"}, wantKind: apperr.KindExternalService, wantMsg: "GitHub request failed"},
		{name: "network error", repo: repoID, listErr: errors.New("dial tcp: refused"), wantKind: apperr.KindExternalService, wantMsg: "GitHub request failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			gh := &fakeGitHub{listErr: tc.listErr, listed: []domain.Listed{{Number: 1}, {Number: 2}, {Number: 3}}}
			res, err := newService(st, gh).Poll(t.Context(), wsID, tc.repo)
			if tc.wantMsg != "" {
				kind, ae := kindOf(t, err)
				if kind != tc.wantKind || ae.Message != tc.wantMsg {
					t.Errorf("Poll() error = %v %q, want %v %q", kind, ae.Message, tc.wantKind, tc.wantMsg)
				}
			} else if err != nil {
				t.Fatalf("Poll() error = %v", err)
			}
			if res.Synced != tc.wantSynced {
				t.Errorf("Poll().Synced = %d, want %d", res.Synced, tc.wantSynced)
			}
			if got := !st.polledAt.IsZero(); got != tc.wantPolled {
				t.Errorf("last_polled_at set = %v, want %v", got, tc.wantPolled)
			}
			if tc.wantPolled && !st.polledAt.Equal(now) {
				t.Errorf("last_polled_at = %v, want %v", st.polledAt, now)
			}
			if tc.wantPolled && len(st.upserted) != 3 {
				t.Errorf("upserted = %d, want 3", len(st.upserted))
			}
			if gh.listCalls > 1 {
				t.Errorf("list calls = %d, want at most 1", gh.listCalls)
			}
		})
	}
}

func freshFetched() domain.Fetched {
	return domain.Fetched{
		Listed:    domain.Listed{Number: 7, Title: "gh title", Author: "gh", Branch: "f", Base: "main", HeadSHA: "h2", Status: "open"},
		Body:      ptr("Fixes #12"),
		DiffStats: domain.DiffStats{Additions: 9, Deletions: 3, FilesCount: 2},
		Files: []domain.File{
			{Path: "a.go", Additions: 1, Patch: ptr("p1")},
			{Path: "b.go", Additions: 2},
			{Path: "a.go", Additions: 3, Patch: ptr("p2")},
		},
		Commits: []domain.Commit{{SHA: "c1", Message: "m", Author: "x"}},
	}
}

func TestDetail_freshFromGitHub(t *testing.T) {
	st := newStore()
	st.pulls = []domain.Pull{stored(7, domain.DiffStats{})}
	id := st.pulls[0].ID
	gh := &fakeGitHub{fetched: map[int]domain.Fetched{7: freshFetched()}, issue: domain.Issue{Number: 12, Title: "bug", State: "open"}}

	got, err := newService(st, gh).Detail(t.Context(), wsID, id)
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if !got.FromGitHub || got.Pull.Status != "open" || got.Pull.ID != id || got.Pull.Title != "gh title" {
		t.Errorf("Detail() pull = %+v fresh=%v, want GitHub values with the stored id", got.Pull, got.FromGitHub)
	}
	wantFiles := []domain.File{{Path: "a.go", Additions: 4, Patch: ptr("p1\np2")}, {Path: "b.go", Additions: 2}}
	if diff := cmp.Diff(wantFiles, got.Files); diff != "" {
		t.Errorf("Detail() files mismatch (-want +got):\n%s", diff)
	}
	if got.LinkedIssue == nil || got.LinkedIssue.Number != 12 {
		t.Errorf("Detail() linked issue = %+v, want #12", got.LinkedIssue)
	}
	m := st.mirrors[id]
	if diff := cmp.Diff(wantFiles, m.Files); diff != "" {
		t.Errorf("mirrored files mismatch (-want +got):\n%s", diff)
	}
	if m.Body == nil || *m.Body != "Fixes #12" || m.Additions != 9 || m.FilesCount != 2 || len(m.Commits) != 1 {
		t.Errorf("mirror = %+v, want body, stats 9/3/2 and 1 commit", m)
	}
}

func TestDetail_servesStoredMirror(t *testing.T) {
	tests := []struct {
		name      string
		fetchErr  error
		mirrorErr error
	}{
		{name: "no token", fetchErr: app.ErrNoToken},
		{name: "github 500", fetchErr: &app.GitHubError{Status: 500, Message: "boom"}},
		{name: "database mirror fails", mirrorErr: errors.New("db down")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			st.mirrorErr = tc.mirrorErr
			pr := stored(7, domain.DiffStats{Additions: 1})
			pr.Status = "needs_review"
			st.pulls = []domain.Pull{pr}
			st.files[pr.ID] = []domain.File{{Path: "s.go", Patch: ptr("p")}}
			st.commits[pr.ID] = []domain.Commit{{SHA: "c"}}
			gh := &fakeGitHub{fetchErr: tc.fetchErr, fetched: map[int]domain.Fetched{7: freshFetched()}}

			got, err := newService(st, gh).Detail(t.Context(), wsID, pr.ID)
			if err != nil {
				t.Fatalf("Detail() error = %v", err)
			}
			if got.FromGitHub || got.Pull.Status != "needs_review" || got.LinkedIssue != nil {
				t.Errorf("Detail() = fresh %v status %q issue %v, want the stored mirror with raw status and no issue", got.FromGitHub, got.Pull.Status, got.LinkedIssue)
			}
			if len(got.Files) != 1 || got.Files[0].Path != "s.go" || len(got.Commits) != 1 {
				t.Errorf("Detail() files/commits = %+v / %+v, want the stored ones", got.Files, got.Commits)
			}
		})
	}
}

func TestDetail_issueFailureOrNoReferenceOmitsIssue(t *testing.T) {
	tests := []struct {
		name      string
		body      *string
		issueErr  error
		wantCalls int
	}{
		{name: "issue 404", body: ptr("see #99"), issueErr: &app.GitHubError{Status: 404, Message: "Not Found"}, wantCalls: 1},
		{name: "no reference", body: ptr("nothing"), wantCalls: 0},
		{name: "null body", body: nil, wantCalls: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			st.pulls = []domain.Pull{stored(7, domain.DiffStats{})}
			f := freshFetched()
			f.Body = tc.body
			gh := &fakeGitHub{fetched: map[int]domain.Fetched{7: f}, issueErr: tc.issueErr}
			got, err := newService(st, gh).Detail(t.Context(), wsID, st.pulls[0].ID)
			if err != nil {
				t.Fatalf("Detail() error = %v", err)
			}
			if !got.FromGitHub || got.LinkedIssue != nil {
				t.Errorf("Detail() fresh = %v issue = %v, want fresh without issue", got.FromGitHub, got.LinkedIssue)
			}
			if len(gh.issueCalls) != tc.wantCalls {
				t.Errorf("issue fetches = %v, want %d", gh.issueCalls, tc.wantCalls)
			}
		})
	}
}

func TestPullNotFound(t *testing.T) {
	svc := newService(newStore(), &fakeGitHub{})
	_, detailErr := svc.Detail(t.Context(), wsID, "nope")
	_, commentsErr := svc.Comments(t.Context(), wsID, "nope")
	_, postErr := svc.PostComment(t.Context(), wsID, "nope", app.CommentRequest{Path: "a", Line: 1, Body: "x"})
	for name, err := range map[string]error{"Detail": detailErr, "Comments": commentsErr, "PostComment": postErr} {
		kind, ae := kindOf(t, err)
		if kind != apperr.KindNotFound || ae.Message != "Pull request not found" {
			t.Errorf("%s(unknown) = %v %q, want not_found %q", name, kind, ae.Message, "Pull request not found")
		}
	}
}

func TestComments(t *testing.T) {
	want := []domain.ReviewComment{{ID: 1, Path: "a.go", Side: "RIGHT"}}
	tests := []struct {
		name string
		gh   *fakeGitHub
		want []domain.ReviewComment
	}{
		{name: "proxied", gh: &fakeGitHub{comments: want}, want: want},
		{name: "no token", gh: &fakeGitHub{commentsErr: app.ErrNoToken}, want: []domain.ReviewComment{}},
		{name: "github 500", gh: &fakeGitHub{commentsErr: &app.GitHubError{Status: 500}}, want: []domain.ReviewComment{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			st.pulls = []domain.Pull{stored(7, domain.DiffStats{})}
			got, err := newService(st, tc.gh).Comments(t.Context(), wsID, st.pulls[0].ID)
			if err != nil {
				t.Fatalf("Comments() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Comments() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPostComment(t *testing.T) {
	created := domain.ReviewComment{ID: 55, Path: "a.ts", Side: "RIGHT", Body: "x"}
	tests := []struct {
		name        string
		createErr   error
		req         app.CommentRequest
		wantCode    string
		wantMsg     string
		wantCause   string
		wantCreated bool
	}{
		{name: "created", req: app.CommentRequest{Path: "a.ts", Line: 3, Body: "x"}, wantCreated: true},
		{name: "reply", req: app.CommentRequest{Path: "a.ts", Line: 3, Body: "x", InReplyTo: ptr(int64(5))}, wantCreated: true},
		{name: "no token", createErr: app.ErrNoToken, req: app.CommentRequest{Path: "a.ts", Line: 3, Body: "x"},
			wantCode: "github_unavailable", wantMsg: "Connect a GitHub token to post comments."},
		{name: "github rejects", createErr: &app.GitHubError{Status: 422, Message: "Validation Failed"}, req: app.CommentRequest{Path: "a.ts", Line: 3, Body: "x"},
			wantCode: "github_comment_failed", wantMsg: "Validation Failed", wantCause: "github: HTTP 422: Validation Failed"},
		{name: "network error", createErr: errors.New("connection reset"), req: app.CommentRequest{Path: "a.ts", Line: 3, Body: "x"},
			wantCode: "github_comment_failed", wantMsg: "Failed to post the comment to GitHub.", wantCause: "connection reset"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore()
			st.pulls = []domain.Pull{stored(7, domain.DiffStats{})}
			gh := &fakeGitHub{createErr: tc.createErr, created: created}
			got, err := newService(st, gh).PostComment(t.Context(), wsID, st.pulls[0].ID, tc.req)

			if len(gh.createCalls) != 1 {
				t.Fatalf("create calls = %d, want exactly 1 (never retried)", len(gh.createCalls))
			}
			in := gh.createCalls[0]
			if in.CommitID != "head" || in.Path != tc.req.Path || in.Line != tc.req.Line || (in.InReplyTo == nil) != (tc.req.InReplyTo == nil) {
				t.Errorf("create input = %+v, want head sha, path, line and reply from %+v", in, tc.req)
			}
			if tc.wantCode == "" {
				if err != nil || !tc.wantCreated || got.ID != 55 {
					t.Errorf("PostComment() = %+v, %v, want the created comment", got, err)
				}
				return
			}
			kind, ae := kindOf(t, err)
			if kind != apperr.KindInvalidInput || ae.Code != tc.wantCode || ae.Message != tc.wantMsg {
				t.Errorf("PostComment() error = %v %q %q, want invalid_input %q %q", kind, ae.Code, ae.Message, tc.wantCode, tc.wantMsg)
			}
			var cause any
			if tc.wantCause != "" {
				cause = map[string]string{"cause": tc.wantCause}
			}
			if diff := cmp.Diff(cause, ae.Details); diff != "" {
				t.Errorf("PostComment() details mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

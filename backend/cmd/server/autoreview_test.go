package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	autoreviewapp "dev-digest-go/backend/internal/autoreview/app"
	autoreviewdomain "dev-digest-go/backend/internal/autoreview/domain"
	"dev-digest-go/backend/internal/config"
	gitlabconnapp "dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	reposdomain "dev-digest-go/backend/internal/repos/domain"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	settingsapp "dev-digest-go/backend/internal/settings/app"
	spendapp "dev-digest-go/backend/internal/spend/app"
	spenddomain "dev-digest-go/backend/internal/spend/domain"
)

// Unit tests of the adapters that bind the automatic-reviews context (plan S16), then the wiring
// through the real server.

func TestAutoSettings_ReadsTheFullObjectWithEnabledAtAsATime(t *testing.T) {
	at := time.Date(2026, 10, 25, 8, 30, 0, 123_000_000, time.UTC)
	tests := []struct {
		name string
		bag  settingsapp.Bag
		want autoreviewdomain.Setting
	}{
		{name: "no row is the defaults, off", bag: settingsapp.Bag{},
			want: autoreviewdomain.Setting{AgentIDs: []string{}, OnNewPR: true, OnNewCommits: true}},
		{name: "an armed object carries enabled_at", bag: settingsapp.Bag{settingsapp.AutomaticReviewsKey: json.RawMessage(
			`{"enabled":true,"agent_ids":["a1","a2"],"on_new_pr":true,"on_new_commits":false,"enabled_at":"2026-10-25T08:30:00.123Z"}`)},
			want: autoreviewdomain.Setting{Enabled: true, AgentIDs: []string{"a1", "a2"}, OnNewPR: true, EnabledAt: &at}},
		{name: "a legacy boolean is enabled without agents", bag: settingsapp.Bag{settingsapp.AutomaticReviewsKey: json.RawMessage(`true`)},
			want: autoreviewdomain.Setting{Enabled: true, AgentIDs: []string{}, OnNewPR: true, OnNewCommits: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := autoSettings{settings: fakeBag{bag: tc.bag}}.AutomaticReviews(t.Context(), "ws")
			if err != nil {
				t.Fatalf("AutomaticReviews() error = %v, want nil", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("AutomaticReviews() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAutoSettings_ReadErrorIsReturned(t *testing.T) {
	boom := errors.New("boom")
	if _, err := (autoSettings{settings: fakeBag{err: boom}}).AutomaticReviews(t.Context(), "ws"); !errors.Is(err, boom) {
		t.Errorf("AutomaticReviews() error = %v, want it to wrap %v", err, boom)
	}
}

func TestAutoInterval_ClampsLikeThePoller(t *testing.T) {
	tests := []struct {
		name string
		raw  string // "" = no row
		want time.Duration
	}{
		{name: "no row is the poller default", want: 5 * time.Minute},
		{name: "null is the poller default", raw: `null`, want: 5 * time.Minute},
		{name: "a stored value", raw: `15`, want: 15 * time.Minute},
		{name: "above the ceiling is cut", raw: `99999`, want: 24 * time.Hour},
		{name: "below a minute is raised", raw: `0.1`, want: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bag := settingsapp.Bag{}
			if tc.raw != "" {
				bag[pollingIntervalKey] = json.RawMessage(tc.raw)
			}
			got, err := autoInterval{intervals: settingsIntervals{settings: fakeBag{bag: bag}}}.Interval(t.Context(), "ws")
			if err != nil || got != tc.want {
				t.Errorf("Interval(%q) = %v, %v, want %v, nil", tc.raw, got, err, tc.want)
			}
		})
	}
}

type fakeAutoStarter struct {
	res reviewsapp.StartResult
	err error
}

func (f fakeAutoStarter) StartAuto(context.Context, string, string, []string) (reviewsapp.StartResult, error) {
	return f.res, f.err
}

func TestAutoStarter_CountsRunsAndTranslatesTheCap(t *testing.T) {
	capErr := apperr.PaymentRequired("cap").WithCode(spendapp.CodeCapReached)
	other := apperr.NotFound("no such PR")
	tests := []struct {
		name     string
		starter  fakeAutoStarter
		wantRuns int
		wantCap  bool
		wantErr  error // an error that must stay visible, nil = none
	}{
		{name: "runs created", starter: fakeAutoStarter{res: reviewsapp.StartResult{Runs: make([]reviewsapp.RunTarget, 3)}}, wantRuns: 3},
		{name: "a 402 is the cap", starter: fakeAutoStarter{err: capErr}, wantCap: true, wantErr: capErr},
		{name: "another error passes through", starter: fakeAutoStarter{err: other}, wantErr: other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs, err := autoStarter{reviews: tc.starter}.StartAuto(t.Context(), "ws", "pr", []string{"a"})
			if runs != tc.wantRuns {
				t.Errorf("StartAuto() runs = %d, want %d", runs, tc.wantRuns)
			}
			if got := errors.Is(err, autoreviewapp.ErrCapReached); got != tc.wantCap {
				t.Errorf("StartAuto() error %v is ErrCapReached = %v, want %v", err, got, tc.wantCap)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("StartAuto() error = %v, want it to wrap %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && err != nil {
				t.Errorf("StartAuto() error = %v, want nil", err)
			}
		})
	}
}

type fakeSpend struct {
	admitErr error
	op       string
	status   spenddomain.Status
	statErr  error
}

func (f *fakeSpend) Admit(_ context.Context, _, op string) error { f.op = op; return f.admitErr }
func (f *fakeSpend) Status(context.Context, string) (spenddomain.Status, error) {
	return f.status, f.statErr
}

func TestAutoBudget_AdmitsWithTheAutoOpAndTranslatesTheCap(t *testing.T) {
	boom := errors.New("read failed")
	tests := []struct {
		name    string
		admit   error
		wantCap bool
		wantErr bool
	}{
		{name: "allowed"},
		{name: "cap reached", admit: apperr.PaymentRequired("cap"), wantCap: true, wantErr: true},
		{name: "check failed", admit: boom, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := &fakeSpend{admitErr: tc.admit}
			err := autoBudget{spend: sp}.Admit(t.Context(), "ws")
			if (err != nil) != tc.wantErr || errors.Is(err, autoreviewapp.ErrCapReached) != tc.wantCap {
				t.Errorf("Admit() = %v, want error %v, cap %v", err, tc.wantErr, tc.wantCap)
			}
			if sp.op != spendapp.OpAutoReviewStart {
				t.Errorf("Admit() op = %q, want %q", sp.op, spendapp.OpAutoReviewStart)
			}
		})
	}
}

func TestAutoCaps_ReadsTheStatusWithoutAdmit(t *testing.T) {
	capped := spenddomain.Micros(1_000_000)
	tests := []struct {
		name    string
		status  spenddomain.Status
		err     error
		want    bool
		wantErr bool
	}{
		{name: "no cap", status: spenddomain.Status{Spent: 5}},
		{name: "under the cap", status: spenddomain.Status{Cap: &capped, Spent: 5}},
		{name: "at the cap", status: spenddomain.Status{Cap: &capped, Spent: capped}, want: true},
		{name: "read error", err: errors.New("boom"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp := &fakeSpend{status: tc.status, statErr: tc.err}
			got, err := autoCaps{spend: sp}.CapReached(t.Context(), "ws")
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Errorf("CapReached() = %v, %v, want %v, error %v", got, err, tc.want, tc.wantErr)
			}
			if sp.op != "" {
				t.Errorf("CapReached() called Admit (op %q), want a log-free Status read", sp.op)
			}
		})
	}
}

// fakeRepoList serves a fixed repository list (and an error) to autoTokens.
type fakeRepoList struct {
	repos []reposdomain.Repo
	err   error
}

func (f fakeRepoList) List(context.Context, string) ([]reposdomain.Repo, error) {
	return f.repos, f.err
}

// fakeConnTokens serves connections by host key.
type fakeConnTokens struct {
	byHost map[string]gitlabconnapp.Conn
	err    error
}

func (f fakeConnTokens) Resolve(_ context.Context, host string) (gitlabconnapp.Conn, bool, error) {
	c, ok := f.byHost[host]
	return c, ok, f.err
}

func TestAutoTokens_CodeHostAccess(t *testing.T) {
	ghRepo := reposdomain.Repo{Provider: codehost.GitHub, Host: "github.com"}
	legacy := reposdomain.Repo{} // a row from before the provider column reads as GitHub
	glRepo := func(host string) reposdomain.Repo { return reposdomain.Repo{Provider: codehost.GitLab, Host: host} }
	conns := fakeConnTokens{byHost: map[string]gitlabconnapp.Conn{
		"gl.example":    {Host: "gl.example", Token: "glpat-x"},
		"nokey.example": {Host: "nokey.example"},
	}}
	tests := []struct {
		name    string
		token   string
		repos   fakeRepoList
		conns   fakeConnTokens
		want    autoreviewdomain.CodeHostAccess
		wantErr bool
	}{
		{name: "nothing stored, no repos"},
		{name: "a GitHub token and no repos", token: "ghp_x",
			want: autoreviewdomain.CodeHostAccess{GitHubToken: true}},
		{name: "a GitHub repo with the token", token: "ghp_x", repos: fakeRepoList{repos: []reposdomain.Repo{ghRepo}},
			want: autoreviewdomain.CodeHostAccess{GitHubToken: true, RepoWithToken: true}},
		{name: "a legacy repo counts as GitHub", token: "ghp_x", repos: fakeRepoList{repos: []reposdomain.Repo{legacy}},
			want: autoreviewdomain.CodeHostAccess{GitHubToken: true, RepoWithToken: true}},
		{name: "a GitHub repo without the token", repos: fakeRepoList{repos: []reposdomain.Repo{ghRepo}}},
		{name: "a GitLab repo with a connection token", conns: conns, repos: fakeRepoList{repos: []reposdomain.Repo{glRepo("gl.example")}},
			want: autoreviewdomain.CodeHostAccess{GitLabRepos: true, RepoWithToken: true}},
		{name: "a GitLab repo whose connection has no token", conns: conns, repos: fakeRepoList{repos: []reposdomain.Repo{glRepo("nokey.example")}},
			want: autoreviewdomain.CodeHostAccess{GitLabRepos: true}},
		{name: "a GitLab repo without a connection", conns: conns, repos: fakeRepoList{repos: []reposdomain.Repo{glRepo("gone.example")}},
			want: autoreviewdomain.CodeHostAccess{GitLabRepos: true}},
		{name: "a GitHub token does not cover a GitLab repo", token: "ghp_x", conns: conns, repos: fakeRepoList{repos: []reposdomain.Repo{glRepo("gone.example")}},
			want: autoreviewdomain.CodeHostAccess{GitLabRepos: true, GitHubToken: true}},
		{name: "one repo with a token among several is enough", conns: conns,
			repos: fakeRepoList{repos: []reposdomain.Repo{glRepo("gone.example"), glRepo("gl.example"), glRepo("nokey.example")}},
			want:  autoreviewdomain.CodeHostAccess{GitLabRepos: true, RepoWithToken: true}},
		{name: "the repo list fails", repos: fakeRepoList{err: errors.New("db down")}, wantErr: true},
		{name: "the connection lookup fails", conns: fakeConnTokens{err: errors.New("db down")},
			repos: fakeRepoList{repos: []reposdomain.Repo{glRepo("gl.example")}}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens := autoTokens{token: func() string { return tc.token }, repos: tc.repos, conns: tc.conns}
			got, err := tokens.CodeHostAccess(t.Context(), "ws")
			if (err != nil) != tc.wantErr {
				t.Fatalf("CodeHostAccess() error = %v, want error %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("CodeHostAccess() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ---- wiring through the real server ----

func autoStatus(t *testing.T, s *stack) map[string]any {
	t.Helper()
	r := s.get("/settings/automatic-reviews-status")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /settings/automatic-reviews-status = %d %s, want 200", r.Status, r.Body)
	}
	return r.Obj(t)
}

func putAuto(t *testing.T, s *stack, v any) {
	t.Helper()
	if r := s.do(http.MethodPut, "/settings", map[string]any{"automatic_reviews": v}); r.Status != http.StatusOK {
		t.Fatalf("PUT /settings automatic_reviews = %d %s, want 200", r.Status, r.Body)
	}
}

// The route is served and the state follows the setting, the deployment's poll switch (the default
// zero config has the poll off) and, with the poll on, the token, the agents and the spend cap.
func TestAutoReview_StatusRouteIsWired(t *testing.T) {
	s := startStack(t, spec{})
	if got := autoStatus(t, s); got["state"] != "off" || got["prs_tracked"] == nil || got["poll_interval_min"] != float64(5) {
		t.Errorf("status of a fresh install = %v, want state off, prs_tracked set, poll_interval_min 5", got)
	}
	putAuto(t, s, true)
	if got := autoStatus(t, s)["state"]; got != "poll_disabled" {
		t.Errorf("status with the poll switched off = %v, want poll_disabled", got)
	}
}

func TestAutoReview_StatusFollowsTokenAgentsAndCap(t *testing.T) {
	s := startStack(t, spec{
		env:    map[string]string{"GITHUB_TOKEN": "ghp_wiring"},
		config: func(c *config.Config) { c.PRPollEnabled = true },
	})
	putAuto(t, s, true)
	if got := autoStatus(t, s)["state"]; got != "no_agents" {
		t.Fatalf("status with the poll on, a token and no agent = %v, want no_agents", got)
	}
	agents, ok := s.get("/agents").JSON(t).([]any)
	if !ok || len(agents) == 0 {
		t.Fatalf("GET /agents = %v, want the seeded agents", agents)
	}
	putAuto(t, s, map[string]any{"agent_ids": []string{agents[0].(map[string]any)["id"].(string)}})
	if got := autoStatus(t, s)["state"]; got != "active" {
		t.Errorf("status with an agent selected = %v, want active", got)
	}
	setCap(t, s, 0)
	if got := autoStatus(t, s)["state"]; got != "paused_spend_cap" {
		t.Errorf("status with the cap reached = %v, want paused_spend_cap", got)
	}
}

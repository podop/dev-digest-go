package leaderboardhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/app"
	"dev-digest-go/backend/internal/leaderboard/domain"
	leaderboardhttp "dev-digest-go/backend/internal/leaderboard/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	repoID      = "7e1d0000-0000-4000-8000-000000000001"
	uiOrigin    = "http://localhost:3100"
)

func ptr[T any](v T) *T { return &v }

// call is one recorded use-case call.
type call struct {
	WorkspaceID string
	Period      domain.PeriodKey
	TZ          string
	Repo        string
}

// fakeAPI implements app.API with a canned answer and records its calls.
type fakeAPI struct {
	mu    sync.Mutex
	calls []call

	err error
	lb  domain.Leaderboard
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) Leaderboard(_ context.Context, ws string, q domain.Query) (domain.Leaderboard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{WorkspaceID: ws, Period: q.Period, TZ: q.TZ, Repo: q.Repo})
	return f.lb, f.err
}

func (f *fakeAPI) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func newServer(t *testing.T, api app.API) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		CORSOrigins:  []string{uiOrigin},
		AllowedHosts: []string{"127.0.0.1"},
	})
	leaderboardhttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

type response struct {
	Status int
	Body   string
}

// do sends one request; header may carry Host and Origin overrides.
func do(t *testing.T, srv *httptest.Server, method, path string, header map[string]string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, path, err)
	}
	return response{Status: resp.StatusCode, Body: string(b)}
}

func get(t *testing.T, srv *httptest.Server, path string) response {
	t.Helper()
	return do(t, srv, http.MethodGet, path, nil)
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return v
}

// issueDetail is the part of a 422 details item the tests compare.
type issueDetail struct {
	Keyword, InstancePath, Message string
}

func details(t *testing.T, r response) []issueDetail {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Keyword      string `json:"keyword"`
				InstancePath string `json:"instancePath"`
				Message      string `json:"message"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
	if r.Status != http.StatusUnprocessableEntity || body.Error.Code != "validation_error" || body.Error.Message != "Request validation failed" {
		t.Fatalf("response = %d %s, want 422 validation_error", r.Status, r.Body)
	}
	var out []issueDetail
	for _, d := range body.Error.Details {
		out = append(out, issueDetail{d.Keyword, d.InstancePath, d.Message})
	}
	return out
}

func TestLeaderboard_InvalidQueryIs422(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []issueDetail
	}{
		{"unknown period", "period=7d", []issueDetail{
			{"invalid_enum_value", "/period", "must be one of week, last_week, 30d"}}},
		{"unknown tz", "tz=Mars/Base", []issueDetail{
			{"invalid_string", "/tz", "must be a known IANA time zone name"}}},
		{"server-local tz", "tz=Local", []issueDetail{
			{"invalid_string", "/tz", "must be a known IANA time zone name"}}},
		{"path-like tz", "tz=..%2Fetc%2Fpasswd", []issueDetail{
			{"invalid_string", "/tz", "must be a known IANA time zone name"}}},
		{"overlong tz", "tz=" + strings.Repeat("A", 65), []issueDetail{
			{"invalid_string", "/tz", "must be at most 64 characters"}}},
		{"repo neither all nor uuid", "repo=acme%2Fapi", []issueDetail{
			{"invalid_string", "/repo", `must be "all" or a repository id (UUID)`}}},
		{"empty value", "period=", []issueDetail{
			{"invalid_enum_value", "/period", "must not be empty"}}},
		{"repeated key", "period=week&period=30d", []issueDetail{
			{"invalid_enum_value", "/period", "must be given exactly once"}}},
		{"extra key", "limit=5", []issueDetail{
			{"unrecognized_keys", "/limit", "unknown query parameter"}}},
		{"all at once, in a fixed order", "zebra=1&repo=x&tz=Nope&period=x&apple=2", []issueDetail{
			{"invalid_enum_value", "/period", "must be one of week, last_week, 30d"},
			{"invalid_string", "/tz", "must be a known IANA time zone name"},
			{"invalid_string", "/repo", `must be "all" or a repository id (UUID)`},
			{"unrecognized_keys", "/apple", "unknown query parameter"},
			{"unrecognized_keys", "/zebra", "unknown query parameter"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			r := get(t, newServer(t, api), "/leaderboard?"+tc.query)
			if diff := cmp.Diff(tc.want, details(t, r)); diff != "" {
				t.Errorf("GET /leaderboard?%s details mismatch (-want +got):\n%s", tc.query, diff)
			}
			if n := len(api.recorded()); n != 0 {
				t.Errorf("use case calls = %d, want 0 before validation passes", n)
			}
		})
	}
}

func TestLeaderboard_PassesTheValidatedQueryAndWorkspace(t *testing.T) {
	tests := []struct {
		name, query string
		want        call
	}{
		{"defaults", "", call{workspaceID, domain.PeriodWeek, "UTC", domain.RepoAll}},
		{"everything", "period=30d&tz=Europe%2FKyiv&repo=" + strings.ToUpper(repoID),
			call{workspaceID, domain.Period30d, "Europe/Kyiv", repoID}},
		{"last week", "period=last_week&tz=America%2FArgentina%2FBuenos_Aires", call{workspaceID, domain.PeriodLastWeek, "America/Argentina/Buenos_Aires", domain.RepoAll}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{}
			path := "/leaderboard"
			if tc.query != "" {
				path += "?" + tc.query
			}
			if r := get(t, newServer(t, api), path); r.Status != http.StatusOK {
				t.Fatalf("GET %s = %d %s, want 200", path, r.Status, r.Body)
			}
			if diff := cmp.Diff([]call{tc.want}, api.recorded()); diff != "" {
				t.Errorf("use case calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLeaderboard_UnknownRepoIs404(t *testing.T) {
	api := &fakeAPI{err: apperr.NotFound("Repo not found")}
	r := get(t, newServer(t, api), "/leaderboard?repo="+repoID)
	want := `{"error":{"code":"not_found","message":"Repo not found"}}`
	if r.Status != http.StatusNotFound || strings.TrimSpace(r.Body) != want {
		t.Errorf("GET /leaderboard = %d %s, want 404 %s", r.Status, r.Body, want)
	}
}

func TestLeaderboard_UseCaseFailureIs500WithoutDetails(t *testing.T) {
	api := &fakeAPI{err: errors.New("pq: connection refused at 10.0.0.5")}
	r := get(t, newServer(t, api), "/leaderboard")
	if r.Status != http.StatusInternalServerError || strings.Contains(r.Body, "10.0.0.5") || !strings.Contains(r.Body, `"internal_error"`) {
		t.Errorf("GET /leaderboard = %d %s, want 500 internal_error without the cause", r.Status, r.Body)
	}
}

// TestLeaderboard_OnlyReadsAreServed pins NFR2/NFR5: the route is GET (and HEAD, which Echo
// mirrors from GET without a body); a write method never reaches the use case, whatever its
// Origin, and a foreign Host is refused before routing.
func TestLeaderboard_OnlyReadsAreServed(t *testing.T) {
	api := &fakeAPI{}
	srv := newServer(t, api)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for origin, want := range map[string]int{"": http.StatusNotFound, uiOrigin: http.StatusNotFound, "https://evil.example": http.StatusForbidden} {
			hdr := map[string]string{}
			if origin != "" {
				hdr["Origin"] = origin
			}
			if r := do(t, srv, method, "/leaderboard", hdr); r.Status != want {
				t.Errorf("%s /leaderboard (Origin %q) = %d %s, want %d", method, origin, r.Status, r.Body, want)
			}
		}
	}
	if n := len(api.recorded()); n != 0 {
		t.Errorf("use case calls after write methods = %d, want 0", n)
	}

	if r := do(t, srv, http.MethodGet, "/leaderboard", map[string]string{"Host": "evil.example"}); r.Status != http.StatusForbidden {
		t.Errorf("GET /leaderboard with a foreign Host = %d %s, want 403", r.Status, r.Body)
	}
	if n := len(api.recorded()); n != 0 {
		t.Errorf("use case calls after the foreign Host = %d, want 0", n)
	}

	head := do(t, srv, http.MethodHead, "/leaderboard", nil)
	if head.Status != http.StatusOK || head.Body != "" {
		t.Errorf("HEAD /leaderboard = %d %q, want 200 with an empty body", head.Status, head.Body)
	}
}

func TestLeaderboard_EmptyAnswerKeepsEveryListAnArray(t *testing.T) {
	kyiv := mustLocation(t, "Europe/Kyiv")
	from := time.Date(2026, time.October, 19, 0, 0, 0, 0, kyiv)
	api := &fakeAPI{lb: domain.Leaderboard{
		Window: domain.Window{
			Key: domain.PeriodWeek, Location: kyiv, From: from, To: from.AddDate(0, 0, 7),
			HasPrev: true, PrevFrom: from.AddDate(0, 0, -7), PrevTo: from, Days: 7,
		},
		Activity: domain.ActivityChart{Granularity: domain.GranularityDay},
	}}
	r := get(t, newServer(t, api), "/leaderboard?tz=Europe%2FKyiv")
	const kpis = `{"prs_reviewed":0,"merged":0,"avg_health":null,"time_to_first_review_s":null,"time_to_merge_s":null,"critical_found":0,"open_critical":0,"review_cost_usd":0}`
	want := `{"period":{"key":"week","tz":"Europe/Kyiv","from":"2026-10-19T00:00:00.000+03:00","to":"2026-10-26T00:00:00.000+02:00",` +
		`"previous_from":"2026-10-12T00:00:00.000+03:00","previous_to":"2026-10-19T00:00:00.000+03:00"},` +
		`"repos":[],"summary":` + kpis + `,"previous_summary":null,` +
		`"awards":{"cleanest":null,"hottest":null,"fastest_merge":null,"most_rounds":null,"monster":null,"priciest":null},` +
		`"rows":[],"needs_attention":{"total":0,"items":[]},"hot_files":[],"health_by_size":[],"health_by_author":[],"health_by_repo":[],"activity":{"granularity":"day","points":[]}}`
	if r.Status != http.StatusOK || strings.TrimSpace(r.Body) != want {
		t.Errorf("GET /leaderboard = %d\n%s\nwant\n%s", r.Status, r.Body, want)
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

// TestLeaderboard_WireShape compares the decoded response of a full answer with the shape of
// contracts/leaderboard.ts: key names, nullability (size, health, medians, award, agent id),
// the optional count/days that are omitted and never null, money rounded to 6 decimals,
// millisecond timestamps (local with an offset in period, UTC elsewhere) and whole seconds.
func TestLeaderboard_WireShape(t *testing.T) {
	kyiv := mustLocation(t, "Europe/Kyiv")
	from := time.Date(2026, time.October, 19, 0, 0, 0, 0, kyiv)
	opened := time.Date(2026, time.October, 18, 21, 30, 15, 987_654_000, time.UTC) // µs truncate to ms
	polled := time.Date(2026, time.October, 22, 11, 0, 0, 0, time.UTC)

	api := &fakeAPI{lb: domain.Leaderboard{
		Window: domain.Window{
			Key: domain.PeriodWeek, Location: kyiv, From: from, To: from.AddDate(0, 0, 7),
			HasPrev: true, PrevFrom: from.AddDate(0, 0, -7), PrevTo: from, Days: 7,
		},
		Repos: []domain.RepoRef{
			{ID: repoID, FullName: "acme/api", LastPolledAt: &polled},
			{ID: "r2", FullName: "acme/web"},
		},
		Summary: domain.KpiSet{
			PRsReviewed: 2, Merged: 1, AvgHealth: ptr(75), TimeToFirstReviewS: ptr(int64(129600)),
			CriticalFound: 1, OpenCritical: 1, ReviewCostUSD: 0.1234565000001,
		},
		PreviousSummary: &domain.KpiSet{PRsReviewed: 1, Merged: 2, AvgHealth: ptr(80), TimeToMergeS: ptr(int64(3600))},
		Awards: domain.AwardSet{
			Cleanest: &domain.Award{PRID: "p1", RepoID: repoID, Repo: "api", Number: 7, Title: "Add cache", Value: 90},
			Priciest: &domain.Award{PRID: "p1", RepoID: repoID, Repo: "api", Number: 7, Title: "Add cache", Value: 0.30000000004},
		},
		Rows: []domain.Row{
			{
				PRID: "p1", RepoID: repoID, Repo: "api", Number: 7, Title: "Add cache", Author: "ann", Status: domain.StatusMerged,
				Additions: 10, Deletions: 5, FilesCount: 2, Size: domain.SizeS, Health: ptr(90), Risk: domain.RiskMedium,
				Findings: domain.FindingCounts{Critical: 1, Warning: 2}, OpenCritical: 1, Rounds: 2,
				TimeToFirstReviewS: ptr(int64(172800)), TimeToMergeS: ptr(int64(259200)), CostUSD: 0.2500004,
				NewCommitsSinceReview: true, OpenedAt: &opened, MergedAt: ptr(opened.Add(72 * time.Hour)),
				Reviews: []domain.RowReview{
					{ReviewID: "rv1", AgentID: ptr("a1"), AgentName: "Alpha", Score: ptr(90), Findings: domain.FindingCounts{Critical: 1, Warning: 2}},
					{ReviewID: "rv2", AgentName: domain.DeletedAgentName},
				},
				TopFindings: []domain.TopFinding{
					{FindingID: "f1", Severity: domain.SeverityCritical, Title: "Nil deref", File: "a.go", StartLine: 12, AgentName: "Alpha", State: domain.FindingOpen},
					{FindingID: "f2", Severity: domain.SeverityWarning, Title: "Slow loop", File: "b.go", StartLine: 0, AgentName: domain.DeletedAgentName, State: domain.FindingDismissed},
				},
			},
			{
				PRID: "p2", RepoID: "r2", Repo: "web", Number: 3, Title: "Zero stats", Author: "bob", Status: domain.StatusOpen,
				Risk: domain.RiskLow, Reviews: []domain.RowReview{}, TopFindings: []domain.TopFinding{},
			},
		},
		NeedsAttention: domain.AttentionList{Total: 21, Items: []domain.AttentionItem{{
			PRID: "p1", RepoID: repoID, Repo: "api", Number: 7, Title: "Add cache", Author: "ann", OpenedAt: &opened,
			Reasons: []domain.AttentionReason{
				{Kind: domain.ReasonMergedOpenCritical, Count: 1},
				{Kind: domain.ReasonNewCommits},
				{Kind: domain.ReasonOpenLong, Days: 9},
			},
		}, {
			PRID: "p2", RepoID: "r2", Repo: "web", Number: 3, Title: "Zero stats", Author: "bob",
			Reasons: []domain.AttentionReason{{Kind: domain.ReasonNoReview}},
		}}},
		HotFiles: []domain.HotFile{{Path: "a.go", RepoID: repoID, Repo: "api", Findings: 3, Critical: 1, PRs: []int{3, 7}}},
		HealthBySize: []domain.SizeBucket{
			{Size: domain.SizeXS}, {Size: domain.SizeS, PRs: 1, AvgHealth: ptr(90), AvgFindings: ptr(3.5)},
			{Size: domain.SizeM}, {Size: domain.SizeL}, {Size: domain.SizeXL},
		},
		HealthByAuthor: []domain.HealthGroup{
			{Key: "ann", Label: "ann", PRs: 1, AvgHealth: ptr(90), AvgFindings: ptr(3.0)},
			{Key: "bob", Label: "bob", PRs: 1, AvgFindings: ptr(0.0)},
		},
		HealthByRepo: []domain.HealthGroup{
			{Key: repoID, Label: "api", PRs: 1, AvgHealth: ptr(90), AvgFindings: ptr(3.0)},
			{Key: "r2", Label: "web", PRs: 1, AvgFindings: ptr(0.0)},
		},
		Activity: domain.ActivityChart{Granularity: domain.GranularityDay, Points: []domain.ActivityPoint{
			{From: "2026-10-19", To: "2026-10-19", Opened: 1, Critical: 1},
		}},
	}}

	r := get(t, newServer(t, api), "/leaderboard?tz=Europe%2FKyiv")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /leaderboard = %d %s, want 200", r.Status, r.Body)
	}
	const want = `{
	  "period": {"key":"week","tz":"Europe/Kyiv","from":"2026-10-19T00:00:00.000+03:00","to":"2026-10-26T00:00:00.000+02:00",
	             "previous_from":"2026-10-12T00:00:00.000+03:00","previous_to":"2026-10-19T00:00:00.000+03:00"},
	  "repos": [{"id":"` + repoID + `","full_name":"acme/api","last_polled_at":"2026-10-22T11:00:00.000Z"},
	            {"id":"r2","full_name":"acme/web","last_polled_at":null}],
	  "summary": {"prs_reviewed":2,"merged":1,"avg_health":75,"time_to_first_review_s":129600,"time_to_merge_s":null,
	              "critical_found":1,"open_critical":1,"review_cost_usd":0.123457},
	  "previous_summary": {"prs_reviewed":1,"merged":2,"avg_health":80,"time_to_first_review_s":null,"time_to_merge_s":3600,
	                       "critical_found":0,"open_critical":0,"review_cost_usd":0},
	  "awards": {
	    "cleanest": {"pr_id":"p1","repo_id":"` + repoID + `","repo":"api","number":7,"title":"Add cache","value":90},
	    "hottest": null, "fastest_merge": null, "most_rounds": null, "monster": null,
	    "priciest": {"pr_id":"p1","repo_id":"` + repoID + `","repo":"api","number":7,"title":"Add cache","value":0.3}
	  },
	  "rows": [
	    {"pr_id":"p1","repo_id":"` + repoID + `","repo":"api","number":7,"title":"Add cache","author":"ann","status":"merged","draft":false,
	     "additions":10,"deletions":5,"files_count":2,"size":"S","health":90,"risk":"medium",
	     "findings":{"CRITICAL":1,"WARNING":2,"SUGGESTION":0},"open_critical":1,"rounds":2,
	     "time_to_first_review_s":172800,"time_to_merge_s":259200,"open_age_s":null,"cost_usd":0.25,
	     "new_commits_since_review":true,"opened_at":"2026-10-18T21:30:15.987Z","merged_at":"2026-10-21T21:30:15.987Z",
	     "reviews":[
	       {"review_id":"rv1","agent_id":"a1","agent_name":"Alpha","score":90,"findings":{"CRITICAL":1,"WARNING":2,"SUGGESTION":0}},
	       {"review_id":"rv2","agent_id":null,"agent_name":"Deleted agent","score":null,"findings":{"CRITICAL":0,"WARNING":0,"SUGGESTION":0}}],
	     "top_findings":[
	       {"finding_id":"f1","severity":"CRITICAL","title":"Nil deref","file":"a.go","start_line":12,"agent_name":"Alpha","state":"open"},
	       {"finding_id":"f2","severity":"WARNING","title":"Slow loop","file":"b.go","start_line":0,"agent_name":"Deleted agent","state":"dismissed"}]},
	    {"pr_id":"p2","repo_id":"r2","repo":"web","number":3,"title":"Zero stats","author":"bob","status":"open","draft":false,
	     "additions":0,"deletions":0,"files_count":0,"size":null,"health":null,"risk":"low",
	     "findings":{"CRITICAL":0,"WARNING":0,"SUGGESTION":0},"open_critical":0,"rounds":0,
	     "time_to_first_review_s":null,"time_to_merge_s":null,"open_age_s":null,"cost_usd":0,
	     "new_commits_since_review":false,"opened_at":null,"merged_at":null,"reviews":[],"top_findings":[]}
	  ],
	  "needs_attention": {"total":21,"items":[
	    {"pr_id":"p1","repo_id":"` + repoID + `","repo":"api","number":7,"title":"Add cache","author":"ann","opened_at":"2026-10-18T21:30:15.987Z",
	     "reasons":[{"kind":"merged_open_critical","count":1},{"kind":"new_commits"},{"kind":"open_long","days":9}]},
	    {"pr_id":"p2","repo_id":"r2","repo":"web","number":3,"title":"Zero stats","author":"bob","opened_at":null,
	     "reasons":[{"kind":"no_review"}]}]},
	  "hot_files": [{"path":"a.go","repo_id":"` + repoID + `","repo":"api","findings":3,"critical":1,"prs":[3,7]}],
	  "health_by_size": [{"size":"XS","prs":0,"avg_health":null,"avg_findings":null},
	                     {"size":"S","prs":1,"avg_health":90,"avg_findings":3.5},
	                     {"size":"M","prs":0,"avg_health":null,"avg_findings":null},
	                     {"size":"L","prs":0,"avg_health":null,"avg_findings":null},
	                     {"size":"XL","prs":0,"avg_health":null,"avg_findings":null}],
	  "health_by_author": [{"author":"ann","prs":1,"avg_health":90,"avg_findings":3},
	                       {"author":"bob","prs":1,"avg_health":null,"avg_findings":0}],
	  "health_by_repo": [{"repo_id":"` + repoID + `","repo":"api","prs":1,"avg_health":90,"avg_findings":3},
	                     {"repo_id":"r2","repo":"web","prs":1,"avg_health":null,"avg_findings":0}],
	  "activity": {"granularity":"day","points":[{"from":"2026-10-19","to":"2026-10-19","opened":1,"merged":0,"critical":1}]}
	}`
	if diff := cmp.Diff(decode(t, want), decode(t, r.Body)); diff != "" {
		t.Errorf("GET /leaderboard body mismatch (-want +got):\n%s", diff)
	}
}

// TestLeaderboard_RepoCodeHost: a GitLab repository carries provider, host and web_url (FR46),
// a GitHub one none of them (its response is the one it always was), and the row and hot file
// `repo` stays the short repository name (SCR6), told apart by repo_id.
func TestLeaderboard_RepoCodeHost(t *testing.T) {
	api := &fakeAPI{lb: domain.Leaderboard{
		Window: domain.Window{Key: domain.Period30d, Location: time.UTC, From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), Days: 30},
		Repos: []domain.RepoRef{
			{ID: "r1", FullName: "acme/api"},
			{ID: "r2", FullName: "grp/sub/api", Provider: "gitlab", Host: "gitlab.example.com", WebURL: "https://gitlab.example.com/grp/sub/api"},
			{ID: "r3", FullName: "grp/sub/api", Provider: "gitlab", Host: "git.other.org:8443", WebURL: "https://git.other.org:8443/grp/sub/api"},
		},
		HotFiles: []domain.HotFile{
			{Path: "a.go", RepoID: "r2", Repo: "api", Findings: 1, PRs: []int{1}},
			{Path: "a.go", RepoID: "r3", Repo: "api", Findings: 1, PRs: []int{1}},
		},
		Activity: domain.ActivityChart{Granularity: domain.GranularityWeek},
	}}
	r := get(t, newServer(t, api), "/leaderboard?period=30d&tz=UTC")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /leaderboard = %d %s, want 200", r.Status, r.Body)
	}
	var got struct {
		Repos    []map[string]any `json:"repos"`
		HotFiles []map[string]any `json:"hot_files"`
	}
	if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
	wantRepos := []map[string]any{
		{"id": "r1", "full_name": "acme/api", "last_polled_at": nil},
		{"id": "r2", "full_name": "grp/sub/api", "last_polled_at": nil, "provider": "gitlab", "host": "gitlab.example.com", "web_url": "https://gitlab.example.com/grp/sub/api"},
		{"id": "r3", "full_name": "grp/sub/api", "last_polled_at": nil, "provider": "gitlab", "host": "git.other.org:8443", "web_url": "https://git.other.org:8443/grp/sub/api"},
	}
	if diff := cmp.Diff(wantRepos, got.Repos); diff != "" {
		t.Errorf("GET /leaderboard repos mismatch (-want +got):\n%s", diff)
	}
	for _, f := range got.HotFiles {
		if f["repo"] != "api" {
			t.Errorf("GET /leaderboard hot file repo = %v, want the short name api", f["repo"])
		}
	}
	if len(got.HotFiles) != 2 || got.HotFiles[0]["repo_id"] == got.HotFiles[1]["repo_id"] {
		t.Errorf("GET /leaderboard hot files = %v, want two entries of different repo_id", got.HotFiles)
	}
}

// TestLeaderboard_WireShape30d: a window without a previous one has null previous bounds and a
// null previous summary, and its activity is weekly with clipped Monday-started weeks.
func TestLeaderboard_WireShape30d(t *testing.T) {
	kyiv := mustLocation(t, "Europe/Kyiv")
	from := time.Date(2026, time.September, 23, 0, 0, 0, 0, kyiv)
	api := &fakeAPI{lb: domain.Leaderboard{
		Window: domain.Window{
			Key: domain.Period30d, Location: kyiv, From: from, To: from.AddDate(0, 0, 30), Days: 30,
		},
		Activity: domain.ActivityChart{Granularity: domain.GranularityWeek, Points: []domain.ActivityPoint{
			{From: "2026-09-23", To: "2026-09-27", Opened: 2},
			{From: "2026-09-28", To: "2026-10-04", Merged: 1, Critical: 3},
		}},
	}}
	r := get(t, newServer(t, api), "/leaderboard?period=30d&tz=Europe%2FKyiv")
	if r.Status != http.StatusOK {
		t.Fatalf("GET /leaderboard = %d %s, want 200", r.Status, r.Body)
	}
	var got struct {
		Period          map[string]any `json:"period"`
		PreviousSummary any            `json:"previous_summary"`
		Activity        any            `json:"activity"`
	}
	if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
		t.Fatalf("decode %q: %v", r.Body, err)
	}
	wantPeriod := map[string]any{
		"key": "30d", "tz": "Europe/Kyiv", "from": "2026-09-23T00:00:00.000+03:00", "to": "2026-10-23T00:00:00.000+03:00",
		"previous_from": nil, "previous_to": nil,
	}
	if diff := cmp.Diff(wantPeriod, got.Period); diff != "" {
		t.Errorf("GET /leaderboard period mismatch (-want +got):\n%s", diff)
	}
	if got.PreviousSummary != nil {
		t.Errorf("GET /leaderboard previous_summary = %v, want null", got.PreviousSummary)
	}
	wantActivity := decode(t, `{"granularity":"week","points":[
	  {"from":"2026-09-23","to":"2026-09-27","opened":2,"merged":0,"critical":0},
	  {"from":"2026-09-28","to":"2026-10-04","opened":0,"merged":1,"critical":3}]}`)
	if diff := cmp.Diff(wantActivity, got.Activity); diff != "" {
		t.Errorf("GET /leaderboard activity mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(r.Body, `"daily"`) {
		t.Errorf("GET /leaderboard body still has the removed \"daily\" key: %s", r.Body)
	}
}

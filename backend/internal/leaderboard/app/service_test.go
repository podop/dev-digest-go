package app_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/app"
	"dev-digest-go/backend/internal/leaderboard/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

const workspace = "ws-1"

func at(month time.Month, day, hour int) time.Time {
	return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

// now is Thursday 2026-10-22 12:00 UTC: the week is [Oct 19, Oct 26), the previous one
// [Oct 12, Oct 19).
var now = at(time.October, 22, 12)

func clock() time.Time { return now }

// call is one recorded Reader call.
type call struct {
	Method   string
	Repo     string // RepoScope only
	RepoIDs  []string
	From, To time.Time // WindowFacts only: the read range [win.ReadFrom(), win.To)
	Split    time.Time // WindowFacts only: win.From, where the window starts inside the range
}

// fakeReader answers canned data and records its calls; a method listed in failOn fails.
type fakeReader struct {
	repos  []domain.RepoRef
	facts  domain.Facts
	failOn map[string]error

	calls []call
}

var _ app.Reader = (*fakeReader)(nil)

func (f *fakeReader) RepoScope(_ context.Context, ws, repo string) ([]domain.RepoRef, error) {
	f.calls = append(f.calls, call{Method: "RepoScope", Repo: repo})
	if ws != workspace {
		return nil, errors.New("wrong workspace")
	}
	if err := f.failOn["RepoScope"]; err != nil {
		return nil, err
	}
	return f.repos, nil
}

func (f *fakeReader) WindowFacts(_ context.Context, _ string, ids []string, win domain.Window) (domain.Facts, error) {
	f.calls = append(f.calls, call{Method: "WindowFacts", RepoIDs: ids, From: win.ReadFrom(), To: win.To, Split: win.From})
	if err := f.failOn["WindowFacts"]; err != nil {
		return domain.Facts{}, err
	}
	return f.facts, nil
}

func (f *fakeReader) methods() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Method
	}
	return out
}

func weekQuery() domain.Query {
	return domain.Query{Period: domain.PeriodWeek, TZ: "UTC", Location: time.UTC, Repo: domain.RepoAll}
}

// busyReader has two repositories, a merged PR with an open critical finding and a clean open PR
// in the current week, one reviewed PR in the previous week and one open PR never reviewed.
func busyReader() *fakeReader {
	return &fakeReader{
		repos: []domain.RepoRef{
			{ID: "r-api", Owner: "acme", Name: "api", FullName: "acme/api"},
			{ID: "r-web", Owner: "acme", Name: "web", FullName: "acme/web"},
		},
		facts: domain.Facts{
			From: at(time.October, 12, 0), To: at(time.October, 26, 0),
			Pulls: []domain.PullFact{
				{PRID: "p-low", RepoID: "r-web", Repo: "web", Number: 3, Title: "Low", Author: "bob",
					Status: domain.StatusOpen, OpenedAt: ptr(at(time.October, 20, 8)), FirstReviewAt: ptr(at(time.October, 21, 8))},
				{PRID: "p-top", RepoID: "r-api", Repo: "api", Number: 7, Title: "Top", Author: "ann",
					Status: domain.StatusMerged, OpenedAt: ptr(at(time.October, 18, 10)), MergedAt: ptr(at(time.October, 21, 10)),
					FirstReviewAt: ptr(at(time.October, 20, 10)), OpenCritical: 1},
				{PRID: "p-old", RepoID: "r-api", Repo: "api", Number: 5, Title: "Old", Author: "ann",
					Status: domain.StatusMerged, OpenedAt: ptr(at(time.October, 13, 10)), MergedAt: ptr(at(time.October, 15, 10)),
					FirstReviewAt: ptr(at(time.October, 14, 10))},
				{PRID: "p-new", RepoID: "r-web", Repo: "web", Number: 9, Title: "New", Author: "cy", HeadSHA: "h",
					Status: domain.StatusOpen, OpenedAt: ptr(at(time.October, 21, 8)), UpdatedAt: ptr(at(time.October, 21, 8))},
			},
			Reviews: []domain.ReviewFact{
				{ReviewID: "rv-low", PRID: "p-low", AgentID: "a1", AgentName: "Alpha", Score: ptr(60), CreatedAt: at(time.October, 21, 8)},
				{ReviewID: "rv-top", PRID: "p-top", AgentID: "a1", AgentName: "Alpha", Score: ptr(90), CreatedAt: at(time.October, 20, 10)},
				{ReviewID: "rv-old", PRID: "p-old", AgentID: "a1", AgentName: "Alpha", Score: ptr(50), CreatedAt: at(time.October, 14, 10)},
			},
			Counts: []domain.ReviewCounts{
				{ReviewID: "rv-top", All: domain.FindingCounts{Critical: 1}, Live: domain.FindingCounts{Critical: 1}, OpenCritical: 1},
				{ReviewID: "rv-old", All: domain.FindingCounts{Warning: 1}, Live: domain.FindingCounts{Warning: 1}},
			},
			HotFiles: []domain.HotFile{
				{Path: "b.go", RepoID: "r-api", Repo: "api", Findings: 1, PRs: []int{5}},
				{Path: "a.go", RepoID: "r-api", Repo: "api", Findings: 2, Critical: 1, PRs: []int{7}},
			},
			Runs: []domain.RunFact{
				{PRID: "p-top", RanAt: at(time.October, 20, 9), CostUSD: ptr(0.25)},
				{PRID: "p-old", RanAt: at(time.October, 14, 9), CostUSD: ptr(0.5)},
			},
		},
	}
}

func TestLeaderboard_ReadsOnceInOrderAndFoldsBothWindows(t *testing.T) {
	reader := busyReader()
	got, err := app.NewService(reader, clock).Leaderboard(t.Context(), workspace, weekQuery())
	if err != nil {
		t.Fatalf("Leaderboard() error = %v, want nil", err)
	}

	wantCalls := []call{
		{Method: "RepoScope", Repo: domain.RepoAll},
		{
			Method: "WindowFacts", RepoIDs: []string{"r-api", "r-web"},
			From: at(time.October, 12, 0), To: at(time.October, 26, 0), Split: at(time.October, 19, 0),
		},
	}
	if diff := cmp.Diff(wantCalls, reader.calls); diff != "" {
		t.Errorf("Reader calls mismatch (-want +got):\n%s", diff)
	}

	if got.Window.From != at(time.October, 19, 0) || got.Window.To != at(time.October, 26, 0) || got.Window.Key != domain.PeriodWeek {
		t.Errorf("Window = %+v, want the week of Oct 19", got.Window)
	}
	// health: p-low 100 (no findings), p-top 100 - 10 (one open critical)
	if diff := cmp.Diff([]string{"p-low", "p-top"}, rowIDs(got.Rows)); diff != "" {
		t.Errorf("rows are not ranked by health (-want +got):\n%s", diff)
	}
	wantSummary := domain.KpiSet{
		PRsReviewed: 2, Merged: 1, AvgHealth: ptr(95),
		TimeToFirstReviewS: ptr(int64(36 * 3600)), // median of p-top 48 h and p-low 24 h
		TimeToMergeS:       ptr(int64(72 * 3600)), CriticalFound: 1, OpenCritical: 1, ReviewCostUSD: 0.25,
	}
	if diff := cmp.Diff(wantSummary, got.Summary); diff != "" {
		t.Errorf("Summary mismatch (-want +got):\n%s", diff)
	}
	// The previous week holds p-old only (one warning: health 97), folded from the same facts.
	wantPrevious := &domain.KpiSet{
		PRsReviewed: 1, Merged: 1, AvgHealth: ptr(97),
		TimeToFirstReviewS: ptr(int64(24 * 3600)), TimeToMergeS: ptr(int64(48 * 3600)), ReviewCostUSD: 0.5,
	}
	if diff := cmp.Diff(wantPrevious, got.PreviousSummary); diff != "" {
		t.Errorf("PreviousSummary mismatch (-want +got):\n%s", diff)
	}
	if got.Awards.Cleanest == nil || got.Awards.Cleanest.PRID != "p-low" || got.Awards.Cleanest.Value != 100 {
		t.Errorf("Awards.Cleanest = %+v, want p-low with 100", got.Awards.Cleanest)
	}
	if got.Awards.FastestMerge == nil || got.Awards.FastestMerge.PRID != "p-top" {
		t.Errorf("Awards.FastestMerge = %+v, want p-top (p-old merged in the previous window)", got.Awards.FastestMerge)
	}
	// The hot files of the store are ordered by the domain.
	wantHot := []domain.HotFile{
		{Path: "a.go", RepoID: "r-api", Repo: "api", Findings: 2, Critical: 1, PRs: []int{7}},
		{Path: "b.go", RepoID: "r-api", Repo: "api", Findings: 1, PRs: []int{5}},
	}
	if diff := cmp.Diff(wantHot, got.HotFiles); diff != "" {
		t.Errorf("HotFiles mismatch (-want +got):\n%s", diff)
	}
	if got.NeedsAttention.Total != 2 {
		t.Errorf("NeedsAttention.Total = %d, want 2 (merged p-top with an open critical, p-new never reviewed)", got.NeedsAttention.Total)
	}
	if got.Activity.Granularity != domain.GranularityDay || len(got.Activity.Points) != 7 || len(got.HealthBySize) != 5 {
		t.Errorf("Activity = %s with %d points, len(HealthBySize) = %d, want day with 7 points and 5 buckets",
			got.Activity.Granularity, len(got.Activity.Points), len(got.HealthBySize))
	}
	if diff := cmp.Diff(busyReader().repos, got.Repos); diff != "" {
		t.Errorf("Repos mismatch (-want +got):\n%s", diff)
	}
}

func rowIDs(rows []domain.Row) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.PRID
	}
	return ids
}

func TestLeaderboard_EmptyScopeReadsNothingElseAndAnswersZeros(t *testing.T) {
	reader := &fakeReader{}
	got, err := app.NewService(reader, clock).Leaderboard(t.Context(), workspace, weekQuery())
	if err != nil {
		t.Fatalf("Leaderboard() error = %v, want nil", err)
	}
	if diff := cmp.Diff([]string{"RepoScope"}, reader.methods()); diff != "" {
		t.Errorf("Reader calls mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(domain.KpiSet{}, got.Summary); diff != "" {
		t.Errorf("Summary mismatch (-want +got):\n%s", diff)
	}
	if got.PreviousSummary != nil {
		t.Errorf("PreviousSummary = %+v, want nil (the previous week has no rows)", got.PreviousSummary)
	}
	if len(got.Rows) != 0 || got.NeedsAttention.Total != 0 || len(got.HotFiles) != 0 || got.Awards != (domain.AwardSet{}) {
		t.Errorf("Leaderboard() = %+v, want no rows, attention, hot files or awards", got)
	}
	if len(got.Activity.Points) != 7 || len(got.HealthBySize) != 5 {
		t.Errorf("len(Activity.Points), len(HealthBySize) = %d, %d, want 7, 5", len(got.Activity.Points), len(got.HealthBySize))
	}
}

func TestLeaderboard_PreviousSummaryIsNilWithoutPreviousRowsAndFor30d(t *testing.T) {
	tests := []struct {
		name      string
		q         domain.Query
		dropOld   bool
		wantPrev  bool
		wantStart time.Time
	}{
		{"week with previous rows", weekQuery(), false, true, at(time.October, 12, 0)},
		{"week whose previous week has no rows", weekQuery(), true, false, at(time.October, 12, 0)},
		{"30d never has one", domain.Query{Period: domain.Period30d, TZ: "UTC", Location: time.UTC, Repo: domain.RepoAll}, false, false, at(time.September, 23, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := busyReader()
			if tc.dropOld {
				reader.facts.Reviews = slices.DeleteFunc(reader.facts.Reviews, func(r domain.ReviewFact) bool { return r.PRID == "p-old" })
			}
			got, err := app.NewService(reader, clock).Leaderboard(t.Context(), workspace, tc.q)
			if err != nil {
				t.Fatalf("Leaderboard() error = %v, want nil", err)
			}
			if (got.PreviousSummary != nil) != tc.wantPrev {
				t.Errorf("PreviousSummary = %+v, want non-nil: %v", got.PreviousSummary, tc.wantPrev)
			}
			if c := reader.calls[1]; !c.From.Equal(tc.wantStart) {
				t.Errorf("WindowFacts range starts at %v, want %v", c.From, tc.wantStart)
			}
			if got.Window.HasPrev != (tc.q.Period != domain.Period30d) {
				t.Errorf("Window.HasPrev = %v for %s", got.Window.HasPrev, tc.q.Period)
			}
		})
	}
}

func TestLeaderboard_PassesTheRepoFilter(t *testing.T) {
	const repo = "7e1d0000-0000-4000-8000-000000000001"
	reader := &fakeReader{repos: []domain.RepoRef{{ID: repo, FullName: "acme/api"}}}
	q := weekQuery()
	q.Repo = repo
	if _, err := app.NewService(reader, clock).Leaderboard(t.Context(), workspace, q); err != nil {
		t.Fatalf("Leaderboard() error = %v, want nil", err)
	}
	if got := reader.calls[0]; got.Method != "RepoScope" || got.Repo != repo {
		t.Errorf("first call = %+v, want RepoScope(%s)", got, repo)
	}
	if got := reader.calls[1].RepoIDs; !slices.Equal(got, []string{repo}) {
		t.Errorf("WindowFacts repo ids = %v, want [%s]", got, repo)
	}
}

func TestLeaderboard_UnknownRepoIsNotFound(t *testing.T) {
	reader := &fakeReader{failOn: map[string]error{"RepoScope": errors.Join(errors.New("select"), domain.ErrNotFound)}}
	_, err := app.NewService(reader, clock).Leaderboard(t.Context(), workspace, weekQuery())
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindNotFound || ae.Message != "Repo not found" {
		t.Fatalf("Leaderboard() error = %v, want apperr not_found \"Repo not found\"", err)
	}
	if diff := cmp.Diff([]string{"RepoScope"}, reader.methods()); diff != "" {
		t.Errorf("Reader calls mismatch (-want +got):\n%s", diff)
	}
}

func TestLeaderboard_ReaderFailureIsReturnedWrappedAndNotAnAppError(t *testing.T) {
	boom := errors.New("connection refused")
	for _, method := range []string{"RepoScope", "WindowFacts"} {
		t.Run(method, func(t *testing.T) {
			reader := busyReader()
			reader.failOn = map[string]error{method: boom}
			_, err := app.NewService(reader, clock).Leaderboard(t.Context(), workspace, weekQuery())
			if !errors.Is(err, boom) {
				t.Fatalf("Leaderboard() error = %v, want it to wrap %v", err, boom)
			}
			if ae, ok := apperr.As(err); ok {
				t.Errorf("Leaderboard() error is an apperr of kind %v, want a plain error (httpx renders it as 500)", ae.Kind)
			}
		})
	}
}

func TestLeaderboard_WindowFollowsTheQueryZoneAndPeriod(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// Sunday 22:30 UTC is already Monday 01:30 in Kyiv: the week there has just begun.
	sundayNight := time.Date(2026, time.October, 25, 22, 30, 0, 0, time.UTC)
	tests := []struct {
		name         string
		now          time.Time
		q            domain.Query
		wantFrom     time.Time // WindowFacts from = previous window start (the window start for 30d)
		wantTo       time.Time
		wantGran     string
		wantPoints   int
		wantWinStart time.Time
	}{
		{"week in UTC", sundayNight, weekQuery(),
			at(time.October, 12, 0), at(time.October, 26, 0), domain.GranularityDay, 7, at(time.October, 19, 0)},
		{"week in Kyiv", sundayNight, domain.Query{Period: domain.PeriodWeek, TZ: "Europe/Kyiv", Location: kyiv, Repo: domain.RepoAll},
			time.Date(2026, time.October, 19, 0, 0, 0, 0, kyiv), time.Date(2026, time.November, 2, 0, 0, 0, 0, kyiv), domain.GranularityDay, 7,
			time.Date(2026, time.October, 26, 0, 0, 0, 0, kyiv)},
		{"last week", now, domain.Query{Period: domain.PeriodLastWeek, TZ: "UTC", Location: time.UTC, Repo: domain.RepoAll},
			at(time.October, 5, 0), at(time.October, 19, 0), domain.GranularityDay, 7, at(time.October, 12, 0)},
		{"30 days", now, domain.Query{Period: domain.Period30d, TZ: "UTC", Location: time.UTC, Repo: domain.RepoAll},
			// Wed Sep 23 .. Thu Oct 22: the weeks of Sep 23, Sep 28, Oct 5, Oct 12 and Oct 19.
			at(time.September, 23, 0), at(time.October, 23, 0), domain.GranularityWeek, 5, at(time.September, 23, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{repos: []domain.RepoRef{{ID: "r1"}}}
			got, err := app.NewService(reader, func() time.Time { return tc.now }).Leaderboard(t.Context(), workspace, tc.q)
			if err != nil {
				t.Fatalf("Leaderboard() error = %v, want nil", err)
			}
			if c := reader.calls[1]; !c.From.Equal(tc.wantFrom) || !c.To.Equal(tc.wantTo) {
				t.Errorf("WindowFacts range = [%v, %v), want [%v, %v)", c.From, c.To, tc.wantFrom, tc.wantTo)
			}
			if !got.Window.From.Equal(tc.wantWinStart) || got.Activity.Granularity != tc.wantGran || len(got.Activity.Points) != tc.wantPoints {
				t.Errorf("Window.From = %v with %d %s points, want %v with %d %s", got.Window.From,
					len(got.Activity.Points), got.Activity.Granularity, tc.wantWinStart, tc.wantPoints, tc.wantGran)
			}
		})
	}
}

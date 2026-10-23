package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
	pullsdomain "dev-digest-go/backend/internal/pulls/domain"
)

// The window of these tests is the week 2026-10-19 .. 2026-10-26 (UTC), the previous window
// 2026-10-12 .. 2026-10-19, and "now" is Thursday 2026-10-22 12:00 UTC.

func rowsAt(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, time.UTC)
}

func rowsPtr[T any](v T) *T { return &v }

func rowsWindow() domain.Window {
	return domain.Window{
		Key:      domain.PeriodWeek,
		Location: time.UTC,
		From:     rowsAt(19, 0, 0),
		To:       rowsAt(26, 0, 0),
		PrevFrom: rowsAt(12, 0, 0),
		PrevTo:   rowsAt(19, 0, 0),
		Days:     7,
	}
}

func rowsNow() time.Time { return rowsAt(22, 12, 0) }

func rowsFacts() domain.Facts {
	return domain.Facts{From: rowsAt(12, 0, 0), To: rowsAt(26, 0, 0)}
}

func rowsFinding(review, sev string) domain.FindingFact {
	return domain.FindingFact{ReviewID: review, File: "f.go", Severity: sev}
}

func rowIDs(rows []domain.Row) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.PRID)
	}
	return ids
}

// TestBuildRows_Fixture checks every column of one busy PR in both windows against values
// computed by hand.
func TestBuildRows_Fixture(t *testing.T) {
	headReviewed := "h1"
	f := rowsFacts()
	f.Pulls = []domain.PullFact{{
		PRID: "p1", RepoID: "r-api", Repo: "api", Number: 7, Title: "Add cache", Author: "ann",
		HeadSHA: "h2", LastReviewedSHA: &headReviewed,
		Additions: 120, Deletions: 80, FilesCount: 5,
		Status:        "open",
		OpenedAt:      rowsPtr(rowsAt(12, 10, 0)),
		UpdatedAt:     rowsPtr(rowsAt(22, 9, 0)),
		FirstReviewAt: rowsPtr(rowsAt(14, 9, 0)),
	}}
	f.Reviews = []domain.ReviewFact{
		// previous window: Alpha reviewed once
		{ReviewID: "r0", PRID: "p1", AgentID: "a-alpha", AgentName: "Alpha", Score: rowsPtr(10), CreatedAt: rowsAt(14, 9, 0)},
		// current window: Alpha twice (r3 is newest), Beta once, the deleted agent once
		{ReviewID: "r1", PRID: "p1", AgentID: "a-alpha", AgentName: "Alpha", Score: rowsPtr(80), CreatedAt: rowsAt(20, 9, 0)},
		{ReviewID: "r2", PRID: "p1", AgentID: "a-beta", AgentName: "Beta", Score: rowsPtr(71), CreatedAt: rowsAt(20, 10, 0)},
		{ReviewID: "r3", PRID: "p1", AgentID: "a-alpha", AgentName: "Alpha", Score: rowsPtr(60), CreatedAt: rowsAt(21, 9, 0)},
		{ReviewID: "r4", PRID: "p1", AgentID: "", AgentName: "", CreatedAt: rowsAt(21, 10, 0)},
	}
	accepted := rowsPtr(rowsAt(21, 11, 0))
	dismissed := rowsPtr(rowsAt(21, 12, 0))
	f.Findings = []domain.FindingFact{
		{ReviewID: "r0", File: "old.go", Severity: domain.SeverityWarning},
		// r1 is not a snapshot review: ignored
		rowsFinding("r1", domain.SeverityCritical), rowsFinding("r1", domain.SeverityCritical),
		// r3: open critical, accepted critical, dismissed warning, suggestion
		{ReviewID: "r3", File: "a.go", Severity: domain.SeverityCritical},
		{ReviewID: "r3", File: "b.go", Severity: domain.SeverityCritical, AcceptedAt: accepted},
		{ReviewID: "r3", File: "a.go", Severity: domain.SeverityWarning, DismissedAt: dismissed},
		{ReviewID: "r3", File: "c.go", Severity: domain.SeveritySuggestion},
		// r2: accepted and later dismissed critical (EC7), open warning
		{ReviewID: "r2", File: "d.go", Severity: domain.SeverityCritical, AcceptedAt: accepted, DismissedAt: dismissed},
		{ReviewID: "r2", File: "d.go", Severity: domain.SeverityWarning},
		// r4: two suggestions
		rowsFinding("r4", domain.SeveritySuggestion), rowsFinding("r4", domain.SeveritySuggestion),
	}
	cost := func(v float64) *float64 { return &v }
	f.Runs = []domain.RunFact{
		{PRID: "p1", RanAt: rowsAt(15, 9, 0), CostUSD: cost(1)},     // previous window
		{PRID: "p1", RanAt: rowsAt(20, 9, 0), CostUSD: cost(0.25)},  // window
		{PRID: "p1", RanAt: rowsAt(20, 9, 30)},                      // unknown cost = 0
		{PRID: "p1", RanAt: rowsAt(21, 9, 0), CostUSD: cost(0.125)}, // window
		{PRID: "p1", RanAt: rowsAt(26, 0, 0), CostUSD: cost(5)},     // at "to": next window
	}

	cur, prev := domain.BuildRows(rowsWindow(), f, rowsNow())

	// Hand computation, window: snapshot = r3 (Alpha 60), r2 (Beta 71), r4 (Deleted agent, no
	// score); health = round((60+71)/2 = 65.5) = 66; rounds = 2 (Alpha); findings C=2+1=3,
	// W=1+1=2, S=1+2=3; open critical = 1 (r3 first; r2's is dismissed); risk points =
	// 35+35+3 (r3) + 12 (r2) + 6 (r4) = 91 -> high; lines 200 -> M; cost = .25+.125 = .375;
	// first review 47 h after opening = 169200 s; open age = 10 d 2 h = 871200 s.
	wantCur := domain.Row{
		PRID: "p1", RepoID: "r-api", Repo: "api", Number: 7, Title: "Add cache", Author: "ann",
		Status: domain.StatusNeedsReview, Additions: 120, Deletions: 80, FilesCount: 5, Size: domain.SizeM,
		Health: rowsPtr(66), Risk: domain.RiskHigh,
		Findings:           domain.FindingCounts{Critical: 3, Warning: 2, Suggestion: 3},
		OpenCritical:       1,
		Rounds:             2,
		TimeToFirstReviewS: rowsPtr(int64(169200)),
		OpenAgeS:           rowsPtr(int64(871200)),
		CostUSD:            0.375,
		OpenedAt:           rowsPtr(rowsAt(12, 10, 0)),
		FirstReviewAt:      rowsPtr(rowsAt(14, 9, 0)),

		NewCommitsSinceReview: true,
		Reviews: []domain.RowReview{
			{
				ReviewID: "r3", AgentID: rowsPtr("a-alpha"), AgentName: "Alpha", Score: rowsPtr(60),
				Findings:  domain.FindingCounts{Critical: 2, Warning: 1, Suggestion: 1},
				CreatedAt: rowsAt(21, 9, 0),
				Items: []domain.FindingItem{
					{File: "a.go", Severity: domain.SeverityCritical},
					{File: "b.go", Severity: domain.SeverityCritical, AcceptedAt: accepted},
					{File: "a.go", Severity: domain.SeverityWarning, DismissedAt: dismissed},
					{File: "c.go", Severity: domain.SeveritySuggestion},
				},
			},
			{
				ReviewID: "r2", AgentID: rowsPtr("a-beta"), AgentName: "Beta", Score: rowsPtr(71),
				Findings:  domain.FindingCounts{Critical: 1, Warning: 1},
				CreatedAt: rowsAt(20, 10, 0),
				Items: []domain.FindingItem{
					{File: "d.go", Severity: domain.SeverityCritical, AcceptedAt: accepted, DismissedAt: dismissed},
					{File: "d.go", Severity: domain.SeverityWarning},
				},
			},
			{
				ReviewID: "r4", AgentName: domain.DeletedAgentName,
				Findings:  domain.FindingCounts{Suggestion: 2},
				CreatedAt: rowsAt(21, 10, 0),
				Items: []domain.FindingItem{
					{File: "f.go", Severity: domain.SeveritySuggestion},
					{File: "f.go", Severity: domain.SeveritySuggestion},
				},
			},
		},
	}
	if diff := cmp.Diff([]domain.Row{wantCur}, cur); diff != "" {
		t.Errorf("BuildRows() current rows mismatch (-want +got):\n%s", diff)
	}

	// Previous window: only r0 (Alpha 10, one warning = 12 points -> low), cost 1.
	wantPrev := wantCur
	wantPrev.Health = rowsPtr(10)
	wantPrev.Risk = domain.RiskLow
	wantPrev.Findings = domain.FindingCounts{Warning: 1}
	wantPrev.OpenCritical = 0
	wantPrev.Rounds = 1
	wantPrev.CostUSD = 1
	wantPrev.Reviews = []domain.RowReview{{
		ReviewID: "r0", AgentID: rowsPtr("a-alpha"), AgentName: "Alpha", Score: rowsPtr(10),
		Findings:  domain.FindingCounts{Warning: 1},
		CreatedAt: rowsAt(14, 9, 0),
		Items:     []domain.FindingItem{{File: "old.go", Severity: domain.SeverityWarning}},
	}}
	if diff := cmp.Diff([]domain.Row{wantPrev}, prev); diff != "" {
		t.Errorf("BuildRows() previous rows mismatch (-want +got):\n%s", diff)
	}
}

// TestBuildRows_Population: a row needs a review of kind review created in the window's
// half-open range; merged or opened PRs without such a review are no rows.
func TestBuildRows_Population(t *testing.T) {
	f := rowsFacts()
	pull := func(id string, n int) domain.PullFact {
		return domain.PullFact{PRID: id, RepoID: "r1", Repo: "api", Number: n, Status: "open", HeadSHA: "h"}
	}
	f.Pulls = []domain.PullFact{
		pull("at-from", 1), pull("at-to", 2), pull("just-before-from", 3), pull("both", 4),
		pull("unreviewed", 5), pull("last-instant", 6),
	}
	f.Reviews = []domain.ReviewFact{
		{ReviewID: "x1", PRID: "at-from", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(19, 0, 0)},
		{ReviewID: "x2", PRID: "at-to", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(26, 0, 0)},
		{ReviewID: "x3", PRID: "just-before-from", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(19, 0, 0).Add(-time.Nanosecond)},
		{ReviewID: "x4", PRID: "both", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(15, 0, 0)},
		{ReviewID: "x5", PRID: "both", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(23, 0, 0)},
		{ReviewID: "x6", PRID: "last-instant", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(26, 0, 0).Add(-time.Nanosecond)},
		{ReviewID: "x7", PRID: "no-such-pull", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 0, 0)},
	}

	cur, prev := domain.BuildRows(rowsWindow(), f, rowsNow())

	// Ordered by repo, then number.
	if diff := cmp.Diff([]string{"at-from", "both", "last-instant"}, rowIDs(cur)); diff != "" {
		t.Errorf("BuildRows() current PRs mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"just-before-from", "both"}, rowIDs(prev)); diff != "" {
		t.Errorf("BuildRows() previous PRs mismatch (-want +got):\n%s", diff)
	}
	for _, r := range cur {
		if r.PRID == "both" && r.Rounds != 1 {
			t.Errorf("BuildRows() PR both: current rounds = %d, want 1 (the review of the previous window does not count)", r.Rounds)
		}
	}
}

func TestBuildRows_Empty(t *testing.T) {
	cur, prev := domain.BuildRows(rowsWindow(), rowsFacts(), rowsNow())
	if len(cur) != 0 || len(prev) != 0 {
		t.Errorf("BuildRows(no facts) = %d, %d rows, want 0, 0", len(cur), len(prev))
	}
	if cur == nil || prev == nil {
		t.Errorf("BuildRows(no facts) = %v, %v, want empty non-nil slices", cur, prev)
	}
}

// TestBuildRows_Health: mean of the snapshot scores, rounded half up; reviews without a score
// do not count; no score at all gives no health (null).
func TestBuildRows_Health(t *testing.T) {
	tests := []struct {
		name   string
		scores []*int // one agent each
		want   *int
	}{
		{"one score", []*int{rowsPtr(73)}, rowsPtr(73)},
		{"half rounds up", []*int{rowsPtr(80), rowsPtr(71)}, rowsPtr(76)},                      // 75.5
		{"exact mean", []*int{rowsPtr(80), rowsPtr(70)}, rowsPtr(75)},                          // 75
		{"below half rounds down", []*int{rowsPtr(80), rowsPtr(70), rowsPtr(71)}, rowsPtr(74)}, // 73.67 -> 74
		{"one third", []*int{rowsPtr(1), rowsPtr(0), rowsPtr(0)}, rowsPtr(0)},                  // 0.33 -> 0
		{"unscored review is skipped", []*int{rowsPtr(90), nil}, rowsPtr(90)},
		{"no score at all", []*int{nil, nil}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := rowsFacts()
			f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open"}}
			for i, s := range tc.scores {
				id := fmt.Sprintf("agent-%d", i)
				f.Reviews = append(f.Reviews, domain.ReviewFact{
					ReviewID: fmt.Sprintf("r%d", i), PRID: "p", AgentID: id, AgentName: id, Score: s, CreatedAt: rowsAt(20, 9, i),
				})
			}
			cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
			if len(cur) != 1 {
				t.Fatalf("BuildRows() = %d rows, want 1", len(cur))
			}
			if diff := cmp.Diff(tc.want, cur[0].Health); diff != "" {
				t.Errorf("BuildRows() health for scores %v mismatch (-want +got):\n%s", tc.scores, diff)
			}
		})
	}
}

// TestBuildRows_Rounds: the highest review count of any one agent; the deleted agent counts as
// one agent; the snapshot keeps the newest review of each (ties: the larger review id).
func TestBuildRows_Rounds(t *testing.T) {
	f := rowsFacts()
	f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open"}}
	f.Reviews = []domain.ReviewFact{
		{ReviewID: "a1", PRID: "p", AgentID: "a", AgentName: "A", Score: rowsPtr(10), CreatedAt: rowsAt(20, 9, 0)},
		{ReviewID: "b1", PRID: "p", AgentID: "b", AgentName: "B", Score: rowsPtr(20), CreatedAt: rowsAt(20, 9, 0)},
		{ReviewID: "b2", PRID: "p", AgentID: "b", AgentName: "B", Score: rowsPtr(30), CreatedAt: rowsAt(20, 10, 0)},
		// three reviews of deleted agents (even of different, now deleted, agents) = one agent
		{ReviewID: "d1", PRID: "p", CreatedAt: rowsAt(20, 9, 0)},
		{ReviewID: "d2", PRID: "p", CreatedAt: rowsAt(20, 11, 0)},
		{ReviewID: "d3", PRID: "p", CreatedAt: rowsAt(20, 11, 0)}, // same instant as d2: larger id wins
	}
	cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
	if len(cur) != 1 {
		t.Fatalf("BuildRows() = %d rows, want 1", len(cur))
	}
	if got, want := cur[0].Rounds, 3; got != want {
		t.Errorf("BuildRows() rounds = %d, want %d", got, want)
	}
	var snapshot []string
	for _, r := range cur[0].Reviews {
		snapshot = append(snapshot, r.AgentName+":"+r.ReviewID)
	}
	want := []string{"A:a1", "B:b2", "Deleted agent:d3"}
	if diff := cmp.Diff(want, snapshot); diff != "" {
		t.Errorf("BuildRows() snapshot reviews mismatch (-want +got):\n%s", diff)
	}
}

// TestBuildRows_Risk: weights 35/12/3 over non-dismissed findings; accepted ones still count.
func TestBuildRows_Risk(t *testing.T) {
	type fnd struct {
		sev       string
		dismissed bool
	}
	rep := func(n int, f fnd) []fnd {
		out := make([]fnd, n)
		for i := range out {
			out[i] = f
		}
		return out
	}
	join := func(parts ...[]fnd) []fnd {
		var out []fnd
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	c, w, s := fnd{sev: domain.SeverityCritical}, fnd{sev: domain.SeverityWarning}, fnd{sev: domain.SeveritySuggestion}
	cd := fnd{sev: domain.SeverityCritical, dismissed: true}
	tests := []struct {
		name string
		in   []fnd
		want domain.Risk
	}{
		{"no findings", nil, domain.RiskLow},
		{"9 suggestions = 27", rep(9, s), domain.RiskLow},
		{"10 suggestions = 30", rep(10, s), domain.RiskMedium},
		{"1 critical = 35", rep(1, c), domain.RiskMedium},
		{"5 warnings + 3 suggestions = 69", join(rep(5, w), rep(3, s)), domain.RiskMedium},
		{"5 warnings + 4 suggestions = 72", join(rep(5, w), rep(4, s)), domain.RiskHigh},
		{"2 critical = 70", rep(2, c), domain.RiskHigh},
		{"dismissed criticals are free", join(rep(5, cd), rep(1, w)), domain.RiskLow},
		{"1 critical + 1 dismissed = 35", join(rep(1, c), rep(1, cd)), domain.RiskMedium},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := rowsFacts()
			f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open"}}
			f.Reviews = []domain.ReviewFact{{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)}}
			for _, x := range tc.in {
				ff := domain.FindingFact{ReviewID: "r", File: "f.go", Severity: x.sev}
				if x.dismissed {
					ff.DismissedAt = rowsPtr(rowsAt(21, 9, 0))
				}
				f.Findings = append(f.Findings, ff)
			}
			cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
			if got := cur[0].Risk; got != tc.want {
				t.Errorf("BuildRows() risk for %s = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestBuildRows_OpenCritical: only CRITICAL findings with neither accepted nor dismissed time.
func TestBuildRows_OpenCritical(t *testing.T) {
	at := rowsPtr(rowsAt(21, 9, 0))
	f := rowsFacts()
	f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open"}}
	f.Reviews = []domain.ReviewFact{{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)}}
	f.Findings = []domain.FindingFact{
		{ReviewID: "r", Severity: domain.SeverityCritical},                                  // open
		{ReviewID: "r", Severity: domain.SeverityCritical},                                  // open
		{ReviewID: "r", Severity: domain.SeverityCritical, AcceptedAt: at},                  // accepted
		{ReviewID: "r", Severity: domain.SeverityCritical, DismissedAt: at},                 // dismissed
		{ReviewID: "r", Severity: domain.SeverityCritical, AcceptedAt: at, DismissedAt: at}, // EC7: dismissed
		{ReviewID: "r", Severity: domain.SeverityWarning},                                   // not critical
	}
	cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
	if got, want := cur[0].OpenCritical, 2; got != want {
		t.Errorf("BuildRows() open critical = %d, want %d", got, want)
	}
	if got, want := cur[0].Findings, (domain.FindingCounts{Critical: 5, Warning: 1}); got != want {
		t.Errorf("BuildRows() findings = %+v, want %+v (every status counts)", got, want)
	}
}

// TestBuildRows_Size: lines = additions + deletions; zero lines, zero files = unknown (EC9).
func TestBuildRows_Size(t *testing.T) {
	tests := []struct {
		name            string
		add, del, files int
		want            domain.Size
	}{
		{"all zero", 0, 0, 0, domain.SizeNone},
		{"files only", 0, 0, 1, domain.SizeS},
		{"100 lines", 60, 40, 2, domain.SizeS},
		{"101 lines", 100, 1, 2, domain.SizeM},
		{"400 lines", 399, 1, 2, domain.SizeM},
		{"401 lines", 1, 400, 2, domain.SizeL},
		{"1000 lines", 1000, 0, 2, domain.SizeL},
		{"1001 lines", 0, 1001, 2, domain.SizeXL},
		{"lines without files", 5, 0, 0, domain.SizeS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := rowsFacts()
			f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open", Additions: tc.add, Deletions: tc.del, FilesCount: tc.files}}
			f.Reviews = []domain.ReviewFact{{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)}}
			cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
			if got := cur[0].Size; got != tc.want {
				t.Errorf("BuildRows() size for +%d -%d files %d = %q, want %q", tc.add, tc.del, tc.files, got, tc.want)
			}
		})
	}
}

// TestBuildRows_Times: time to first review, time to merge and open age (EC1), in whole seconds.
func TestBuildRows_Times(t *testing.T) {
	opened := rowsPtr(rowsAt(18, 8, 0))
	first := rowsPtr(rowsAt(18, 9, 30)) // 5400 s after opening
	merged := rowsPtr(rowsAt(20, 8, 0).Add(1500 * time.Millisecond))
	tests := []struct {
		name                 string
		pull                 domain.PullFact
		wantFirst, wantMerge *int64
		wantAge              *int64
	}{
		{
			name:      "open",
			pull:      domain.PullFact{Status: "open", OpenedAt: opened, FirstReviewAt: first},
			wantFirst: rowsPtr(int64(5400)),
			wantAge:   rowsPtr(int64(4*86400 + 4*3600)), // 18th 08:00 -> 22nd 12:00
		},
		{
			name:      "merged: merge time, no open age",
			pull:      domain.PullFact{Status: "merged", OpenedAt: opened, FirstReviewAt: first, MergedAt: merged},
			wantFirst: rowsPtr(int64(5400)),
			wantMerge: rowsPtr(int64(2*86400 + 1)), // 2 d 1.5 s, truncated to whole seconds
		},
		{
			name:      "merged without merge time",
			pull:      domain.PullFact{Status: "merged", OpenedAt: opened, FirstReviewAt: first},
			wantFirst: rowsPtr(int64(5400)),
		},
		{
			name:      "closed: no merge time, no open age",
			pull:      domain.PullFact{Status: "closed", OpenedAt: opened, FirstReviewAt: first, MergedAt: merged},
			wantFirst: rowsPtr(int64(5400)),
		},
		{
			name: "EC1 seeded row without opened_at",
			pull: domain.PullFact{Status: "open", FirstReviewAt: first},
		},
		{
			name: "EC1 merged without opened_at",
			pull: domain.PullFact{Status: "merged", FirstReviewAt: first, MergedAt: merged},
		},
		{
			name:    "no first review instant",
			pull:    domain.PullFact{Status: "open", OpenedAt: opened},
			wantAge: rowsPtr(int64(4*86400 + 4*3600)),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.pull.PRID, tc.pull.Repo, tc.pull.Number = "p", "api", 1
			f := rowsFacts()
			f.Pulls = []domain.PullFact{tc.pull}
			f.Reviews = []domain.ReviewFact{{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)}}
			cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
			got := cur[0]
			if diff := cmp.Diff(tc.wantFirst, got.TimeToFirstReviewS); diff != "" {
				t.Errorf("BuildRows() TimeToFirstReviewS mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantMerge, got.TimeToMergeS); diff != "" {
				t.Errorf("BuildRows() TimeToMergeS mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAge, got.OpenAgeS); diff != "" {
				t.Errorf("BuildRows() OpenAgeS mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestBuildRows_Cost: runs of the window by RanAt in [from, to), any status, unknown cost = 0,
// rounded to MoneyDecimals.
func TestBuildRows_Cost(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	f := rowsFacts()
	f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open"}, {PRID: "q", Repo: "api", Number: 2, Status: "open"}}
	f.Reviews = []domain.ReviewFact{
		{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)},
		{ReviewID: "s", PRID: "q", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)},
	}
	f.Runs = []domain.RunFact{
		{PRID: "p", RanAt: rowsAt(19, 0, 0), CostUSD: cost(0.1)}, // at "from": window
		{PRID: "p", RanAt: rowsAt(20, 0, 0), CostUSD: cost(0.2)},
		{PRID: "p", RanAt: rowsAt(21, 0, 0), CostUSD: cost(0.3)},
		{PRID: "p", RanAt: rowsAt(22, 0, 0)},                     // unknown cost
		{PRID: "p", RanAt: rowsAt(18, 23, 59), CostUSD: cost(7)}, // previous window
		{PRID: "q", RanAt: rowsAt(20, 0, 0), CostUSD: cost(0.0000004)},
		{PRID: "other", RanAt: rowsAt(20, 0, 0), CostUSD: cost(9)},
	}
	cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
	got := map[string]float64{}
	for _, r := range cur {
		got[r.PRID] = r.CostUSD
	}
	// 0.1 + 0.2 + 0.3 is 0.6000000000000001 in float64; the row shows 0.6. 0.0000004 rounds to 0.
	want := map[string]float64{"p": 0.6, "q": 0}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildRows() cost per PR mismatch (-want +got):\n%s", diff)
	}
}

// TestBuildRows_Status: merged and closed as stored, otherwise derived; checked against the
// PR list's DeriveStatus (plan P1).
func TestBuildRows_Status(t *testing.T) {
	now := rowsNow()
	head, other := "h2", "h1"
	tests := []struct {
		name     string
		stored   string
		reviewed *string
		updated  *time.Time
		want     string
	}{
		{"merged as stored", "merged", &head, nil, domain.StatusMerged},
		{"closed as stored", "closed", nil, nil, domain.StatusClosed},
		{"open, never reviewed", "open", nil, rowsPtr(now), domain.StatusNeedsReview},
		{"open, other head reviewed", "open", &other, rowsPtr(now), domain.StatusNeedsReview},
		{"open, head reviewed, fresh", "open", &head, rowsPtr(now.Add(-time.Hour)), domain.StatusReviewed},
		{"open, head reviewed, no update time", "open", &head, nil, domain.StatusReviewed},
		{"exactly 7 days is not stale", "open", &head, rowsPtr(now.Add(-domain.StaleAfter)), domain.StatusReviewed},
		{"7 days and 1 s is stale", "open", &head, rowsPtr(now.Add(-domain.StaleAfter - time.Second)), domain.StatusStale},
		{"seed status stale is re-derived", "stale", &head, rowsPtr(now), domain.StatusReviewed},
		{"seed status reviewed is re-derived", "reviewed", nil, rowsPtr(now), domain.StatusNeedsReview},
		{"seed status needs_review is re-derived", "needs_review", &head, rowsPtr(now), domain.StatusReviewed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := rowsFacts()
			f.Pulls = []domain.PullFact{{
				PRID: "p", Repo: "api", Number: 1, Status: tc.stored, HeadSHA: head,
				LastReviewedSHA: tc.reviewed, UpdatedAt: tc.updated,
			}}
			f.Reviews = []domain.ReviewFact{{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)}}
			cur, _ := domain.BuildRows(rowsWindow(), f, now)
			got := cur[0].Status
			if got != tc.want {
				t.Errorf("BuildRows() status for %s = %q, want %q", tc.name, got, tc.want)
			}
			// Cross-check: the leaderboard re-implements the PR list's rule and must agree.
			if list := pullsdomain.DeriveStatus(tc.stored, tc.reviewed, head, tc.updated, now); list != got {
				t.Errorf("BuildRows() status for %s = %q, but the PR list's DeriveStatus = %q", tc.name, got, list)
			}
		})
	}
}

// TestBuildRows_NewCommits: true only when a reviewed head is known and differs (plan Q10).
func TestBuildRows_NewCommits(t *testing.T) {
	same, differs := "h2", "h1"
	tests := []struct {
		name     string
		reviewed *string
		want     bool
	}{
		{"never reviewed", nil, false},
		{"reviewed head equals head", &same, false},
		{"reviewed head differs", &differs, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := rowsFacts()
			f.Pulls = []domain.PullFact{{PRID: "p", Repo: "api", Number: 1, Status: "open", HeadSHA: "h2", LastReviewedSHA: tc.reviewed}}
			f.Reviews = []domain.ReviewFact{{ReviewID: "r", PRID: "p", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)}}
			cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
			if got := cur[0].NewCommitsSinceReview; got != tc.want {
				t.Errorf("BuildRows() NewCommitsSinceReview for %s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestBuildRows_Deterministic: the order of the input slices does not change the rows.
func TestBuildRows_Deterministic(t *testing.T) {
	build := func(reverse bool) []domain.Row {
		f := rowsFacts()
		f.Pulls = []domain.PullFact{
			{PRID: "p2", RepoID: "r2", Repo: "web", Number: 1, Status: "open"},
			{PRID: "p1", RepoID: "r1", Repo: "api", Number: 9, Status: "open"},
			{PRID: "p3", RepoID: "r1", Repo: "api", Number: 2, Status: "open"},
		}
		f.Reviews = []domain.ReviewFact{
			{ReviewID: "r1", PRID: "p1", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)},
			{ReviewID: "r2", PRID: "p2", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)},
			{ReviewID: "r3", PRID: "p3", AgentID: "a", AgentName: "A", CreatedAt: rowsAt(20, 9, 0)},
		}
		if reverse {
			for i, j := 0, len(f.Pulls)-1; i < j; i, j = i+1, j-1 {
				f.Pulls[i], f.Pulls[j] = f.Pulls[j], f.Pulls[i]
			}
			for i, j := 0, len(f.Reviews)-1; i < j; i, j = i+1, j-1 {
				f.Reviews[i], f.Reviews[j] = f.Reviews[j], f.Reviews[i]
			}
		}
		cur, _ := domain.BuildRows(rowsWindow(), f, rowsNow())
		return cur
	}
	want := []string{"p3", "p1", "p2"} // api #2, api #9, web #1
	for _, reverse := range []bool{false, true} {
		if diff := cmp.Diff(want, rowIDs(build(reverse))); diff != "" {
			t.Errorf("BuildRows() PR order (reversed input: %v) mismatch (-want +got):\n%s", reverse, diff)
		}
	}
}

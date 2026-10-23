package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

var aggNow = aggAt(10) // Wed Oct 15, 2026 00:00 UTC

// aggWide is a window that contains every instant of the reason and order tests.
func aggWide() domain.Span { return domain.Span{From: aggAt(-60), To: aggAt(60)} }

// aggOpen is an open, reviewed PR with nothing to report: opened 2 h and updated 1 h before aggNow.
func aggOpen(id string, number int) domain.PullFact {
	return domain.PullFact{
		PRID: id, RepoID: "r1", Repo: "api", Number: number, Title: "PR " + id, Author: "ann",
		Status: domain.StatusOpen, HeadSHA: "h1", FirstReviewAt: aggPtr(aggNow.Add(-time.Hour)),
		OpenedAt: aggPtr(aggNow.Add(-2 * time.Hour)), UpdatedAt: aggPtr(aggNow.Add(-time.Hour)),
	}
}

func TestAttentionReasons(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		name   string
		mutate func(*domain.PullFact)
		want   []domain.AttentionReason
	}{
		{"nothing to report", func(*domain.PullFact) {}, nil},
		{"open criticals on an open PR", func(p *domain.PullFact) { p.OpenCritical = 3 },
			[]domain.AttentionReason{{Kind: domain.ReasonOpenCritical, Count: 3}}},
		{"merged with open criticals only has (a)", func(p *domain.PullFact) {
			p.Status, p.OpenCritical = domain.StatusMerged, 2
			p.LastReviewedSHA, p.FirstReviewAt, p.UpdatedAt = aggPtr("h0"), nil, aggPtr(aggNow.Add(-30*day))
		}, []domain.AttentionReason{{Kind: domain.ReasonMergedOpenCritical, Count: 2}}},
		{"merged without open criticals", func(p *domain.PullFact) { p.Status = domain.StatusMerged }, nil},
		{"closed (not merged) with open criticals is (b), nothing else", func(p *domain.PullFact) {
			p.Status, p.OpenCritical, p.OpenedAt = domain.StatusClosed, 1, aggPtr(aggNow.Add(-30*day))
		}, []domain.AttentionReason{{Kind: domain.ReasonOpenCritical, Count: 1}}},
		{"never reviewed is reported at once, no grace", func(p *domain.PullFact) {
			p.FirstReviewAt = nil
		}, []domain.AttentionReason{{Kind: domain.ReasonNoReview}}},
		{"never reviewed draft is not reported", func(p *domain.PullFact) {
			p.FirstReviewAt, p.Draft = nil, true
		}, nil},
		{"new commits since the last review", func(p *domain.PullFact) { p.LastReviewedSHA = aggPtr("h0") },
			[]domain.AttentionReason{{Kind: domain.ReasonNewCommits}}},
		{"head equals the reviewed sha", func(p *domain.PullFact) { p.LastReviewedSHA = aggPtr("h1") }, nil},
		{"never reviewed head has no new commits", func(p *domain.PullFact) { p.LastReviewedSHA = nil }, nil},
		{"opened exactly 5 days ago", func(p *domain.PullFact) { p.OpenedAt = aggPtr(aggNow.Add(-5 * day)) },
			[]domain.AttentionReason{{Kind: domain.ReasonOpenLong, Days: 5}}},
		{"opened just under 5 days ago", func(p *domain.PullFact) { p.OpenedAt = aggPtr(aggNow.Add(-5*day + time.Second)) }, nil},
		{"open long counts whole days since opened", func(p *domain.PullFact) {
			p.OpenedAt = aggPtr(aggNow.Add(-9*day - 12*time.Hour))
		}, []domain.AttentionReason{{Kind: domain.ReasonOpenLong, Days: 9}}},
		{"stale without an opened time counts days since the update", func(p *domain.PullFact) {
			p.OpenedAt, p.LastReviewedSHA, p.UpdatedAt = nil, aggPtr("h1"), aggPtr(aggNow.Add(-8*day))
		}, []domain.AttentionReason{{Kind: domain.ReasonOpenLong, Days: 8}}},
		{"old update of a head that needs a review is not stale", func(p *domain.PullFact) {
			p.OpenedAt, p.UpdatedAt = nil, aggPtr(aggNow.Add(-8*day))
		}, nil},
		{"unknown opened and update times", func(p *domain.PullFact) { p.OpenedAt, p.UpdatedAt = nil, nil }, nil},
		{"draft with criticals and long open is still reported", func(p *domain.PullFact) {
			p.Draft, p.OpenCritical, p.OpenedAt = true, 1, aggPtr(aggNow.Add(-8*day))
		}, []domain.AttentionReason{
			{Kind: domain.ReasonOpenCritical, Count: 1}, {Kind: domain.ReasonOpenLong, Days: 8},
		}},
		{"all open reasons in (b) to (e) order", func(p *domain.PullFact) {
			p.OpenCritical, p.FirstReviewAt, p.OpenedAt = 2, nil, aggPtr(aggNow.Add(-10*day))
			p.LastReviewedSHA = aggPtr("h0")
		}, []domain.AttentionReason{
			{Kind: domain.ReasonOpenCritical, Count: 2}, {Kind: domain.ReasonNewCommits},
			{Kind: domain.ReasonNoReview}, {Kind: domain.ReasonOpenLong, Days: 10},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := aggOpen("p", 1)
			tc.mutate(&p)
			reviewed := []domain.Row{{PRID: "p"}} // in the window even without an opened time
			total, items := domain.Attention(aggWide(), reviewed, []domain.PullFact{p}, aggNow)
			if tc.want == nil {
				if total != 0 || len(items) != 0 {
					t.Errorf("Attention(pull) = %d, %+v, want nothing", total, items)
				}
				return
			}
			if total != 1 || len(items) != 1 {
				t.Fatalf("Attention(pull) = %d, %+v, want one item", total, items)
			}
			if diff := cmp.Diff(tc.want, items[0].Reasons); diff != "" {
				t.Errorf("Attention(pull) reasons mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAttentionItemFields(t *testing.T) {
	opened := aggPtr(aggAt(1))
	p := aggOpen("m", 7)
	p.Status, p.OpenCritical, p.Author, p.Title, p.OpenedAt = domain.StatusMerged, 2, "bo", "merged", opened
	total, items := domain.Attention(aggWide(), nil, []domain.PullFact{p}, aggNow)
	want := []domain.AttentionItem{{
		PRID: "m", RepoID: "r1", Repo: "api", Number: 7, Title: "merged", Author: "bo", OpenedAt: opened,
		Reasons: []domain.AttentionReason{{Kind: domain.ReasonMergedOpenCritical, Count: 2}},
	}}
	if total != 1 {
		t.Errorf("Attention(merged) total = %d, want 1", total)
	}
	if diff := cmp.Diff(want, items); diff != "" {
		t.Errorf("Attention(merged) mismatch (-want +got):\n%s", diff)
	}
}

// The population is the PRs opened, reviewed (a row) or merged in the window; the reasons are
// judged on the current state whatever the window (spec FR13, D21).
func TestAttentionPopulationIsThePRsOfTheWindow(t *testing.T) {
	span := aggSpan() // [Oct 5, Oct 12)
	withCritical := func(id string, number int, mutate func(*domain.PullFact)) domain.PullFact {
		p := aggOpen(id, number)
		p.OpenCritical = 1
		mutate(&p)
		return p
	}
	pulls := []domain.PullFact{
		withCritical("opened-in", 1, func(p *domain.PullFact) { p.OpenedAt = aggPtr(aggAt(2)) }),
		withCritical("merged-in", 2, func(p *domain.PullFact) {
			p.OpenedAt, p.MergedAt, p.Status = aggPtr(aggAt(-20)), aggPtr(aggAt(3)), domain.StatusMerged
		}),
		withCritical("reviewed-in", 3, func(p *domain.PullFact) { p.OpenedAt = aggPtr(aggAt(-20)) }),
		withCritical("opened-before", 4, func(p *domain.PullFact) { p.OpenedAt = aggPtr(aggAt(-20)) }),
		withCritical("opened-at-to", 5, func(p *domain.PullFact) { p.OpenedAt = aggPtr(aggAt(7)) }),
		withCritical("merged-before", 6, func(p *domain.PullFact) {
			p.OpenedAt, p.MergedAt, p.Status = aggPtr(aggAt(-20)), aggPtr(aggAt(-3)), domain.StatusMerged
		}),
		withCritical("no-instants", 7, func(p *domain.PullFact) { p.OpenedAt = nil }),
	}
	rows := []domain.Row{{PRID: "reviewed-in"}}
	total, items := domain.Attention(span, rows, pulls, aggNow)

	var got []string
	for _, it := range items {
		got = append(got, it.PRID)
	}
	want := []string{"merged-in", "reviewed-in", "opened-in"} // criticals, oldest opened first
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Attention(population) mismatch (-want +got):\n%s", diff)
	}
	if total != len(want) {
		t.Errorf("Attention(population) total = %d, want %d", total, len(want))
	}
}

func TestAttentionOrder(t *testing.T) {
	merged := aggOpen("merged", 9)
	merged.Status, merged.OpenCritical, merged.OpenedAt = domain.StatusMerged, 1, aggPtr(aggAt(5))
	crit := aggOpen("crit", 1)
	crit.OpenCritical, crit.OpenedAt = 1, aggPtr(aggAt(4))
	critOld := aggOpen("crit-old", 2)
	critOld.OpenCritical, critOld.OpenedAt = 1, aggPtr(aggAt(1))
	critUnknown := aggOpen("crit-unknown", 3)
	critUnknown.OpenCritical, critUnknown.OpenedAt = 1, nil
	noReview := aggOpen("no-review", 4)
	noReview.FirstReviewAt, noReview.OpenedAt = nil, aggPtr(aggAt(8))
	newCommits := aggOpen("new-commits", 5)
	newCommits.LastReviewedSHA, newCommits.OpenedAt = aggPtr("h0"), aggPtr(aggAt(7))
	long := aggOpen("long", 6)
	long.OpenedAt = aggPtr(aggAt(0))
	// Reasons (b) and (e): sorts with the critical group by its opened time.
	critLong := aggOpen("crit-long", 7)
	critLong.OpenCritical, critLong.OpenedAt = 1, aggPtr(aggAt(3))

	pulls := []domain.PullFact{long, newCommits, critUnknown, noReview, crit, critLong, critOld, merged}
	reviewed := []domain.Row{{PRID: "crit-unknown"}} // in the window without an opened time
	total, items := domain.Attention(aggWide(), reviewed, pulls, aggNow)

	var got []string
	for _, it := range items {
		got = append(got, it.PRID)
	}
	want := []string{"crit-old", "crit-long", "crit", "merged", "crit-unknown", "long", "new-commits", "no-review"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Attention order mismatch (-want +got):\n%s", diff)
	}
	if total != len(want) {
		t.Errorf("Attention total = %d, want %d", total, len(want))
	}
	// critLong carries both reasons, in (b), (e) order.
	wantReasons := []domain.AttentionReason{{Kind: domain.ReasonOpenCritical, Count: 1}, {Kind: domain.ReasonOpenLong, Days: 7}}
	if diff := cmp.Diff(wantReasons, items[1].Reasons); diff != "" {
		t.Errorf("Attention item crit-long reasons mismatch (-want +got):\n%s", diff)
	}
}

func TestAttentionCapsItemsButCountsAll(t *testing.T) {
	var pulls []domain.PullFact
	for i := 1; i <= 25; i++ {
		p := aggOpen(fmt.Sprintf("p%02d", i), i)
		p.OpenCritical = 1
		p.OpenedAt = aggPtr(aggAt(0, i)) // p01 is the oldest
		pulls = append(pulls, p)
	}
	total, items := domain.Attention(aggWide(), nil, pulls, aggNow)
	if total != 25 {
		t.Errorf("Attention(25 PRs) total = %d, want 25", total)
	}
	if len(items) != domain.AttentionLimit {
		t.Fatalf("Attention(25 PRs) returned %d items, want %d", len(items), domain.AttentionLimit)
	}
	if items[0].PRID != "p01" || items[19].PRID != "p20" {
		t.Errorf("Attention(25 PRs) kept %s..%s, want the oldest p01..p20", items[0].PRID, items[19].PRID)
	}
}

func TestAttention_Empty(t *testing.T) {
	total, items := domain.Attention(aggWide(), nil, nil, aggNow)
	if total != 0 || items == nil || len(items) != 0 {
		t.Errorf("Attention(nil, nil) = %d, %#v, want 0 and an empty non-nil slice", total, items)
	}
}

func TestAttentionOneItemPerPR(t *testing.T) {
	p := aggOpen("x", 1)
	p.OpenCritical = 2
	row := domain.Row{PRID: "x"}
	total, items := domain.Attention(aggWide(), []domain.Row{row}, []domain.PullFact{p, p}, aggNow)
	if total != 1 || len(items) != 1 {
		t.Fatalf("Attention(row + the pull twice) = %d, %+v, want one item", total, items)
	}
}

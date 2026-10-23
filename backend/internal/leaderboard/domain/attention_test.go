package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

var aggNow = aggAt(10) // Wed Oct 15, 2026 00:00 UTC

func aggOpen(id string, number int) domain.OpenPull {
	return domain.OpenPull{
		PRID: id, RepoID: "r1", Repo: "api", Number: number, Title: "PR " + id, Author: "ann",
		HeadSHA: "h1", Reviewed: true, OpenedAt: aggPtr(aggNow.Add(-2 * time.Hour)), UpdatedAt: aggPtr(aggNow.Add(-time.Hour)),
	}
}

func aggReasons(it domain.AttentionItem) []domain.AttentionReason { return it.Reasons }

func TestAttentionReasons(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.OpenPull)
		want   []domain.AttentionReason
	}{
		{"nothing to report", func(*domain.OpenPull) {}, nil},
		{"open criticals", func(p *domain.OpenPull) { p.OpenCritical = 3 },
			[]domain.AttentionReason{{Kind: domain.ReasonOpenCritical, Count: 3}}},
		{"never reviewed, opened just over 24 h ago", func(p *domain.OpenPull) {
			p.Reviewed, p.OpenedAt = false, aggPtr(aggNow.Add(-24*time.Hour-time.Second))
		}, []domain.AttentionReason{{Kind: domain.ReasonNoReview}}},
		{"never reviewed, opened exactly 24 h ago is within the grace", func(p *domain.OpenPull) {
			p.Reviewed, p.OpenedAt = false, aggPtr(aggNow.Add(-24*time.Hour))
		}, nil},
		{"never reviewed draft is not reported", func(p *domain.OpenPull) {
			p.Reviewed, p.Draft, p.OpenedAt = false, true, aggPtr(aggNow.Add(-72*time.Hour))
		}, nil},
		{"never reviewed without opened time is not reported (EC1)", func(p *domain.OpenPull) {
			p.Reviewed, p.OpenedAt = false, nil
		}, nil},
		{"new commits since the last review", func(p *domain.OpenPull) { p.LastReviewedSHA = aggPtr("h0") },
			[]domain.AttentionReason{{Kind: domain.ReasonNewCommits}}},
		{"head equals the reviewed sha", func(p *domain.OpenPull) { p.LastReviewedSHA = aggPtr("h1") }, nil},
		{"never reviewed has no new commits", func(p *domain.OpenPull) { p.LastReviewedSHA = nil }, nil},
		{"updated exactly 7 days ago", func(p *domain.OpenPull) { p.UpdatedAt = aggPtr(aggNow.Add(-7 * 24 * time.Hour)) },
			[]domain.AttentionReason{{Kind: domain.ReasonOpenLong, Days: 7}}},
		{"updated just under 7 days ago", func(p *domain.OpenPull) { p.UpdatedAt = aggPtr(aggNow.Add(-7*24*time.Hour + time.Second)) }, nil},
		{"open long counts whole days", func(p *domain.OpenPull) { p.UpdatedAt = aggPtr(aggNow.Add(-9*24*time.Hour - 12*time.Hour)) },
			[]domain.AttentionReason{{Kind: domain.ReasonOpenLong, Days: 9}}},
		{"unknown update time is not open long", func(p *domain.OpenPull) { p.UpdatedAt = nil }, nil},
		{"draft with criticals and stale still reported", func(p *domain.OpenPull) {
			p.Draft, p.OpenCritical, p.UpdatedAt = true, 1, aggPtr(aggNow.Add(-8*24*time.Hour))
		}, []domain.AttentionReason{
			{Kind: domain.ReasonOpenCritical, Count: 1}, {Kind: domain.ReasonOpenLong, Days: 8},
		}},
		{"all reasons in (b) to (e) order", func(p *domain.OpenPull) {
			p.OpenCritical, p.Reviewed, p.OpenedAt = 2, false, aggPtr(aggNow.Add(-48*time.Hour))
			p.LastReviewedSHA, p.UpdatedAt = aggPtr("h0"), aggPtr(aggNow.Add(-10*24*time.Hour))
		}, []domain.AttentionReason{
			{Kind: domain.ReasonOpenCritical, Count: 2}, {Kind: domain.ReasonNoReview},
			{Kind: domain.ReasonNewCommits}, {Kind: domain.ReasonOpenLong, Days: 10},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := aggOpen("p", 1)
			tc.mutate(&p)
			total, items := domain.Attention(nil, []domain.OpenPull{p}, aggNow)
			if tc.want == nil {
				if total != 0 || len(items) != 0 {
					t.Errorf("Attention(open) = %d, %+v, want nothing", total, items)
				}
				return
			}
			if total != 1 || len(items) != 1 {
				t.Fatalf("Attention(open) = %d, %+v, want one item", total, items)
			}
			if diff := cmp.Diff(tc.want, aggReasons(items[0])); diff != "" {
				t.Errorf("Attention(open) reasons mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAttentionMergedRowWithOpenCriticals(t *testing.T) {
	opened := aggPtr(aggAt(1))
	rows := []domain.Row{
		{PRID: "m", Repo: "api", Number: 7, Title: "merged", Author: "bo", Status: domain.StatusMerged, OpenCritical: 2, OpenedAt: opened},
		{PRID: "clean", Status: domain.StatusMerged},
		{PRID: "open-row", Status: domain.StatusNeedsReview, OpenCritical: 4}, // (b) comes from the open list only
		{PRID: "closed", Status: domain.StatusClosed, OpenCritical: 1},
	}
	total, items := domain.Attention(rows, nil, aggNow)
	want := []domain.AttentionItem{{
		PRID: "m", Repo: "api", Number: 7, Title: "merged", Author: "bo", OpenedAt: opened,
		Reasons: []domain.AttentionReason{{Kind: domain.ReasonMergedOpenCritical, Count: 2}},
	}}
	if total != 1 {
		t.Errorf("Attention(rows) total = %d, want 1", total)
	}
	if diff := cmp.Diff(want, items); diff != "" {
		t.Errorf("Attention(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestAttentionOrder(t *testing.T) {
	merged := domain.Row{PRID: "merged", Repo: "api", Number: 9, Status: domain.StatusMerged, OpenCritical: 1, OpenedAt: aggPtr(aggAt(5))}

	crit := aggOpen("crit", 1)
	crit.OpenCritical, crit.OpenedAt = 1, aggPtr(aggAt(4))
	critOld := aggOpen("crit-old", 2)
	critOld.OpenCritical, critOld.OpenedAt = 1, aggPtr(aggAt(1))
	critUnknown := aggOpen("crit-unknown", 3)
	critUnknown.OpenCritical, critUnknown.OpenedAt = 1, nil
	noReview := aggOpen("no-review", 4)
	noReview.Reviewed, noReview.OpenedAt = false, aggPtr(aggAt(0))
	newCommits := aggOpen("new-commits", 5)
	newCommits.LastReviewedSHA, newCommits.OpenedAt = aggPtr("h0"), aggPtr(aggAt(0))
	long := aggOpen("long", 6)
	long.UpdatedAt, long.OpenedAt = aggPtr(aggAt(0)), aggPtr(aggAt(0))
	// first reason (b) although it also has (e): sorts with the (b) group by opened time
	critLong := aggOpen("crit-long", 7)
	critLong.OpenCritical, critLong.UpdatedAt, critLong.OpenedAt = 1, aggPtr(aggAt(0)), aggPtr(aggAt(3))

	open := []domain.OpenPull{long, newCommits, critUnknown, noReview, crit, critLong, critOld}
	total, items := domain.Attention([]domain.Row{merged}, open, aggNow)

	var got []string
	for _, it := range items {
		got = append(got, it.PRID)
	}
	want := []string{"merged", "crit-old", "crit-long", "crit", "crit-unknown", "no-review", "new-commits", "long"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Attention order mismatch (-want +got):\n%s", diff)
	}
	if total != len(want) {
		t.Errorf("Attention total = %d, want %d", total, len(want))
	}
	// critLong carries both reasons, in (b), (e) order.
	wantReasons := []domain.AttentionReason{{Kind: domain.ReasonOpenCritical, Count: 1}, {Kind: domain.ReasonOpenLong, Days: 10}}
	if diff := cmp.Diff(wantReasons, items[2].Reasons); diff != "" {
		t.Errorf("Attention item crit-long reasons mismatch (-want +got):\n%s", diff)
	}
}

func TestAttentionCapsItemsButCountsAll(t *testing.T) {
	var open []domain.OpenPull
	for i := 1; i <= 25; i++ {
		p := aggOpen(fmt.Sprintf("p%02d", i), i)
		p.OpenCritical = 1
		p.OpenedAt = aggPtr(aggAt(0, i)) // p01 is the oldest
		open = append(open, p)
	}
	total, items := domain.Attention(nil, open, aggNow)
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
	total, items := domain.Attention(nil, nil, aggNow)
	if total != 0 || items == nil || len(items) != 0 {
		t.Errorf("Attention(nil, nil) = %d, %#v, want 0 and an empty non-nil slice", total, items)
	}
}

func TestAttentionOneItemPerPR(t *testing.T) {
	// The same PR as a merged row and (inconsistently) in the open list still yields one item.
	row := domain.Row{PRID: "x", Repo: "api", Number: 1, Status: domain.StatusMerged, OpenCritical: 1}
	p := aggOpen("x", 1)
	p.OpenCritical = 2
	total, items := domain.Attention([]domain.Row{row}, []domain.OpenPull{p}, aggNow)
	if total != 1 || len(items) != 1 || len(items[0].Reasons) != 2 {
		t.Fatalf("Attention(row+open of one PR) = %d, %+v, want one item with 2 reasons", total, items)
	}
	if items[0].Reasons[0].Kind != domain.ReasonMergedOpenCritical || items[0].Reasons[1].Kind != domain.ReasonOpenCritical {
		t.Errorf("Attention reasons = %+v, want (a) then (b)", items[0].Reasons)
	}
}

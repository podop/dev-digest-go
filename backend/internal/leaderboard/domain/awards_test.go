package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/leaderboard/domain"
)

func aggAwardRow(id string, number int) domain.Row {
	return domain.Row{PRID: id, Repo: "api", Number: number, Title: "PR " + id, Rounds: 1}
}

func TestAwards_Empty(t *testing.T) {
	if diff := cmp.Diff(domain.AwardSet{}, domain.Awards(aggSpan(), nil)); diff != "" {
		t.Errorf("Awards(nil) mismatch (-want +got):\n%s", diff)
	}
}

func TestAwards_Winners(t *testing.T) {
	a, b, c := aggAwardRow("a", 1), aggAwardRow("b", 2), aggAwardRow("c", 3)
	a.Health, b.Health, c.Health = aggPtr(80), aggPtr(95), aggPtr(60)
	a.Findings.Critical, b.Findings.Critical, c.Findings.Critical = 1, 3, 2
	a.TimeToMergeS, c.TimeToMergeS = aggPtr[int64](900), aggPtr[int64](300)
	a.MergedAt, c.MergedAt = aggPtr(aggAt(2)), aggPtr(aggAt(3))
	a.Rounds, b.Rounds, c.Rounds = 2, 1, 4
	a.Size, a.Additions, a.Deletions = domain.SizeM, 200, 50
	c.Size, c.Additions, c.Deletions = domain.SizeXL, 2000, 100
	b.CostUSD, c.CostUSD = 0.5, 1.25

	got := domain.Awards(aggSpan(), []domain.Row{a, b, c})
	want := domain.AwardSet{
		Cleanest:     &domain.Award{PRID: "b", Repo: "api", Number: 2, Title: "PR b", Value: 95},
		Hottest:      &domain.Award{PRID: "b", Repo: "api", Number: 2, Title: "PR b", Value: 3},
		FastestMerge: &domain.Award{PRID: "c", Repo: "api", Number: 3, Title: "PR c", Value: 300},
		MostRounds:   &domain.Award{PRID: "c", Repo: "api", Number: 3, Title: "PR c", Value: 4},
		Monster:      &domain.Award{PRID: "c", Repo: "api", Number: 3, Title: "PR c", Value: 2100},
		Priciest:     &domain.Award{PRID: "c", Repo: "api", Number: 3, Title: "PR c", Value: 1.25},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Awards(rows) mismatch (-want +got):\n%s", diff)
	}
}

func TestAwardsTiesKeepTheLowerTableRank(t *testing.T) {
	var rows []domain.Row
	for i, id := range []string{"first", "second", "third"} {
		r := aggAwardRow(id, i+1)
		r.Health = aggPtr(90)
		r.Findings.Critical = 2
		r.TimeToMergeS, r.MergedAt = aggPtr[int64](600), aggPtr(aggAt(2))
		r.Rounds = 3
		r.Size, r.Additions = domain.SizeS, 40
		r.CostUSD = 0.75
		rows = append(rows, r)
	}
	got := domain.Awards(aggSpan(), rows)
	for name, aw := range map[string]*domain.Award{
		"Cleanest": got.Cleanest, "Hottest": got.Hottest, "FastestMerge": got.FastestMerge,
		"MostRounds": got.MostRounds, "Monster": got.Monster, "Priciest": got.Priciest,
	} {
		if aw == nil || aw.PRID != "first" {
			t.Errorf("Awards(tied rows).%s = %+v, want the first row", name, aw)
		}
	}
}

func TestAwardsNeedAPositiveValue(t *testing.T) {
	r := aggAwardRow("a", 1)
	r.Health = aggPtr(0)                  // health 0 is a value: Cleanest still awarded
	r.Size, r.Additions = domain.SizeS, 0 // known size but no lines
	got := domain.Awards(aggSpan(), []domain.Row{r})
	if got.Hottest != nil {
		t.Errorf("Awards(no critical).Hottest = %+v, want nil", got.Hottest)
	}
	if got.Priciest != nil {
		t.Errorf("Awards(cost 0).Priciest = %+v, want nil", got.Priciest)
	}
	if got.Monster != nil {
		t.Errorf("Awards(0 lines).Monster = %+v, want nil", got.Monster)
	}
	if got.FastestMerge != nil {
		t.Errorf("Awards(not merged).FastestMerge = %+v, want nil", got.FastestMerge)
	}
	if got.Cleanest == nil || got.Cleanest.Value != 0 {
		t.Errorf("Awards(health 0).Cleanest = %+v, want a winner with value 0", got.Cleanest)
	}
	if got.MostRounds == nil {
		t.Error("Awards(one row).MostRounds = nil, want the row")
	}
}

func TestAwardsLeaveOutUnknownHealthAndSize(t *testing.T) {
	unknown := aggAwardRow("unknown", 1) // no health, no size (diff stats not back-filled, EC9)
	unknown.Additions, unknown.Deletions = 0, 0
	sizeNoneWithLines := aggAwardRow("none", 2)
	sizeNoneWithLines.Additions = 50 // inconsistent on purpose: SizeNone is never a Monster
	known := aggAwardRow("known", 3)
	known.Health = aggPtr(10)
	known.Size, known.Additions, known.Deletions = domain.SizeS, 1, 1

	got := domain.Awards(aggSpan(), []domain.Row{unknown, sizeNoneWithLines, known})
	if got.Cleanest == nil || got.Cleanest.PRID != "known" {
		t.Errorf("Awards(rows).Cleanest = %+v, want the row with a health", got.Cleanest)
	}
	if got.Monster == nil || got.Monster.PRID != "known" || got.Monster.Value != 2 {
		t.Errorf("Awards(rows).Monster = %+v, want the sized row with value 2", got.Monster)
	}
}

func TestAwardsFastestMergeOnlyAmongRowsMergedInTheSpan(t *testing.T) {
	inSpan := aggAwardRow("in", 1)
	inSpan.MergedAt, inSpan.TimeToMergeS = aggPtr(aggAt(2)), aggPtr[int64](900)
	before := aggAwardRow("before", 2) // faster, but merged before the window
	before.MergedAt, before.TimeToMergeS = aggPtr(aggAt(-2)), aggPtr[int64](60)
	atTo := aggAwardRow("at-to", 3) // faster, merged exactly at To: next window
	atTo.MergedAt, atTo.TimeToMergeS = aggPtr(aggAt(7)), aggPtr[int64](30)
	noMergeTime := aggAwardRow("no-merged-at", 4)
	noMergeTime.TimeToMergeS = aggPtr[int64](10)
	rows := []domain.Row{before, atTo, noMergeTime, inSpan}

	got := domain.Awards(aggSpan(), rows).FastestMerge
	if got == nil || got.PRID != "in" || got.Value != 900 {
		t.Errorf("Awards(span, rows).FastestMerge = %+v, want the row merged in the span (value 900)", got)
	}
	if got := domain.Awards(aggSpan(), []domain.Row{before, atTo}).FastestMerge; got != nil {
		t.Errorf("Awards(span, rows merged elsewhere).FastestMerge = %+v, want nil", got)
	}
}

package domain

// Awards picks the six award winners (spec FR12) from the rows of the window span, which must
// already be ranked: on a tie the row with the lower table rank (the earlier row) wins. An award without an eligible row is nil.
//
//   - Cleanest: highest health among rows that have one.
//   - Hottest: most snapshot CRITICAL findings, only when that is above 0.
//   - FastestMerge: shortest time to merge among rows merged in span (Row.MergedAt is the
//     stored value, not clipped to the window) that have a TimeToMergeS (spec FR12).
//   - MostRounds: highest round count.
//   - Monster: most changed lines among rows with a known size, only when above 0 (EC9).
//   - Priciest: highest run cost, only when above 0.
func Awards(span Span, rows []Row) AwardSet {
	var (
		set                                       AwardSet
		clean, hot, fast, rounds, monster, pricey *Row
		cleanV, hotV, fastV, roundsV, monsterV    int64
		priceyV                                   float64
	)
	for i := range rows {
		r := &rows[i]
		if r.Health != nil && (clean == nil || int64(*r.Health) > cleanV) {
			clean, cleanV = r, int64(*r.Health)
		}
		if c := int64(r.Findings.Critical); c > 0 && (hot == nil || c > hotV) {
			hot, hotV = r, c
		}
		mergedHere := r.MergedAt != nil && span.Contains(*r.MergedAt)
		if r.TimeToMergeS != nil && mergedHere && (fast == nil || *r.TimeToMergeS < fastV) {
			fast, fastV = r, *r.TimeToMergeS
		}
		if rounds == nil || int64(r.Rounds) > roundsV {
			rounds, roundsV = r, int64(r.Rounds)
		}
		if lines := int64(r.Additions + r.Deletions); r.Size != SizeNone && lines > 0 && (monster == nil || lines > monsterV) {
			monster, monsterV = r, lines
		}
		if r.CostUSD > 0 && (pricey == nil || r.CostUSD > priceyV) {
			pricey, priceyV = r, r.CostUSD
		}
	}
	set.Cleanest = aggAward(clean, float64(cleanV))
	set.Hottest = aggAward(hot, float64(hotV))
	set.FastestMerge = aggAward(fast, float64(fastV))
	set.MostRounds = aggAward(rounds, float64(roundsV))
	set.Monster = aggAward(monster, float64(monsterV))
	set.Priciest = aggAward(pricey, priceyV)
	return set
}

func aggAward(r *Row, value float64) *Award {
	if r == nil {
		return nil
	}
	return &Award{PRID: r.PRID, RepoID: r.RepoID, Repo: r.Repo, Number: r.Number, Title: r.Title, Value: value}
}

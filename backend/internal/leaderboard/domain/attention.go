package domain

import (
	"cmp"
	"slices"
	"time"
)

// aggReasonRank orders ReasonKind by the display priority (a)..(e).
var aggReasonRank = map[ReasonKind]int{
	ReasonMergedOpenCritical: 0,
	ReasonOpenCritical:       1,
	ReasonNoReview:           2,
	ReasonNewCommits:         3,
	ReasonOpenLong:           4,
}

// Attention folds the needs-attention block (spec FR13). Reason (a) comes from the window's rows
// (merged rows with open criticals); (b)-(e) come from the current state of the open pulls,
// independent of the window. One item per PR carries all its reasons in (a)..(e) order. Items are
// ordered by their first reason, then oldest opened first (unknown opened time last, then repo,
// number). total counts every item, items is capped at AttentionLimit.
func Attention(rows []Row, open []OpenPull, now time.Time) (total int, items []AttentionItem) {
	byPR := make(map[string]*AttentionItem)
	var all []*AttentionItem
	item := func(prID, repoID, repo string, number int, title, author string, openedAt *time.Time) *AttentionItem {
		if it, ok := byPR[prID]; ok {
			return it
		}
		it := &AttentionItem{PRID: prID, RepoID: repoID, Repo: repo, Number: number, Title: title, Author: author, OpenedAt: openedAt}
		byPR[prID] = it
		all = append(all, it)
		return it
	}

	for _, r := range rows {
		if r.Status == StatusMerged && r.OpenCritical >= 1 {
			it := item(r.PRID, r.RepoID, r.Repo, r.Number, r.Title, r.Author, r.OpenedAt)
			it.Reasons = append(it.Reasons, AttentionReason{Kind: ReasonMergedOpenCritical, Count: r.OpenCritical})
		}
	}
	for _, p := range open {
		var reasons []AttentionReason
		if p.OpenCritical >= 1 {
			reasons = append(reasons, AttentionReason{Kind: ReasonOpenCritical, Count: p.OpenCritical})
		}
		if !p.Draft && !p.Reviewed && p.OpenedAt != nil && now.Sub(*p.OpenedAt) > NoReviewGrace {
			reasons = append(reasons, AttentionReason{Kind: ReasonNoReview})
		}
		if p.LastReviewedSHA != nil && *p.LastReviewedSHA != p.HeadSHA {
			reasons = append(reasons, AttentionReason{Kind: ReasonNewCommits})
		}
		if p.UpdatedAt != nil && now.Sub(*p.UpdatedAt) >= OpenLongAfter {
			days := int(now.Sub(*p.UpdatedAt) / (24 * time.Hour))
			reasons = append(reasons, AttentionReason{Kind: ReasonOpenLong, Days: days})
		}
		if len(reasons) == 0 {
			continue
		}
		it := item(p.PRID, p.RepoID, p.Repo, p.Number, p.Title, p.Author, p.OpenedAt)
		it.Reasons = append(it.Reasons, reasons...)
	}

	slices.SortStableFunc(all, func(a, b *AttentionItem) int {
		return cmp.Or(
			cmp.Compare(aggReasonRank[a.Reasons[0].Kind], aggReasonRank[b.Reasons[0].Kind]),
			aggCompareOpenedAsc(a.OpenedAt, b.OpenedAt),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.Number, b.Number),
			cmp.Compare(a.PRID, b.PRID),
		)
	})

	total = len(all)
	items = make([]AttentionItem, 0, min(total, AttentionLimit))
	for _, it := range all[:min(total, AttentionLimit)] {
		slices.SortStableFunc(it.Reasons, func(a, b AttentionReason) int {
			return cmp.Compare(aggReasonRank[a.Kind], aggReasonRank[b.Kind])
		})
		items = append(items, *it)
	}
	return total, items
}

// aggCompareOpenedAsc orders oldest first, an unknown opened time last.
func aggCompareOpenedAsc(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

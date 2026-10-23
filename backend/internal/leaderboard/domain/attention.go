package domain

import (
	"cmp"
	"slices"
	"time"
)

// Attention folds the needs-attention block (spec FR13, D21). Its population is the PRs of the
// window span: every row (reviewed in the window) plus every pull opened or merged in it. Each
// is judged on its current state, not on the window:
//
//	(a) merged with at least one open critical in its current reviews
//	(b) not merged with at least one open critical
//	(c) open with commits since the last review
//	(d) open, not a draft, never reviewed
//	(e) open and stale (derived status) or opened at least OpenLongDays days ago
//
// "Open" is a stored status that is neither merged nor closed. One item per PR carries all its
// reasons in (a)..(e) order. Items are ordered by the number of critical reasons ((a), (b))
// descending, then the oldest opened first (unknown opened time last, then repo, number). total
// counts every item, items is capped at AttentionLimit.
func Attention(span Span, rows []Row, pulls []PullFact, now time.Time) (total int, items []AttentionItem) {
	reviewed := make(map[string]bool, len(rows))
	for _, r := range rows {
		reviewed[r.PRID] = true
	}

	var all []AttentionItem
	seen := make(map[string]bool, len(pulls))
	for _, p := range pulls {
		if seen[p.PRID] {
			continue
		}
		inWindow := reviewed[p.PRID] ||
			(p.OpenedAt != nil && span.Contains(*p.OpenedAt)) ||
			(p.MergedAt != nil && span.Contains(*p.MergedAt))
		if !inWindow {
			continue
		}
		seen[p.PRID] = true
		reasons := attentionReasons(p, now)
		if len(reasons) == 0 {
			continue
		}
		all = append(all, AttentionItem{
			PRID: p.PRID, RepoID: p.RepoID, Repo: p.Repo, Number: p.Number, Title: p.Title,
			Author: p.Author, OpenedAt: p.OpenedAt, Reasons: reasons,
		})
	}

	slices.SortStableFunc(all, func(a, b AttentionItem) int {
		return cmp.Or(
			cmp.Compare(attentionCriticalReasons(b), attentionCriticalReasons(a)),
			aggCompareOpenedAsc(a.OpenedAt, b.OpenedAt),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(a.Number, b.Number),
			cmp.Compare(a.PRID, b.PRID),
		)
	})

	total = len(all)
	items = make([]AttentionItem, 0, min(total, AttentionLimit))
	return total, append(items, all[:min(total, AttentionLimit)]...)
}

// attentionReasons lists the reasons (a)..(e) of one pull in that order.
func attentionReasons(p PullFact, now time.Time) []AttentionReason {
	var reasons []AttentionReason
	merged := p.Status == StatusMerged
	open := !merged && p.Status != StatusClosed
	if p.OpenCritical >= 1 {
		kind := ReasonOpenCritical
		if merged {
			kind = ReasonMergedOpenCritical
		}
		reasons = append(reasons, AttentionReason{Kind: kind, Count: p.OpenCritical})
	}
	if !open {
		return reasons
	}
	if p.LastReviewedSHA != nil && *p.LastReviewedSHA != p.HeadSHA {
		reasons = append(reasons, AttentionReason{Kind: ReasonNewCommits})
	}
	if !p.Draft && p.FirstReviewAt == nil {
		reasons = append(reasons, AttentionReason{Kind: ReasonNoReview})
	}
	if days, long := openDays(p, now); long {
		reasons = append(reasons, AttentionReason{Kind: ReasonOpenLong, Days: days})
	}
	return reasons
}

// openDays is the whole number of days an open pull has been open (since its opened time, else
// since its last update) and whether it is "open long": stale by the PR list's derived status or
// opened at least OpenLongDays days ago.
func openDays(p PullFact, now time.Time) (days int, long bool) {
	since := p.OpenedAt
	if since == nil {
		since = p.UpdatedAt
	}
	if since != nil {
		days = max(0, int(now.Sub(*since)/(24*time.Hour)))
	}
	openedLong := p.OpenedAt != nil && days >= OpenLongDays
	return days, openedLong || rowStatus(p, now) == StatusStale
}

// attentionCriticalReasons counts the critical reasons ((a), (b)) of an item.
func attentionCriticalReasons(it AttentionItem) int {
	n := 0
	for _, r := range it.Reasons {
		if r.Kind == ReasonMergedOpenCritical || r.Kind == ReasonOpenCritical {
			n++
		}
	}
	return n
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

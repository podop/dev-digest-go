package domain

import (
	"slices"
	"sort"
	"strings"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// SelectPriorPRs picks the prior merged pull requests that touched files of this one (Node
// blast/domain/history.ts, spec 7.8): candidates other than number whose files overlap files;
// the overlap is the distinct shared paths in code-unit order (JavaScript's default sort); a
// candidate without overlap is skipped. The result is ordered by the parsed MergedAt descending
// (an unparseable one counts as 0), then PR number descending, and holds at most HistoryMax
// items. It is never nil.
func SelectPriorPRs(candidates []MergedPR, number int, files []string) []PriorPR {
	changed := make(map[string]struct{}, len(files))
	for _, f := range files {
		changed[f] = struct{}{}
	}
	out := []PriorPR{}
	for _, pr := range candidates {
		if pr.Number == number {
			continue
		}
		seen := make(map[string]struct{})
		var overlap []string
		for _, f := range pr.Files {
			if _, ok := changed[f]; !ok {
				continue
			}
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			overlap = append(overlap, f)
		}
		if len(overlap) == 0 {
			continue
		}
		slices.SortStableFunc(overlap, reviewer.CompareUnits)
		out = append(out, PriorPR{
			PRNumber: pr.Number, Title: pr.Title, MergedAt: pr.MergedAt, Author: pr.Author, FilesOverlap: overlap,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := parseTime(out[i].MergedAt), parseTime(out[j].MergedAt)
		if ti != tj {
			return ti > tj
		}
		return out[i].PRNumber > out[j].PRNumber
	})
	if len(out) > HistoryMax {
		out = out[:HistoryMax]
	}
	return out
}

// timeLayouts are the forms of JavaScript's Date.parse that GitHub timestamps and their
// truncations take. A form without an offset is read as UTC (the server runs in UTC).
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseTime is Node's `Date.parse(iso) || 0` in milliseconds: 0 for a value that does not parse.
func parseTime(s string) int64 {
	if strings.HasSuffix(s, "z") { // V8 accepts a lower-case zone designator
		s = s[:len(s)-1] + "Z"
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

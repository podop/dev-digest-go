package domain

import (
	"encoding/json"
	"sort"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// trendSlices cuts the window per hour up to HourlyTrendMaxHours and per day beyond, at least in
// two slices (a line needs two points).
func trendSlices(win PeriodWindow) sliceSpec {
	span := win.spanMs()
	unit := msPerDay
	if span <= HourlyTrendMaxHours*msPerHour {
		unit = msPerHour
	}
	count := max(2, int(ceilDiv(span, unit)))
	return newSlices(win, count)
}

// ceilDiv is Math.ceil(a / b) for positive b, computed on float64 values like JavaScript.
func ceilDiv(a, b int64) float64 {
	q := float64(a) / float64(b)
	c := float64(int64(q))
	if c < q {
		c++
	}
	return c
}

// RunsTrend counts the runs per slice of the window; the slices are always present.
func RunsTrend(runs []RunFact, win PeriodWindow) []StatPoint {
	s := trendSlices(win)
	values := make([]int, s.count)
	for i := range runs {
		if at := s.index(runs[i].RanAt); at >= 0 {
			values[at]++
		}
	}
	out := make([]StatPoint, s.count)
	for i, v := range values {
		out[i] = StatPoint{Label: s.start(i), Value: v}
	}
	return out
}

// FindingsBySeverity counts the findings per severity in SeveritySlices equal slices of the
// window; other severities are ignored.
func FindingsBySeverity(facts []FindingFact, win PeriodWindow) []SeverityBucket {
	s := newSlices(win, SeveritySlices)
	out := make([]SeverityBucket, s.count)
	for i := range out {
		out[i].From = s.start(i)
	}
	for _, f := range facts {
		at := s.index(f.RanAt)
		if at < 0 { // a zero-span window has no slice for the finding (the periods of the routes are never empty)
			continue
		}
		b := &out[at]
		switch f.Severity {
		case "CRITICAL":
			b.Critical += f.Count
		case "WARNING":
			b.Warning += f.Count
		case "SUGGESTION":
			b.Suggestion += f.Count
		}
	}
	return out
}

// FindingsByCategory sums the counts per category: most findings first, then the category in
// code-unit order; categories without findings are dropped.
func FindingsByCategory(facts []FindingFact) []CategoryCount {
	sums := map[string]int{}
	var order []string
	for _, f := range facts {
		if _, seen := sums[f.Category]; !seen {
			order = append(order, f.Category)
		}
		sums[f.Category] += f.Count
	}
	out := make([]CategoryCount, 0, len(order))
	for _, c := range order {
		if sums[c] > 0 {
			out = append(out, CategoryCount{Category: c, Count: sums[c]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return compareNames(out[i].Category, out[j].Category) < 0
	})
	return out
}

// top keeps the entries with runs, most runs first, then label in code-unit order (then key, so
// that equal labels come out in a fixed order), at most TopUsage.
func top(items []UsageCount) []UsageCount {
	out := make([]UsageCount, 0, len(items))
	for _, u := range items {
		if u.Runs > 0 {
			out = append(out, u)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		if c := compareNames(out[i].Label, out[j].Label); c != 0 {
			return c < 0
		}
		return compareNames(out[i].Key, out[j].Key) < 0
	})
	return out[:min(len(out), TopUsage)]
}

// TopSkills lists the most used skills.
func TopSkills(facts []SkillFact) []UsageCount {
	items := make([]UsageCount, len(facts))
	for i, f := range facts {
		items[i] = UsageCount{Key: f.SkillID, Label: f.Name, Runs: f.Runs}
	}
	return top(items)
}

// TopMemories lists the most pulled memories from the runs' trace.memory_pulled values (one per
// counted run that has a trace). A memory counts once per run and is keyed by its text; a value
// that is not an array, an entry that is not an object with a non-blank string text, are skipped.
func TopMemories(pulledPerRun []json.RawMessage) []UsageCount {
	runs := map[string]int{}
	var order []string
	for _, raw := range pulledPerRun {
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			var obj map[string]json.RawMessage // exact key match, as JavaScript property access
			if json.Unmarshal(entry, &obj) != nil {
				continue
			}
			var text string
			if json.Unmarshal(obj["text"], &text) != nil || reviewer.JSTrim(text) == "" || seen[text] {
				continue
			}
			seen[text] = true
			if runs[text] == 0 {
				order = append(order, text)
			}
			runs[text]++
		}
	}
	items := make([]UsageCount, len(order))
	for i, text := range order {
		items[i] = UsageCount{Key: text, Label: text, Runs: runs[text]}
	}
	return top(items)
}

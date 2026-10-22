package domain

import (
	"sort"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// BuildRadius maps the answer of the repo-intel port to the BlastRadius read model (Node
// blast/domain/blast-radius.ts, spec 7.7.3).
//
// A caller that sits in the file declaring the symbol it references (same name and file as a
// changed symbol) is dropped. The rest are grouped by the referenced symbol in first-seen order,
// a repeated (symbol, file, line) inside a group is dropped, callers are sorted by rank
// descending, then name, file (English collation) and line ascending, and groups by their highest
// rank descending, then symbol (English collation). A group's endpoints and crons are the union of
// the facts of its distinct caller files in sorted caller order, first occurrence kept. Degraded
// and Reason are copied.
func BuildRadius(res PortResult) BlastRadius {
	type declKey struct{ name, file string }
	declaredIn := make(map[declKey]struct{}, len(res.ChangedSymbols))
	for _, s := range res.ChangedSymbols {
		declaredIn[declKey{s.Name, s.File}] = struct{}{}
	}

	type rankedCaller struct {
		CallerRef
		rank float64
	}
	type group struct {
		symbol  string
		callers []rankedCaller
		seen    map[CallerRef]struct{}
	}
	var groups []*group
	bySymbol := make(map[string]*group)
	for _, c := range res.Callers {
		if _, ok := declaredIn[declKey{c.ViaSymbol, c.File}]; ok {
			continue
		}
		g := bySymbol[c.ViaSymbol]
		if g == nil {
			g = &group{symbol: c.ViaSymbol, seen: make(map[CallerRef]struct{})}
			bySymbol[c.ViaSymbol] = g
			groups = append(groups, g)
		}
		ref := CallerRef{Name: c.Symbol, File: c.File, Line: c.Line}
		if _, dup := g.seen[ref]; !dup {
			g.seen[ref] = struct{}{}
			g.callers = append(g.callers, rankedCaller{CallerRef: ref, rank: c.Rank})
		}
	}

	cmp := reviewer.EnglishComparer()
	maxRank := make(map[*group]float64, len(groups))
	for _, g := range groups {
		best := g.callers[0].rank
		for _, c := range g.callers[1:] {
			best = max(best, c.rank)
		}
		maxRank[g] = best
		sort.SliceStable(g.callers, func(i, j int) bool {
			a, b := g.callers[i], g.callers[j]
			if a.rank != b.rank {
				return a.rank > b.rank
			}
			if n := cmp(a.Name, b.Name); n != 0 {
				return n < 0
			}
			if n := cmp(a.File, b.File); n != 0 {
				return n < 0
			}
			return a.Line < b.Line
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if maxRank[a] != maxRank[b] {
			return maxRank[a] > maxRank[b]
		}
		return cmp(a.symbol, b.symbol) < 0
	})

	downstream := make([]Downstream, len(groups))
	for i, g := range groups {
		refs := make([]CallerRef, len(g.callers))
		endpoints, crons := []string{}, []string{}
		seenFile := make(map[string]struct{}, len(g.callers))
		seenEndpoint, seenCron := map[string]struct{}{}, map[string]struct{}{}
		for j, c := range g.callers {
			refs[j] = c.CallerRef
			if _, dup := seenFile[c.File]; dup {
				continue
			}
			seenFile[c.File] = struct{}{}
			facts := res.FactsByFile[c.File]
			for _, e := range facts.Endpoints {
				if _, dup := seenEndpoint[e]; !dup {
					seenEndpoint[e] = struct{}{}
					endpoints = append(endpoints, e)
				}
			}
			for _, e := range facts.Crons {
				if _, dup := seenCron[e]; !dup {
					seenCron[e] = struct{}{}
					crons = append(crons, e)
				}
			}
		}
		downstream[i] = Downstream{Symbol: g.symbol, Callers: refs, EndpointsAffected: endpoints, CronsAffected: crons}
	}

	changed := make([]ChangedSymbol, len(res.ChangedSymbols))
	copy(changed, res.ChangedSymbols)
	return BlastRadius{
		ChangedSymbols: changed,
		Downstream:     downstream,
		Summary:        Summary(len(res.ChangedSymbols), downstream),
		Degraded:       res.Degraded,
		Reason:         res.Reason,
	}
}

// Summary is the one-line summary of a blast radius, built from distinct counts, e.g. "3 changed
// symbols · 7 callers · 2 endpoints · 1 cron" (the separator is U+00B7). A caller that reaches
// several symbols counts once, by (file, name, line); endpoints and crons count distinct over all
// groups.
func Summary(changedSymbols int, downstream []Downstream) string {
	type callerKey struct {
		file, name string
		line       int
	}
	callers := map[callerKey]struct{}{}
	endpoints, crons := map[string]struct{}{}, map[string]struct{}{}
	for _, d := range downstream {
		for _, c := range d.Callers {
			callers[callerKey{c.File, c.Name, c.Line}] = struct{}{}
		}
		for _, e := range d.EndpointsAffected {
			endpoints[e] = struct{}{}
		}
		for _, e := range d.CronsAffected {
			crons[e] = struct{}{}
		}
	}
	return strings.Join([]string{
		plural(changedSymbols, "changed symbol", "changed symbols"),
		plural(len(callers), "caller", "callers"),
		plural(len(endpoints), "endpoint", "endpoints"),
		plural(len(crons), "cron", "crons"),
	}, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

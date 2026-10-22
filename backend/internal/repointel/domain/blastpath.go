package domain

import (
	"slices"
	"strings"
)

// The pure rules of BlastRadius (spec section 7.7, Node application/blast-radius.ts and
// domain/rules.ts). The use case in package app reads the rows and the clone; everything that
// decides lives here.

// ResolvedCaller is a "references" row whose decl_file is one of the changed files, joined with
// the file_rank of its from_path.
type ResolvedCaller struct {
	File     string
	ToSymbol string
	Line     int
	Rank     float64
}

// UsableIndexFlags says how a usable (full or partial) index is tagged: a state whose
// stats.bounded is above zero is repo_too_large, a partial one index_partial, any other is
// complete.
func UsableIndexFlags(st IndexState) (degraded bool, reason DegradedReason) {
	if statInt(st.Stats["bounded"]) > 0 {
		return true, ReasonRepoTooLarge
	}
	if st.Status == StatusPartial {
		return true, ReasonIndexPartial
	}
	return false, ""
}

// UsableIndex reports whether the persistent path may answer from the state: status full or
// partial.
func UsableIndex(st IndexState) bool {
	return st.Status == StatusFull || st.Status == StatusPartial
}

// UnusableIndexReason is why an unusable index (status failed or degraded, or any other) sends
// the call to the clone scan: failed is index_failed; a degraded row gives its
// stats.degradedReason, or index_failed without one; anything else is no_data.
func UnusableIndexReason(st IndexState) DegradedReason {
	switch st.Status {
	case StatusFailed:
		return ReasonIndexFailed
	case StatusDegraded:
		if s, ok := st.Stats[StatDegradedReason].(string); ok && validReason(DegradedReason(s)) {
			return DegradedReason(s)
		}
		return ReasonIndexFailed
	}
	return ReasonNoData
}

// LineName is a symbol of a file as the enclosing-symbol rule sees it.
type LineName struct {
	Line int
	Name string
}

// Enclosing answers "which top-level symbol does a reference at line belong to": among the
// symbols whose name has no '.' and whose line is not after the reference line, the one with the
// largest line. Of symbols on the same line the one given first wins. It is built once per
// file and answers in O(log n).
type Enclosing struct{ syms []LineName }

// NewEnclosing indexes the symbols of one file; they are taken in the order given, which is the
// tie-break of symbols on one line (extraction order, or name byte order for stored rows).
func NewEnclosing(syms []LineName) Enclosing {
	cand := make([]LineName, 0, len(syms))
	for _, s := range syms {
		if !strings.Contains(s.Name, ".") {
			cand = append(cand, s)
		}
	}
	slices.SortStableFunc(cand, func(a, b LineName) int { return a.Line - b.Line })
	out := cand[:0]
	for _, s := range cand {
		if len(out) == 0 || out[len(out)-1].Line != s.Line {
			out = append(out, s)
		}
	}
	return Enclosing{syms: out}
}

// At returns the enclosing symbol of a reference at line; ok is false when none lies at or
// before it.
func (e Enclosing) At(line int) (name string, ok bool) {
	i, _ := slices.BinarySearchFunc(e.syms, line, func(s LineName, line int) int {
		if s.Line <= line {
			return -1
		}
		return 1
	})
	if i == 0 {
		return "", false
	}
	return e.syms[i-1].Name, true
}

// CallerLabel is the symbol that labels a caller row: the enclosing symbol, else the last
// segment of the file path (Node's fromPath.split('/').pop()).
func CallerLabel(e Enclosing, file string, line int) string {
	if name, ok := e.At(line); ok {
		return name
	}
	return file[strings.LastIndexByte(file, '/')+1:]
}

// ChangedSymbols returns the symbols the index holds for the changed files, in the given order
// (path, line, name), without the qualified "Class.method" forms and deduplicated by (name,
// file) with the first kept, and the distinct names of the kept ones.
func ChangedSymbols(rows []SymbolInfo) (symbols []BlastSymbol, names []string) {
	type key struct{ name, file string }
	seen := map[key]struct{}{}
	known := map[string]struct{}{}
	for _, r := range rows {
		if strings.Contains(r.Name, ".") {
			continue
		}
		if _, ok := known[r.Name]; !ok {
			known[r.Name] = struct{}{}
			names = append(names, r.Name)
		}
		k := key{r.Name, r.File}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		symbols = append(symbols, BlastSymbol{File: r.File, Name: r.Name, Kind: r.Kind})
	}
	return symbols, names
}

// AssembleCallers turns the resolved caller rows, already in rank order (descending, then path,
// line and symbol), into caller rows: each is labelled with its enclosing symbol from the stored
// symbols of its file (symbolsByFile), repeats of (file, enclosing symbol, changed symbol) are
// dropped keeping the first, the order is made rank descending (stable), and at most
// MaxCallersPerSymbol rows are kept per changed symbol.
func AssembleCallers(rows []ResolvedCaller, symbolsByFile map[string][]LineName) []BlastCaller {
	type key struct{ file, enclosing, via string }
	var (
		index = map[string]Enclosing{}
		seen  = map[key]struct{}{}
		all   []BlastCaller
	)
	for _, c := range rows {
		enc, ok := index[c.File]
		if !ok {
			enc = NewEnclosing(symbolsByFile[c.File])
			index[c.File] = enc
		}
		label := CallerLabel(enc, c.File, c.Line)
		k := key{c.File, label, c.ToSymbol}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		all = append(all, BlastCaller{File: c.File, Symbol: label, ViaSymbol: c.ToSymbol, Line: c.Line, Rank: c.Rank})
	}
	slices.SortStableFunc(all, func(a, b BlastCaller) int {
		switch {
		case a.Rank > b.Rank:
			return -1
		case a.Rank < b.Rank:
			return 1
		}
		return 0
	})
	perSymbol := map[string]int{}
	kept := all[:0]
	for _, c := range all {
		if perSymbol[c.ViaSymbol] >= MaxCallersPerSymbol {
			continue
		}
		perSymbol[c.ViaSymbol]++
		kept = append(kept, c)
	}
	return kept
}

// CallerFiles returns the distinct files of callers, in first-seen order.
func CallerFiles(callers []BlastCaller) []string {
	var files []string
	seen := map[string]struct{}{}
	for _, c := range callers {
		if _, dup := seen[c.File]; dup {
			continue
		}
		seen[c.File] = struct{}{}
		files = append(files, c.File)
	}
	return files
}

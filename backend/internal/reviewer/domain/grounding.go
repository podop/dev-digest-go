package domain

import "strconv"

// GroundingResult is the outcome of GroundFindings.
type GroundingResult struct {
	Kept    []Finding
	Dropped []Dropped
}

// Summary is "<kept>/<total> passed".
func (r GroundingResult) Summary() string {
	return strconv.Itoa(len(r.Kept)) + "/" + strconv.Itoa(len(r.Kept)+len(r.Dropped)) + " passed"
}

// isFullFileKind says whether a finding kind is tied to a file rather than a diff line.
func isFullFileKind(k Kind) bool {
	switch k {
	case KindSecretLeak, KindLethalTrifecta, KindPhantom, KindHook:
		return true
	}
	return false
}

// lineSet is the set of new-side line numbers a file's hunks cover. A hunk without listed lines
// covers its declared range, kept as a span so that a huge declared range costs nothing.
type lineSet struct {
	lines []int
	spans [][2]int
}

func (s lineSet) intersects(lo, hi int) bool {
	for _, n := range s.lines {
		if n >= lo && n <= hi {
			return true
		}
	}
	for _, sp := range s.spans {
		if sp[0] <= hi && sp[1] >= lo {
			return true
		}
	}
	return false
}

// lineIndex maps each file path to the lines its hunks cover. Like the Node index, the last file
// of a repeated path wins.
func lineIndex(d Diff) map[string]lineSet {
	idx := make(map[string]lineSet, len(d.Files))
	for _, f := range d.Files {
		var set lineSet
		for _, h := range f.Hunks {
			if len(h.NewLineNumbers) > 0 {
				set.lines = append(set.lines, h.NewLineNumbers...)
				continue
			}
			set.spans = append(set.spans, [2]int{h.NewStart, h.NewStart + max(h.NewLines, 1) - 1})
		}
		idx[f.Path] = set
	}
	return idx
}

// GroundFindings is the citation gate (spec FR4027): a finding is kept only when its file is a
// file of the diff and, unless its kind is a full-file kind, its inclusive line range (either
// order) contains a new-side line of one of the file's hunks. The check walks the hunk lines,
// never the cited range, so a range of 10^12 lines costs nothing; a range ending below line 1
// never matches. Order is preserved in both results.
func GroundFindings(findings []Finding, d Diff) GroundingResult {
	idx := lineIndex(d)
	inDiff := make(map[string]bool, len(d.Files))
	for _, f := range d.Files {
		inDiff[f.Path] = true
	}
	var res GroundingResult
	for _, f := range findings {
		if !inDiff[f.File] {
			res.Dropped = append(res.Dropped, Dropped{Finding: f, Reason: "file '" + f.File + "' not present in diff"})
			continue
		}
		if isFullFileKind(f.Kind) {
			res.Kept = append(res.Kept, f)
			continue
		}
		lo, hi := min(f.StartLine, f.EndLine), max(f.StartLine, f.EndLine)
		if hi >= 1 && idx[f.File].intersects(lo, hi) {
			res.Kept = append(res.Kept, f)
			continue
		}
		res.Dropped = append(res.Dropped, Dropped{
			Finding: f,
			Reason: "lines " + strconv.Itoa(f.StartLine) + "-" + strconv.Itoa(f.EndLine) +
				" do not intersect any diff hunk in '" + f.File + "'",
		})
	}
	return res
}

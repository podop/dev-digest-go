package domain

import "strings"

// lineDiffMaxCells is the size of the LCS table above which the changed middle of a prompt diff is
// reported as "delete all, add all".
const lineDiffMaxCells = 4_000_000

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// LineDiff is the line diff of two texts by longest common subsequence. The text is split on "\n"
// (empty text has no lines); the common head and tail are trimmed first and kept as OpSame; the
// changed middle is diffed by LCS, preferring a deletion on ties; a middle with an empty side, or
// too big for the LCS table ((|A|+1)(|B|+1) > 4,000,000), is all deletions then all additions.
// The result is never nil.
func LineDiff(before, after string) []DiffLine {
	a, b := splitLines(before), splitLines(after)
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	out := make([]DiffLine, 0, len(a)+len(b)-head-tail)
	for _, l := range a[:head] {
		out = append(out, DiffLine{Op: OpSame, Line: l})
	}
	out = append(out, diffMiddle(a[head:len(a)-tail], b[head:len(b)-tail])...)
	for _, l := range a[len(a)-tail:] {
		out = append(out, DiffLine{Op: OpSame, Line: l})
	}
	return out
}

func diffMiddle(a, b []string) []DiffLine {
	out := make([]DiffLine, 0, len(a)+len(b))
	if len(a) == 0 || len(b) == 0 || (len(a)+1)*(len(b)+1) > lineDiffMaxCells {
		for _, l := range a {
			out = append(out, DiffLine{Op: OpDel, Line: l})
		}
		for _, l := range b {
			out = append(out, DiffLine{Op: OpAdd, Line: l})
		}
		return out
	}
	// lcs[i*width+j] is the LCS length of a[i:] and b[j:].
	width := len(b) + 1
	lcs := make([]uint32, (len(a)+1)*width)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
			} else {
				lcs[i*width+j] = max(lcs[(i+1)*width+j], lcs[i*width+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{Op: OpSame, Line: a[i]})
			i++
			j++
		case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
			out = append(out, DiffLine{Op: OpDel, Line: a[i]})
			i++
		default:
			out = append(out, DiffLine{Op: OpAdd, Line: b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, DiffLine{Op: OpDel, Line: a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, DiffLine{Op: OpAdd, Line: b[j]})
	}
	return out
}

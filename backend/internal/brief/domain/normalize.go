package domain

// Turns the model's output into the stored brief content and derives the stale flag (spec
// section 7.9.5 and 7.9.6; Node brief/domain/normalize.ts). The model only proposes: every file is
// checked against the pull request's changed files and the blast data, every line is grounded
// again, text is clamped to the stored limits (so a stored brief passes the contract), and
// nothing is asked again.

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// GroundFile is a changed file of the pull request with its ranges.
type GroundFile struct {
	Path   string
	Ranges []LineRange
}

// GroundFinding is where a current finding sits.
type GroundFinding struct {
	File               string
	StartLine, EndLine int
}

// GroundingFacts is what the model's file and line claims are checked against.
type GroundingFacts struct {
	// Files are all the changed files of the pull request (not only the listed ones).
	Files []GroundFile
	// Findings are all the current findings.
	Findings []GroundFinding
	// Blast is the usable blast; nil when missing or degraded.
	Blast *Blast
}

// NewGroundingFacts builds the facts from the changed files, the findings and the usable blast.
func NewGroundingFacts(files []ChangedFile, findings []FindingFact, blast *Blast) GroundingFacts {
	g := GroundingFacts{Blast: blast}
	for _, f := range files {
		g.Files = append(g.Files, GroundFile{Path: f.Path, Ranges: f.Ranges})
	}
	for _, f := range findings {
		g.Findings = append(g.Findings, GroundFinding{File: f.File, StartLine: f.StartLine, EndLine: f.EndLine})
	}
	return g
}

// DroppedCounts are the items removed by normalisation (counts only, never text; they go to the
// generation log line).
type DroppedCounts struct {
	Risks       int
	ReviewFocus int
	// RiskRefs counts the individual risk file refs removed (unknown path), those of dropped
	// risks included.
	RiskRefs int
}

// NormalizedBrief is the clamped, grounded and limited model output.
type NormalizedBrief struct {
	Summary     string
	Risks       []Risk
	ReviewFocus []FocusItem
	Dropped     DroppedCounts
}

// NormalizePath trims a path, removes leading "./" repeatedly and then leading "/"s.
func NormalizePath(raw string) string {
	p := reviewer.JSTrim(raw)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return strings.TrimLeft(p, "/")
}

// fileGrounding is what is known about one file. Requested lines are float64 (the model can
// write any number, as JavaScript numbers are); known lines are ints.
type fileGrounding struct {
	inPR        bool
	ranges      []LineRange
	findings    []LineRange
	callerLines []int
}

type groundingIndex map[string]*fileGrounding

func (ix groundingIndex) entry(path string) *fileGrounding {
	e := ix[path]
	if e == nil {
		e = &fileGrounding{}
		ix[path] = e
	}
	return e
}

func newGroundingIndex(f GroundingFacts) groundingIndex {
	ix := groundingIndex{}
	for _, file := range f.Files {
		e := ix.entry(NormalizePath(file.Path))
		e.inPR = true
		e.ranges = append(e.ranges, file.Ranges...)
	}
	for _, fd := range f.Findings {
		// A finding on a file outside the PR's files and the blast data does not make that file known.
		if e := ix[NormalizePath(fd.File)]; e != nil {
			e.findings = append(e.findings, LineRange{Start: fd.StartLine, End: fd.EndLine})
		}
	}
	if f.Blast != nil {
		for _, s := range f.Blast.ChangedSymbols {
			ix.entry(NormalizePath(s.File))
		}
		for _, g := range f.Blast.Downstream {
			for _, c := range g.Callers {
				e := ix.entry(NormalizePath(c.File))
				e.callerLines = append(e.callerLines, c.Line)
			}
		}
	}
	return ix
}

func (g *fileGrounding) grounded(line float64) bool {
	for _, r := range g.ranges {
		if line >= float64(r.Start) && line <= float64(r.End) {
			return true
		}
	}
	for _, r := range g.findings {
		if line >= float64(r.Start) && line <= float64(r.End) {
			return true
		}
	}
	for _, c := range g.callerLines {
		if line == float64(c) {
			return true
		}
	}
	return false
}

// groundLine keeps a grounded requested line; else the first changed line (a pull request file)
// or the first caller line (a blast-only file); else the requested line when it is at least 1,
// else 1. requested is nil when the model gave no usable line.
func (g *fileGrounding) groundLine(requested *float64) (line float64, kept bool) {
	var wanted *float64
	if requested != nil && *requested >= 1 {
		wanted = requested
	}
	if wanted != nil && g.grounded(*wanted) {
		return *wanted, true
	}
	if g.inPR {
		if len(g.ranges) > 0 {
			m := g.ranges[0].Start
			for _, r := range g.ranges[1:] {
				m = min(m, r.Start)
			}
			return float64(m), false
		}
	} else if len(g.callerLines) > 0 {
		m := g.callerLines[0]
		for _, c := range g.callerLines[1:] {
			m = min(m, c)
		}
		return float64(m), false
	}
	if wanted != nil {
		return *wanted, true
	}
	return 1, false
}

// toLine is Math.trunc of a finite number; a non-finite one is no line.
func toLine(n float64) *float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	t := math.Trunc(n)
	return &t
}

// refPattern is /^(.*?):(\d+)(?:-(\d+))?$/ of the JavaScript: "." there does not match a line
// terminator (\n, \r, U+2028, U+2029), which Go's "." would (apart from \n).
var refPattern = regexp.MustCompile("^([^\\n\\r  ]*?):(\\d+)(?:-(\\d+))?$")

type fileRef struct {
	path       string
	start, end *float64
}

// parseRef splits "path", "path:12" or "path:12-20"; a missing line is nil. The numbers are
// JavaScript Numbers (float64).
func parseRef(raw string) fileRef {
	m := refPattern.FindStringSubmatch(reviewer.JSTrim(raw))
	if m == nil {
		return fileRef{path: NormalizePath(raw)}
	}
	r := fileRef{path: NormalizePath(m[1])}
	s, _ := strconv.ParseFloat(m[2], 64)
	r.start = &s
	if m[3] != "" {
		e, _ := strconv.ParseFloat(m[3], 64)
		r.end = &e
	}
	return r
}

func normalizeRefs(raw []string, ix groundingIndex) (refs []string, removed int) {
	for _, s := range raw {
		p := parseRef(s)
		var g *fileGrounding
		if p.path != "" {
			g = ix[p.path]
		}
		if g == nil {
			removed++
			continue
		}
		line, kept := g.groundLine(p.start)
		ref := p.path + ":" + reviewer.JSNumber(line)
		if kept && p.end != nil && *p.end > line {
			ref = p.path + ":" + reviewer.JSNumber(line) + "-" + reviewer.JSNumber(*p.end)
		}
		dup := false
		for _, have := range refs {
			if have == ref {
				dup = true
				break
			}
		}
		if !dup {
			refs = append(refs, ref)
		}
	}
	return refs, removed
}

// maxFocusLine is the largest line a FocusItem can hold exactly (JavaScript's largest safe
// integer); a larger one the model asked for, and no fact contradicts, is clamped to it.
const maxFocusLine = 1<<53 - 1

// NormalizeBrief checks the model's output against the facts. Items are validated first and the
// limits applied afterwards, so a valid item is never lost to an invalid one.
func NormalizeBrief(raw LLMOutput, facts GroundingFacts) NormalizedBrief {
	ix := newGroundingIndex(facts)

	removedRefs := 0
	var validRisks []Risk
	for _, r := range raw.Risks {
		refs, removed := normalizeRefs(r.FileRefs, ix)
		removedRefs += removed
		title := reviewer.Cut(r.Title, MaxRiskTitle)
		if len(refs) == 0 || title == "" {
			continue
		}
		kind := reviewer.Cut(r.Kind, MaxRiskKind)
		if kind == "" {
			kind = DefaultRiskKind
		}
		validRisks = append(validRisks, Risk{
			Kind:        kind,
			Title:       title,
			Explanation: reviewer.Cut(r.Explanation, MaxRiskExplanation),
			Severity:    RiskSeverity(r.Severity),
			FileRefs:    refs,
		})
	}
	risks := validRisks
	if len(risks) > MaxRisks {
		risks = risks[:MaxRisks]
	}

	seen := map[string]bool{}
	var validFocus []FocusItem
	for _, f := range raw.ReviewFocus {
		path := NormalizePath(f.File)
		g := ix[path]
		reason := reviewer.Cut(f.Reason, MaxFocusReason)
		if g == nil || reason == "" {
			continue
		}
		line, _ := g.groundLine(toLine(f.Line))
		key := path + ":" + reviewer.JSNumber(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		validFocus = append(validFocus, FocusItem{File: path, Line: int(math.Min(line, maxFocusLine)), Reason: reason})
	}
	focus := validFocus
	if len(focus) > MaxFocus {
		focus = focus[:MaxFocus]
	}

	return NormalizedBrief{
		Summary:     reviewer.Cut(raw.Summary, MaxSummary),
		Risks:       risks,
		ReviewFocus: focus,
		Dropped: DroppedCounts{
			Risks:       len(raw.Risks) - len(risks),
			ReviewFocus: len(raw.ReviewFocus) - len(focus),
			RiskRefs:    removedRefs,
		},
	}
}

// IsStale reports whether a stored brief is out of date: its head SHA is not the pull request's
// current one, or its prompt version is not PromptVersion.
func IsStale(storedHeadSHA, storedPromptVersion, currentHeadSHA string) bool {
	return storedHeadSHA != currentHeadSHA || storedPromptVersion != PromptVersion
}

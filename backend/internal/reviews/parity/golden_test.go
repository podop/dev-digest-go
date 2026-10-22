package parity_test

// Parity of the Go engine with the Node implementation of dev-digest (commit d1c9093): the
// goldens in testdata/goldens were produced by running Node (testdata/gen/README.md). This file
// holds what every parity test shares: loading a golden, the `$blob` digest of a long string,
// the synthetic diffs of the corpus and the wire shape of a finding.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// loadGolden decodes testdata/goldens/<name>.json into v.
func loadGolden(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "goldens", name+".json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
}

// loadHandwritten decodes testdata/handwritten/<name>.json into v.
func loadHandwritten(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "handwritten", name+".json"))
	if err != nil {
		t.Fatalf("read handwritten %s: %v", name, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode handwritten %s: %v", name, err)
	}
}

// utf16Len is the length of s in UTF-16 code units, as JavaScript counts it.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
		if utf16.RuneLen(r) < 0 { // an invalid rune counts as U+FFFD
			n++
		}
	}
	return n
}

// blob is the digest a golden writes for a string above 16,384 UTF-16 units.
type blob struct {
	Len    int    `json:"len"`
	SHA256 string `json:"sha256"`
	Head   string `json:"head"`
	Tail   string `json:"tail"`
}

// text is a golden string: the text itself or its blob digest.
type text struct {
	s string
	b *blob
}

func (x *text) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '{' {
		var d struct {
			Blob bool `json:"$blob"`
			blob
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		if !d.Blob {
			return fmt.Errorf("object %s is not a $blob digest", raw)
		}
		b := d.blob
		x.b = &b
		return nil
	}
	return json.Unmarshal(raw, &x.s)
}

// matches reports whether got equals the golden string (by length and SHA-256 for a blob).
func (x text) matches(got string) bool {
	if x.b == nil {
		return got == x.s
	}
	sum := sha256.Sum256([]byte(got))
	return utf16Len(got) == x.b.Len && hex.EncodeToString(sum[:]) == x.b.SHA256
}

// describe is what an error message shows for the golden string.
func (x text) describe() string {
	if x.b == nil {
		return fmt.Sprintf("%q", x.s)
	}
	return fmt.Sprintf("blob{len=%d sha256=%s head=%q}", x.b.Len, x.b.SHA256, x.b.Head)
}

// describeGot is the same description for a string a Go function returned.
func describeGot(got string) string {
	if utf16Len(got) <= 16_384 {
		return fmt.Sprintf("%q", got)
	}
	sum := sha256.Sum256([]byte(got))
	r := []rune(got)
	return fmt.Sprintf("blob{len=%d sha256=%s head=%q}", utf16Len(got), hex.EncodeToString(sum[:]), string(r[:min(80, len(r))]))
}

// checkText fails t when got differs from the golden string.
func checkText(t testing.TB, what string, want text, got string) {
	t.Helper()
	if !want.matches(got) {
		t.Errorf("%s = %s, want %s", what, describeGot(got), want.describe())
	}
}

// synthHunk is one hunk of a synthetic diff (README "Synthetic diffs").
type synthHunk struct {
	Start int    `json:"start"`
	Add   int    `json:"add"`
	Del   int    `json:"del"`
	Ctx   int    `json:"ctx"`
	Text  string `json:"text"`
}

// synthSpec describes a synthetic diff of the corpus.
type synthSpec struct {
	Files []struct {
		Path  string      `json:"path"`
		Hunks []synthHunk `json:"hunks"`
	} `json:"files"`
}

func (h synthHunk) lines() []string {
	txt := h.Text
	if txt == "" {
		txt = "const value = compute();"
	}
	out := []string{fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.Start, h.Ctx+h.Del, h.Start, h.Ctx+h.Add)}
	for i := 1; i <= h.Ctx; i++ {
		out = append(out, fmt.Sprintf(" %s ctx%d", txt, i))
	}
	for i := 1; i <= h.Del; i++ {
		out = append(out, fmt.Sprintf("-%s old%d", txt, i))
	}
	for i := 1; i <= h.Add; i++ {
		out = append(out, fmt.Sprintf("+%s new%d", txt, i))
	}
	return out
}

// raw is the raw diff of the spec: per file the diff --git, --- and +++ lines and its hunks,
// joined with LF without a trailing newline.
func (s synthSpec) raw() string {
	var lines []string
	for _, f := range s.Files {
		lines = append(lines, "diff --git a/"+f.Path+" b/"+f.Path, "--- a/"+f.Path, "+++ b/"+f.Path)
		for _, h := range f.Hunks {
			lines = append(lines, h.lines()...)
		}
	}
	return strings.Join(lines, "\n")
}

// padTo extends the last line of raw with "x" until it is exactly n UTF-16 units long.
func padTo(raw string, n int) string {
	if k := utf16Len(raw); k < n {
		return raw + strings.Repeat("x", n-k)
	}
	return raw
}

// rawOf is the raw diff of a golden case: the stored text, or — for a blob — the synthetic diff
// rebuilt from its spec, which must reproduce the blob's digest.
func rawOf(t testing.TB, stored text, synth *synthSpec, pad int) string {
	t.Helper()
	if stored.b == nil {
		return stored.s
	}
	if synth == nil {
		t.Fatalf("a blob diff needs a synth spec (%s)", stored.describe())
	}
	raw := synth.raw()
	if pad > 0 {
		raw = padTo(raw, pad)
	}
	if !stored.matches(raw) {
		t.Fatalf("the diff rebuilt from synth is %s, want %s", describeGot(raw), stored.describe())
	}
	return raw
}

var elapsed = regexp.MustCompile(`\(\d+ms\)`)

// normMS replaces every "(<n>ms)" of a step message: timings come from a logical clock and are
// not parity data.
func normMS(s string) string { return elapsed.ReplaceAllString(s, "(Nms)") }

// wireFinding is a finding in the JSON of the Review schema (the golden's expanded form).
type wireFinding struct {
	ID                 string          `json:"id"`
	Severity           string          `json:"severity"`
	Category           string          `json:"category"`
	Title              string          `json:"title"`
	File               string          `json:"file"`
	StartLine          int             `json:"start_line"`
	EndLine            int             `json:"end_line"`
	Rationale          string          `json:"rationale"`
	Suggestion         *string         `json:"suggestion"`
	Confidence         float64         `json:"confidence"`
	Kind               *string         `json:"kind"`
	TrifectaComponents []string        `json:"trifecta_components"`
	Evidence           json.RawMessage `json:"evidence"`
	Skill              *string         `json:"skill"`
	OutOfScope         *bool           `json:"out_of_scope"`
}

func (f wireFinding) domain() reviewer.Finding {
	var kind reviewer.Kind
	if f.Kind != nil {
		kind = reviewer.Kind(*f.Kind)
	}
	return reviewer.Finding{
		ID: f.ID, Severity: reviewer.Severity(f.Severity), Category: reviewer.Category(f.Category),
		Title: f.Title, File: f.File, StartLine: f.StartLine, EndLine: f.EndLine, Rationale: f.Rationale,
		Suggestion: f.Suggestion, Confidence: f.Confidence, Kind: kind, TrifectaComponents: f.TrifectaComponents,
		Skill: f.Skill, OutOfScope: f.OutOfScope,
	}
}

func domainFindings(in []wireFinding) []reviewer.Finding {
	out := make([]reviewer.Finding, len(in))
	for i, f := range in {
		out[i] = f.domain()
	}
	return out
}

// wireReview is a review in the JSON of the Review schema.
type wireReview struct {
	Verdict  string        `json:"verdict"`
	Summary  string        `json:"summary"`
	Score    int           `json:"score"`
	Findings []wireFinding `json:"findings"`
}

func (r wireReview) domain() reviewer.Review {
	return reviewer.Review{Verdict: reviewer.Verdict(r.Verdict), Summary: r.Summary, Score: r.Score, Findings: domainFindings(r.Findings)}
}

// diffFileWire is a parsed diff file with the Node field names.
type diffFileWire struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Hunks     []struct {
		File           string `json:"file"`
		OldStart       int    `json:"oldStart"`
		OldLines       int    `json:"oldLines"`
		NewStart       int    `json:"newStart"`
		NewLines       int    `json:"newLines"`
		NewLineNumbers []int  `json:"newLineNumbers"`
	} `json:"hunks"`
}

func domainFiles(in []diffFileWire) []reviewer.DiffFile {
	out := make([]reviewer.DiffFile, len(in))
	for i, f := range in {
		out[i] = reviewer.DiffFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions}
		for _, h := range f.Hunks {
			out[i].Hunks = append(out[i].Hunks, reviewer.Hunk{
				File: h.File, OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines,
				NewLineNumbers: h.NewLineNumbers,
			})
		}
	}
	return out
}

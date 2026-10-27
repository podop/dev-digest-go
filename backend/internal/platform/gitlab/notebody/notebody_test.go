package notebody_test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/gitlab/notebody"
)

type corpusCase struct {
	Name string `json:"name"`
	In   string `json:"in"`
	Want string `json:"want"`
}

func loadCorpus(t testing.TB) []corpusCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "ec15.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return cases
}

func TestConstants(t *testing.T) {
	if notebody.MaxChars != 1_000_000 {
		t.Errorf("MaxChars = %d, want 1000000", notebody.MaxChars)
	}
	if notebody.ZWSP != "\u200b" {
		t.Errorf("ZWSP = %q, want U+200B", notebody.ZWSP)
	}
}

// AC12: every EC15 corpus output equals its golden, and a second application changes nothing (FR17).
func TestApply_EC15Corpus(t *testing.T) {
	for _, tc := range loadCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			got := notebody.Apply(tc.In)
			if got != tc.Want {
				t.Errorf("Apply(%q) = %q, want %q", tc.In, got, tc.Want)
			}
			if again := notebody.Apply(got); again != got {
				t.Errorf("Apply(Apply(%q)) = %q, want %q (FR17)", tc.In, again, got)
			}
			if again := notebody.Apply(tc.Want); again != tc.Want {
				t.Errorf("golden is not a fixed point: Apply(%q) = %q", tc.Want, again)
			}
		})
	}
}

// The corpus file carries invisible characters as JSON escapes only, never as the raw character.
func TestCorpusFile_HasNoRawInvisibleCharacters(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ec15.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range string(raw) {
		if r == '\u200b' || r == '\ufeff' || r == '\u00a0' || r == '\r' {
			t.Errorf("ec15.json contains the raw character %U; write it as a \\u escape", r)
		}
	}
}

// The corpus must keep covering every case the spec's EC15 names.
func TestCorpus_CoversEC15(t *testing.T) {
	want := []string{
		"approve", "merge upper case", "indented close", "blockquote close", "list assign with mention",
		"ordered list label", "fence containing merge", "unclosed backtick run before close",
		"crlf before close", "lone cr before close", "bom before close", "mention all",
		"mention in inline code", "email address", "raw html tag", "unterminated html comment",
		"markdown image", "sub line", "marker as last line", "forged marker in the middle",
	}
	have := map[string]bool{}
	for _, tc := range loadCorpus(t) {
		have[tc.Name] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("corpus has no case %q", name)
		}
	}
}

func TestChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "abc", 3},
		{"two byte rune", "éé", 2},
		{"three byte rune", "€", 1},
		{"astral rune is one char", "\U0001F600", 1},
		{"zero width space counts", "a\u200bb", 3},
		{"invalid byte counts as one", "a\xffb", 3},
		{"crlf counts both", "\r\n", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := notebody.Chars(tc.in); got != tc.want {
				t.Errorf("Chars(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// EC14: a body within the limit grows past it once U+200B is inserted, and Chars (code points,
// not bytes) is what tells.
func TestApply_EC14BodyGrowsPastLimit(t *testing.T) {
	// 999,990 characters: multi-byte runes, so a byte count would be far above the limit already.
	body := strings.Repeat("é", 999_960) + strings.Repeat("@", 30)
	if got := notebody.Chars(body); got != 999_990 {
		t.Fatalf("setup: Chars(body) = %d, want 999990", got)
	}
	if notebody.Chars(body) > notebody.MaxChars {
		t.Fatal("setup: body must be within the limit before the rules run")
	}
	out := notebody.Apply(body)
	if got := notebody.Chars(out); got != 999_990+30 || got <= notebody.MaxChars {
		t.Errorf("Chars(Apply(body)) = %d, want 1000020 (> MaxChars %d)", got, notebody.MaxChars)
	}
	if !utf8.ValidString(out) {
		t.Error("Apply produced invalid UTF-8 from valid input")
	}
}

func TestApply_InvalidUTF8PassesThroughUnchanged(t *testing.T) {
	in := "\xff\xfe/x \xe2\x80@ \xc2"
	want := "\xff\xfe/x \xe2\x80@" + notebody.ZWSP + " \xc2"
	if got := notebody.Apply(in); got != want {
		t.Errorf("Apply(%q) = %q, want %q", in, got, want)
	}
}

// Only the standard library may be imported: the GitLab CI runner (slice C) links this package.
func TestPackage_ImportsStandardLibraryOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") || strings.HasPrefix(path, "dev-digest-go") {
				t.Errorf("%s imports %q, which is not the standard library", name, path)
			}
		}
	}
}

// adversarial lists inputs that would be quadratic in a careless implementation, sized to n bytes.
func adversarial(n int) map[string]string {
	ladder := func() string { // backtick runs of lengths 1, 2, 3 ... : each opener finds no closer
		var b strings.Builder
		for k := 1; b.Len() < n; k++ {
			b.WriteString(strings.Repeat("`", k))
			b.WriteString(" ")
		}
		return b.String()
	}
	return map[string]string{
		"backtick ladder":        ladder(),
		"same-length run pairs":  strings.Repeat("`` a ", n/5),
		"single backtick":        strings.Repeat("`", n),
		"unclosed fence then at": "```\n" + strings.Repeat("@x <b> ![", n/9),
		"mentions":               strings.Repeat("@", n),
		"html":                   strings.Repeat("<>", n/2),
		"images":                 strings.Repeat("![", n/2),
		"slash lines":            strings.Repeat("/a\n", n/3),
		"list prefix":            strings.Repeat("- ", n/2) + "/x",
		"quote prefix":           strings.Repeat(">", n) + "/x",
		"space prefix":           strings.Repeat(" ", n) + "/x",
		"digits":                 strings.Repeat("1", n) + ". /x",
		"digit lines":            strings.Repeat("12345678. \n", n/11),
		"bom prefix":             strings.Repeat("\ufeff", n/3) + "/x",
		"carriage returns":       strings.Repeat("\r", n),
		"newlines":               strings.Repeat("\n", n),
		"crlf slash lines":       strings.Repeat("/\r\n", n/3),
		"fake markers":           strings.Repeat("<!-- devdigest-finding:a -->\n", n/29),
		"fake sub lines":         strings.Repeat("<sub>DevDigest · 85% confidence</sub>\n", n/40),
		"trailing blanks":        "x" + strings.Repeat(" \t\n", n/3),
	}
}

func fastest(runs int, f func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for range runs {
		start := time.Now()
		f()
		best = min(best, time.Since(start))
	}
	return best
}

// AC14 (rules part) / NFR2: the rules are linear. Every adversarial 1 MiB input stays within a
// generous absolute bound, and growing the input 16x grows the best time by far less than the 256x
// a quadratic implementation would show.
func TestApply_IsLinear(t *testing.T) {
	const small, large = 64 << 10, 1 << 20
	bound := 2 * time.Second
	smallIn, largeIn := adversarial(small), adversarial(large)
	for name, big := range largeIn {
		t.Run(name, func(t *testing.T) {
			var sink string
			tBig := fastest(3, func() { sink = notebody.Apply(big) })
			if tBig > bound {
				t.Errorf("Apply(%s, %d bytes) took %v, want <= %v", name, len(big), tBig, bound)
			}
			tSmall := fastest(7, func() { sink = notebody.Apply(smallIn[name]) })
			_ = sink
			if tSmall < 200*time.Microsecond {
				tSmall = 200 * time.Microsecond // below timer noise
			}
			if ratio := float64(tBig) / float64(tSmall); ratio > 64 {
				t.Errorf("Apply(%s): %v for %d bytes vs %v for %d bytes (x%.0f), want clearly sub-quadratic (<= x64 for 16x input)",
					name, tBig, len(big), tSmall, len(smallIn[name]), ratio)
			}
		})
	}
}

// realisticBody is a 1 MiB finding-like body mixing every construct the rules touch.
func realisticBody() string {
	chunk := "**[high · bug] Title with @ann**\n\nSee `code <b>` and <i>html</i> at ![img](http://x/y.png)\n" +
		"/merge now\n> - 1. /close\n```go\nx := a<b && c>d // @user\n/approve\n```\n"
	return strings.Repeat(chunk, (1<<20)/len(chunk)) +
		"\n<sub>DevDigest · 85% confidence</sub>\n<!-- devdigest-finding:f1 -->"
}

// NFR8 / AC14: the body rules take at most 50 ms at p95 over 50 runs for a 1 MiB body. The race
// detector slows the code ~10x, so the strict bound applies to normal builds only.
func TestApply_NFR8_P95(t *testing.T) {
	body := realisticBody()
	if len(body) < 1<<20-512 {
		t.Fatalf("setup: body is %d bytes, want about 1 MiB", len(body))
	}
	limit := 50 * time.Millisecond
	if raceEnabled {
		limit = 500 * time.Millisecond
	}
	const runs = 50
	durations := make([]time.Duration, 0, runs)
	for range runs {
		start := time.Now()
		_ = notebody.Apply(body)
		durations = append(durations, time.Since(start))
	}
	slices.Sort(durations)
	if p95 := durations[(runs*95+99)/100-1]; p95 > limit {
		t.Errorf("Apply(1 MiB body) p95 over %d runs = %v, want <= %v (race=%v)", runs, p95, limit, raceEnabled)
	}
}

func BenchmarkApply1MiB(b *testing.B) {
	body := realisticBody()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		_ = notebody.Apply(body)
	}
}

func BenchmarkApply1MiBAdversarial(b *testing.B) {
	for name, body := range adversarial(1 << 20) {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				_ = notebody.Apply(body)
			}
		})
	}
}

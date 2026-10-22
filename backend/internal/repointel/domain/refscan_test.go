package domain_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// scanAll runs one Scan and returns the lines per symbol (empty, not nil, for no hit).
func scanAll(t testing.TB, content string, symbols []string) [][]int {
	t.Helper()
	got := make([][]int, len(symbols))
	for i := range got {
		got[i] = []int{}
	}
	err := domain.NewReferenceScanner(symbols).Scan(context.Background(), content, nil, func(i, line int) (bool, error) {
		got[i] = append(got[i], line)
		return true, nil
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return got
}

// wantLines is what ReferenceLines gives, per symbol.
func wantLines(content string, symbols []string) [][]int {
	want := make([][]int, len(symbols))
	for i, s := range symbols {
		want[i] = domain.ReferenceLines(content, s)
		if want[i] == nil {
			want[i] = []int{}
		}
	}
	return want
}

// TestReferenceScanner_MatchesNode replays the Node golden of ReferenceLines through the
// single-pass scanner, one symbol at a time.
func TestReferenceScanner_MatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/callers_refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Content string `json:"content"`
		Symbol  string `json:"symbol"`
		Lines   []int  `json:"lines"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		want := tc.Lines
		if want == nil {
			want = []int{}
		}
		if diff := cmp.Diff(want, scanAll(t, tc.Content, []string{tc.Symbol})[0]); diff != "" {
			t.Errorf("case %d: Scan(%q, %q) mismatch (-want +got):\n%s", i, tc.Content, tc.Symbol, diff)
		}
	}
}

// fragments are the pieces of the random corpus: names, keywords, quotes, comment markers, line
// terminators and the JS space characters the reference rules are picky about.
var fragments = []string{
	"foo", "bar", "fooBar", "a", "aa", "aaa", "$x", "x$", "_", "café", "é", "A", "Foo", "foo2",
	"function ", "function", "function*", "class ", "interface ", "type ", "enum ", "const ", "let ", "var ", "new ", "new",
	"(", ")", "()", " (", ".", "<", ">", "</", "/", "/>", "{", "}", "*", ";", ",", "=", "=>", ":",
	"'", "\"", "`", "\\", "\\'", "//", "/*", "// ", "import ", "export ", "export {a} from 'x'", "export * from", "from ",
	" ", "  ", "\t", "\u00a0", "\ufeff", "\u2003", "\r", "\u2028", "\n", "\n", "\n", "\r\n", "ünï", "日本",
}

var symbolPool = []string{"foo", "bar", "fooBar", "a", "aa", "aaa", "$x", "x$", "café", "é", "A", "Foo", "foo2", "Cls.foo", "ns.a.bar", "", "zzz", "foo", "日本", "ünï", "function", "new", "\"\"", "\"", "x\"\""}

// lineTemplates are lines the reference rules tell apart; each %s is a name from the pool.
var lineTemplates = []string{
	"%s();", "  x = %s(1)", "a.%s(b)", "new %s", "new %s()", "<%s />", "<%s>", "<%s\n", "function %s() {", "function* %s()",
	"async function %s(", "class %s {", "const %s = () =>", "let %s;", "export const %s = %s()", "// %s()", "  * %s()", "/* %s */",
	"import { %s } from './a'", "import %s from 'x'", "export { %s } from './a'", "export * from './%s'", "'%s(' + %s()", "\"%s\" %s(",
	"`${%s()}` // %s()", "x%s()", "%s2()", "$%s()", ".%s (1)", "%s\t(", "%s\r", "a // %s()\r", "%s() // c\r", "typeof %s", "%s.%s()", "%s %s()",
}

func randomContent(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.IntN(12); n >= 0; n-- {
		if rng.IntN(3) == 0 {
			for k := rng.IntN(20); k >= 0; k-- {
				b.WriteString(fragments[rng.IntN(len(fragments))])
			}
		} else {
			tpl := lineTemplates[rng.IntN(len(lineTemplates))]
			for strings.Contains(tpl, "%s") {
				name := symbolPool[rng.IntN(len(symbolPool))]
				if k := strings.LastIndexByte(name, '.'); k >= 0 {
					name = name[k+1:]
				}
				tpl = strings.Replace(tpl, "%s", name, 1)
			}
			b.WriteString(tpl)
		}
		b.WriteString([]string{"\n", "\n", "\n", "\r\n", "\n\n"}[rng.IntN(5)])
	}
	return b.String()
}

func randomSymbols(rng *rand.Rand) []string {
	out := make([]string, 1+rng.IntN(8))
	for i := range out {
		out[i] = symbolPool[rng.IntN(len(symbolPool))]
	}
	return out
}

// TestReferenceScanner_DifferentialAgainstReferenceLines: on random contents and symbol sets the
// one-pass scan gives, for every symbol, exactly the lines ReferenceLines gives.
func TestReferenceScanner_DifferentialAgainstReferenceLines(t *testing.T) {
	rng := rand.New(rand.NewPCG(4004, 4061))
	hits := 0
	for i := range 6000 {
		content, symbols := randomContent(rng), randomSymbols(rng)
		got, want := scanAll(t, content, symbols), wantLines(content, symbols)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("case %d: Scan(%q, %q) mismatch (-ReferenceLines +Scan):\n%s", i, content, symbols, diff)
		}
		for _, l := range want {
			hits += len(l)
		}
	}
	if hits < 2000 {
		t.Errorf("the corpus produced %d reference lines, want at least 2000 for the comparison to mean something", hits)
	}
}

func FuzzReferenceScanner(f *testing.F) {
	f.Add("function foo() {}\nfoo();\nnew Foo\n", "foo", "Foo")
	f.Add("a(); // a()\r\n'a' + a (1)\n<a />\n", "a", "a.a")
	f.Add("café(1)\nfunction café(){}\n", "café", "é")
	f.Fuzz(func(t *testing.T, content, s1, s2 string) {
		symbols := []string{s1, s2, "foo"}
		if diff := cmp.Diff(wantLines(content, symbols), scanAll(t, content, symbols)); diff != "" {
			t.Errorf("Scan(%q, %q) mismatch (-ReferenceLines +Scan):\n%s", content, symbols, diff)
		}
	})
}

func TestReferenceScanner_SkipAndStop(t *testing.T) {
	content := "foo();\nbar();\nfoo();\nbar();\nfoo();\n"
	s := domain.NewReferenceScanner([]string{"foo", "bar", "baz"})

	var got [][2]int
	err := s.Scan(t.Context(), content, func(i int) bool { return i == 1 }, func(i, line int) (bool, error) {
		got = append(got, [2]int{i, line})
		return len(got) < 2, nil // foo stops after its second line
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if diff := cmp.Diff([][2]int{{0, 1}, {0, 3}}, got); diff != "" {
		t.Errorf("Scan(skip bar, stop foo at 2) hits mismatch (-want +got):\n%s", diff)
	}

	boom := errors.New("boom")
	err = s.Scan(t.Context(), content, nil, func(int, int) (bool, error) { return true, boom })
	if !errors.Is(err, boom) {
		t.Errorf("Scan() with a failing hit error = %v, want %v", err, boom)
	}

	called := false
	err = s.Scan(t.Context(), content, func(int) bool { return true }, func(int, int) (bool, error) { called = true; return true, nil })
	if err != nil || called {
		t.Errorf("Scan(every symbol skipped) = %v, hit called = %v, want nil and no hit", err, called)
	}
}

func TestReferenceScanner_LongNamesNeverReference(t *testing.T) {
	long := strings.Repeat("n", domain.MaxReferenceNameBytes+1)
	ok := strings.Repeat("n", domain.MaxReferenceNameBytes)
	got := scanAll(t, long+"();\n"+ok+"();\n", []string{long, ok})
	if diff := cmp.Diff([][]int{{}, {2}}, got); diff != "" {
		t.Errorf("Scan(long names) mismatch (-want +got):\n%s", diff)
	}
}

// TestReferenceScanner_ContextIsHonouredInsideAFile: the work of one file with names that are
// prefixes of each other is quadratic (every position of a 2 MB line ends up to 256 names); the
// scan ends at the context anyway, within a small slack.
func TestReferenceScanner_ContextIsHonouredInsideAFile(t *testing.T) {
	symbols := make([]string, 0, domain.MaxReferenceNameBytes)
	for n := 1; n <= domain.MaxReferenceNameBytes; n++ {
		symbols = append(symbols, strings.Repeat("a", n))
	}
	content := strings.Repeat("a", 2_000_000) + "\n"
	s := domain.NewReferenceScanner(symbols)
	hit := func(int, int) (bool, error) { return true, nil }

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := s.Scan(ctx, content, nil, hit)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Scan() error = %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("Scan() with a 100 ms deadline took %v, want it to end within the deadline plus 1.9 s", d)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(50*time.Millisecond, cancel)
		start := time.Now()
		err := s.Scan(ctx, content, nil, hit)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Scan() error = %v, want context.Canceled", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("Scan() cancelled after 50 ms took %v, want it to end within 1.95 s of the cancel", d)
		}
	})
	t.Run("already ended", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := s.Scan(ctx, "a();\n", nil, hit); !errors.Is(err, context.Canceled) {
			t.Errorf("Scan(cancelled ctx) error = %v, want context.Canceled", err)
		}
	})
}

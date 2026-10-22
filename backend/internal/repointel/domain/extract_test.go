package domain_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// TestExtractSymbols_MatchesNode replays 3,307 cases (seeded random source fragments, class
// bodies and a few fixed files) whose expected symbols come from Node's extractSymbols
// (testdata/gen_extract_symbols.mts).
func TestExtractSymbols_MatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/extract_symbols.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Content string `json:"content"`
		Symbols []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Line int    `json:"line"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 3000 {
		t.Fatalf("golden holds %d cases, want at least 3000", len(cases))
	}
	for i, tc := range cases {
		want := []domain.ExtractedSymbol{}
		for _, s := range tc.Symbols {
			want = append(want, domain.ExtractedSymbol{Name: s.Name, Kind: s.Kind, Line: s.Line})
		}
		got, err := domain.ExtractSymbols(t.Context(), tc.Content)
		if err != nil {
			t.Fatalf("case %d: ExtractSymbols() error = %v", i, err)
		}
		if got == nil {
			got = []domain.ExtractedSymbol{}
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("case %d: ExtractSymbols(%q) mismatch (-want +got):\n%s", i, tc.Content, diff)
		}
	}
}

func TestExtractSymbols(t *testing.T) {
	sym := func(name, kind string, line int) domain.ExtractedSymbol {
		return domain.ExtractedSymbol{Name: name, Kind: kind, Line: line}
	}
	tests := []struct {
		name, content string
		want          []domain.ExtractedSymbol
	}{
		{name: "declarations", content: "export function a() {}\nexport const b = () => 1;\nclass C {}\ninterface I {}\ntype T = string;\nenum E {}\n",
			want: []domain.ExtractedSymbol{sym("a", "function", 1), sym("b", "function", 2), sym("C", "class", 3), sym("I", "interface", 4), sym("T", "type", 5), sym("E", "enum", 6)}},
		{name: "class methods dual emit", content: "class A {\n  run() {\n  }\n  static make(x): X {\n  }\n}\nfunction after() {}\n",
			want: []domain.ExtractedSymbol{sym("A", "class", 1), sym("A.run", "method", 2), sym("run", "method", 2), sym("A.make", "method", 4), sym("make", "method", 4), sym("after", "function", 7)}},
		{name: "keyword names are not methods", content: "class A {\n  if (x) {\n  }\n  constructor() {\n  }\n}\n", want: []domain.ExtractedSymbol{sym("A", "class", 1)}},
		{name: "comment lines are skipped", content: "// function a() {}\n * function b() {}\n/* function c() {}\n", want: []domain.ExtractedSymbol{}},
		{name: "string contents are blanked", content: "const s = \"function a() {}\";\n", want: []domain.ExtractedSymbol{}},
		{name: "braces of a comment line still count", content: "class A {\n// }\n  m() {\n  }\n}\n", want: []domain.ExtractedSymbol{sym("A", "class", 1)}},
		{name: "repeat of name kind and line", content: "function a() {} function a() {}\n", want: []domain.ExtractedSymbol{sym("a", "function", 1)}},
		{name: "no-break space is white space", content: "class N {}\n", want: []domain.ExtractedSymbol{sym("N", "class", 1)}},
		{name: "empty", content: "", want: []domain.ExtractedSymbol{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ExtractSymbols(t.Context(), tc.content)
			if err != nil {
				t.Fatalf("ExtractSymbols() error = %v", err)
			}
			if got == nil {
				got = []domain.ExtractedSymbol{}
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExtractSymbols(%q) mismatch (-want +got):\n%s", tc.content, diff)
			}
		})
	}
}

// A hostile file: the shapes that make a backtracking engine blow up (nested optional groups,
// long runs of the characters a negated class repeats over) and a context that ends. RE2 stays
// linear, the scan looks at ctx inside the file.
func TestExtractSymbols_Hostile(t *testing.T) {
	shapes := map[string]string{
		"spaces":       "const x" + strings.Repeat(" ", 1_900_000) + "\n",
		"equals chain": "const x: " + strings.Repeat("a", 500_000) + "\n",
		"parens":       "const x = " + strings.Repeat("(", 500_000) + "\n",
		"function run": strings.Repeat("function ", 200_000) + "\n",
		"method run":   "class A {\n" + strings.Repeat("m(", 900_000) + "\n}\n",
		"many lines":   strings.Repeat("a\n", 900_000),
	}
	for name, content := range shapes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			if _, err := domain.ExtractSymbols(t.Context(), content); err != nil {
				t.Fatalf("ExtractSymbols() error = %v", err)
			}
			if d := time.Since(start); d > 30*time.Second {
				t.Errorf("ExtractSymbols() on a %d byte file took %v, want a linear scan", len(content), d)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := domain.ExtractSymbols(ctx, strings.Repeat("function f() {}\n", 200_000))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ExtractSymbols() with a cancelled context error = %v, want context.Canceled", err)
		}
	})
}

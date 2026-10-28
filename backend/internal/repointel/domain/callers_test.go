package domain_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// TestReferenceLines_MatchesNode replays 1,512 cases (a seeded random corpus of line fragments
// and a few fixed files) whose expected lines come from Node's extractReferences
// (testdata/gen_callers_refs.mts).
func TestReferenceLines_MatchesNode(t *testing.T) {
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
	if len(cases) < 1500 {
		t.Fatalf("golden holds %d cases, want at least 1500", len(cases))
	}
	for i, tc := range cases {
		want := tc.Lines
		if want == nil {
			want = []int{}
		}
		got := domain.ReferenceLines(tc.Content, tc.Symbol)
		if got == nil {
			got = []int{}
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("case %d: ReferenceLines(%q, %q) mismatch (-want +got):\n%s", i, tc.Content, tc.Symbol, diff)
		}
	}
}

func TestReferenceLines(t *testing.T) {
	tests := []struct {
		name, content, symbol string
		want                  []int
	}{
		{name: "call", content: "foo();", symbol: "foo", want: []int{1}},
		{name: "call with space", content: "foo ();", symbol: "foo", want: []int{1}},
		{name: "member call", content: "this.foo(1);", symbol: "foo", want: []int{1}},
		{name: "new", content: "const a = new Foo;", symbol: "Foo", want: []int{1}},
		{name: "jsx", content: "return <Foo a={1} />;\n<Foo>", symbol: "Foo", want: []int{1, 2}},
		{name: "not preceded by a word", content: "xfoo(); $foo(); a.b.foo(); _foo();", symbol: "foo", want: []int{1}},
		{name: "only a preceded call", content: "xfoo(); $foo(); _foo();", symbol: "foo", want: nil},
		{name: "declaration line", content: "function foo() { foo(); }", symbol: "foo", want: nil},
		{name: "const declaration", content: "const foo = bar(foo);", symbol: "foo", want: nil},
		{name: "comment lines", content: "// foo()\n * foo()\n/* foo() */", symbol: "foo", want: nil},
		{name: "import lines", content: "import { foo } from 'a'; foo();\nexport { foo } from 'a';\nexport * from 'a'", symbol: "foo", want: nil},
		{name: "export without from is code", content: "export { foo };\nexport const x = foo();", symbol: "foo", want: []int{2}},
		{name: "string literal", content: `log("foo(");` + "\n" + `log('x', foo());`, symbol: "foo", want: []int{2}},
		{name: "comment tail", content: "a(); // foo()", symbol: "foo", want: nil},
		{name: "comment tail kept in a CRLF line", content: "a(); // foo()\r\nb();\r\n", symbol: "foo", want: []int{1}},
		{name: "dotted symbol reduces to its member", content: "obj.run();", symbol: "Cls.run", want: []int{1}},
		{name: "empty symbol", content: "foo();", symbol: "", want: nil},
		{name: "regex metacharacters", content: "a$b(1);", symbol: "a$b", want: []int{1}},
		{name: "empty content", content: "", symbol: "foo", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, domain.ReferenceLines(tc.content, tc.symbol)); diff != "" {
				t.Errorf("ReferenceLines(%q, %q) mismatch (-want +got):\n%s", tc.content, tc.symbol, diff)
			}
		})
	}
}

func TestIsCallerScanFile(t *testing.T) {
	for p, want := range map[string]bool{
		"a.ts": true, "src/a.tsx": true, "b.js": true, "b.jsx": true, "c.mjs": true, "c.cjs": true,
		"main.go": true, "pkg/x.go": true, "Svc.java": true, "src/main/App.java": true,
		"a.TS": false, "a.mts": false, "a.d.ts.map": false, ".ts": false, "a": false, "a.json": false,
		"A.GO": false, "A.JAVA": false,
	} {
		if got := domain.IsCallerScanFile(p); got != want {
			t.Errorf("IsCallerScanFile(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestIsCallerScanDir(t *testing.T) {
	for n, want := range map[string]bool{
		".git": true, "node_modules": true, "dist": true, "build": true, ".next": true, "coverage": true,
		"vendor": true, "target": true,
		"out": false, "src": false,
	} {
		if got := domain.IsCallerScanDir(n); got != want {
			t.Errorf("IsCallerScanDir(%q) = %v, want %v", n, got, want)
		}
	}
}

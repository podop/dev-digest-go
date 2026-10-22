package treesitter_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/domain"
)

func extract(t *testing.T, file, src string) domain.Extraction {
	t.Helper()
	ex, err := treesitter.New().Extract(t.Context(), file, src)
	if err != nil {
		t.Fatalf("Extract(%q) error = %v", file, err)
	}
	return ex
}

func sym(name, kind string, line, endLine int, exported bool, sig string) domain.Symbol {
	return domain.Symbol{Name: name, Kind: kind, Line: line, EndLine: endLine, Exported: exported, Signature: sig}
}

func TestExtract_Symbols(t *testing.T) {
	longHead := "export function " + strings.Repeat("n", 130) + "(a: string) { return a; }\n"
	tests := []struct {
		name string
		file string
		src  string
		want []domain.Symbol
	}{
		{
			name: "function and generator",
			file: "a.ts",
			src:  "function plain(a: number): void { helper(a); }\nfunction* gen<T>(items: T[]) { yield* items; }\n",
			want: []domain.Symbol{
				sym("plain", "function", 1, 1, false, "function plain(a: number): void"),
				sym("gen", "function", 2, 2, false, "function* gen<T>(items: T[])"),
			},
		},
		{
			name: "export default function and class",
			file: "a.ts",
			src:  "export default function defaulted() {}\nexport default class Named {}\n",
			want: []domain.Symbol{
				sym("defaulted", "function", 1, 1, true, "function defaulted()"),
				sym("Named", "class", 2, 2, true, "class Named"),
			},
		},
		{
			name: "multi-line head",
			file: "a.ts",
			src:  "export async function fetchIt(\n  url: string,\n  opts: RequestInit = {},\n): Promise<Response> {\n  return fetch(url);\n}\n",
			want: []domain.Symbol{
				sym("fetchIt", "function", 1, 6, true, "async function fetchIt( url: string, opts: RequestInit = {}, ): Promise<Response>"),
			},
		},
		{
			name: "variable functions use the declaration head",
			file: "a.ts",
			src: "const arrow = async (x: number) => { await helper(x); };\n" +
				"export const exported = (a, b) => a + b;\n" +
				"let fe = function named(a) { return a; }, g2 = () => 1;\n" +
				"var v1 = function () {};\n" +
				"const notFn = 42;\n",
			want: []domain.Symbol{
				sym("arrow", "function", 1, 1, false, "const arrow = async (x: number)"),
				sym("exported", "function", 2, 2, true, "const exported = (a, b)"),
				sym("fe", "function", 3, 3, false, "let fe = function named(a)"),
				sym("g2", "function", 3, 3, false, "let fe = function named(a) { return a; }, g2 = ()"),
				sym("v1", "function", 4, 4, false, "var v1 = function ()"),
			},
		},
		{
			name: "class methods are emitted qualified and bare",
			file: "a.ts",
			src:  "export class Svc<T> extends Base<T> {\n  private x = 1;\n  constructor(a: number) { super(); }\n  get value() { return 1; }\n  static create(): Svc<any> { return new Svc(); }\n  abstractLike(): void;\n}\n",
			want: []domain.Symbol{
				sym("Svc", "class", 1, 7, true, "class Svc<T> extends Base<T>"),
				sym("Svc.value", "method", 4, 4, true, "get value()"),
				sym("value", "method", 4, 4, true, "get value()"),
				sym("Svc.create", "method", 5, 5, true, "static create(): Svc<any>"),
				sym("create", "method", 5, 5, true, "static create(): Svc<any>"),
			},
		},
		{
			name: "interface type enum",
			file: "a.ts",
			src:  "export interface IFoo<T = string> extends Bar { a: string }\nexport type Alias<T> = { a: T } | null;\nenum Hidden { A }\nconst enum CE { A }\n",
			want: []domain.Symbol{
				sym("IFoo", "interface", 1, 1, true, "interface IFoo<T = string> extends Bar"),
				sym("Alias", "type", 2, 2, true, "type Alias<T> = { a: T } | null;"),
				sym("Hidden", "enum", 3, 3, false, "enum Hidden"),
				sym("CE", "enum", 4, 4, false, "const enum CE"),
			},
		},
		{
			name: "nested declarations and other statements yield nothing",
			file: "a.ts",
			src:  "function outer() { function inner() {} const f = () => 1; }\nnamespace NS { export function ns() {} }\ndeclare function dfn(a: number): void;\nabstract class Abs {}\nexport default () => 1;\nexport * from './re';\n",
			want: []domain.Symbol{sym("outer", "function", 1, 1, false, "function outer()")},
		},
		{
			name: "keyword names are skipped",
			file: "a.ts",
			src:  "function get() {}\nfunction set() {}\nfunction constructor() {}\nclass K { constructor() {} get() {} set() {} real() {} }\nconst from = () => {};\n",
			want: []domain.Symbol{
				sym("K", "class", 4, 4, false, "class K"),
				sym("K.real", "method", 4, 4, false, "real()"),
				sym("real", "method", 4, 4, false, "real()"),
			},
		},
		{
			name: "export clause back-patches the last declaration of a name",
			file: "a.ts",
			src:  "type Local = string;\nclass Local2 {}\nconst arrow = () => 1;\nfunction hidden() {}\nexport { Local, Local2 as L2, arrow as ar };\n",
			want: []domain.Symbol{
				sym("Local", "type", 1, 1, true, "type Local = string;"),
				sym("Local2", "class", 2, 2, true, "class Local2"),
				sym("arrow", "function", 3, 3, true, "const arrow = ()"),
				sym("hidden", "function", 4, 4, false, "function hidden()"),
			},
		},
		{
			name: "overloads on different lines stay separate and signatures without a body are whole",
			file: "a.ts",
			src:  "function over(a: string): void;\nfunction over(a: number): void;\nfunction over(a: any): void {}\n",
			want: []domain.Symbol{sym("over", "function", 3, 3, false, "function over(a: any): void")},
		},
		{
			name: "CRLF line endings count one line each",
			file: "a.ts",
			src:  "function crlf(a,\r\n b) {\r\n  return call(a);\r\n}\r\nconst x = () => {\r\n};\r\n",
			want: []domain.Symbol{
				sym("crlf", "function", 1, 4, false, "function crlf(a, b)"),
				sym("x", "function", 5, 6, false, "const x = ()"),
			},
		},
		{
			name: "a byte order mark stays in the text",
			file: "a.ts",
			src:  "\ufeffexport function bom() {}\n",
			want: []domain.Symbol{sym("bom", "function", 1, 1, true, "function bom()")},
		},
		{
			name: "signature over 120 units is cut to 119 plus an ellipsis",
			file: "a.ts",
			src:  longHead,
			want: []domain.Symbol{sym(strings.Repeat("n", 130), "function", 1, 1, true, ("function " + strings.Repeat("n", 130) + "(a: string)")[:119]+"…")},
		},
		{
			name: "javascript file",
			file: "a.mjs",
			src:  "export const x = async function* () {};\nclass A { static async *gen() {} }\nexport { A as default };\n",
			want: []domain.Symbol{
				sym("x", "function", 1, 1, true, "const x = async function* ()"),
				sym("A", "class", 2, 2, true, "class A"),
				sym("A.gen", "method", 2, 2, false, "static async *gen()"),
				sym("gen", "method", 2, 2, false, "static async *gen()"),
			},
		},
		{
			name: "decorated exports are not unwrapped",
			file: "a.ts",
			src:  "@Component({ selector: 'x' })\nexport class Dec {}\nexport abstract class Abs2 {}\n",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, tc.file, tc.src).Symbols
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Extract(%q).Symbols mismatch (-want +got):\n%s", tc.file, diff)
			}
		})
	}
}

func TestExtract_SignatureLimitCountsUTF16Units(t *testing.T) {
	// Each emoji is two UTF-16 units: 70 of them exceed the limit although they are 70 runes.
	src := "function e(a = \"" + strings.Repeat("😀", 70) + "\") {}\n"
	got := extract(t, "a.ts", src).Symbols
	if len(got) != 1 {
		t.Fatalf("Extract() returned %d symbols, want 1", len(got))
	}
	sig := got[0].Signature
	// 119 units plus the ellipsis; a pair that would straddle the cut is dropped whole, one unit less.
	if units := domain.UTF16Len(sig); units < domain.MaxSignatureUnits-1 || units > domain.MaxSignatureUnits {
		t.Errorf("signature = %d UTF-16 units, want %d or %d (%q)", units, domain.MaxSignatureUnits-1, domain.MaxSignatureUnits, sig)
	}
	if !strings.HasSuffix(sig, "…") {
		t.Errorf("signature %q does not end with an ellipsis", sig)
	}
}

func TestExtract_SyntaxErrorKeepsParsableParts(t *testing.T) {
	// AC3044: the second function is broken; the first and the third are still extracted.
	src := "export function first() { return 1; }\nexport function second() {\n  const broken = ;\n}\nexport function third(a: number) { return helper(a); }\n"
	got := extract(t, "a.ts", src).Symbols
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	if !contains(names, "first") || !contains(names, "third") {
		t.Errorf("Extract() symbols = %v, want first and third among them", names)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestExtract_UnsupportedExtension(t *testing.T) {
	if _, err := treesitter.New().Extract(t.Context(), "a.py", "def f(): pass"); err == nil {
		t.Error("Extract(a.py) error = nil, want an error")
	}
	if _, err := treesitter.New().Imports(t.Context(), "README.md", "# x"); err == nil {
		t.Error("Imports(README.md) error = nil, want an error")
	}
}

func TestExtract_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := treesitter.New().Extract(ctx, "a.ts", "function f() {}")
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("Extract(cancelled ctx) error = %v, want one wrapping context.Canceled", err)
	}
	if _, err := treesitter.New().Imports(ctx, "a.ts", "import 'x'"); err == nil {
		t.Error("Imports(cancelled ctx) error = nil, want an error")
	}
}

// cancelAfterCtx reports cancellation once Err has been called n times: the pre-parse check is
// the first call, the parser's progress callback the next ones.
type cancelAfterCtx struct {
	context.Context
	n     int
	calls int
}

func (c *cancelAfterCtx) Err() error {
	c.calls++
	if c.calls > c.n {
		return context.Canceled
	}
	return nil
}

func TestExtract_StopsWhenContextEndsDuringParse(t *testing.T) {
	// P10: the 2,000 ms parse limit is a context deadline seen by the parser's progress callback.
	src := strings.Repeat("export const a = 1;\n", 20000)
	ctx := &cancelAfterCtx{Context: t.Context(), n: 1}
	_, err := treesitter.New().Extract(ctx, "a.ts", src)
	if err == nil {
		t.Fatal("Extract() error = nil, want the parse to stop")
	}
	if ctx.calls < 2 {
		t.Errorf("context checked %d times, want the progress callback to check it", ctx.calls)
	}
	// The analyzer is reusable after a stopped parse.
	if _, err := treesitter.New().Extract(t.Context(), "a.ts", "function f() {}"); err != nil {
		t.Errorf("Extract() after a stopped parse error = %v", err)
	}
}

func TestAnalyzer_ConcurrentUse(t *testing.T) {
	a := treesitter.New()
	files := map[string]string{
		"a.ts":  "export function f(a: number) { return g(a); }\nimport { x } from './x'; x();\n",
		"b.tsx": "export const C = () => <Foo />;\n",
		"c.js":  "const d = require('d'); function h() { d(); }\n",
	}
	errs := make(chan error, 64)
	done := make(chan struct{})
	n := 0
	for file, src := range files {
		for range 4 {
			n++
			go func() {
				defer func() { done <- struct{}{} }()
				for range 25 {
					if _, err := a.Extract(t.Context(), file, src); err != nil {
						errs <- err
						return
					}
					if _, err := a.Imports(t.Context(), file, src); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
	}
	for range n {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

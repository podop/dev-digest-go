package treesitter_test

import (
	"strings"
	"sync"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/domain"
)

// parse parses src with a fresh parser of the syntax and returns the root's kind, whether the
// tree has syntax errors, and the S-expression. The tree and the parser are closed.
func parse(t *testing.T, s domain.Syntax, src string) (kind string, hasError bool, sexp string) {
	t.Helper()
	lang, err := treesitter.Language(s)
	if err != nil {
		t.Fatalf("Language(%v) error = %v", s, err)
	}
	p := sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(lang); err != nil {
		t.Fatalf("SetLanguage(%v) error = %v", s, err)
	}
	tree := p.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatalf("Parse(%v) = nil tree", s)
	}
	defer tree.Close()
	root := tree.RootNode()
	return root.Kind(), root.HasError(), root.ToSexp()
}

func TestLanguage_ParsesEachGrammar(t *testing.T) {
	tests := []struct {
		name    string
		syntax  domain.Syntax
		src     string
		wantIn  string // a node kind that must appear in the tree
		wantErr bool
	}{
		{"typescript type alias", domain.SyntaxTypeScript, "export type A = { x: number };\ninterface B {}\n", "type_alias_declaration", false},
		{"typescript interface", domain.SyntaxTypeScript, "interface B { y: string }\n", "interface_declaration", false},
		{"tsx jsx element", domain.SyntaxTSX, "const a = <Foo x={1} />;\n", "jsx_self_closing_element", false},
		{"javascript function", domain.SyntaxJavaScript, "function f(a) { return a; }\n", "function_declaration", false},
		{"javascript rejects types", domain.SyntaxJavaScript, "const x: number = 1;\n", "", true},
		{"typescript recovers from errors", domain.SyntaxTypeScript, "function f( {\nconst ok = 1;\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, hasErr, sexp := parse(t, tc.syntax, tc.src)
			if kind != "program" {
				t.Errorf("root kind = %q, want program", kind)
			}
			if hasErr != tc.wantErr {
				t.Errorf("HasError = %v, want %v (tree %s)", hasErr, tc.wantErr, sexp)
			}
			if tc.wantIn != "" && !strings.Contains(sexp, tc.wantIn) {
				t.Errorf("tree has no %q node: %s", tc.wantIn, sexp)
			}
		})
	}
}

func TestLanguage_UnknownSyntax(t *testing.T) {
	if _, err := treesitter.Language(0); err == nil {
		t.Error("Language(0) error = nil, want an error")
	}
}

// TestLanguage_ConcurrentParsers is the CGO smoke test: one parser per goroutine, all three
// grammars, under the race detector and its checkptr instrumentation.
func TestLanguage_ConcurrentParsers(t *testing.T) {
	syntaxes := []domain.Syntax{domain.SyntaxTypeScript, domain.SyntaxTSX, domain.SyntaxJavaScript}
	sources := map[domain.Syntax]string{
		domain.SyntaxTypeScript: "export class A { m(): void {} }\nexport const f = (x: number) => x;\n",
		domain.SyntaxTSX:        "export const C = () => <div><Child a=\"b\" /></div>;\n",
		domain.SyntaxJavaScript: "import x from 'y';\nexport function g() { return x(); }\n",
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := range 16 {
		s := syntaxes[i%len(syntaxes)]
		wg.Go(func() {
			lang, err := treesitter.Language(s)
			if err != nil {
				errs <- err.Error()
				return
			}
			p := sitter.NewParser()
			defer p.Close()
			if err := p.SetLanguage(lang); err != nil {
				errs <- err.Error()
				return
			}
			for range 50 {
				tree := p.Parse([]byte(sources[s]), nil)
				if tree == nil || tree.RootNode().HasError() {
					errs <- "unexpected parse result for " + s.String()
					if tree != nil {
						tree.Close()
					}
					return
				}
				// Walk the tree through a cursor and read node text, the calls the extractors use.
				cur := tree.Walk()
				n := 0
				for ok := cur.GotoFirstChild(); ok; ok = cur.GotoNextSibling() {
					if cur.Node().Utf8Text([]byte(sources[s])) == "" {
						errs <- "empty node text"
					}
					n++
				}
				cur.Close()
				tree.Close()
				if n == 0 {
					errs <- "program has no children"
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestLanguage_ProgressCallbackCancels checks the cancellation path the parser uses for the
// 2,000 ms limit: a callback that returns true stops the parse, which yields no tree.
func TestLanguage_ProgressCallbackCancels(t *testing.T) {
	lang, err := treesitter.Language(domain.SyntaxTypeScript)
	if err != nil {
		t.Fatal(err)
	}
	p := sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(lang); err != nil {
		t.Fatal(err)
	}
	src := []byte(strings.Repeat("export const a = 1;\n", 20000))
	calls := 0
	tree := p.ParseWithOptions(func(i int, _ sitter.Point) []byte {
		if i < len(src) {
			return src[i:]
		}
		return nil
	}, nil, &sitter.ParseOptions{ProgressCallback: func(sitter.ParseState) bool {
		calls++
		return true
	}})
	if tree != nil {
		tree.Close()
		t.Fatalf("ParseWithOptions with a cancelling callback returned a tree (callback calls = %d)", calls)
	}
	if calls == 0 {
		t.Error("progress callback was never called")
	}
	// The parser stays usable after a cancelled parse.
	p.Reset()
	tree = p.Parse([]byte("let a = 1;"), nil)
	if tree == nil {
		t.Fatal("Parse after a cancelled parse returned no tree")
	}
	tree.Close()
}

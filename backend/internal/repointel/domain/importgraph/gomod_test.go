package importgraph_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// importsOf returns a SourceImports that serves fixed specifiers per file, with the Go package
// key for .go files as the parser reports it.
func importsOf(m map[string][]string) importgraph.SourceImports {
	return func(_ context.Context, file string) domain.FileImports {
		fi := domain.FileImports{Specs: m[file]}
		if strings.HasSuffix(file, ".go") {
			fi.Package = domain.GoPackageKey(file)
		}
		return fi
	}
}

// treeFiles returns the paths of tree that the walk would select: every supported source file.
func treeFiles(tree fakeTree) []string {
	var out []string
	for _, p := range tree.paths() {
		if _, ok := domain.SyntaxForPath(p); ok {
			out = append(out, p)
		}
	}
	return out
}

func TestGoResolver_SingleModule(t *testing.T) {
	tree := fakeTree{
		"go.mod":                 "module example.com/r\n\ngo 1.22\n",
		"main.go":                "",
		"internal/svc/a.go":      "",
		"internal/svc/b.go":      "",
		"internal/svc/a_test.go": "",
		"internal/svc/x.ts":      "",
	}
	r := importgraph.NewGoResolver(tree, treeFiles(tree))
	tests := []struct {
		spec string
		want []string
	}{
		{"example.com/r/internal/svc", []string{"internal/svc/a.go", "internal/svc/b.go"}},
		{"example.com/r", []string{"main.go"}},
		{"example.com/r/internal", nil},
		{"example.com/r/internal/svc/../svc", nil},
		{"example.com/r//internal/svc", nil},
		{"example.com/r/./internal/svc", nil},
		{"example.com/rx/internal/svc", nil},
		{"fmt", nil},
		{"github.com/x/y", nil},
		{"", nil},
	}
	for _, tc := range tests {
		if diff := cmp.Diff(tc.want, r.ResolveAll(tc.spec)); diff != "" {
			t.Errorf("ResolveAll(%q) mismatch (-want +got):\n%s", tc.spec, diff)
		}
	}
}

// TestGoResolver_NestedModuleLongestPrefix is AC5 / EC2: the longest module path wins.
func TestGoResolver_NestedModuleLongestPrefix(t *testing.T) {
	tree := fakeTree{
		"go.mod":         "module example.com/r\n",
		"main.go":        "",
		"tools/go.mod":   "// tools module\nmodule \"example.com/r/tools\" // quoted\n",
		"tools/tools.go": "",
		"tools/x/x.go":   "",
		"x/wrong.go":     "",
	}
	r := importgraph.NewGoResolver(tree, treeFiles(tree))
	if diff := cmp.Diff([]string{"tools/x/x.go"}, r.ResolveAll("example.com/r/tools/x")); diff != "" {
		t.Errorf("ResolveAll(example.com/r/tools/x) mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"tools/tools.go"}, r.ResolveAll("example.com/r/tools")); diff != "" {
		t.Errorf("ResolveAll(example.com/r/tools) mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"x/wrong.go"}, r.ResolveAll("example.com/r/x")); diff != "" {
		t.Errorf("ResolveAll(example.com/r/x) mismatch (-want +got):\n%s", diff)
	}
}

// TestGoResolver_NestedModuleWithoutDirectFiles: a go.mod in a directory that holds no walked
// file itself, only subdirectories that do, still declares a module (multi-module repositories).
func TestGoResolver_NestedModuleWithoutDirectFiles(t *testing.T) {
	src := &fakeFS{files: map[string]string{
		"go.mod":     "module example.com/r\n",
		"api/go.mod": "module example.com/api\n",
	}}
	files := []string{"api/v1/types.go", "api/v1/types_test.go", "api/v1/zz.go", "main.go"}
	imports := importsOf(map[string][]string{"main.go": {"example.com/api/v1"}})
	g, _, err := importgraph.Build(t.Context(), src, "/clone", files, imports)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	want := []domain.Edge{edge("main.go", "api/v1/types.go"), edge("main.go", "api/v1/zz.go")}
	if diff := cmp.Diff(want, g.Edges); diff != "" {
		t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
	}
}

// TestGoResolver_GoModWithoutModuleOrTooLarge is EC3: such a go.mod contributes no module.
func TestGoResolver_GoModWithoutModuleOrTooLarge(t *testing.T) {
	big := "module example.com/r\n// " + strings.Repeat("x", 65<<10) + "\n"
	tests := []struct {
		name  string
		gomod string
	}{
		{"no module line", "go 1.22\n\nrequire example.com/x v1.0.0\n"},
		{"empty", ""},
		{"module without a path", "module\n"},
		{"over 64 KiB", big},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := fakeTree{"go.mod": tc.gomod, "main.go": "", "a/a.go": ""}
			r := importgraph.NewGoResolver(tree, treeFiles(tree))
			if got := r.ResolveAll("example.com/r/a"); got != nil {
				t.Errorf("ResolveAll(example.com/r/a) = %v, want nil", got)
			}
		})
	}
	t.Run("no go.mod at all", func(t *testing.T) {
		tree := fakeTree{"main.go": "", "a/a.go": ""}
		if got := importgraph.NewGoResolver(tree, treeFiles(tree)).ResolveAll("a"); got != nil {
			t.Errorf("ResolveAll(a) = %v, want nil", got)
		}
	})
}

// TestGoResolver_AliasDotBlankAndRoot: aliased, dot and blank imports all reach the resolver as
// their path; an import of the root package from a root file has edges to the other root files,
// never to the importer itself.
func TestGoResolver_AliasDotBlankAndRoot(t *testing.T) {
	src := &fakeFS{files: map[string]string{"go.mod": "module example.com/r\n"}}
	files := []string{"a/a.go", "b/b.go", "c/c.go", "main.go", "util.go"}
	imports := importsOf(map[string][]string{
		// import ( "example.com/r"; x "example.com/r/a"; . "example.com/r/b"; _ "example.com/r/c" )
		"main.go": {"example.com/r", "example.com/r/a", "example.com/r/b", "example.com/r/c"},
	})
	g, _, err := importgraph.Build(t.Context(), src, "/clone", files, imports)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	want := []domain.Edge{
		edge("main.go", "util.go"),
		edge("main.go", "a/a.go"),
		edge("main.go", "b/b.go"),
		edge("main.go", "c/c.go"),
	}
	if diff := cmp.Diff(want, g.Edges); diff != "" {
		t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
	}
}

func TestBuild_GoGraph(t *testing.T) {
	src := &fakeFS{files: map[string]string{"go.mod": "module example.com/r\n"}}
	files := []string{
		"internal/api/handler.go",
		"internal/api/routes.go",
		"internal/svc/a.go",
		"internal/svc/a_test.go",
		"internal/svc/b.go",
		"web/app.ts",
		"web/util.ts",
	}
	imports := importsOf(map[string][]string{
		"internal/api/handler.go": {"net/http", "example.com/r/internal/svc", "github.com/labstack/echo/v4", "example.com/r/internal/svc"},
		"internal/api/routes.go":  {"example.com/r/internal/api"},
		"internal/svc/a_test.go":  {"example.com/r/internal/svc", "testing"},
		"web/app.ts":              {"./util", "./util.ts", "react"},
	})
	g, aliasErr, err := importgraph.Build(t.Context(), src, "/clone", files, imports)
	if err != nil || aliasErr != nil {
		t.Fatalf("Build() = %v, %v, want no errors", aliasErr, err)
	}
	wantEdges := []domain.Edge{
		edge("internal/api/handler.go", "internal/svc/a.go"),
		edge("internal/api/handler.go", "internal/svc/b.go"),
		edge("internal/api/routes.go", "internal/api/handler.go"),
		edge("internal/svc/a_test.go", "internal/svc/a.go"),
		edge("internal/svc/a_test.go", "internal/svc/b.go"),
		edge("web/app.ts", "web/util.ts"),
	}
	if diff := cmp.Diff(wantEdges, g.Edges); diff != "" {
		t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
	}
	wantPackages := []domain.FilePackage{
		{Path: "internal/api/handler.go", Key: "go:internal/api"},
		{Path: "internal/api/routes.go", Key: "go:internal/api"},
		{Path: "internal/svc/a.go", Key: "go:internal/svc"},
		{Path: "internal/svc/a_test.go", Key: "go:internal/svc"},
		{Path: "internal/svc/b.go", Key: "go:internal/svc"},
	}
	if diff := cmp.Diff(wantPackages, g.Packages); diff != "" {
		t.Errorf("Build() packages mismatch (-want +got):\n%s", diff)
	}
}

// TestBuild_EmptyGraphIsNotNil: no files, or files without package keys, give empty non-nil
// slices.
func TestBuild_EmptyGraphIsNotNil(t *testing.T) {
	for _, files := range [][]string{nil, {"a.ts"}} {
		g, _, err := importgraph.Build(t.Context(), &fakeFS{}, "/clone", files, importsOf(nil))
		if err != nil || g.Edges == nil || g.Packages == nil || len(g.Edges)+len(g.Packages) != 0 {
			t.Errorf("Build(%v) = %#v, %v, want empty non-nil slices", files, g, err)
		}
	}
}

// TestBuild_TSOnlyCloneReadsNoGoMod: without a walked Go file no go.mod is looked for.
func TestBuild_TSOnlyCloneReadsNoGoMod(t *testing.T) {
	src := &countFS{names: map[string]int{}, files: map[string]string{"go.mod": "module example.com/r\n"}}
	if _, _, err := importgraph.Build(t.Context(), src, "/clone", []string{"a.ts", "lib/b.ts"}, importsOf(nil)); err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	for name := range src.names {
		if strings.HasSuffix(name, "go.mod") {
			t.Errorf("Build(TS only) read %s, want no go.mod read", name)
		}
	}
}

// TestNewGoResolver_ReadsGoModOnlyWhereFilesAreWalked: go.mod is read once in every directory
// above a walked file (the root included), nowhere else.
func TestNewGoResolver_ReadsGoModOnlyWhereFilesAreWalked(t *testing.T) {
	tree := &countTree{perFile: map[string]int{}, files: map[string]string{}}
	importgraph.NewGoResolver(tree, []string{"a/b/c.go", "a/b/d.go", "e/f.ts"})
	want := map[string]int{"go.mod": 1, "a/go.mod": 1, "a/b/go.mod": 1, "e/go.mod": 1}
	if diff := cmp.Diff(want, tree.perFile); diff != "" {
		t.Errorf("NewGoResolver() reads mismatch (-want +got):\n%s", diff)
	}
}

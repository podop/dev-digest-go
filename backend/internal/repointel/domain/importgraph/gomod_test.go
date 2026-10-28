package importgraph_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// newGoResolver is NewGoResolver with the test's context; an error fails the test.
func newGoResolver(t *testing.T, tree importgraph.Tree, files []string) *importgraph.GoResolver {
	t.Helper()
	r, err := importgraph.NewGoResolver(t.Context(), tree, files)
	if err != nil {
		t.Fatalf("NewGoResolver() error = %v", err)
	}
	return r
}

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
	r := newGoResolver(t, tree, treeFiles(tree))
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
	r := newGoResolver(t, tree, treeFiles(tree))
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
			r := newGoResolver(t, tree, treeFiles(tree))
			if got := r.ResolveAll("example.com/r/a"); got != nil {
				t.Errorf("ResolveAll(example.com/r/a) = %v, want nil", got)
			}
		})
	}
	t.Run("no go.mod at all", func(t *testing.T) {
		tree := fakeTree{"main.go": "", "a/a.go": ""}
		if got := newGoResolver(t, tree, treeFiles(tree)).ResolveAll("a"); got != nil {
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
	newGoResolver(t, tree, []string{"a/b/c.go", "a/b/d.go", "e/f.ts"})
	want := map[string]int{"go.mod": 1, "a/go.mod": 1, "a/b/go.mod": 1, "e/go.mod": 1}
	if diff := cmp.Diff(want, tree.perFile); diff != "" {
		t.Errorf("NewGoResolver() reads mismatch (-want +got):\n%s", diff)
	}
}

// deepDir returns a directory of depth segments "a".
func deepDir(depth int) string { return strings.TrimSuffix(strings.Repeat("a/", depth), "/") }

// TestNewGoResolver_DeepChainProbesOnlyShallowDirectories is S-M1: one file 1,000 directories
// deep made NewGoResolver probe go.mod in all 1,000 ancestors, each probe costing O(depth) in the
// clone file system (depth 500: 46 s). Only directories of at most 64 segments are probed now.
func TestNewGoResolver_DeepChainProbesOnlyShallowDirectories(t *testing.T) {
	tree := &countTree{perFile: map[string]int{}, files: map[string]string{}}
	start := time.Now()
	newGoResolver(t, tree, []string{deepDir(1000) + "/x.go"})
	if took := time.Since(start); took > time.Second {
		t.Errorf("NewGoResolver(1,000-deep file) took %v, want under 1s", took)
	}
	if got := tree.reads.Load(); got != 65 {
		t.Errorf("NewGoResolver(1,000-deep file) read %d go.mod files, want 65 (the root and 64 levels)", got)
	}
	deepest := 0
	for rel := range tree.perFile {
		deepest = max(deepest, strings.Count(rel, "/"))
	}
	if deepest > 64 {
		t.Errorf("NewGoResolver() read a go.mod %d directories deep, want at most 64", deepest)
	}
}

// TestGoResolver_GoModDepthLimit: a go.mod 64 directories deep is a module root; one deeper is
// ignored, like a missing one.
func TestGoResolver_GoModDepthLimit(t *testing.T) {
	for _, tc := range []struct {
		depth int
		want  bool
	}{{64, true}, {65, false}} {
		t.Run(fmt.Sprintf("depth %d", tc.depth), func(t *testing.T) {
			dir := deepDir(tc.depth)
			tree := fakeTree{dir + "/go.mod": "module example.com/deep\n", dir + "/p/p.go": ""}
			got := newGoResolver(t, tree, treeFiles(tree)).ResolveAll("example.com/deep/p")
			if (got != nil) != tc.want {
				t.Errorf("ResolveAll(example.com/deep/p) with go.mod at depth %d = %v, want resolved: %v", tc.depth, got, tc.want)
			}
		})
	}
}

func TestNewGoResolver_CancelledContext(t *testing.T) {
	tree := &countTree{files: map[string]string{}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, err := importgraph.NewGoResolver(ctx, tree, []string{deepDir(1000) + "/x.go", "b/y.go"})
	if !errors.Is(err, context.Canceled) || r != nil {
		t.Errorf("NewGoResolver(cancelled) = resolver %t, %v, want nil and context.Canceled", r != nil, err)
	}
	if got := tree.reads.Load(); got != 0 {
		t.Errorf("NewGoResolver(cancelled) read %d go.mod files, want none", got)
	}
}

func TestBuild_CancelledWhileProbingGoMod(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src := &cancelFS{onGoMod: cancel}
	_, _, err := importgraph.Build(ctx, src, "/clone", []string{"a/x.go", "b/y.go", "c/z.go"}, importsOf(nil))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Build() error = %v, want context.Canceled", err)
	}
	if src.goMods != 1 {
		t.Errorf("Build() read %d go.mod files, want it to stop after the first", src.goMods)
	}
}

// cancelFS is a SourceFS without files that calls onGoMod on every go.mod read.
type cancelFS struct {
	onGoMod func()
	goMods  int
}

func (f *cancelFS) Walk(context.Context, string) (domain.WalkResult, error) {
	return domain.WalkResult{}, nil
}

func (f *cancelFS) ReadFile(_, rel string) ([]byte, error) {
	if strings.HasSuffix(rel, "go.mod") {
		f.goMods++
		f.onGoMod()
	}
	return nil, fs.ErrNotExist
}

// TestResolveAll_LongSpecResolvesToNothing is S-M3: every step of the prefix walk hashed the rest
// of the specifier, so one 400 KB specifier took about 2 s. A specifier over 1,024 bytes is no
// import path and resolves to nothing at once.
func TestResolveAll_LongSpecResolvesToNothing(t *testing.T) {
	const size = domain.MaxFileBytes
	goSpec := "m/" + strings.Repeat("a/", size/2-2) + "a"
	javaSpec := strings.Repeat("a.", size/2-1) + "A"
	tree := &countTree{gen: func(rel string) (string, bool) {
		dir, ok := strings.CutSuffix(rel, "go.mod")
		return "module m/" + dir + "\n", ok
	}}
	var files []string
	for i := range 16 {
		files = append(files, fmt.Sprintf("d%c/a.go", 'a'+i))
	}
	goRes := newGoResolver(t, tree, files)
	pkgs := map[string][]string{}
	for i := range 16 {
		pkgs[fmt.Sprintf("p%c", 'a'+i)] = []string{"p/A.java"}
	}
	javaRes := importgraph.NewJavaResolver(pkgs)
	tests := []struct {
		name    string
		resolve func(string) []string
		spec    string
	}{
		{"go", goRes.ResolveAll, goSpec},
		{"java", javaRes.ResolveAll, javaSpec},
		{"java static", javaRes.ResolveAll, "static:" + javaSpec + ".*"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := tc.resolve(tc.spec)
			took := time.Since(start)
			t.Logf("ResolveAll(%d-byte spec) took %v", len(tc.spec), took)
			if got != nil {
				t.Errorf("ResolveAll(%d-byte spec) = %v, want nil", len(tc.spec), got)
			}
			if took > 10*time.Millisecond {
				t.Errorf("ResolveAll(%d-byte spec) took %v, want under 10ms", len(tc.spec), took)
			}
		})
	}
}

// TestResolveAll_SpecLengthLimit: a resolvable specifier of exactly 1,024 bytes still resolves.
func TestResolveAll_SpecLengthLimit(t *testing.T) {
	goPkg := strings.Repeat("b", 1024-len("example.com/r/"))
	tree := fakeTree{"go.mod": "module example.com/r\n", goPkg + "/b.go": ""}
	goRes := newGoResolver(t, tree, treeFiles(tree))
	if got := goRes.ResolveAll("example.com/r/" + goPkg); len(got) != 1 {
		t.Errorf("ResolveAll(1,024-byte Go spec) = %v, want its file", got)
	}
	if got := goRes.ResolveAll("example.com/r/" + goPkg + "c"); got != nil {
		t.Errorf("ResolveAll(1,025-byte Go spec) = %v, want nil", got)
	}
	javaPkg := strings.Repeat("p", 1024-len(".A"))
	javaRes := importgraph.NewJavaResolver(map[string][]string{javaPkg: {"x/A.java"}})
	if got := javaRes.ResolveAll(javaPkg + ".A"); len(got) != 1 {
		t.Errorf("ResolveAll(1,024-byte Java spec) = %v, want its file", got)
	}
	if got := javaRes.ResolveAll(javaPkg + ".AB"); got != nil {
		t.Errorf("ResolveAll(1,025-byte Java spec) = %v, want nil", got)
	}
}

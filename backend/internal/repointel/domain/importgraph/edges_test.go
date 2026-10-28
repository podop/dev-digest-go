package importgraph_test

import (
	"context"
	"fmt"
	"io/fs"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// fakeFS is a domain.SourceFS over an in-memory clone; it records the roots it was asked about.
type fakeFS struct {
	files map[string]string
	roots map[string]bool
}

func (f *fakeFS) Walk(context.Context, string) (domain.WalkResult, error) {
	return domain.WalkResult{}, nil
}

func (f *fakeFS) ReadFile(root, rel string) ([]byte, error) {
	if f.roots == nil {
		f.roots = map[string]bool{}
	}
	f.roots[root] = true
	if s, ok := f.files[rel]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("read %s: %w", rel, fs.ErrNotExist)
}

// specsOf returns a SpecifierSource that serves fixed specifiers per file.
func specsOf(m map[string][]string) importgraph.SpecifierSource {
	return func(_ context.Context, file string) []string { return m[file] }
}

func edge(from, to string) domain.Edge { return domain.Edge{From: from, To: to} }

func TestBuildEdges(t *testing.T) {
	files := []string{"a.ts", "b.ts", "c.tsx", "lib/index.ts", "lib/util.js"}
	tree := fakeTree{"a.ts": "", "b.ts": "", "c.tsx": "", "lib/index.ts": "", "lib/util.js": "", "data.json": "", "other.ts": ""}
	specs := specsOf(map[string][]string{
		"a.ts":         {"./b", "./b.ts", "./lib", "react", "node:fs", "./a", "./other", "./data.json", "./c", "./missing"},
		"b.ts":         {"./c", "./a"},
		"c.tsx":        {"./lib/util"},
		"lib/index.ts": {"./util", "../a"},
	})
	got, err := importgraph.BuildEdges(t.Context(), files, importgraph.NewResolver(tree, nil), specs)
	if err != nil {
		t.Fatalf("BuildEdges() error: %v", err)
	}
	want := []domain.Edge{
		edge("a.ts", "b.ts"),
		edge("a.ts", "lib/index.ts"),
		edge("a.ts", "c.tsx"),
		edge("b.ts", "c.tsx"),
		edge("b.ts", "a.ts"),
		edge("c.tsx", "lib/util.js"),
		edge("lib/index.ts", "lib/util.js"),
		edge("lib/index.ts", "a.ts"),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildEdges() mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildEdges_EmptyIsNotNil(t *testing.T) {
	got, err := importgraph.BuildEdges(t.Context(), nil, importgraph.NewResolver(fakeTree{}, nil), specsOf(nil))
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("BuildEdges(no files) = %#v, %v, want an empty non-nil slice", got, err)
	}
}

func TestBuildEdges_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	specs := func(context.Context, string) []string { calls++; return nil }
	_, err := importgraph.BuildEdges(ctx, []string{"a.ts"}, importgraph.NewResolver(fakeTree{"a.ts": ""}, nil), specs)
	if err == nil || ctx.Err() == nil || calls != 0 {
		t.Errorf("BuildEdges(cancelled) = %v after %d calls, want the context error before any file", err, calls)
	}
}

func TestNewTree(t *testing.T) {
	src := &fakeFS{files: map[string]string{"tsconfig.json": "{}", "src/a.ts": "x", "src/shadow.mts": ""}}
	tree := importgraph.NewTree(src, "/clone", []string{"src/a.ts"})
	if !tree.IsFile("src/a.ts") {
		t.Error("IsFile(walked file) = false, want true")
	}
	if tree.IsFile("src/shadow.mts") || tree.IsFile("tsconfig.json") || tree.IsFile("src") {
		t.Error("IsFile(not walked) = true, want false: only walked files are landing places")
	}
	data, err := tree.ReadFile("tsconfig.json")
	if err != nil || string(data) != "{}" {
		t.Errorf("ReadFile(tsconfig.json) = %q, %v, want {}", data, err)
	}
	if !src.roots["/clone"] || len(src.roots) != 1 {
		t.Errorf("ReadFile asked roots %v, want only /clone", src.roots)
	}
}

// TestBuild_BadTSConfig covers AC3050: a malformed tsconfig, an extends to ../outside.json and an
// extends to a missing file still give the relative edges and report the problem without failing
// the build. A malformed root has no alias edge; a bad extends only drops that parent, so the
// root's own paths still produce the alias edge.
func TestBuild_BadTSConfig(t *testing.T) {
	alias := `"compilerOptions":{"paths":{"@/*":["src/*"]}}`
	tests := []struct {
		name      string
		tsconfig  string
		wantErr   bool
		wantAlias bool
	}{
		{"malformed", `{"compilerOptions": {`, true, false},
		{"extends outside the clone", `{"extends":"../outside.json",` + alias + `}`, true, true},
		{"extends a missing file", `{"extends":"./missing.json",` + alias + `}`, true, true},
		{"extends a package", `{"extends":"@tsconfig/node20/tsconfig.json",` + alias + `}`, true, true},
		{"malformed parent holding the paths", `{"extends":"./p.json"}`, true, false},
		{"healthy control", `{` + alias + `}`, false, true},
	}
	files := []string{"src/a.ts", "src/b.ts", "src/c.ts"}
	imports := importsOf(map[string][]string{"src/a.ts": {"./b", "@/c"}})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeFS{files: map[string]string{"tsconfig.json": tc.tsconfig, "p.json": `{"compilerOptions":{"paths":{"@/*":["src/*"]}},`}}
			g, aliasErr, err := importgraph.Build(t.Context(), src, "/clone", files, imports)
			if err != nil {
				t.Fatalf("Build() error: %v", err)
			}
			if (aliasErr != nil) != tc.wantErr {
				t.Errorf("Build() aliasErr = %v, want error: %v", aliasErr, tc.wantErr)
			}
			want := []domain.Edge{edge("src/a.ts", "src/b.ts")}
			if tc.wantAlias {
				want = append(want, edge("src/a.ts", "src/c.ts"))
			}
			if diff := cmp.Diff(want, g.Edges); diff != "" {
				t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuild_NoTSConfig(t *testing.T) {
	src := &fakeFS{files: map[string]string{}}
	g, aliasErr, err := importgraph.Build(t.Context(), src, "/clone", []string{"a.ts", "b.ts"}, importsOf(map[string][]string{"a.ts": {"./b"}}))
	if err != nil || aliasErr != nil {
		t.Fatalf("Build() = %v, %v, want no errors without a tsconfig", aliasErr, err)
	}
	if diff := cmp.Diff([]domain.Edge{edge("a.ts", "b.ts")}, g.Edges); diff != "" {
		t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
	}
}

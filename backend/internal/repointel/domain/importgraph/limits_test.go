package importgraph_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// countTree is a Tree that serves files from a map or from gen, and counts every ReadFile.
type countTree struct {
	files   map[string]string
	gen     func(rel string) (string, bool)
	reads   atomic.Int64
	perFile map[string]int
	onRead  func(n int64)
}

func (c *countTree) IsFile(rel string) bool {
	_, ok := c.files[rel]
	return ok
}

func (c *countTree) ReadFile(rel string) ([]byte, error) {
	n := c.reads.Add(1)
	if c.perFile != nil {
		c.perFile[rel]++
	}
	if c.onRead != nil {
		c.onRead(n)
	}
	if s, ok := c.files[rel]; ok {
		return []byte(s), nil
	}
	if c.gen != nil {
		if s, ok := c.gen(rel); ok {
			return []byte(s), nil
		}
	}
	return nil, fmt.Errorf("read %s: %w", rel, fs.ErrNotExist)
}

// extendsK returns a tsconfig that extends every name in names.
func extendsK(names ...string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = `"` + n + `"`
	}
	return `{"extends":[` + strings.Join(quoted, ",") + `]}`
}

func wantAliasError(t *testing.T, err error) *importgraph.AliasError {
	t.Helper()
	var ae *importgraph.AliasError
	if !errors.As(err, &ae) {
		t.Fatalf("error = %v, want an *AliasError", err)
	}
	return ae
}

// TestLoadAliases_ExtendsFanoutIsBounded is F2: a k-ary extends fan-out ten levels deep (the
// security probe: k^10 reads and error strings) reads each file at most once, stops at
// MaxConfigReads and returns a short error.
func TestLoadAliases_ExtendsFanoutIsBounded(t *testing.T) {
	t.Run("one file extending itself k times", func(t *testing.T) {
		for _, k := range []int{5, 6, 50} {
			self := extendsK(strings.Split(strings.TrimSuffix(strings.Repeat("./a.json,", k), ","), ",")...)
			tree := &countTree{files: map[string]string{"tsconfig.json": self, "a.json": self}, perFile: map[string]int{}}
			start := time.Now()
			_, err := importgraph.LoadAliases(t.Context(), tree)
			elapsed := time.Since(start)
			if elapsed > time.Second || tree.reads.Load() > importgraph.MaxConfigReads {
				t.Errorf("LoadAliases(k=%d) took %v with %d reads, want under 1s and at most %d reads", k, elapsed, tree.reads.Load(), importgraph.MaxConfigReads)
			}
			for f, n := range tree.perFile {
				if n != 1 {
					t.Errorf("LoadAliases(k=%d) read %s %d times, want once", k, f, n)
				}
			}
			ae := wantAliasError(t, err)
			if len(err.Error()) > 1000 || ae.Count != k {
				t.Errorf("LoadAliases(k=%d) error has %d bytes and %d problems, want under 1000 bytes and %d problems", k, len(err.Error()), ae.Count, k)
			}
		}
	})
	t.Run("distinct files, k=5 to depth 10", func(t *testing.T) {
		// n<path> extends five children n<path>0..4 until it is 10 levels deep: 5^10 files.
		tree := &countTree{gen: func(rel string) (string, bool) {
			name, ok := strings.CutSuffix(rel, ".json")
			if !ok || !strings.HasPrefix(name, "n") {
				return "", false
			}
			kids := make([]string, 5)
			for i := range kids {
				kids[i] = fmt.Sprintf("./%s%d.json", name, i)
			}
			return extendsK(kids...), true
		}, files: map[string]string{"tsconfig.json": extendsK("./n.json", "./n.json", "./n.json")}}
		start := time.Now()
		_, err := importgraph.LoadAliases(t.Context(), tree)
		elapsed := time.Since(start)
		if elapsed > time.Second || tree.reads.Load() > importgraph.MaxConfigReads {
			t.Errorf("LoadAliases(5-ary tree) took %v with %d reads, want under 1s and at most %d reads", elapsed, tree.reads.Load(), importgraph.MaxConfigReads)
		}
		ae := wantAliasError(t, err)
		if len(err.Error()) > 1000 {
			t.Errorf("LoadAliases(5-ary tree) error has %d bytes, want under 1000", len(err.Error()))
		}
		if ae.Count < 1 || !contains(ae.Categories, importgraph.ProblemTooManyConfigs) {
			t.Errorf("LoadAliases(5-ary tree) categories = %v, want %q", ae.Categories, importgraph.ProblemTooManyConfigs)
		}
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestLoadAliases_CycleReadsEachFileOnce: a cycle is cut at the first repeat, the options read
// before it still apply, and no file is read twice.
func TestLoadAliases_CycleReadsEachFileOnce(t *testing.T) {
	tree := &countTree{perFile: map[string]int{}, files: map[string]string{
		"tsconfig.json": `{"extends":"./a.json"}`,
		"a.json":        `{"extends":"./b.json","compilerOptions":{"paths":{"@/*":["a/*"]}}}`,
		"b.json":        `{"extends":["./a.json","./tsconfig.json"]}`,
	}}
	aliases, err := importgraph.LoadAliases(t.Context(), tree)
	ae := wantAliasError(t, err)
	if ae.Disabled || !contains(ae.Categories, importgraph.ProblemExtendsCycle) || ae.Count != 2 {
		t.Errorf("LoadAliases(cycle) error = %+v, want 2 cycle problems and aliases kept", ae)
	}
	if got := aliases.Candidates("@/x"); len(got) != 1 || got[0] != "a/x" {
		t.Errorf("Candidates(@/x) = %v, want [a/x]", got)
	}
	for f, n := range tree.perFile {
		if n != 1 {
			t.Errorf("read %s %d times, want once", f, n)
		}
	}
}

// TestLoadAliases_DiamondReusesTheParent: a config named twice is read once, and its options
// still apply to both children.
func TestLoadAliases_DiamondReusesTheParent(t *testing.T) {
	tree := &countTree{perFile: map[string]int{}, files: map[string]string{
		"tsconfig.json": `{"extends":["./l.json","./r.json"]}`,
		"l.json":        `{"extends":"./base.json"}`,
		"r.json":        `{"extends":"./base.json"}`,
		"base.json":     `{"compilerOptions":{"paths":{"@/*":["src/*"]}}}`,
	}}
	aliases, err := importgraph.LoadAliases(t.Context(), tree)
	if err != nil {
		t.Fatalf("LoadAliases() error = %v", err)
	}
	if got := aliases.Candidates("@/x"); len(got) != 1 || got[0] != "src/x" {
		t.Errorf("Candidates(@/x) = %v, want [src/x]", got)
	}
	if n := tree.perFile["base.json"]; n != 1 {
		t.Errorf("base.json read %d times, want once", n)
	}
}

// TestLoadAliases_Cancellation: loading stops at once when ctx ends, before or during the walk.
func TestLoadAliases_Cancellation(t *testing.T) {
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		tree := &countTree{files: map[string]string{"tsconfig.json": "{}"}}
		aliases, err := importgraph.LoadAliases(ctx, tree)
		if !errors.Is(err, context.Canceled) || tree.reads.Load() != 0 || aliases == nil {
			t.Errorf("LoadAliases(cancelled) = %v, %v after %d reads, want usable aliases, context.Canceled and no read", aliases, err, tree.reads.Load())
		}
	})
	t.Run("cancelled while following extends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		tree := &countTree{
			gen: func(rel string) (string, bool) {
				return extendsK("./x.json", "./y.json", "./z.json"), true
			},
			files:  map[string]string{"tsconfig.json": extendsK("./a.json", "./b.json", "./c.json")},
			onRead: func(n int64) { cancel() },
		}
		start := time.Now()
		_, err := importgraph.LoadAliases(ctx, tree)
		if !errors.Is(err, context.Canceled) || tree.reads.Load() > 2 || time.Since(start) > time.Second {
			t.Errorf("LoadAliases(cancelled on first read) = %v after %d reads, want context.Canceled after at most 2 reads", err, tree.reads.Load())
		}
	})
}

// TestLoadAliases_PathsAreCapped is F3: patterns and substitutions past the caps are ignored and
// reported once.
func TestLoadAliases_PathsAreCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"compilerOptions":{"baseUrl":".","paths":{`)
	for i := 0; i < importgraph.MaxPathsPatterns+44; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"p%d/*":[`, i)
		for j := 0; j < importgraph.MaxSubstitutions+9; j++ {
			if j > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `"s%d/*"`, j)
		}
		b.WriteString("]")
	}
	b.WriteString(`}}}`)
	aliases, err := importgraph.LoadAliases(t.Context(), &countTree{files: map[string]string{"tsconfig.json": b.String()}})
	ae := wantAliasError(t, err)
	if ae.Disabled || ae.Count != 1 || !contains(ae.Categories, importgraph.ProblemPathsCapped) {
		t.Errorf("LoadAliases(huge paths) error = %+v, want one %q problem and aliases kept", ae, importgraph.ProblemPathsCapped)
	}
	// 16 substitutions of the pattern, then the baseUrl lookup of the specifier itself.
	if got := aliases.Candidates(fmt.Sprintf("p%d/x", importgraph.MaxPathsPatterns-1)); len(got) != importgraph.MaxSubstitutions+1 {
		t.Errorf("Candidates(last kept pattern) = %d paths, want %d", len(got), importgraph.MaxSubstitutions+1)
	}
	if got := aliases.Candidates(fmt.Sprintf("p%d/x", importgraph.MaxPathsPatterns)); len(got) != 1 {
		t.Errorf("Candidates(first ignored pattern) = %v, want only the baseUrl lookup", got)
	}
}

// countFS is a domain.SourceFS that counts reads per file name.
type countFS struct {
	files map[string]string
	reads atomic.Int64
	names map[string]int
}

func (f *countFS) Walk(context.Context, string) (domain.WalkResult, error) {
	return domain.WalkResult{}, nil
}

func (f *countFS) ReadFile(_, rel string) ([]byte, error) {
	f.reads.Add(1)
	f.names[rel]++
	if s, ok := f.files[rel]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("read %s: %w", rel, fs.ErrNotExist)
}

// TestBuild_SubstitutionFanoutIsBounded is the F3 probe: 2,000 substitutions and 2,000 bare
// imports used to cost 4 million reads, 20 s and 380 MB. Now package.json is read only in
// directories that hold a walked file, so the reads are bounded by the walk, not by the product.
func TestBuild_SubstitutionFanoutIsBounded(t *testing.T) {
	const subs, specs = 2000, 2000
	var b strings.Builder
	b.WriteString(`{"compilerOptions":{"baseUrl":".","paths":{"*":[`)
	for i := 0; i < subs; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"a%d/*"`, i)
	}
	b.WriteString(`]}}}`)
	src := &countFS{files: map[string]string{"tsconfig.json": b.String()}, names: map[string]int{}}
	list := make([]string, specs)
	for i := range list {
		list[i] = fmt.Sprintf("pkg%d", i)
	}
	files := []string{"src/x.ts", "src/y.ts"}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	g, aliasErr, err := importgraph.Build(t.Context(), src, "/clone", files, importsOf(map[string][]string{"src/x.ts": list}))
	elapsed := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if err != nil || len(g.Edges) != 0 {
		t.Fatalf("Build() = %v, %v, want no edges and no error", g.Edges, err)
	}
	_ = wantAliasError(t, aliasErr)
	// the root tsconfig, plus package.json of the two directories above the walked files.
	if got := src.reads.Load(); got > 3 {
		t.Errorf("Build() made %d reads %v, want at most 3", got, src.names)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Build() took %v, want a few seconds at most", elapsed)
	}
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 32<<20 {
		t.Errorf("Build() retained %d MB, want under 32", grown>>20)
	}
}

// TestResolver_PackageJSONOnlyWhereFilesAreWalked: a package.json is read in a directory that
// holds a walked file (and honoured), and never in one that does not.
func TestResolver_PackageJSONOnlyWhereFilesAreWalked(t *testing.T) {
	src := &countFS{names: map[string]int{}, files: map[string]string{
		"src/pkg/package.json": `{"main":"lib/entry.ts"}`,
		"empty/package.json":   `{"main":"x.ts"}`,
	}}
	files := []string{"src/a.ts", "src/pkg/lib/entry.ts"}
	tree := importgraph.NewTree(src, "/clone", files)
	r := importgraph.NewResolver(tree, nil)
	if got, ok := r.Resolve("src/a.ts", "./pkg"); !ok || got != "src/pkg/lib/entry.ts" {
		t.Errorf("Resolve(./pkg) = %q, %v, want src/pkg/lib/entry.ts through package.json main", got, ok)
	}
	if got, ok := r.Resolve("src/a.ts", "../empty"); ok {
		t.Errorf("Resolve(../empty) = %q, true, want no result", got)
	}
	if src.names["empty/package.json"] != 0 {
		t.Errorf("package.json read in a directory without walked files: %v", src.names)
	}
}

// TestBuildEdges_StopsOnCancellationInsideAFile: ctx is looked at every few hundred specifiers,
// not only between files.
func TestBuildEdges_StopsOnCancellationInsideAFile(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var isFile atomic.Int64
	tree := &probeTree{isFile: func() {
		if isFile.Add(1) == 1 {
			cancel()
		}
	}}
	list := make([]string, 20000)
	for i := range list {
		list[i] = fmt.Sprintf("./f%d", i)
	}
	_, err := importgraph.BuildEdges(ctx, []string{"a.ts"}, importgraph.NewResolver(tree, nil), specsOf(map[string][]string{"a.ts": list}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildEdges() error = %v, want context.Canceled", err)
	}
	// each specifier costs about 30 lookups; unbounded it would be 600,000.
	if got := isFile.Load(); got > 20000 {
		t.Errorf("BuildEdges() made %d lookups after the cancel, want it to stop within a few hundred specifiers", got)
	}
}

type probeTree struct{ isFile func() }

func (p *probeTree) IsFile(string) bool              { p.isFile(); return false }
func (p *probeTree) ReadFile(string) ([]byte, error) { return nil, fs.ErrNotExist }

// TestAliasError_HoldsNoTSConfigContent is L2 / NFR3005: a marker placed in a key, a value or an
// extends string of a tsconfig is in neither the error text nor its categories.
func TestAliasError_HoldsNoTSConfigContent(t *testing.T) {
	const marker = "TSCONFIG-CONTENT-MARKER"
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"bad value", map[string]string{"tsconfig.json": `{"compilerOptions": {"baseUrl": ` + marker + `}}`}, importgraph.ProblemMalformed},
		{"missing colon after key", map[string]string{"tsconfig.json": `{"` + marker + `" 1}`}, importgraph.ProblemMalformed},
		{"unexpected character", map[string]string{"tsconfig.json": `{` + "\x01" + marker + `}`}, importgraph.ProblemMalformed},
		{"package extends", map[string]string{"tsconfig.json": `{"extends":"@corp/` + marker + `"}`}, importgraph.ProblemExtendsNotPath},
		{"extends outside", map[string]string{"tsconfig.json": `{"extends":"../` + marker + `"}`}, importgraph.ProblemExtendsOutside},
		{"extends absolute", map[string]string{"tsconfig.json": `{"extends":"/` + marker + `"}`}, importgraph.ProblemExtendsNotPath},
		{"extends missing", map[string]string{"tsconfig.json": `{"extends":"./` + marker + `"}`}, importgraph.ProblemExtendsMissing},
		{"malformed parent", map[string]string{"tsconfig.json": `{"extends":"./p"}`, "p.json": `{"a": ` + marker + `}`}, importgraph.ProblemMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := importgraph.LoadAliases(t.Context(), &countTree{files: tc.files})
			ae := wantAliasError(t, err)
			if strings.Contains(err.Error(), marker) || strings.Contains(strings.Join(ae.Categories, ","), marker) {
				t.Errorf("LoadAliases() error = %q, want no tsconfig content", err)
			}
			if !contains(ae.Categories, tc.want) {
				t.Errorf("LoadAliases() categories = %v, want %q", ae.Categories, tc.want)
			}
		})
	}
}

// FuzzLoadAliases checks that no root tsconfig, however hostile, makes the load read more than
// MaxConfigReads files or return an error of unbounded size. The root may extend files that
// extend it back.
func FuzzLoadAliases(f *testing.F) {
	for _, seed := range []string{
		`{"extends":["./a.json","./a.json","./tsconfig.json"],"compilerOptions":{"paths":{"@/*":["src/*"]}}}`,
		`{"extends":"./a"}`,
		`{"compilerOptions":{"baseUrl":"..","paths":{"*":["*","x/*"],"a":[]}}}`,
		`{"extends":{`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		self := extendsK("./a.json", "./b.json", "./tsconfig.json", "./a")
		tree := &countTree{files: map[string]string{"tsconfig.json": string(data), "a.json": self, "b.json": self}}
		_, err := importgraph.LoadAliases(t.Context(), tree)
		if tree.reads.Load() > importgraph.MaxConfigReads {
			t.Fatalf("LoadAliases() made %d reads, want at most %d", tree.reads.Load(), importgraph.MaxConfigReads)
		}
		if err != nil && len(err.Error()) > 4096 {
			t.Fatalf("LoadAliases() error has %d bytes, want it bounded", len(err.Error()))
		}
	})
}

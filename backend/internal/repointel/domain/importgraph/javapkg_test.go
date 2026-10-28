package importgraph_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

// TestJavaResolver is AC11 and EC4 at the resolver level.
func TestJavaResolver(t *testing.T) {
	r := importgraph.NewJavaResolver(map[string][]string{
		"com.acme.svc":  {"src/main/java/com/acme/svc/OrderService.java", "src/main/java/com/acme/svc/Helper.java"},
		"com.acme.util": {"src/main/java/com/acme/util/Ids.java"},
		"com.acme.dup":  {"a/com/acme/dup/C.java", "b/com/acme/dup/C.java"},
	})
	order := []string{"src/main/java/com/acme/svc/OrderService.java"}
	ids := []string{"src/main/java/com/acme/util/Ids.java"}
	tests := []struct {
		name string
		spec string
		want []string
	}{
		{"single type", "com.acme.svc.OrderService", order},
		{"nested type", "com.acme.svc.OrderService.Page", order},
		{"doubly nested type", "com.acme.svc.OrderService.Page.Row", order},
		{"wildcard", "com.acme.svc.*", []string{"src/main/java/com/acme/svc/OrderService.java", "src/main/java/com/acme/svc/Helper.java"}},
		{"static member", "static:com.acme.util.Ids.next", ids},
		{"static wildcard", "static:com.acme.util.Ids.*", ids},
		{"jdk", "java.util.List", nil},
		{"jdk wildcard", "java.util.*", nil},
		{"unknown class in known package", "com.acme.svc.Missing", nil},
		{"duplicated class in two roots (EC4)", "com.acme.dup.C", []string{"a/com/acme/dup/C.java", "b/com/acme/dup/C.java"}},
		{"one segment", "Foo", nil},
		{"two segments", "com.Foo", nil},
		{"empty", "", nil},
		{"static only", "static:", nil},
		{"star only", "*", nil},
		{"empty segment", "com.acme.svc..OrderService", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, r.ResolveAll(tt.spec)); diff != "" {
				t.Errorf("ResolveAll(%q) mismatch (-want +got):\n%s", tt.spec, diff)
			}
		})
	}
}

// TestJavaResolver_PackageFromDeclarationNotPath is Review Focus 2: a file whose directory does
// not match its package declaration is found by the declared package.
func TestJavaResolver_PackageFromDeclarationNotPath(t *testing.T) {
	r := importgraph.NewJavaResolver(map[string][]string{"com.acme.svc": {"weird/place/OrderService.java"}})
	if diff := cmp.Diff([]string{"weird/place/OrderService.java"}, r.ResolveAll("com.acme.svc.OrderService")); diff != "" {
		t.Errorf("declared package mismatch (-want +got):\n%s", diff)
	}
	if got := r.ResolveAll("weird.place.OrderService"); got != nil {
		t.Errorf("ResolveAll(path-derived spec) = %v, want nil", got)
	}
}

func TestJavaResolver_DefaultPackageWildcardNotResolved(t *testing.T) {
	r := importgraph.NewJavaResolver(map[string][]string{"": {"A.java"}})
	if got := r.ResolveAll("A"); got != nil {
		t.Errorf("ResolveAll(%q) = %v, want nil", "A", got)
	}
}

func TestBuild_JavaGraph(t *testing.T) {
	src := &fakeFS{files: map[string]string{}}
	files := []string{
		"src/main/java/com/acme/web/OrderController.java",
		"src/main/java/com/acme/svc/OrderService.java",
		"src/main/java/com/acme/svc/Helper.java",
		"src/main/java/com/acme/util/Ids.java",
		"src/test/java/com/acme/svc/OrderServiceTest.java",
		"web/app.ts",
		"web/util.ts",
	}
	pkgs := map[string]string{
		files[0]: "com.acme.web", files[1]: "com.acme.svc", files[2]: "com.acme.svc",
		files[3]: "com.acme.util", files[4]: "com.acme.svc",
	}
	specs := map[string][]string{
		files[0]:     {"com.acme.svc.OrderService", "com.acme.svc.*", "static:com.acme.util.Ids.next", "java.util.List", "com.acme.web.OrderController"},
		files[4]:     {"static:com.acme.util.Ids.*"},
		"web/app.ts": {"./util"},
	}
	imports := func(_ context.Context, file string) domain.FileImports {
		fi := domain.FileImports{Specs: specs[file]}
		if p, ok := pkgs[file]; ok {
			fi.Package = domain.JavaPackageKey(p)
		}
		return fi
	}
	g, _, err := importgraph.Build(t.Context(), src, "/clone", files, imports)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	wantEdges := []domain.Edge{
		edge(files[0], files[1]),
		edge(files[0], files[2]),
		edge(files[0], files[4]), // the wildcard covers the test source root: main and test share the package
		edge(files[0], files[3]),
		edge(files[4], files[3]),
		edge("web/app.ts", "web/util.ts"),
	}
	if diff := cmp.Diff(wantEdges, g.Edges); diff != "" {
		t.Errorf("Build() edges mismatch (-want +got):\n%s", diff)
	}
	wantPkgs := []domain.FilePackage{
		{Path: files[0], Key: "java:com.acme.web"},
		{Path: files[1], Key: "java:com.acme.svc"},
		{Path: files[2], Key: "java:com.acme.svc"},
		{Path: files[3], Key: "java:com.acme.util"},
		{Path: files[4], Key: "java:com.acme.svc"},
	}
	if diff := cmp.Diff(wantPkgs, g.Packages); diff != "" {
		t.Errorf("Build() packages mismatch (-want +got):\n%s", diff)
	}
}

// A Go file's "go:" key must not be mistaken for a Java package.
func TestBuild_JavaIgnoresGoKeys(t *testing.T) {
	src := &fakeFS{files: map[string]string{"go.mod": "module example.com/r\n"}}
	files := []string{"a/a.go", "A.java"}
	imports := func(_ context.Context, file string) domain.FileImports {
		if strings.HasSuffix(file, ".go") {
			return domain.FileImports{Package: domain.GoPackageKey(file)}
		}
		return domain.FileImports{Package: domain.JavaPackageKey(""), Specs: []string{"a.A"}}
	}
	g, _, err := importgraph.Build(t.Context(), src, "/clone", files, imports)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(g.Edges) != 0 {
		t.Errorf("edges = %v, want none", g.Edges)
	}
}

// longPackage returns a dotted Java package name of at least n bytes.
func longPackage(n int) string {
	var segs []string
	for i := 0; len(segs)*6 < n; i++ {
		segs = append(segs, fmt.Sprintf("p%04d", i))
	}
	return strings.Join(segs, ".")
}

// TestBuild_LongPackageKeyIsDropped is S-M2: a 3.4 KB package declaration made a 3.4 KB key that
// Postgres refused in the package-key index (btree rows hold at most 2,704 bytes), which failed
// the whole index. A key over 1,024 bytes gives the file no package row; 1,024 bytes still do.
func TestBuild_LongPackageKeyIsDropped(t *testing.T) {
	long := longPackage(3400)
	edgePkg := strings.Repeat("q", 1024-len("java:"))
	files := []string{"a/Long.java", "a/Edge.java", "b/B.java", "g/g.go"}
	keys := map[string]string{
		files[0]: domain.JavaPackageKey(long),
		files[1]: domain.JavaPackageKey(edgePkg),
		files[2]: domain.JavaPackageKey("b"),
		files[3]: "go:" + strings.Repeat("g", 1022),
	}
	imports := func(_ context.Context, file string) domain.FileImports {
		return domain.FileImports{Package: keys[file]}
	}
	g, _, err := importgraph.Build(t.Context(), &fakeFS{files: map[string]string{}}, "/clone", files, imports)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	want := []domain.FilePackage{{Path: files[1], Key: keys[files[1]]}, {Path: files[2], Key: "java:b"}}
	if diff := cmp.Diff(want, g.Packages); diff != "" {
		t.Errorf("Build() packages mismatch (-want +got):\n%s", diff)
	}
}

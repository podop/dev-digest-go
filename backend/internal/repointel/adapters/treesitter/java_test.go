package treesitter_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestExtractJava_Symbols(t *testing.T) {
	longParams := strings.Repeat("a", 130)
	tests := []struct {
		name string
		src  string
		want []domain.Symbol
	}{
		{
			name: "nested types, interface and annotation members, constructor and methods",
			src: "package com.acme.web;\n" +
				"\n" +
				"@RestController\n" +
				"public class OrderController {\n" +
				"    static class Page {\n" +
				"    }\n" +
				"    record R(int a) {}\n" +
				"    interface Repo {\n" +
				"        List<X> all();\n" +
				"    }\n" +
				"    enum Kind { A }\n" +
				"    @interface Audit {}\n" +
				"    @Autowired\n" +
				"    public OrderController(OrderService service) {\n" +
				"        this.service = service;\n" +
				"    }\n" +
				"    Order find(\n" +
				"            long id) {\n" +
				"        return null;\n" +
				"    }\n" +
				"}\n",
			want: []domain.Symbol{
				sym("OrderController", "class", 3, 21, true, "public class OrderController"),
				sym("Page", "class", 5, 6, false, "static class Page"),
				sym("R", "class", 7, 7, false, "record R(int a)"),
				sym("Repo", "interface", 8, 10, false, "interface Repo"),
				sym("all", "method", 9, 9, true, "List<X> all()"),
				sym("Kind", "enum", 11, 11, false, "enum Kind"),
				sym("Audit", "interface", 12, 12, false, "@interface Audit"),
				sym("OrderController", "method", 13, 16, true, "public OrderController(OrderService service)"),
				sym("find", "method", 17, 20, false, "Order find( long id)"),
			},
		},
		{
			name: "members of interface and annotation types are exported at any depth",
			src: "interface Api {\n" +
				"    class Impl { void run() {} }\n" +
				"    default void hello() {}\n" +
				"}\n" +
				"@interface Tag {\n" +
				"    enum Level { LOW }\n" +
				"}\n",
			want: []domain.Symbol{
				sym("Api", "interface", 1, 4, false, "interface Api"),
				sym("Impl", "class", 2, 2, true, "class Impl"),
				sym("run", "method", 2, 2, false, "void run()"),
				sym("hello", "method", 3, 3, true, "default void hello()"),
				sym("Tag", "interface", 5, 7, false, "@interface Tag"),
				sym("Level", "enum", 6, 6, true, "enum Level"),
			},
		},
		{
			name: "annotations are not part of the signature; public between annotations counts",
			src: "class C {\n" +
				"    @RequestMapping(value = \"/search\",\n" +
				"        method = RequestMethod.POST)\n" +
				"    @Override public List<Order> search(Query q) { return null; }\n" +
				"}\n",
			want: []domain.Symbol{
				sym("C", "class", 1, 5, false, "class C"),
				sym("search", "method", 2, 4, true, "public List<Order> search(Query q)"),
			},
		},
		{
			name: "multi-line head is collapsed and limited",
			src:  "class L {\n    public void m(\n        int " + longParams + ") {\n    }\n}\n",
			want: []domain.Symbol{
				sym("L", "class", 1, 5, false, "class L"),
				sym("m", "method", 2, 4, true, "public void m( int "+strings.Repeat("a", 100)+"…"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, "src/main/java/com/acme/web/A.java", tc.src)
			if diff := cmp.Diff(tc.want, got.Symbols, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Extract(A.java) symbols mismatch (-want +got):\n%s", diff)
			}
			for _, s := range got.Symbols {
				if n := domain.UTF16Len(s.Signature); n > domain.MaxSignatureUnits {
					t.Errorf("Extract(A.java) signature of %s has %d units, want at most %d", s.Name, n, domain.MaxSignatureUnits)
				}
			}
		})
	}
}

func TestExtractJava_References(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []domain.Reference
	}{
		{
			name: "method invocations, object creations, then annotations",
			src: "class C {\n" +
				"    @Transactional\n" +
				"    void a() {\n" +
				"        service.find(id);\n" +
				"        Order o = new Order();\n" +
				"        List<X> l = new java.util.ArrayList<>();\n" +
				"    }\n" +
				"    @GetMapping(\"/x\")\n" +
				"    void b() {}\n" +
				"}\n",
			want: []domain.Reference{
				ref("find", 4), ref("Order", 5), ref("ArrayList", 6),
				ref("Transactional", 2), ref("GetMapping", 8),
			},
		},
		{
			name: "scoped names take their last identifier",
			src: "class C {\n" +
				"    @org.junit.Test\n" +
				"    void t() { Outer.Inner i = new Outer.Inner(); new Box<String>(); }\n" +
				"}\n",
			want: []domain.Reference{ref("Inner", 3), ref("Box", 3), ref("Test", 2)},
		},
		{
			name: "same-line declaration and duplicates are dropped",
			src:  "class C {\n    void g() { g(); h(); h(); }\n}\n",
			want: []domain.Reference{ref("h", 2)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, "C.java", tc.src)
			if diff := cmp.Diff(tc.want, got.References, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Extract(C.java) references mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestImportsJava(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want domain.FileImports
	}{
		{
			name: "single, wildcard, static and static wildcard imports",
			src: "package com.acme.web;\n" +
				"\n" +
				"import com.acme.svc.OrderService;\n" +
				"import com.acme.svc.*;\n" +
				"import static com.acme.util.Ids.next;\n" +
				"import static com.acme.util.Ids.*;\n" +
				"import com.acme.svc.OrderService;\n" +
				"\n" +
				"class OrderController {}\n",
			want: domain.FileImports{
				Package: "java:com.acme.web",
				Specs: []string{
					"com.acme.svc.OrderService",
					"com.acme.svc.*",
					"static:com.acme.util.Ids.next",
					"static:com.acme.util.Ids.*",
				},
			},
		},
		{
			name: "white space and comments inside a name",
			src:  "package a . /* x */ b;\nimport a . b . /* y */ C;\n",
			want: domain.FileImports{Package: "java:a.b", Specs: []string{"a.b.C"}},
		},
		{
			name: "default package",
			src:  "import java.util.List;\nclass A {}\n",
			want: domain.FileImports{Package: "java:", Specs: []string{"java.util.List"}},
		},
		{
			name: "no package and no imports",
			src:  "class A {}\n",
			want: domain.FileImports{Package: "java:"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := treesitter.New().Imports(t.Context(), "src/A.java", tc.src)
			if err != nil {
				t.Fatalf("Imports(src/A.java) error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Imports(src/A.java) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

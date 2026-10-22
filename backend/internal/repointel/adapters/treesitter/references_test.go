package treesitter_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/domain"
)

func ref(name string, line int) domain.Reference { return domain.Reference{Name: name, Line: line} }

func TestExtract_References(t *testing.T) {
	tests := []struct {
		name string
		file string
		src  string
		want []domain.Reference
	}{
		{
			name: "identifier call, member call, nested member call",
			file: "a.ts",
			src:  "callIt(1);\nobj.method(2);\na.b.c(3);\nfoo()();\n",
			want: []domain.Reference{ref("callIt", 1), ref("method", 2), ref("c", 3), ref("foo", 4)},
		},
		{
			name: "new expressions use the rightmost name",
			file: "a.ts",
			src:  "new Thing();\nnew ns.Other();\nnew (getClass())();\n",
			want: []domain.Reference{ref("getClass", 3), ref("Thing", 1), ref("Other", 2)},
		},
		{
			name: "calls come before new expressions, each in document order",
			file: "a.ts",
			src:  "new A();\nb();\nnew C();\nd();\n",
			want: []domain.Reference{ref("b", 2), ref("d", 4), ref("A", 1), ref("C", 3)},
		},
		{
			name: "nothing inside an import statement",
			file: "a.ts",
			src:  "import x from './x';\nimport('y');\nrequire('z');\n",
			want: []domain.Reference{ref("require", 3)},
		},
		{
			name: "keywords are skipped",
			file: "a.ts",
			src:  "if (a) { b(); }\nfunction f() { return get(); }\nset();\nnew Real();\n",
			want: []domain.Reference{ref("b", 1), ref("Real", 4)},
		},
		{
			name: "a call on its own declaration line is dropped",
			file: "a.ts",
			src:  "function f() { f(); }\nfunction g() {\n  f();\n  g();\n}\nconst x = () => 1; x();\nclass K { m() { this.m(); K.s(); } }\n",
			want: []domain.Reference{ref("f", 3), ref("g", 4), ref("s", 7)},
		},
		{
			name: "de-duplicated by name and line",
			file: "a.ts",
			src:  "dup2(); dup2(); other();\nother()\n",
			want: []domain.Reference{ref("dup2", 1), ref("other", 1), ref("other", 2)},
		},
		{
			name: "multi-line call is on its first line",
			file: "a.ts",
			src:  "bar(\n  1,\n);\nx\n  .chain()\n  .more();\n",
			want: []domain.Reference{ref("bar", 1), ref("more", 4), ref("chain", 4)},
		},
		{
			name: "jsx elements in a tsx file: capitalised names only",
			file: "a.tsx",
			src:  "const el = <Foo a={1}><div>t</div><Bar.Baz /><lower.cased /><Self/></Foo>;\nconst b = <Foo.Inner />;\n",
			want: []domain.Reference{ref("Foo", 1), ref("Bar", 1), ref("Self", 1), ref("Foo", 2)},
		},
		{
			name: "jsx in a jsx file uses the tsx grammar",
			file: "a.jsx",
			src:  "const el = <Foo />;\nrun();\n",
			want: []domain.Reference{ref("run", 2), ref("Foo", 1)},
		},
		{
			name: "jsx in a js file is parsed but not a reference",
			file: "a.js",
			src:  "const el = <Foo />;\nrun();\n",
			want: []domain.Reference{ref("run", 2)},
		},
		{
			name: "a jsx element on a declaration line is dropped",
			file: "a.tsx",
			src:  "function Self() { return 1; }\nconst x = <Self />;\nfunction Foo() { return <Foo />; }\n",
			want: []domain.Reference{ref("Self", 2)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, tc.file, tc.src).References
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Extract(%q).References mismatch (-want +got):\n%s", tc.file, diff)
			}
		})
	}
}

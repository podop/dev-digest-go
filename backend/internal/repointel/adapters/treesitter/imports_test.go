package treesitter_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
)

func TestImports(t *testing.T) {
	tests := []struct {
		name string
		file string
		src  string
		want []string
	}{
		// TypeScript: what transpileModule keeps.
		{"ts side effect import", "a.ts", "import './polyfill';\n", []string{"./polyfill"}},
		{"ts default used as a value", "a.ts", "import a from './a';\na();\n", []string{"./a"}},
		{"ts unused import is elided", "a.ts", "import a from './a';\n", nil},
		{"ts import type statement", "a.ts", "import type { T } from './t';\nlet x: T;\n", nil},
		{"ts inline type specifiers only", "a.ts", "import { type A } from './a';\nlet x: A;\n", nil},
		{"ts mixed type and value specifiers", "a.ts", "import { type A, b } from './m';\nb();\n", []string{"./m"}},
		{"ts empty braces", "a.ts", "import {} from './e';\n", nil},
		{"ts import equals require", "a.ts", "import x = require('./x');\nx();\n", nil},
		{"ts binding used only in type positions", "a.ts", "import { T, U, V, W, I } from './t';\nconst x: T = 1;\ntype Q = typeof U;\nlet y: Array<V>;\nclass C implements I {}\nconst z = 1 as W;\n", nil},
		{"ts namespace used only as a type qualifier", "a.ts", "import * as ns from './n';\nlet a: ns.Foo;\n", nil},
		{"ts namespace used as a value", "a.ts", "import * as ns from './n';\nns.run();\n", []string{"./n"}},
		{"ts export of a binding is a value use", "a.ts", "import { n } from './n';\nexport { n };\n", []string{"./n"}},
		{"ts export default of a binding", "a.ts", "import d from './d';\nexport default d;\n", []string{"./d"}},
		{"ts export type of a binding is not a value use", "a.ts", "import { T } from './t';\nexport type { T };\n", nil},
		{"ts extends is a value use, implements is not", "a.ts", "import { B } from './b';\nimport { I } from './i';\nclass A extends B implements I {}\n", []string{"./b"}},
		{"ts decorator is a value use", "a.ts", "import { Dec } from './d';\n@Dec() class A {}\n", []string{"./d"}},
		{"ts shorthand property is a value use", "a.ts", "import { a } from './a';\nconst o = { a };\n", []string{"./a"}},
		{"ts typeof import in a type", "a.ts", "const x: typeof import('./q') = 1;\n", nil},
		{"ts value after satisfies", "a.ts", "import { v, T } from './m';\nconst a = v satisfies T;\n", []string{"./m"}},
		{"ts parameter shadows the binding", "a.ts", "import { name } from './n';\nfunction f(name) { return name; }\n", nil},
		{"ts destructured local shadows the binding", "a.ts", "import { id } from './n';\nfunction f(o) { const { id } = o; return id; }\n", nil},
		{"ts shadow does not hide a use outside its scope", "a.ts", "import { name } from './n';\nfunction f(name) { return name; }\nname;\n", []string{"./n"}},
		{"ts block-scoped local shadows only its block", "a.ts", "import { x } from './x';\n{ let x = 1; x; }\n", nil},
		{"ts var shadows in the whole function", "a.ts", "import { x } from './x';\nfunction f() { { var x = 1; } return x; }\n", nil},
		{"ts ambient declarations contribute nothing", "a.ts", "declare module 'm' {\n  import x from './y';\n}\n", nil},
		{"ts import after its use", "a.ts", "run();\nimport { run } from './r';\n", []string{"./r"}},
		{"ts tsx jsx element is a value use", "a.tsx", "import { Foo } from './foo';\nconst e = <Foo />;\n", []string{"./foo"}},
		{"ts tsx member jsx element", "a.tsx", "import * as UI from './ui';\nconst e = <UI.Button />;\n", []string{"./ui"}},
		{"ts tsx lower-case tag is intrinsic", "a.tsx", "import foo from './foo';\nconst e = <foo />;\n", nil},
		{"ts tsx unused import is elided", "a.tsx", "import { Foo } from './foo';\n", nil},
		// Re-exports.
		{
			name: "re-exports",
			file: "a.ts",
			src:  "export * from './a';\nexport * as ns from './b';\nexport { x } from './c';\nexport type { T } from './d';\nexport { type U } from './e';\nexport {} from './f';\n",
			want: []string{"./a", "./b", "./c"},
		},
		// Dynamic loads and literals.
		{
			name: "dynamic import and require take literals only",
			file: "a.ts",
			src:  "import('./x');\nrequire('./y');\nrequire(z);\nimport(`./t${a}`);\nimport(`./lit`);\nrequire();\n",
			want: []string{"./x", "./y", "./lit"},
		},
		{"specifiers in comments and strings are never taken", "a.ts", "// import('./c');\n/* require('./d') */\nconst s = \"require('./q')\";\n", nil},
		{"first-seen order without duplicates", "a.ts", "import './b';\nimport './a';\nimport './b';\nrequire('./a');\n", []string{"./b", "./a"}},
		{"query suffix is kept", "a.ts", "import './a.css?inline';\n", []string{"./a.css?inline"}},
		{"escapes are decoded", "a.ts", "import './a\\x2fb';\nimport './c\\u0064';\n", []string{"./a/b", "./cd"}},
		{"empty specifier is ignored", "a.ts", "import '';\nrequire('');\n", nil},
		{"error-recovered tree still yields specifiers", "a.ts", "import a from './a';\na();\nfunction broken( {\n", []string{"./a"}},
		// JavaScript: no usage check.
		{
			name: "js counts every form",
			file: "a.js",
			src: "import a from './a';\nimport {} from './b';\nimport './c';\nexport * from './d';\nexport { e } from './f';\n" +
				"import('./g');\nrequire('./h');\ndefine(['./i', './j'], function () {});\nrequire(['./k'], cb);\nrequire(dyn);\nrequire(`./l`);\n",
			want: []string{"./a", "./b", "./c", "./d", "./f", "./g", "./h", "./i", "./j", "./k", "./l"},
		},
		{"jsx file follows the javascript rules", "a.jsx", "import { Foo } from './foo';\n", []string{"./foo"}},
		{"mjs file", "a.mjs", "import x from './x.mjs';\n", []string{"./x.mjs"}},
		{"cjs file", "a.cjs", "const x = require('./x');\n", []string{"./x"}},
		{"amd is not taken in typescript", "a.ts", "define(['./i'], function () {});\n", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := treesitter.New().Imports(t.Context(), tc.file, tc.src)
			if err != nil {
				t.Fatalf("Imports(%q) error = %v", tc.file, err)
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Imports(%q) mismatch (-want +got):\n%s", tc.file, diff)
			}
		})
	}
}

package importgraph_test

import (
	"testing"

	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

func resolver(t *testing.T, tree fakeTree) *importgraph.Resolver {
	t.Helper()
	aliases, err := importgraph.LoadAliases(t.Context(), tree)
	if err != nil {
		t.Fatalf("LoadAliases() error: %v", err)
	}
	return importgraph.NewResolver(tree, aliases)
}

func TestResolve(t *testing.T) {
	tree := fakeTree{
		"src/main.ts":                "",
		"src/util.ts":                "",
		"src/both.js":                "",
		"src/both.ts":                "",
		"src/types.d.ts":             "",
		"src/comp.tsx":               "",
		"src/comp.js":                "",
		"src/only.tsx":               "",
		"src/x.js":                   "",
		"src/y.ts":                   "",
		"src/exact.json":             "",
		"src/dir/index.ts":           "",
		"src/idx/index.js":           "",
		"src/idx/index.tsx":          "",
		"src/pkg/package.json":       `{"main":"./lib/entry"}`,
		"src/pkg/lib/entry.ts":       "",
		"src/pkg/index.ts":           "",
		"src/dirmain/package.json":   `{"main":"lib"}`,
		"src/dirmain/lib/index.ts":   "",
		"src/badmain/package.json":   `{"main":"missing.js"}`,
		"src/badmain/index.ts":       "",
		"src/malformed/package.json": `{`,
		"src/malformed/index.ts":     "",
		"src/nested/deep/a.ts":       "",
		"src/ext.test.ts":            "",
		"top.ts":                     "",
		"src/parent.ts":              "",
		"src/loop/package.json":      `{"main":"."}`,
		"src/loop/index.ts":          "",
		"a/b.ts":                     "",
		"src/mjs.mjs":                "",
		"src/c.cjs":                  "",
		"src/z.jsx":                  "",
		"src/z.js":                   "",
		"src/z.tsx":                  "",
		"src/q.ts":                   "",
	}
	r := resolver(t, tree)
	tests := []struct {
		name     string
		importer string
		spec     string
		want     string
		ok       bool
	}{
		{"sibling without extension", "src/main.ts", "./util", "src/util.ts", true},
		{"sibling with extension", "src/main.ts", "./util.ts", "src/util.ts", true},
		{"parent directory", "src/nested/deep/a.ts", "../../parent", "src/parent.ts", true},
		{"up to the root", "src/main.ts", "../top", "top.ts", true},
		{"from a root file", "top.ts", "./src/util", "src/util.ts", true},
		{"extension order: .js before .ts", "src/main.ts", "./both", "src/both.js", true},
		{"extension order: .ts before .tsx before .d.ts", "src/main.ts", "./types", "src/types.d.ts", true},
		{"extension order: .js before .tsx", "src/main.ts", "./comp", "src/comp.js", true},
		{"tsx only", "src/main.ts", "./only", "src/only.tsx", true},
		{"existing file with another extension", "src/main.ts", "./exact.json", "src/exact.json", true},
		{"cjs and mjs", "src/main.ts", "./c", "src/c.cjs", true},
		{"mjs", "src/main.ts", "./mjs", "src/mjs.mjs", true},
		{"directory index", "src/main.ts", "./dir", "src/dir/index.ts", true},
		{"trailing slash", "src/main.ts", "./dir/", "src/dir/index.ts", true},
		{"dot is the directory itself", "src/dir/other.ts", ".", "src/dir/index.ts", true},
		{"dot dot is the parent directory", "src/dir/other.ts", "..", "", false},
		{"index extension order", "src/main.ts", "./idx", "src/idx/index.js", true},
		{"package main as a file plus extension", "src/main.ts", "./pkg", "src/pkg/lib/entry.ts", true},
		{"package main naming a directory", "src/main.ts", "./dirmain", "src/dirmain/lib/index.ts", true},
		{"package main missing falls back to index", "src/main.ts", "./badmain", "src/badmain/index.ts", true},
		{"malformed package.json falls back to index", "src/main.ts", "./malformed", "src/malformed/index.ts", true},
		{"package main pointing at itself terminates", "src/main.ts", "./loop", "src/loop/index.ts", true},
		{".js specifier retries as .ts", "src/main.ts", "./y.js", "src/y.ts", true},
		{".js specifier retries as .tsx", "src/main.ts", "./only.js", "src/only.tsx", true},
		{".js specifier that exists stays", "src/main.ts", "./x.js", "src/x.js", true},
		{".mjs specifier retries as .ts", "src/main.ts", "./y.mjs", "src/y.ts", true},
		{".jsx specifier retries as .tsx when no .js shadows it", "src/main.ts", "./only.jsx", "src/only.tsx", true},
		{".jsx specifier that exists stays", "src/main.ts", "./z.jsx", "src/z.jsx", true},
		{".jsx quirk: x.jsx with x.tsx and x.js present stays unresolved (the retry finds x.js first)", "src/main.ts", "./comp.jsx", "", false},
		{"query suffix", "src/main.ts", "./util?raw", "src/util.ts", true},
		{"query with extension", "src/main.ts", "./util.ts?inline&x=1", "src/util.ts", true},
		{"only a query", "src/main.ts", "?x", "", false},
		{"unresolved", "src/main.ts", "./nope", "", false},
		{"escapes the clone", "src/main.ts", "../../outside", "", false},
		{"escapes via dot dot", "top.ts", "..", "", false},
		{"builtin", "src/main.ts", "fs", "", false},
		{"node scheme", "src/main.ts", "node:fs", "", false},
		{"bare package", "src/main.ts", "react", "", false},
		{"scoped package", "src/main.ts", "@scope/pkg/sub", "", false},
		{"hash import", "src/main.ts", "#internal/x", "", false},
		{"empty specifier", "src/main.ts", "", "", false},
		{"absolute path", "src/main.ts", "/src/util", "", false},
		{"file named like the test", "src/main.ts", "./ext.test", "src/ext.test.ts", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := r.Resolve(tc.importer, tc.spec)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Resolve(%q, %q) = %q, %v, want %q, %v", tc.importer, tc.spec, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestResolve_Aliases(t *testing.T) {
	tree := fakeTree{
		"tsconfig.json": `{
			// Next.js style
			"compilerOptions": {
				"baseUrl": ".",
				"paths": {
					"@/*": ["src/*"],
					"@shared/*": ["packages/shared/src/*", "packages/shared/lib/*"],
					"@lib": ["src/lib/index"],
					"react": ["src/shims/react"],
				},
			},
		}`,
		"src/app.ts":                      "",
		"src/lib/index.ts":                "",
		"src/lib/helper.ts":               "",
		"src/components/Button.tsx":       "",
		"src/components/index.ts":         "",
		"packages/shared/lib/only-lib.ts": "",
		"packages/shared/src/both.ts":     "",
		"packages/shared/lib/both.ts":     "",
		"src/shims/react.ts":              "",
		"src/ts-target.ts":                "",
	}
	r := resolver(t, tree)
	tests := []struct {
		spec string
		want string
		ok   bool
	}{
		{"@/lib/helper", "src/lib/helper.ts", true},
		{"@/components", "src/components/index.ts", true},
		{"@/components/Button", "src/components/Button.tsx", true},
		{"@/ts-target.js", "src/ts-target.ts", true},
		{"@lib", "src/lib/index.ts", true},
		{"@shared/both", "packages/shared/src/both.ts", true},
		{"@shared/only-lib", "packages/shared/lib/only-lib.ts", true},
		{"@shared/missing", "", false},
		{"@/missing", "", false},
		{"react", "src/shims/react.ts", true},
		{"unmapped/pkg", "", false},
		{"@/lib/helper?raw", "src/lib/helper.ts", true},
	}
	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			got, ok := r.Resolve("src/app.ts", tc.spec)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Resolve(src/app.ts, %q) = %q, %v, want %q, %v", tc.spec, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestResolve_BuiltinBeatsAlias(t *testing.T) {
	tree := fakeTree{
		"tsconfig.json":  `{"compilerOptions":{"paths":{"fs":["src/fs-shim"]}}}`,
		"src/fs-shim.ts": "",
		"src/a.ts":       "",
	}
	if got, ok := resolver(t, tree).Resolve("src/a.ts", "fs"); ok {
		t.Errorf("Resolve(fs) = %q, want a builtin to be ignored before aliases", got)
	}
}

func TestResolve_NoAliasesWithoutTSConfig(t *testing.T) {
	tree := fakeTree{"src/a.ts": "", "src/x.ts": ""}
	if got, ok := resolver(t, tree).Resolve("src/a.ts", "@/x"); ok {
		t.Errorf("Resolve(@/x) = %q, want none without a tsconfig", got)
	}
}

func TestResolve_ConcurrentUse(t *testing.T) {
	tree := fakeTree{"a/package.json": `{"main":"m"}`, "a/m.ts": "", "b.ts": ""}
	r := resolver(t, tree)
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 100 {
				if got, ok := r.Resolve("b.ts", "./a"); !ok || got != "a/m.ts" {
					t.Errorf("Resolve(./a) = %q, %v, want a/m.ts", got, ok)
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}

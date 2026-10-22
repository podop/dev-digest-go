package importgraph_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain/importgraph"
)

func TestLoadAliases_Candidates(t *testing.T) {
	tests := []struct {
		name string
		tree fakeTree
		spec string
		want []string
	}{
		{
			name: "star pattern, baseUrl dot",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["src/*"]}}}`},
			spec: "@/lib/util",
			want: []string{"src/lib/util", "@/lib/util"},
		},
		{
			name: "no baseUrl: relative to the file that declares paths",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"~/*":["./app/*"]}}}`},
			spec: "~/x",
			want: []string{"app/x"},
		},
		{
			name: "baseUrl moves the base",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"baseUrl":"./src","paths":{"@/*":["lib/*"]}}}`},
			spec: "@/a",
			want: []string{"src/lib/a", "src/@/a"},
		},
		{
			name: "substitutions are tried in order",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["a/*","b/*","c/fixed"]}}}`},
			spec: "@/x",
			want: []string{"a/x", "b/x", "c/fixed"},
		},
		{
			name: "longest prefix wins",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["one/*"],"@/deep/*":["two/*"],"*":["three/*"]}}}`},
			spec: "@/deep/x",
			want: []string{"two/x"},
		},
		{
			name: "equal prefix: the first declared wins",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*.js":["js/*"],"@/*":["any/*"]}}}`},
			spec: "@/a.js",
			want: []string{"js/a"},
		},
		{
			name: "suffix must match",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*.svg":["icons/*"]}}}`},
			spec: "@/a.png",
			want: nil,
		},
		{
			name: "exact key beats star patterns",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["star/*"],"@/exact":["only/one"]}}}`},
			spec: "@/exact",
			want: []string{"only/one"},
		},
		{
			name: "pattern matching the empty star",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["src/*"]}}}`},
			spec: "@/",
			want: []string{"src"},
		},
		{
			name: "prefix and suffix must not overlap",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"ab*ba":["x/*"]}}}`},
			spec: "aba",
			want: nil,
		},
		{
			name: "JSON5 syntax",
			tree: fakeTree{"tsconfig.json": "{\n // aliases\n compilerOptions: { paths: { '@/*': ['src/*',], }, },\n}"},
			spec: "@/a",
			want: []string{"src/a"},
		},
		{
			name: "extends: child paths replace the parent's, baseUrl inherited and anchored at its own file",
			tree: fakeTree{
				"tsconfig.json":    `{"extends":"./config/base","compilerOptions":{"paths":{"@/*":["src/*"]}}}`,
				"config/base.json": `{"compilerOptions":{"baseUrl":"..","paths":{"@old/*":["old/*"]}}}`,
			},
			spec: "@/a",
			want: []string{"src/a", "@/a"},
		},
		{
			name: "extends: parent paths apply when the child has none; base is the parent's directory",
			tree: fakeTree{
				"tsconfig.json":    `{"extends":"./config/base.json"}`,
				"config/base.json": `{"compilerOptions":{"paths":{"@/*":["../src/*"]}}}`,
			},
			spec: "@/a",
			want: []string{"src/a"},
		},
		{
			name: "extends array: a later entry overrides an earlier one",
			tree: fakeTree{
				"tsconfig.json": `{"extends":["./a","./b"]}`,
				"a.json":        `{"compilerOptions":{"paths":{"@/*":["from-a/*"]}}}`,
				"b.json":        `{"compilerOptions":{"paths":{"@/*":["from-b/*"]}}}`,
			},
			spec: "@/x",
			want: []string{"from-b/x"},
		},
		{
			name: "extends without extension reaches name.json; a file named exactly wins",
			tree: fakeTree{
				"tsconfig.json": `{"extends":"./base"}`,
				"base":          `{"compilerOptions":{"paths":{"@/*":["exact/*"]}}}`,
				"base.json":     `{"compilerOptions":{"paths":{"@/*":["json/*"]}}}`,
			},
			spec: "@/x",
			want: []string{"exact/x"},
		},
		{
			name: "configDir template",
			tree: fakeTree{
				"tsconfig.json": `{"extends":"./cfg/base.json"}`,
				"cfg/base.json": `{"compilerOptions":{"baseUrl":"${configDir}","paths":{"@/*":["${configDir}/src/*"]}}}`,
			},
			spec: "@/x",
			want: []string{"src/x", "@/x"},
		},
		{
			name: "baseUrl outside the clone yields nothing",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"baseUrl":"..","paths":{"@/*":["src/*"]}}}`},
			spec: "@/x",
			want: nil,
		},
		{
			name: "substitution outside the clone is skipped",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["../../etc/*","safe/*"]}}}`},
			spec: "@/x",
			want: []string{"safe/x"},
		},
		{
			name: "absolute substitution is skipped",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":["/etc/*"]}}}`},
			spec: "@/x",
			want: nil,
		},
		{
			name: "malformed shapes are ignored",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"paths":{"@/*":"src/*","~/*":[1,2],"a*b*c":["x"],"ok/*":["fine/*"]}}}`},
			spec: "ok/y",
			want: []string{"fine/y"},
		},
		{
			name: "baseUrl without paths: the specifier is looked up below the base",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":{"baseUrl":"."}}`},
			spec: "lib/x",
			want: []string{"lib/x"},
		},
		{
			name: "compilerOptions of the wrong type",
			tree: fakeTree{"tsconfig.json": `{"compilerOptions":[]}`},
			spec: "@/x",
			want: nil,
		},
		{
			name: "no tsconfig",
			tree: fakeTree{},
			spec: "@/x",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aliases, err := importgraph.LoadAliases(tc.tree)
			if err != nil {
				t.Fatalf("LoadAliases() error: %v", err)
			}
			if diff := cmp.Diff(tc.want, aliases.Candidates(tc.spec)); diff != "" {
				t.Errorf("Candidates(%q) mismatch (-want +got):\n%s", tc.spec, diff)
			}
		})
	}
}

func TestLoadAliases_NoTSConfigIsNotAnError(t *testing.T) {
	aliases, err := importgraph.LoadAliases(fakeTree{})
	if err != nil || aliases == nil {
		t.Errorf("LoadAliases(no tsconfig) = %v, %v, want empty aliases and no error", aliases, err)
	}
}

// TestLoadAliases_BadRootDisablesAliases: an unreadable or malformed root tsconfig gives an error
// and no alias.
func TestLoadAliases_BadRootDisablesAliases(t *testing.T) {
	tests := []struct {
		name string
		tree fakeTree
	}{
		{"malformed root", fakeTree{"tsconfig.json": `{"compilerOptions": {"paths": `}},
		{"root is not an object", fakeTree{"tsconfig.json": `[1]`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aliases, err := importgraph.LoadAliases(tc.tree)
			if err == nil {
				t.Fatal("LoadAliases() error = nil, want the reason aliases are off")
			}
			if aliases == nil {
				t.Fatal("LoadAliases() returned nil aliases, want usable empty aliases")
			}
			if got := aliases.Candidates("@/x"); got != nil {
				t.Errorf("Candidates(@/x) = %v, want none after a failure", got)
			}
		})
	}
}

// TestLoadAliases_BadExtendsDropsOnlyThatParent is EC3025 / AC3050: a failing extends costs only
// the parent (and its ancestors); the declaring file's own paths and the parents read fine still
// apply, and the error is returned for the caller to log.
func TestLoadAliases_BadExtendsDropsOnlyThatParent(t *testing.T) {
	own := `"compilerOptions":{"paths":{"@/*":["src/*"]}}`
	tests := []struct {
		name string
		tree fakeTree
		want []string
	}{
		{"extends leaves the clone", fakeTree{"tsconfig.json": `{"extends":"../outside.json",` + own + `}`}, []string{"src/x"}},
		{"extends a missing relative file", fakeTree{"tsconfig.json": `{"extends":"./missing",` + own + `}`}, []string{"src/x"}},
		{"extends an absolute path", fakeTree{"tsconfig.json": `{"extends":"/etc/x.json",` + own + `}`}, []string{"src/x"}},
		{"extends a package", fakeTree{"tsconfig.json": `{"extends":"@tsconfig/node20/tsconfig.json",` + own + `}`}, []string{"src/x"}},
		{
			"malformed parent holding the only paths",
			fakeTree{"tsconfig.json": `{"extends":"./p.json"}`, "p.json": `{"compilerOptions":{"paths":{"@/*":["src/*"]}},`},
			nil,
		},
		{
			"malformed parent, own paths kept",
			fakeTree{"tsconfig.json": `{"extends":"./p.json",` + own + `}`, "p.json": `{`},
			[]string{"src/x"},
		},
		{
			"good parent kept next to a bad one",
			fakeTree{
				"tsconfig.json": `{"extends":["./good.json","./missing.json"]}`,
				"good.json":     `{"compilerOptions":{"paths":{"@/*":["from-good/*"]}}}`,
			},
			[]string{"from-good/x"},
		},
		{
			"parent kept when its own extends fails",
			fakeTree{
				"tsconfig.json": `{"extends":"./sub/p.json"}`,
				"sub/p.json":    `{"extends":"../../x.json","compilerOptions":{"paths":{"@/*":["lib/*"]}}}`,
			},
			[]string{"sub/lib/x"},
		},
		{
			"extends cycle: the files read before the limit still count",
			fakeTree{
				"tsconfig.json": `{"extends":"./a.json"}`,
				"a.json":        `{"extends":"./b.json","compilerOptions":{"paths":{"@/*":["a/*"]}}}`,
				"b.json":        `{"extends":"./a.json"}`,
			},
			[]string{"a/x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			aliases, err := importgraph.LoadAliases(tc.tree)
			if err == nil {
				t.Error("LoadAliases() error = nil, want the dropped part reported")
			}
			if aliases == nil {
				t.Fatal("LoadAliases() returned nil aliases")
			}
			if diff := cmp.Diff(tc.want, aliases.Candidates("@/x")); diff != "" {
				t.Errorf("Candidates(@/x) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadAliases_ExtendsDepth(t *testing.T) {
	// hops = number of extends links from the root file.
	tests := []struct {
		hops   int
		wantOK bool
	}{{1, true}, {importgraph.MaxExtendsDepth, true}, {importgraph.MaxExtendsDepth + 1, false}}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d hops", tc.hops), func(t *testing.T) {
			tree := fakeTree{"tsconfig.json": `{"extends":"./c1.json"}`}
			for i := 1; i < tc.hops; i++ {
				tree[fmt.Sprintf("c%d.json", i)] = fmt.Sprintf(`{"extends":"./c%d.json"}`, i+1)
			}
			tree[fmt.Sprintf("c%d.json", tc.hops)] = `{"compilerOptions":{"paths":{"@/*":["src/*"]}}}`
			aliases, err := importgraph.LoadAliases(tree)
			if tc.wantOK {
				if err != nil || len(aliases.Candidates("@/x")) != 1 {
					t.Errorf("LoadAliases(%d hops) = %v, %v, want aliases", tc.hops, aliases.Candidates("@/x"), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "deeper") || len(aliases.Candidates("@/x")) != 0 {
				t.Errorf("LoadAliases(%d hops) = %v, %v, want a depth error and the too-deep parent dropped", tc.hops, aliases.Candidates("@/x"), err)
			}
		})
	}
}

func TestAliases_NilReceiver(t *testing.T) {
	var a *importgraph.Aliases
	if got := a.Candidates("@/x"); got != nil {
		t.Errorf("(*Aliases)(nil).Candidates() = %v, want nil", got)
	}
}

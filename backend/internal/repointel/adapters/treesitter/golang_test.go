package treesitter_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/repointel/adapters/treesitter"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestExtractGo_Symbols(t *testing.T) {
	longParams := strings.Repeat("a", 130)
	tests := []struct {
		name string
		src  string
		want []domain.Symbol
	}{
		{
			name: "functions, methods and types; no var or const",
			src: "package svc\n" +
				"\n" +
				"import \"context\"\n" +
				"\n" +
				"func Run() {}\n" +
				"\n" +
				"func helper() {}\n" +
				"\n" +
				"func (s *Server) Start(ctx context.Context) error {\n" +
				"\treturn nil\n" +
				"}\n" +
				"\n" +
				"type Server struct {\n" +
				"\taddr string\n" +
				"}\n" +
				"\n" +
				"type Store interface {\n" +
				"\tGet(id ID) error\n" +
				"}\n" +
				"\n" +
				"type ID string\n" +
				"\n" +
				"var X = 1\n" +
				"\n" +
				"const Y = 2\n",
			want: []domain.Symbol{
				sym("Run", "function", 5, 5, true, "func Run()"),
				sym("helper", "function", 7, 7, false, "func helper()"),
				sym("Start", "method", 9, 11, true, "func (s *Server) Start(ctx context.Context) error"),
				sym("Server", "class", 13, 15, true, "type Server struct"),
				sym("Store", "interface", 17, 19, true, "type Store interface"),
				sym("ID", "type", 21, 21, true, "type ID string"),
			},
		},
		{
			name: "grouped, generic and alias types",
			src: "package p\n" +
				"type (\n" +
				"\tlist[T any] struct{ items []T }\n" +
				"\tAlias = list[int]\n" +
				"\tHandler func(int) error\n" +
				")\n",
			want: []domain.Symbol{
				sym("list", "class", 3, 3, false, "type list[T any] struct"),
				sym("Alias", "type", 4, 4, true, "type Alias = list[int]"),
				sym("Handler", "type", 5, 5, true, "type Handler func(int) error"),
			},
		},
		{
			name: "unicode upper case first rune is exported",
			src:  "package p\nfunc Ärger() {}\nfunc ärger() {}\n",
			want: []domain.Symbol{
				sym("Ärger", "function", 2, 2, true, "func Ärger()"),
				sym("ärger", "function", 3, 3, false, "func ärger()"),
			},
		},
		{
			name: "multi-line head is collapsed and limited",
			src:  "package p\nfunc Long(\n\t" + longParams + " int,\n) {\n}\n",
			want: []domain.Symbol{
				sym("Long", "function", 2, 5, true, "func Long( "+strings.Repeat("a", 108)+"…"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, "svc/a.go", tc.src)
			if diff := cmp.Diff(tc.want, got.Symbols, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Extract(a.go) symbols mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractGo_References(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []domain.Reference
	}{
		{
			name: "calls, function-value arguments and composite literals",
			src: "package p\n" +
				"\n" +
				"func f() {\n" +
				"\tsvc.Do(x)\n" +
				"\trun()\n" +
				"\te.GET(\"/x\", h.GetX)\n" +
				"\tmux.HandleFunc(\"/y\", handleY)\n" +
				"\t_ = pkg.Config{}\n" +
				"\t_ = T{}\n" +
				"\t_ = List[int]{}\n" +
				"\tuse(nil, true, &v, \"s\")\n" +
				"}\n",
			want: []domain.Reference{
				ref("Do", 4), ref("x", 4),
				ref("run", 5),
				ref("GET", 6), ref("GetX", 6),
				ref("HandleFunc", 7), ref("handleY", 7),
				ref("Config", 8),
				ref("T", 9),
				ref("List", 10),
				ref("use", 11),
			},
		},
		{
			name: "same-line declaration and duplicates are dropped",
			src:  "package p\nfunc g() { g(); h(); h() }\n",
			want: []domain.Reference{ref("h", 2)},
		},
		{
			name: "a call's name and argument names come before its nested calls",
			src:  "package p\nfunc f() {\n\ta(b(c), d.e)\n}\n",
			want: []domain.Reference{ref("a", 3), ref("e", 3), ref("b", 3), ref("c", 3)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extract(t, "p/a.go", tc.src)
			if diff := cmp.Diff(tc.want, got.References, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Extract(a.go) references mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestImportsGo(t *testing.T) {
	tests := []struct {
		name string
		file string
		src  string
		want domain.FileImports
	}{
		{
			name: "aliased, dot and blank imports",
			file: "internal/api/h.go",
			src: "package api\n" +
				"\n" +
				"import (\n" +
				"\t\"fmt\"\n" +
				"\tx \"example.com/r/internal/svc\"\n" +
				"\t. \"example.com/r/dot\"\n" +
				"\t_ \"example.com/r/side\"\n" +
				")\n",
			want: domain.FileImports{
				Package: "go:internal/api",
				Specs:   []string{"fmt", "example.com/r/internal/svc", "example.com/r/dot", "example.com/r/side"},
			},
		},
		{
			name: "root file, raw string, duplicates and several declarations",
			file: "main.go",
			src:  "package main\nimport `os`\nimport \"os\"\nimport (\n\t\"strings\"\n)\n",
			want: domain.FileImports{Package: "go:.", Specs: []string{"os", "strings"}},
		},
		{
			name: "no imports",
			file: "pkg/a.go",
			src:  "package pkg\n",
			want: domain.FileImports{Package: "go:pkg"},
		},
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

func TestImports_TypeScriptUnchanged(t *testing.T) {
	got, err := treesitter.New().Imports(t.Context(), "src/a.ts", "import a from './a';\na();\n")
	if err != nil {
		t.Fatalf("Imports(src/a.ts) error = %v", err)
	}
	want := domain.FileImports{Package: "", Specs: []string{"./a"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Imports(src/a.ts) mismatch (-want +got):\n%s", diff)
	}
}

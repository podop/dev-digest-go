package integration_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePrefix = "dev-digest-go/backend/"

// importsOf returns the import paths of the non-test Go files of dir.
func importsOf(t *testing.T, dir string) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range parsed.Imports {
			out[f] = append(out[f], strings.Trim(imp.Path.Value, `"`))
		}
	}
	return out
}

// isNetwork reports a standard-library networking package: net and everything below it, except
// net/netip, which only holds address values (platform/codehost parses host keys with it) and
// opens no socket.
func isNetwork(p string) bool {
	return p != "net/netip" && (p == "net" || strings.HasPrefix(p, "net/"))
}

// AC3067 (imports): nothing the indexer is built from starts a process or opens a socket. The
// check reads the import declarations of every non-test file under internal/repointel, and of
// the internal packages those files import, transitively. The only package allowed a network
// import is the HTTP edge (transport/http), and only net/http: it serves the routes and takes
// no part in an index.
func TestPipelineImportsNoNetworkAndNoProcess(t *testing.T) {
	backend := filepath.Join("..", "..", "..")
	repointel := ".."
	httpEdge := filepath.Join(repointel, "transport", "http")

	var dirs []string
	err := filepath.WalkDir(repointel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	checked := map[string]bool{}
	var visit func(dir string, from string)
	visit = func(dir, from string) {
		if checked[dir] {
			return
		}
		checked[dir] = true
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s imports %s, which is not a directory of the module: %v", from, dir, err)
			return
		}
		for file, imports := range importsOf(t, dir) {
			for _, imp := range imports {
				switch {
				case imp == "os/exec":
					t.Errorf("%s imports os/exec", file)
				case isNetwork(imp) && !(dir == httpEdge && imp == "net/http"):
					t.Errorf("%s imports %s", file, imp)
				case strings.HasPrefix(imp, modulePrefix) && dir != httpEdge:
					visit(filepath.Join(backend, strings.TrimPrefix(imp, modulePrefix)), file)
				}
			}
		}
	}
	for _, dir := range dirs {
		visit(dir, "internal/repointel")
	}

	// Guard against a vacuous pass: the walk must have reached the layers of the pipeline.
	for _, want := range []string{
		path.Join("..", "app"), path.Join("..", "adapters", "treesitter"), path.Join("..", "adapters", "clonetree"),
		path.Join("..", "adapters", "postgres"), path.Join("..", "domain", "importgraph"),
		filepath.Join(backend, "internal", "platform", "clonefs"), filepath.Join(backend, "internal", "platform", "postgres"),
	} {
		if !checked[filepath.FromSlash(want)] {
			t.Errorf("import check never reached %s", want)
		}
	}
	if !slices.Contains(dirs, httpEdge) {
		t.Errorf("import check never saw %s", httpEdge)
	}
}

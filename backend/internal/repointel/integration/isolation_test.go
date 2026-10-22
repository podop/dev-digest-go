package integration_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/repointel/adapters/clonetree"
	"dev-digest-go/backend/internal/repointel/domain"
)

// AC3067 (behaviour): a hostile clone is only read, never run and never followed out of its
// root. It has a package.json postinstall script, a tsconfig that extends a file outside the
// clone, and a symlink named like a source file that points at a file outside the clone.
func TestHostileClone_IsIndexedWithoutLeavingTheRoot(t *testing.T) {
	var reads *readLog
	e := newEnv(t, withFiles(map[string]string{
		"src/uses.ts":       "import { thing } from '@evil/thing';\n\nthing();\n",
		"src/evil/thing.ts": "export function thing(): void {}\n",
		"src/plain.ts":      "export function plain(): void {}\n",
		"tsconfig.json":     `{"extends": "../../etc/x.json"}`,
	}), withFS(func(inner domain.SourceFS) domain.SourceFS {
		reads = newReadLog(inner)
		return reads
	}))
	outside := filepath.Dir(filepath.Dir(e.dir)) // the temp dir that holds the clone's parent
	marker := filepath.Join(outside, "postinstall-ran")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(e.dir, "package.json"),
		fmt.Sprintf(`{"name":"hostile","scripts":{"postinstall":"touch %s"}}`, marker))
	// If this file were read, @evil/* would resolve to the clone's src/evil.
	write(filepath.Join(outside, "etc", "x.json"),
		`{"compilerOptions":{"baseUrl":"../x/y","paths":{"@evil/*":["src/evil/*"]}}}`)
	write(filepath.Join(outside, "secret.ts"), "export function STOLEN_SECRET(): void {}\n")
	if err := os.Symlink(filepath.Join(outside, "secret.ts"), filepath.Join(e.dir, "src", "p.ts")); err != nil {
		t.Fatal(err)
	}

	e.mustFull()

	if st := e.mustState(); st.Status != domain.StatusFull {
		t.Errorf("state status = %s, want full: the hostile files must not fail the index", st.Status)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("postinstall marker: Stat error = %v, want not-exist: no script may run", err)
	}
	for _, name := range e.symbolNames() {
		if strings.Contains(name, "STOLEN_SECRET") || strings.HasPrefix(name, "src/p.ts:") {
			t.Errorf("symbols hold %q, want nothing read through the symlink", name)
		}
	}
	for _, edge := range e.paths("file_edges", "from_file || ' ' || to_file") {
		if strings.Contains(edge, "src/evil/thing.ts") {
			t.Errorf("file_edges hold %q: the alias of a tsconfig outside the clone was used", edge)
		}
	}
	for _, a := range reads.attempts() {
		outsideRoot := filepath.IsAbs(a.rel) || a.rel == ".." || strings.HasPrefix(a.rel, "../")
		if (outsideRoot || a.rel == "src/p.ts") && a.err == nil {
			t.Errorf("ReadFile(%q) succeeded, want a refusal", a.rel)
		}
	}

	// The reader itself, asked directly.
	for _, rel := range []string{"src/p.ts", "../../etc/x.json", filepath.Join(outside, "secret.ts")} {
		if _, err := clonetree.New().ReadFile(e.dir, rel); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%q) error = %v, want a not-exist refusal", rel, err)
		}
	}
}

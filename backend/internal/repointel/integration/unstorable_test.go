package integration_test

import (
	"slices"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/repointel/domain"
)

// L1: one NUL byte inside a file must not fail the index; Postgres text and jsonb refuse U+0000.
func TestFull_NULInContentStillIndexes(t *testing.T) {
	files := baseFiles()
	files["src/nul.ts"] = "export function nul(x = \"a\x00b\"): void {\n  app.get('/p\x00q', () => 1);\n}\n"
	e := newEnv(t, withFiles(files))

	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull {
		t.Fatalf("state status = %s (stats %v), want full", st.Status, st.Stats)
	}
	sigs := e.scanStrings(`SELECT signature FROM symbols WHERE repo_id = ? AND path = 'src/nul.ts'`, e.repo)
	if len(sigs) == 0 {
		t.Fatal("no symbols stored for src/nul.ts, want the file indexed")
	}
	for _, s := range sigs {
		if strings.ContainsRune(s, 0) {
			t.Errorf("stored signature %q holds U+0000, want U+FFFD", s)
		}
	}
	endpoints := e.scanStrings(`SELECT endpoints::text FROM file_facts WHERE repo_id = ? AND file_path = 'src/nul.ts'`, e.repo)
	if len(endpoints) != 1 || !strings.Contains(endpoints[0], "GET /p�q") {
		t.Errorf("file_facts.endpoints of src/nul.ts = %q, want one array holding \"GET /p\\uFFFDq\"", endpoints)
	}
}

// L1: a file name that is not valid UTF-8 is skipped (counted) and the rest still indexes.
func TestFull_InvalidUTF8FileNameIsSkipped(t *testing.T) {
	files := baseFiles()
	files["src/bad\xff.ts"] = "export function hidden(): number {\n  return 1;\n}\n"
	e := newEnv(t, withFiles(files))
	clean := newEnv(t)
	clean.mustFull()

	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull {
		t.Fatalf("state status = %s (stats %v), want full", st.Status, st.Stats)
	}
	if got := statInt(t, st, "filesSeen"); got != 8 {
		t.Errorf("filesSeen = %d, want 8 (the invalid name is not indexed)", got)
	}
	if st.FilesSkipped != 1 {
		t.Errorf("files_skipped = %d, want 1 (the invalid name)", st.FilesSkipped)
	}
	got, want := e.symbolNames(), clean.symbolNames()
	if !slices.Equal(got, want) {
		t.Errorf("symbol names = %v, want %v (as without the invalid name)", got, want)
	}
	if slices.Contains(got, "hidden") {
		t.Error("symbol hidden was indexed from the invalid file name")
	}
}

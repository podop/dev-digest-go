package integration_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

// AC3027: a clean fixture clone is written to all seven tables in one index, and the state's
// stats hold the documented counters equal to the inserted rows.
func TestFull_WritesAllSevenTables(t *testing.T) {
	e := newEnv(t)
	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull || st.IndexerVersion != domain.IndexerVersion || st.LastIndexedSHA != e.git.currentHead() {
		t.Errorf("state = {%s v%d %q}, want {full v2 %q}", st.Status, st.IndexerVersion, st.LastIndexedSHA, e.git.currentHead())
	}
	wantKeys := []string{
		"bounded", "durationMs", "edgesWritten", "factsWritten", "filesSeen", "hotnessAvailable", "parseDegraded",
		"ranked", "referencesWritten", "skippedTooLarge", "softBudgetReached", "symbolsWritten", "totalCandidates",
	}
	var gotKeys []string
	for k := range st.Stats {
		gotKeys = append(gotKeys, k)
	}
	slices.Sort(gotKeys)
	if diff := cmp.Diff(wantKeys, gotKeys); diff != "" {
		t.Errorf("stats keys mismatch (-want +got):\n%s", diff)
	}
	if st.Stats["hotnessAvailable"] != false || st.Stats["softBudgetReached"] != false {
		t.Errorf("hotnessAvailable, softBudgetReached = %v, %v, want false, false", st.Stats["hotnessAvailable"], st.Stats["softBudgetReached"])
	}
	if degraded, ok := st.Stats["parseDegraded"].([]any); !ok || len(degraded) != 0 {
		t.Errorf("stats.parseDegraded = %#v, want an empty array", st.Stats["parseDegraded"])
	}
	counts := []struct {
		stat, table string
	}{
		{"symbolsWritten", "symbols"}, {"referencesWritten", `"references"`}, {"edgesWritten", "file_edges"},
		{"ranked", "file_rank"}, {"factsWritten", "file_facts"},
	}
	for _, c := range counts {
		if got, want := statInt(t, st, c.stat), e.count(c.table); got != want || want == 0 {
			t.Errorf("stats.%s = %d, rows in %s = %d, want equal and non-zero", c.stat, got, c.table, want)
		}
	}
	// 8 walked files: the fixture's .md and .py files are not source.
	if got := statInt(t, st, "filesSeen"); got != 8 || st.FilesIndexed != 8 {
		t.Errorf("filesSeen, files_indexed = %d, %d, want 8, 8", got, st.FilesIndexed)
	}
	if got := e.count("repo_map_cache"); got != 1 {
		t.Errorf("repo_map_cache rows = %d, want 1", got)
	}
	// AC3059: the endpoints column holds a JSON array.
	endpoints := e.scanStrings(`SELECT endpoints::text FROM file_facts WHERE repo_id = ? AND file_path = 'src/server.ts'`, e.repo)
	if diff := cmp.Diff([]string{`["GET /x"]`}, endpoints); diff != "" {
		t.Errorf("file_facts.endpoints of src/server.ts mismatch (-want +got):\n%s", diff)
	}
}

// AC3020: the flag only gates the read port; an index job still writes the state and the rows,
// and the state route still answers.
func TestIndexJob_WritesWithTheFlagOff(t *testing.T) {
	e := newEnv(t, withFlagOff())
	if err := e.jobs.Index(t.Context(), e.payload()); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if st := e.mustState(); st.Status != domain.StatusFull {
		t.Errorf("state status = %s, want full", st.Status)
	}
	if e.count("symbols") == 0 {
		t.Error("symbols rows = 0, want rows written with REPO_INTEL_ENABLED=false")
	}
	if view := e.service.IndexState(t.Context(), e.repo); view.Status != domain.StatusFull {
		t.Errorf("IndexState().Status = %s, want full", view.Status)
	}
	files, err := e.reader.ListIndexedFiles(t.Context(), e.repo)
	if err != nil || len(files) != 0 {
		t.Errorf("ListIndexedFiles() = %v, %v, want an empty result from the disabled port", files, err)
	}
}

// AC3022: a repo without a clone path is degraded with no_clone; a deleted repo gets nothing.
func TestFull_NoCloneAndDeletedRepo(t *testing.T) {
	t.Run("no clone path", func(t *testing.T) {
		e := newEnv(t, withoutClone())
		res := e.mustFull()
		if res.Reason != "no_clone" {
			t.Errorf("Result.Reason = %q, want no_clone", res.Reason)
		}
		st := e.mustState()
		if st.Status != domain.StatusDegraded || st.LastIndexedSHA != "" || st.FilesIndexed != 0 || st.FilesSkipped != 0 {
			t.Errorf("state = {%s %q %d %d}, want {degraded \"\" 0 0}", st.Status, st.LastIndexedSHA, st.FilesIndexed, st.FilesSkipped)
		}
		if st.Stats["reason"] != "no_clone" || st.Stats["degradedReason"] != "no_data" {
			t.Errorf("stats reason, degradedReason = %v, %v, want no_clone, no_data", st.Stats["reason"], st.Stats["degradedReason"])
		}
	})
	t.Run("deleted repo", func(t *testing.T) {
		e := newEnv(t)
		e.exec(`DELETE FROM repos WHERE id = ?`, e.repo)
		if _, err := e.indexer.Full(t.Context(), e.repo); err != nil {
			t.Fatalf("Full(deleted repo) error = %v, want nil", err)
		}
		if _, ok := e.state(); ok {
			t.Error("a repo_index_state row exists for a deleted repo, want none")
		}
	})
}

// AC3023: a clone without source files is partial with no_files and keeps the earlier rows.
func TestFull_NoSourceFilesKeepsEarlierSymbols(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	before := e.dump("symbols")
	if len(before) == 0 {
		t.Fatal("no symbols after the first index")
	}

	var del []string
	for p := range baseFiles() {
		if domain.IsSupported(p) {
			del = append(del, p)
		}
	}
	e.git.Commit(map[string]string{"notes.md": "# notes\n", "script.py": "x = 1\n"}, del...)
	res := e.mustFull()

	if res.Reason != "no_files" {
		t.Errorf("Result.Reason = %q, want no_files", res.Reason)
	}
	st := e.mustState()
	if st.Status != domain.StatusPartial || st.Stats["reason"] != "no_files" {
		t.Errorf("state = {%s %v}, want {partial no_files}", st.Status, st.Stats["reason"])
	}
	if diff := cmp.Diff(before, e.dump("symbols")); diff != "" {
		t.Errorf("symbols changed by an index without source files (-want +got):\n%s", diff)
	}
}

// AC3025: of 5,003 supported files exactly the first 5,000 in byte order are ranked.
func TestFull_BoundsTheWalkAt5000Files(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs Docker (skipped with -short)")
	}
	files, paths := manyFiles(5003)
	e := newEnv(t, withFiles(files))
	e.mustFull()

	st := e.mustState()
	if got := statInt(t, st, "bounded"); got != 3 {
		t.Errorf("stats.bounded = %d, want 3", got)
	}
	if got := statInt(t, st, "totalCandidates"); got != 5003 {
		t.Errorf("stats.totalCandidates = %d, want 5003", got)
	}
	if diff := cmp.Diff(paths[:5000], e.paths("file_rank", "file_path")); diff != "" {
		t.Errorf("file_rank paths mismatch (-want +got):\n%s", diff)
	}
}

// AC3057: 50,000 references are inserted in chunks, every statement within the bind parameter
// limit of the protocol.
func TestFull_ChunksFiftyThousandReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs Docker (skipped with -short)")
	}
	// Many small files: under -race the workers' parses do not run in parallel, so a few large
	// files would run into the 2,000 ms parse limit.
	body := strings.Repeat("f();\n", 1_000)
	files := map[string]string{}
	for i := range 52 {
		files[fmt.Sprintf("src/m%02d.ts", i)] = body
	}
	e := newEnv(t, withFiles(files))
	e.mustFull()

	st := e.mustState()
	if st.Status != domain.StatusFull {
		t.Fatalf("state status = %s, want full", st.Status)
	}
	if got := e.count(`"references"`); got < 50_000 || got != statInt(t, st, "referencesWritten") {
		t.Errorf("references rows = %d (stats %d), want >= 50000 and equal", got, statInt(t, st, "referencesWritten"))
	}
	var refInserts int
	for _, s := range e.stmts.all() {
		if !strings.HasPrefix(s.sql, "INSERT") {
			continue
		}
		if s.params > 65_535 {
			t.Errorf("an INSERT carries %d parameters, want <= 65535: %.80s", s.params, s.sql)
		}
		if strings.Contains(s.sql, `"references"`) {
			refInserts++
		}
	}
	if want := 50_000 / domain.InsertChunkRows; refInserts < want {
		t.Errorf("INSERT statements into references = %d, want >= %d (chunks of %d rows)", refInserts, want, domain.InsertChunkRows)
	}
}

// AC3058: decl_file is set only where exactly one imported file exports the name.
func TestFull_ResolvesDeclFile(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{
		"a.ts": "import { x, y } from './b';\nimport { x as other } from './c';\n\nconst alias = other;\nx();\ny();\nz();\n",
		"b.ts": "export function x(): void {}\nexport function y(): void {}\n",
		"c.ts": "export function x(): void {}\n",
	}))
	e.mustFull()

	rows := e.scanStrings(`SELECT to_symbol || '=' || coalesce(decl_file, 'NULL') FROM "references" WHERE repo_id = ? AND from_path = 'a.ts'`, e.repo)
	slices.Sort(rows)
	want := []string{"x=NULL", "y=b.ts", "z=NULL"}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("decl_file of the references of a.ts mismatch (-want +got):\n%s", diff)
	}
}

// AC3060: the same commit indexed twice gives identical rows and a byte-identical repo map.
func TestFull_SameCommitTwiceIsIdentical(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	first := e.content()
	e.mustFull()
	if diff := cmp.Diff(first, e.content()); diff != "" {
		t.Errorf("rows of the second index differ from the first (-first +second):\n%s", diff)
	}
	if len(first["repo_map_cache"]) != 1 {
		t.Errorf("repo_map_cache rows = %d, want 1", len(first["repo_map_cache"]))
	}
}

// AC3055: equal-rank files with the same signature sort by path in the repo map, every time.
func TestFull_RepoMapOrdersEqualRanksByPath(t *testing.T) {
	e := newEnv(t, withFiles(map[string]string{
		"b/main.ts": "export function main(): void {}\n",
		"a/main.ts": "export function main(): void {}\n",
	}))
	want := mapHeader + "a/main.ts:\n  function main(): void\nb/main.ts:\n  function main(): void\n"
	for i := range 2 {
		e.mustFull()
		got := e.scanStrings(`SELECT map_text FROM repo_map_cache WHERE repo_id = ?`, e.repo)
		if diff := cmp.Diff([]string{want}, got); diff != "" {
			t.Errorf("repo map after index %d mismatch (-want +got):\n%s", i+1, diff)
		}
	}
}

// mapHeader is the first line of every repo map (spec section 7.7).
const mapHeader = "# Repo skeleton (top-ranked by import graph only, partial view)\n"

// AC3059: every write of the state row moves updated_at.
func TestState_EveryWriteMovesUpdatedAt(t *testing.T) {
	e := newEnv(t, withClock(stepClock(time.Millisecond)))
	steps := []struct {
		name string
		run  func()
	}{
		{"full index", func() { e.mustFull() }},
		{"sha unchanged", func() { e.mustIncremental() }},
		{"only unsupported files changed", func() {
			e.git.Commit(map[string]string{"README.md": "# changed\n"})
			e.mustIncremental()
		}},
		{"incremental slice", func() {
			e.git.Commit(map[string]string{"src/a.ts": baseFiles()["src/a.ts"] + "export const more = 1;\n"})
			e.mustIncremental()
		}},
		{"no source files", func() {
			var del []string
			for p := range baseFiles() {
				if domain.IsSupported(p) {
					del = append(del, p)
				}
			}
			e.git.Commit(nil, del...)
			e.mustFull()
		}},
	}
	var last time.Time
	for _, s := range steps {
		s.run()
		got := e.mustState().UpdatedAt
		if !got.After(last) {
			t.Errorf("updated_at after %q = %s, want after %s", s.name, got.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano))
		}
		last = got
	}
}

package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/repointel/domain"
)

// withMore returns a source file's text with one more exported function.
func withMore(path, fn string) string {
	return baseFiles()[path] + fmt.Sprintf("\nexport function %s(): number {\n  return 7;\n}\n", fn)
}

// isIncremental reports whether the state was written by an incremental slice.
func isIncremental(st domain.IndexState) bool {
	v, _ := st.Stats["incremental"].(bool)
	return v
}

// AC3019: a resync job brings the clone to the remote commit and indexes the one changed file.
func TestResync_AdvancesTheCloneAndIndexesTheDiff(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	a := e.git.currentHead()

	next := withMore("src/a.ts", "runA2")
	e.git.Stage(map[string]string{"src/a.ts": next})
	if err := e.jobs.Resync(t.Context(), e.payload()); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}

	b := e.git.currentHead()
	if b == a {
		t.Fatal("the fake remote commit was not applied, want the clone at a new commit")
	}
	if got, err := os.ReadFile(filepath.Join(e.dir, "src", "a.ts")); err != nil || string(got) != next {
		t.Errorf("clone src/a.ts = %q, %v, want the remote content", got, err)
	}
	st := e.mustState()
	if st.LastIndexedSHA != b || !isIncremental(st) || statInt(t, st, "changedFiles") != 1 {
		t.Errorf("state = {sha %q incremental %v changedFiles %v}, want {%q true 1}",
			st.LastIndexedSHA, st.Stats["incremental"], st.Stats["changedFiles"], b)
	}
	if !slices.Contains(e.symbolNames(), "src/a.ts:runA2") {
		t.Error("symbols lack src/a.ts:runA2, want the changed file re-indexed")
	}
}

// AC3031: a state of another indexer version, or none, makes the incremental index a full one.
func TestIncremental_OldVersionOrNoStateRunsFull(t *testing.T) {
	cases := []struct {
		name      string
		statement string
	}{
		{"version 1", `UPDATE repo_index_state SET indexer_version = 1 WHERE repo_id = ?`},
		{"no state row", `DELETE FROM repo_index_state WHERE repo_id = ?`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.mustFull()
			e.exec(tc.statement, e.repo)
			e.mustIncremental()

			st := e.mustState()
			if isIncremental(st) || st.IndexerVersion != 2 {
				t.Errorf("state = {incremental %v version %d}, want a full index at version 2", isIncremental(st), st.IndexerVersion)
			}
			if _, ok := st.Stats["filesSeen"]; !ok {
				t.Errorf("stats = %v, want filesSeen of a full index", st.Stats)
			}
		})
	}
}

// AC3032: at the indexed commit only updated_at moves; an unreadable HEAD changes nothing.
func TestIncremental_SameShaTouchesOnly(t *testing.T) {
	e := newEnv(t, withClock(stepClock(time.Millisecond)))
	e.mustFull()
	before, beforeState := e.content(), e.mustState()

	e.mustIncremental()
	if diff := cmp.Diff(before, e.content()); diff != "" {
		t.Errorf("rows changed at an unchanged commit, want only updated_at (-before +after):\n%s", diff)
	}
	touched := e.mustState()
	if !touched.UpdatedAt.After(beforeState.UpdatedAt) {
		t.Errorf("updated_at = %s, want after %s", touched.UpdatedAt, beforeState.UpdatedAt)
	}

	e.git.setHeadErr(fmt.Errorf("fatal: not a git repository"))
	exact := e.snapshot()
	e.mustIncremental()
	if diff := cmp.Diff(exact, e.snapshot()); diff != "" {
		t.Errorf("tables changed with HEAD unreadable (-before +after):\n%s", diff)
	}
}

// AC3033: a diff that fails (base commit missing) or overflows the git output cap falls back to
// the full index.
func TestIncremental_DiffFailureRunsFull(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env)
	}{
		{"base commit missing", func(e *env) {
			e.exec(`UPDATE repo_index_state SET last_indexed_sha = ? WHERE repo_id = ?`, strings.Repeat("ab", 20), e.repo)
		}},
		{"output over the cap", func(e *env) { e.git.setDiffErr(fmt.Errorf("diff: %w", gitcli.ErrOutputTooLarge)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.mustFull()
			head := e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
			tc.setup(e)
			e.mustIncremental()

			st := e.mustState()
			if isIncremental(st) || st.LastIndexedSHA != head {
				t.Errorf("state = {incremental %v sha %q}, want a full index at %q", isIncremental(st), st.LastIndexedSHA, head)
			}
			if !slices.Contains(e.symbolNames(), "src/a.ts:runA2") {
				t.Error("symbols lack src/a.ts:runA2 after the fallback index")
			}
		})
	}
}

// AC3034: more than 300 changed source files run the full index; changes to files that are not
// source move only the sha.
func TestIncremental_ChangedSetSize(t *testing.T) {
	t.Run("301 source files", func(t *testing.T) {
		e := newEnv(t)
		e.mustFull()
		many, _ := manyFiles(301)
		e.git.Commit(many)
		e.mustIncremental()
		st := e.mustState()
		if isIncremental(st) {
			t.Error("301 changed source files ran an incremental index, want the full index")
		}
		if got := statInt(t, st, "filesSeen"); got != 8+301 {
			t.Errorf("stats.filesSeen = %d, want %d", got, 8+301)
		}
	})
	t.Run("no source file", func(t *testing.T) {
		e := newEnv(t, withClock(stepClock(time.Millisecond)))
		e.mustFull()
		before, stateBefore := e.content(), e.mustState()
		head := e.git.Commit(map[string]string{"README.md": "# changed\n", "tools/build.py": "print('changed')\n"})
		e.mustIncremental()

		after, stateAfter := e.content(), e.mustState()
		delete(before, "repo_index_state")
		delete(after, "repo_index_state")
		if diff := cmp.Diff(before, after); diff != "" {
			t.Errorf("rows changed for a commit without source files (-before +after):\n%s", diff)
		}
		want := stateBefore
		want.LastIndexedSHA, want.UpdatedAt = head, stateAfter.UpdatedAt
		if diff := cmp.Diff(want, stateAfter); diff != "" {
			t.Errorf("state mismatch, want only last_indexed_sha and updated_at changed (-want +got):\n%s", diff)
		}
		if !stateAfter.UpdatedAt.After(stateBefore.UpdatedAt) {
			t.Errorf("updated_at = %s, want after %s", stateAfter.UpdatedAt, stateBefore.UpdatedAt)
		}
	})
}

// AC3035: an edit, a delete, an excluded directory and an oversized file in one slice.
func TestIncremental_EditDeleteExcludedAndOversized(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	skippedBefore := e.mustState().FilesSkipped

	big := "// " + strings.Repeat("x", domain.MaxFileBytes+1-len("// ")-1) + "\n"
	if len(big) != domain.MaxFileBytes+1 {
		t.Fatalf("oversized fixture is %d bytes, want %d", len(big), domain.MaxFileBytes+1)
	}
	e.git.Commit(map[string]string{
		"src/a.ts":   withMore("src/a.ts", "runA2"),
		"dist/c.js":  "export function dist() {}\n",
		"src/big.ts": big,
	}, "src/b.ts")
	e.mustIncremental()

	st := e.mustState()
	if st.Status != domain.StatusFull || !isIncremental(st) {
		t.Errorf("state = {%s incremental %v}, want {full true}", st.Status, isIncremental(st))
	}
	if got := st.FilesSkipped - skippedBefore; got != 3 {
		t.Errorf("files_skipped grew by %d, want 3", got)
	}
	if degraded, _ := st.Stats["parseDegraded"].([]any); len(degraded) != 0 {
		t.Errorf("stats.parseDegraded = %v, want empty", st.Stats["parseDegraded"])
	}
	names := e.symbolNames()
	if !slices.Contains(names, "src/a.ts:runA2") || !slices.Contains(names, "src/a.ts:runA") {
		t.Errorf("symbols of src/a.ts = %v, want runA and runA2", names)
	}
	for _, p := range []string{"src/b.ts", "dist/c.js", "src/big.ts"} {
		for _, table := range []struct{ name, column string }{
			{"symbols", "path"}, {`"references"`, "from_path"}, {"file_rank", "file_path"}, {"file_facts", "file_path"},
		} {
			if slices.Contains(e.paths(table.name, table.column), p) {
				t.Errorf("%s has rows for %s, want none", table.name, p)
			}
		}
	}
	for _, edge := range e.paths("file_edges", "from_file || ' ' || to_file") {
		if strings.Contains(edge, "src/b.ts") {
			t.Errorf("file_edges holds %q after src/b.ts was deleted", edge)
		}
	}
}

// AC3036: when the rank step fails inside the savepoint, the slice's rows are still committed.
func TestIncremental_SavepointFailureKeepsTheSlice(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	ranks, edges := e.dump("file_rank"), e.dump("file_edges")
	e.failInserts("file_rank", "rank write refused")

	e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
	e.mustIncremental()

	st := e.mustState()
	if st.Status != domain.StatusPartial {
		t.Errorf("status = %s, want partial", st.Status)
	}
	if msg, _ := st.Stats["graphFailed"].(string); !strings.Contains(msg, "rank write refused") {
		t.Errorf("stats.graphFailed = %q, want the refused write", msg)
	}
	if !slices.Contains(e.symbolNames(), "src/a.ts:runA2") {
		t.Error("the slice's symbols were rolled back with the savepoint, want them committed")
	}
	if diff := cmp.Diff(ranks, e.dump("file_rank")); diff != "" {
		t.Errorf("file_rank changed (-before +after):\n%s", diff)
	}
	if diff := cmp.Diff(edges, e.dump("file_edges")); diff != "" {
		t.Errorf("file_edges changed with the savepoint rolled back (-before +after):\n%s", diff)
	}
}

// AC3037: a partial state stays partial after a clean slice; a failed read makes a full state
// partial and names the file.
func TestIncremental_StatusTransitions(t *testing.T) {
	t.Run("partial stays partial", func(t *testing.T) {
		e := newEnv(t)
		e.mustFull()
		e.exec(`UPDATE repo_index_state SET status = 'partial' WHERE repo_id = ?`, e.repo)
		e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
		e.mustIncremental()
		if st := e.mustState(); st.Status != domain.StatusPartial {
			t.Errorf("status = %s, want partial", st.Status)
		}
	})
	t.Run("unreadable changed file", func(t *testing.T) {
		var reads *readLog
		e := newEnv(t, withFS(func(inner domain.SourceFS) domain.SourceFS {
			reads = newReadLog(inner)
			return reads
		}))
		e.mustFull()
		if st := e.mustState(); st.Status != domain.StatusFull {
			t.Fatalf("status after the full index = %s, want full", st.Status)
		}
		reads.failRead("src/a.ts")
		e.git.Commit(map[string]string{"src/a.ts": withMore("src/a.ts", "runA2")})
		e.mustIncremental()

		st := e.mustState()
		if st.Status != domain.StatusPartial {
			t.Errorf("status = %s, want partial", st.Status)
		}
		degraded, _ := st.Stats["parseDegraded"].([]any)
		if len(degraded) != 1 {
			t.Fatalf("stats.parseDegraded = %v, want one entry", st.Stats["parseDegraded"])
		}
		if entry, _ := degraded[0].(map[string]any); entry["file"] != "src/a.ts" {
			t.Errorf("parseDegraded[0] = %v, want file src/a.ts", degraded[0])
		}
	})
}

// AC3038: after a clean slice edges, ranks and the map describe the whole tree at HEAD, decl_file
// was recomputed from scratch, and one map is cached.
func TestIncremental_CleanSliceMatchesAFullIndex(t *testing.T) {
	e := newEnv(t)
	e.mustFull()
	declBefore := e.scanStrings(`SELECT coalesce(decl_file, 'NULL') FROM "references" WHERE repo_id = ? AND from_path = 'src/a.ts' AND to_symbol = 'core'`, e.repo)
	if diff := cmp.Diff([]string{"src/core.ts"}, declBefore); diff != "" {
		t.Fatalf("decl_file of core() in src/a.ts before the slice (-want +got):\n%s", diff)
	}

	// core.ts stops exporting `core`: the reference in the unchanged src/a.ts must lose its
	// decl_file, which only a reset before the resolution does.
	head := e.git.Commit(map[string]string{"src/core.ts": "export function core2(): number {\n  return 1;\n}\n\nexport class Store {}\n"})
	e.mustIncremental()
	st := e.mustState()
	if !isIncremental(st) || st.Status != domain.StatusFull {
		t.Fatalf("state = {%s incremental %v}, want a clean incremental slice", st.Status, isIncremental(st))
	}
	declAfter := e.scanStrings(`SELECT coalesce(decl_file, 'NULL') FROM "references" WHERE repo_id = ? AND from_path = 'src/a.ts' AND to_symbol = 'core'`, e.repo)
	if diff := cmp.Diff([]string{"NULL"}, declAfter); diff != "" {
		t.Errorf("decl_file of core() in src/a.ts after the slice (-want +got):\n%s", diff)
	}
	maps := e.scanStrings(`SELECT commit_sha || ' ' || token_budget FROM repo_map_cache WHERE repo_id = ?`, e.repo)
	if diff := cmp.Diff([]string{head + " 1500"}, maps); diff != "" {
		t.Errorf("repo_map_cache rows (-want +got):\n%s", diff)
	}

	slice := e.content()
	e.mustFull()
	full := e.content()
	for _, table := range []string{"file_edges", "file_rank", "repo_map_cache", "symbols", `"references"`, "file_facts"} {
		if diff := cmp.Diff(full[table], slice[table]); diff != "" {
			t.Errorf("%s after the slice differs from a full index of the same commit (-full +slice):\n%s", table, diff)
		}
	}
}

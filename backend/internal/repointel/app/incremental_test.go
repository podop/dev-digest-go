package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/repointel/domain"
)

// incrementalEnv is an env with a previous index at "old-sha" of the given status, a clone of
// src/a.ts and src/c.ts, and HEAD "head-sha".
func incrementalEnv(t *testing.T, status domain.Status) *env {
	t.Helper()
	e := newEnv(t)
	e.addFile("src/a.ts", "new a", fn("a", 1))
	e.addFile("src/c.ts", "c", fn("c", 1))
	e.seedIndex(status)
	e.graph.edges = []domain.Edge{{From: "src/c.ts", To: "src/a.ts"}}
	return e
}

func TestIncremental_FullIndexFallbacks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *env)
	}{
		{name: "no state row", setup: func(e *env) { delete(e.store.states, repoID) }},
		{name: "state of another indexer version", setup: func(e *env) {
			st := e.store.states[repoID]
			st.IndexerVersion = 1
			e.store.states[repoID] = st
		}},
		{name: "diff fails, base missing from the clone", setup: func(e *env) { e.git.diffErr = errors.New("bad object old-sha") }},
		{name: "diff output over the git cap", setup: func(e *env) { e.git.diffErr = fmt.Errorf("diff: %w", gitcli.ErrOutputTooLarge) }},
		{name: "301 changed source files", setup: func(e *env) {
			for i := range 301 {
				e.git.diff = append(e.git.diff, fmt.Sprintf("src/gen%03d.ts", i))
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := incrementalEnv(t, domain.StatusFull)
			tc.setup(e)
			if _, err := e.ix.Incremental(t.Context(), repoID); err != nil {
				t.Fatalf("Incremental() error = %v", err)
			}
			st := e.state(t)
			if _, ok := st.Stats["filesSeen"]; !ok || st.IndexerVersion != domain.IndexerVersion || st.LastIndexedSHA != "head-sha" {
				t.Errorf("state = %+v, want the result of a full index (stats.filesSeen, version 2, head-sha)", st)
			}
			if _, ok := st.Stats["incremental"]; ok {
				t.Error("stats.incremental is set, want a full index")
			}
		})
	}
}

func TestIncremental_EndsWithoutWriting(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(e *env)
		wantReason string
	}{
		{name: "repo is gone", setup: func(e *env) { delete(e.store.repos, repoID) }, wantReason: "no_clone"},
		{name: "repo is not cloned", setup: func(e *env) {
			r := e.store.repos[repoID]
			r.ClonePath = ""
			e.store.repos[repoID] = r
		}, wantReason: "no_clone"},
		{name: "HEAD unreadable", setup: func(e *env) { e.git.headErr = errors.New("fatal: bad HEAD sk-secret") }, wantReason: "git_head_failed:fatal: bad HEAD [redacted]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := incrementalEnv(t, domain.StatusFull)
			tc.setup(e)
			before := e.store.snapshot()
			res, err := e.ix.Incremental(t.Context(), repoID)
			if err != nil {
				t.Fatalf("Incremental() error = %v", err)
			}
			if res.Reason != tc.wantReason || res.Status != domain.StatusDegraded {
				t.Errorf("Incremental() = %+v, want degraded / %q", res, tc.wantReason)
			}
			if diff := cmp.Diff(before, e.store.snapshot(), cmp.AllowUnexported(storeData{})); diff != "" {
				t.Errorf("store changed (-before +after):\n%s", diff)
			}
		})
	}
}

func TestIncremental_UnchangedHeadOnlyTouchesUpdatedAt(t *testing.T) {
	e := incrementalEnv(t, domain.StatusPartial)
	e.git.head = "old-sha"
	before := e.state(t)
	rows := e.store.snapshot()

	res, err := e.ix.Incremental(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	if res.Status != domain.StatusPartial || res.FilesIndexed != 5 || res.Reason != "sha_unchanged" {
		t.Errorf("Incremental() = %+v, want the stored partial state, 5 indexed, sha_unchanged", res)
	}
	after := e.state(t)
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at = %v, want later than %v", after.UpdatedAt, before.UpdatedAt)
	}
	after.UpdatedAt = before.UpdatedAt
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("state changed beyond updated_at (-before +after):\n%s", diff)
	}
	now := e.store.snapshot()
	now.states, rows.states = nil, nil
	if diff := cmp.Diff(rows, now, cmp.AllowUnexported(storeData{})); diff != "" {
		t.Errorf("tables changed (-before +after):\n%s", diff)
	}
}

func TestIncremental_OnlyUnsupportedChangesAdvanceTheSha(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.git.diff = []string{"README.md", "tools/a.py", "src/.ts"}
	before := e.state(t)
	rows := e.store.snapshot()

	if _, err := e.ix.Incremental(t.Context(), repoID); err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	if e.git.diffBase != "old-sha" {
		t.Errorf("diff base = %q, want old-sha", e.git.diffBase)
	}
	after := e.state(t)
	if after.LastIndexedSHA != "head-sha" || !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("state = %+v, want sha head-sha and a newer updated_at", after)
	}
	after.LastIndexedSHA, after.UpdatedAt = before.LastIndexedSHA, before.UpdatedAt
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("state changed beyond sha and updated_at (-before +after):\n%s", diff)
	}
	now := e.store.snapshot()
	now.states, rows.states = nil, nil
	if diff := cmp.Diff(rows, now, cmp.AllowUnexported(storeData{})); diff != "" {
		t.Errorf("tables changed (-before +after):\n%s", diff)
	}
}

func TestIncremental_SliceRewrite(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	// src/a.ts is edited, src/b.ts deleted, dist/c.js in an excluded directory, src/big.ts too
	// large: none of the last three is in the walk.
	e.git.diff = []string{"src/a.ts", "src/b.ts", "dist/c.js", "src/big.ts", "README.md"}

	res, err := e.ix.Incremental(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	if res.Status != domain.StatusFull || res.FilesIndexed != 1 || res.FilesSkipped != 3 {
		t.Errorf("Incremental() = %+v, want full, 1 indexed, 3 skipped", res)
	}
	st := e.state(t)
	if st.Status != domain.StatusFull || st.FilesIndexed != 6 || st.FilesSkipped != 5 || st.LastIndexedSHA != "head-sha" ||
		st.IndexerVersion != domain.IndexerVersion {
		t.Errorf("state = %+v, want full, 6 indexed, 5 skipped, head-sha", st)
	}
	if st.Stats["incremental"] != true || statInt(t, st, "changedFiles") != 4 {
		t.Errorf("stats = %v, want incremental with 4 changed files", st.Stats)
	}
	if got := degradedFiles(t, st); len(got) != 0 {
		t.Errorf("parseDegraded = %v, want none for removed files", got)
	}
	var paths []string
	for _, s := range e.store.data.symbols {
		paths = append(paths, s.Path+":"+s.Name)
	}
	if diff := cmp.Diff([]string{"src/a.ts:a"}, paths); diff != "" {
		t.Errorf("symbols mismatch (-want +got):\n%s", diff)
	}
	d := e.store.data
	if len(d.refs) != 0 {
		t.Errorf("references = %v, want the deleted file's reference gone", d.refs)
	}
	if diff := cmp.Diff([]domain.Edge{{From: "src/c.ts", To: "src/a.ts"}}, d.edges); diff != "" {
		t.Errorf("edges mismatch (-want +got):\n%s", diff)
	}
	if len(d.ranks) != 2 {
		t.Errorf("file_rank rows = %d, want one per walked file (2)", len(d.ranks))
	}
	if diff := cmp.Diff([]bool{true}, d.resolved); diff != "" {
		t.Errorf("ResolveReferences reset flags mismatch (-want +got):\n%s", diff)
	}
	if len(d.maps) != 1 || d.maps[0].CommitSHA != "head-sha" || d.maps[0].Budget != domain.DefaultTokenBudget {
		t.Errorf("repo maps = %+v, want exactly one for (head-sha, 1500)", d.maps)
	}
}

func TestIncremental_FactsAreReplacedForTheChangedFilesOnly(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.addFile("src/a.ts", "router.post('/new', h)", fn("a", 1))
	e.store.data.facts = []domain.FileFacts{
		{Path: "src/a.ts", Endpoints: []string{"GET /old"}},
		{Path: "src/c.ts", Crons: []string{"0 * * * *"}},
	}
	e.git.diff = []string{"src/a.ts"}
	if _, err := e.ix.Incremental(t.Context(), repoID); err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	want := []domain.FileFacts{
		{Path: "src/c.ts", Crons: []string{"0 * * * *"}},
		{Path: "src/a.ts", Endpoints: []string{"POST /new"}, Crons: []string{}},
	}
	if diff := cmp.Diff(want, e.store.data.facts); diff != "" {
		t.Errorf("file_facts mismatch (-want +got):\n%s", diff)
	}
}

func TestIncremental_StatusRules(t *testing.T) {
	tests := []struct {
		name       string
		prev       domain.Status
		unreadable bool
		want       domain.Status
	}{
		{name: "clean slice keeps full", prev: domain.StatusFull, want: domain.StatusFull},
		{name: "clean slice keeps partial", prev: domain.StatusPartial, want: domain.StatusPartial},
		{name: "unreadable changed file degrades full", prev: domain.StatusFull, unreadable: true, want: domain.StatusPartial},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := incrementalEnv(t, tc.prev)
			e.git.diff = []string{"src/a.ts"}
			e.clone.unreadable["src/a.ts"] = tc.unreadable
			if _, err := e.ix.Incremental(t.Context(), repoID); err != nil {
				t.Fatalf("Incremental() error = %v", err)
			}
			st := e.state(t)
			if st.Status != tc.want {
				t.Errorf("status = %s, want %s", st.Status, tc.want)
			}
			if got := degradedFiles(t, st); tc.unreadable != (len(got) == 1) {
				t.Errorf("parseDegraded = %v, want an entry iff the file is unreadable", got)
			}
		})
	}
}

func TestIncremental_SavepointFailureKeepsTheSliceAndMarksPartial(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.git.diff = []string{"src/a.ts"}
	e.store.hooks["ReplaceFileRank"] = func(context.Context) error { return errors.New("rank exploded, sk-secret") }
	prev := e.store.snapshot()

	res, err := e.ix.Incremental(t.Context(), repoID)
	if err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	st := e.state(t)
	if res.Status != domain.StatusPartial || st.Status != domain.StatusPartial {
		t.Errorf("Incremental() = %+v, state %+v, want partial", res, st)
	}
	if got := statStr(st, "graphFailed"); got != "replace file rank: rank exploded, [redacted]" {
		t.Errorf("stats[graphFailed] = %q", got)
	}
	d := e.store.data
	if len(d.symbols) != 2 || d.symbols[len(d.symbols)-1].Name != "a" {
		t.Errorf("symbols = %+v, want the slice committed", d.symbols)
	}
	if diff := cmp.Diff(prev.ranks, d.ranks); diff != "" {
		t.Errorf("file_rank changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(prev.edges, d.edges); diff != "" {
		t.Errorf("file_edges changed despite the savepoint rollback (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(prev.maps, d.maps); diff != "" {
		t.Errorf("repo maps changed (-want +got):\n%s", diff)
	}
}

func TestIncremental_GraphBuilderFailureSkipsTheSavepointStep(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.git.diff = []string{"src/a.ts"}
	e.graph.err = errors.New("graph broke")
	prev := e.store.snapshot()
	if _, err := e.ix.Incremental(t.Context(), repoID); err != nil {
		t.Fatalf("Incremental() error = %v", err)
	}
	st := e.state(t)
	if st.Status != domain.StatusPartial || statStr(st, "graphFailed") != "graph broke" {
		t.Errorf("state = %+v, want partial with graphFailed", st)
	}
	if diff := cmp.Diff(prev.ranks, e.store.data.ranks); diff != "" {
		t.Errorf("file_rank changed (-want +got):\n%s", diff)
	}
	if len(e.store.data.resolved) != 0 {
		t.Errorf("ResolveReferences called %v, want no call", e.store.data.resolved)
	}
}

func TestIncremental_DatabaseErrorRollsBackAndKeepsAUsableState(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.git.diff = []string{"src/a.ts"}
	e.store.hooks["PatchFileFacts"] = func(context.Context) error { return errors.New("db down") }
	prev := e.store.snapshot()
	before := e.state(t)

	if _, err := e.ix.Incremental(t.Context(), repoID); err == nil {
		t.Fatal("Incremental() error = nil, want the database error")
	}
	if diff := cmp.Diff(before, e.state(t)); diff != "" {
		t.Errorf("state changed (-before +after):\n%s", diff)
	}
	now := e.store.snapshot()
	now.states, prev.states = nil, nil
	if diff := cmp.Diff(prev, now, cmp.AllowUnexported(storeData{})); diff != "" {
		t.Errorf("tables changed (-before +after):\n%s", diff)
	}
}

func TestIncremental_ShutdownDuringSavepointWritesNothing(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.git.diff = []string{"src/a.ts"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.store.hooks["ReplaceEdges"] = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	before := e.state(t)
	if _, err := e.ix.Incremental(ctx, repoID); !errors.Is(err, context.Canceled) {
		t.Errorf("Incremental() error = %v, want context.Canceled", err)
	}
	if diff := cmp.Diff(before, e.state(t)); diff != "" {
		t.Errorf("state changed (-before +after):\n%s", diff)
	}
}

func TestIncremental_VanishedRepoInsideSavepointEndsTheIndex(t *testing.T) {
	e := incrementalEnv(t, domain.StatusFull)
	e.git.diff = []string{"src/a.ts"}
	e.store.hooks["ReplaceEdges"] = func(context.Context) error {
		delete(e.store.repos, repoID)
		return domain.ErrRepoNotFound
	}
	if _, err := e.ix.Incremental(t.Context(), repoID); !errors.Is(err, domain.ErrRepoNotFound) {
		t.Errorf("Incremental() error = %v, want ErrRepoNotFound", err)
	}
	if st := e.state(t); st.LastIndexedSHA != "old-sha" {
		t.Errorf("state = %+v, want the previous state", st)
	}
}

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	pgstore "dev-digest-go/backend/internal/repointel/adapters/postgres"
	"dev-digest-go/backend/internal/repointel/domain"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// statementLog is a GORM logger that records every statement and its bind parameter count.
type statementLog struct {
	logger.Interface
	mu    sync.Mutex
	stmts []statement
}

type statement struct {
	sql    string
	params int
}

func (l *statementLog) LogMode(logger.LogLevel) logger.Interface { return l }

// Trace evaluates the statement, which makes GORM call ParamsFilter.
func (l *statementLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	fc()
}

// ParamsFilter sees the statement and its bind values before they are explained.
func (l *statementLog) ParamsFilter(_ context.Context, sql string, params ...any) (string, []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stmts = append(l.stmts, statement{sql: sql, params: len(params)})
	return sql, params
}

func (l *statementLog) all() []statement {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]statement(nil), l.stmts...)
}

type env struct {
	db    *gorm.DB
	store *pgstore.Store
	tx    *platformpg.Tx
	log   *statementLog
	ws    string
	repo  string
}

// newEnv returns a migrated database with one repo and a store that records its statements.
func newEnv(t *testing.T) *env {
	t.Helper()
	d := pgtest.Migrated(t)
	log := &statementLog{Interface: logger.Discard}
	db := d.DB.Session(&gorm.Session{Logger: log})
	e := &env{db: d.DB, store: pgstore.NewStore(db), tx: platformpg.NewTx(db), log: log}
	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('default') RETURNING id`).Scan(&e.ws).Error; err != nil {
		t.Fatal(err)
	}
	e.repo = e.addRepo(t, "o", "r", "/clones/o/r")
	return e
}

// addRepo inserts a repo; clonePath "" leaves clone_path NULL.
func (e *env) addRepo(t *testing.T, owner, name, clonePath string) string {
	t.Helper()
	var clone *string
	if clonePath != "" {
		clone = &clonePath
	}
	var id string
	err := e.db.Raw(`INSERT INTO repos (workspace_id, owner, name, full_name, default_branch, clone_path)
		VALUES (?, ?, ?, ?, 'trunk', ?) RETURNING id`, e.ws, owner, name, owner+"/"+name, clone).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const unknownRepo = "00000000-0000-4000-8000-000000000000"

func count(t *testing.T, e *env, table, repo string) int64 {
	t.Helper()
	var n int64
	q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE repo_id = ?`, table)
	if err := e.db.Raw(q, repo).Scan(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func sym(path, name string, line int, exported bool) domain.SymbolRow {
	return domain.SymbolRow{
		Path: path, Name: name, Kind: "function", Line: line, EndLine: line + 2, Exported: exported,
		Signature: "function " + name + "()", ContentHash: "h",
	}
}

func TestLockRepo_HoldsTheAdvisoryLockUntilCommit(t *testing.T) {
	e := newEnv(t)
	tryLock := func() bool {
		var got bool
		// Autocommit: a successful try releases at once, a failed one changes nothing.
		err := e.db.Raw(`SELECT pg_try_advisory_xact_lock(hashtextextended(?, 0))`, "repo-intel:"+e.repo).Scan(&got).Error
		if err != nil {
			t.Fatalf("try lock: %v", err)
		}
		return got
	}
	err := e.tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := e.store.LockRepo(ctx, e.repo); err != nil {
			return err
		}
		if tryLock() {
			t.Error("lock free while an index transaction holds it, want held")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx() error = %v", err)
	}
	if !tryLock() {
		t.Error("lock still held after commit, want free")
	}
}

func TestInsertSymbols_ClampsNamesAndDedupes(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("a", 5000)
	emoji := strings.Repeat("b", 254) + "\U0001F600" + "tail" // units 255-256 are the emoji
	rows := []domain.SymbolRow{
		sym("a.ts", long, 1, true),
		sym("a.ts", long+"x", 1, true), // equal to the first after the cut: dropped
		sym("a.ts", emoji, 2, false),
		sym("a.ts", "other", 3, false),
	}
	if err := e.store.InsertSymbols(t.Context(), e.repo, rows); err != nil {
		t.Fatalf("InsertSymbols() error = %v", err)
	}
	var got []struct {
		Name string
		Line int
	}
	if err := e.db.Raw(`SELECT name, line FROM symbols WHERE repo_id = ? ORDER BY line`, e.repo).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("stored %d symbols, want 3: %+v", len(got), got)
	}
	if n := domain.UTF16Len(got[0].Name); n != 255 {
		t.Errorf("UTF16Len(stored long name) = %d, want 255", n)
	}
	if n := domain.UTF16Len(got[1].Name); n != 254 {
		t.Errorf("UTF16Len(stored emoji name) = %d, want 254", n)
	}
}

func TestInsertReferences_ClampsTarget(t *testing.T) {
	e := newEnv(t)
	callee := strings.Repeat("c", 254) + "\U0001F600" + strings.Repeat("d", 40)
	rows := []domain.ReferenceRow{{FromPath: "a.ts", ToSymbol: callee, Line: 4, ContentHash: "h"}}
	if err := e.store.InsertReferences(t.Context(), e.repo, rows); err != nil {
		t.Fatalf("InsertReferences() error = %v", err)
	}
	var name string
	if err := e.db.Raw(`SELECT to_symbol FROM "references" WHERE repo_id = ?`, e.repo).Scan(&name).Error; err != nil {
		t.Fatal(err)
	}
	if n := domain.UTF16Len(name); n != 254 {
		t.Errorf("UTF16Len(stored to_symbol) = %d, want 254", n)
	}
}

func TestInserts_ChunkRowsAndStayUnderTheParameterLimit(t *testing.T) {
	e := newEnv(t)
	const n = 1201
	syms := make([]domain.SymbolRow, n)
	refs := make([]domain.ReferenceRow, n)
	for i := range n {
		syms[i] = sym("a.ts", fmt.Sprintf("f%d", i), i+1, true)
		refs[i] = domain.ReferenceRow{FromPath: "a.ts", ToSymbol: fmt.Sprintf("f%d", i), Line: i + 1, ContentHash: "h"}
	}
	if err := e.store.InsertSymbols(t.Context(), e.repo, syms); err != nil {
		t.Fatal(err)
	}
	if err := e.store.InsertReferences(t.Context(), e.repo, refs); err != nil {
		t.Fatal(err)
	}
	inserts := map[string]int{}
	for _, s := range e.log.all() {
		if s.params > 65535 {
			t.Errorf("statement carries %d parameters, want at most 65535", s.params)
		}
		if strings.HasPrefix(s.sql, "INSERT") {
			inserts[strings.Fields(s.sql)[2]]++
			if rows := s.params / 9; strings.Contains(s.sql, `"symbols"`) && rows > domain.InsertChunkRows {
				t.Errorf("symbols insert carries %d rows, want at most %d", rows, domain.InsertChunkRows)
			}
		}
	}
	if got := inserts[`"symbols"`]; got != 3 {
		t.Errorf("symbols INSERT statements = %d, want 3 (500+500+201)", got)
	}
	if got := inserts[`"references"`]; got != 3 {
		t.Errorf("references INSERT statements = %d, want 3", got)
	}
	if got := count(t, e, "symbols", e.repo); got != n {
		t.Errorf("symbols rows = %d, want %d", got, n)
	}
}

func TestInserts_EmptyRowSetsIssueNoStatement(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	steps := []error{
		e.store.InsertSymbols(ctx, e.repo, nil),
		e.store.InsertReferences(ctx, e.repo, nil),
		e.store.DeleteFileRows(ctx, e.repo, nil),
		e.store.PatchFileFacts(ctx, e.repo, nil, nil),
	}
	for i, err := range steps {
		if err != nil {
			t.Errorf("step %d error = %v, want nil", i, err)
		}
	}
	if got := e.log.all(); len(got) != 0 {
		t.Errorf("statements for empty input = %+v, want none", got)
	}
}

func TestWrites_RepoGoneIsErrRepoNotFound(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	tests := []struct {
		name string
		do   func() error
	}{
		{"symbols", func() error {
			return e.store.InsertSymbols(ctx, unknownRepo, []domain.SymbolRow{sym("a.ts", "f", 1, true)})
		}},
		{"references", func() error {
			return e.store.InsertReferences(ctx, unknownRepo, []domain.ReferenceRow{{FromPath: "a.ts", ToSymbol: "f", Line: 1}})
		}},
		{"edges", func() error {
			return e.store.ReplaceEdges(ctx, unknownRepo, []domain.Edge{{From: "a", To: "b"}})
		}},
		{"rank", func() error {
			return e.store.ReplaceFileRank(ctx, unknownRepo, []domain.FileRankRow{{Path: "a", Percentile: 1}})
		}},
		{"facts", func() error {
			return e.store.ReplaceFileFacts(ctx, unknownRepo, []domain.FileFacts{{Path: "a", Endpoints: []string{"GET /x"}}})
		}},
		{"map", func() error {
			return e.store.PutRepoMap(ctx, unknownRepo, domain.RepoMap{CommitSHA: "s", Budget: 1500, Text: "t"})
		}},
		{"state", func() error {
			return e.store.UpsertState(ctx, domain.IndexState{RepoID: unknownRepo, Status: domain.StatusFull, UpdatedAt: time.Now()})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.do(); !errors.Is(err, domain.ErrRepoNotFound) {
				t.Errorf("write for a deleted repo: error = %v, want errors.Is ErrRepoNotFound", err)
			}
		})
	}
}

func TestDeleteRows(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	must(t, e.store.InsertSymbols(ctx, e.repo, []domain.SymbolRow{sym("a.ts", "f", 1, true), sym("b.ts", "g", 1, true), sym("c.ts", "h", 1, true)}))
	must(t, e.store.InsertReferences(ctx, e.repo, []domain.ReferenceRow{
		{FromPath: "a.ts", ToSymbol: "g", Line: 1}, {FromPath: "b.ts", ToSymbol: "h", Line: 1}, {FromPath: "c.ts", ToSymbol: "f", Line: 1},
	}))
	must(t, e.store.DeleteFileRows(ctx, e.repo, []string{"a.ts", "b.ts"}))
	if s, r := count(t, e, "symbols", e.repo), count(t, e, "\"references\"", e.repo); s != 1 || r != 1 {
		t.Errorf("after DeleteFileRows symbols = %d, references = %d, want 1 and 1", s, r)
	}
	must(t, e.store.DeleteRepoRows(ctx, e.repo))
	if s, r := count(t, e, "symbols", e.repo), count(t, e, "\"references\"", e.repo); s != 0 || r != 0 {
		t.Errorf("after DeleteRepoRows symbols = %d, references = %d, want 0 and 0", s, r)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// declFiles returns decl_file of each reference by to_symbol ("" for NULL).
func declFiles(t *testing.T, e *env) map[string]string {
	t.Helper()
	var rows []struct {
		ToSymbol string
		DeclFile *string
	}
	if err := e.db.Raw(`SELECT to_symbol, decl_file FROM "references" WHERE repo_id = ?`, e.repo).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.ToSymbol] = ""
		if r.DeclFile != nil {
			out[r.ToSymbol] = *r.DeclFile
		}
	}
	return out
}

func seedResolution(t *testing.T, e *env) {
	t.Helper()
	ctx := t.Context()
	must(t, e.store.InsertSymbols(ctx, e.repo, []domain.SymbolRow{
		sym("b.ts", "x", 1, true), sym("c.ts", "x", 1, true), sym("b.ts", "y", 5, true), sym("b.ts", "z", 9, false),
	}))
	must(t, e.store.InsertReferences(ctx, e.repo, []domain.ReferenceRow{
		{FromPath: "a.ts", ToSymbol: "x", Line: 1}, {FromPath: "a.ts", ToSymbol: "y", Line: 2}, {FromPath: "a.ts", ToSymbol: "z", Line: 3},
	}))
	must(t, e.store.ReplaceEdges(ctx, e.repo, []domain.Edge{{From: "a.ts", To: "b.ts"}, {From: "a.ts", To: "c.ts"}}))
}

func TestResolveReferences_OnlyUniqueExportedDeclarations(t *testing.T) {
	e := newEnv(t)
	seedResolution(t, e)
	must(t, e.store.ResolveReferences(t.Context(), e.repo, false))
	want := map[string]string{"x": "", "y": "b.ts", "z": ""}
	if diff := cmp.Diff(want, declFiles(t, e)); diff != "" {
		t.Errorf("decl_file after ResolveReferences mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveReferences_ResetClearsStaleResolutions(t *testing.T) {
	e := newEnv(t)
	seedResolution(t, e)
	ctx := t.Context()
	must(t, e.store.ResolveReferences(ctx, e.repo, false))
	// The import of b.ts disappears; c.ts alone now declares x.
	must(t, e.store.ReplaceEdges(ctx, e.repo, []domain.Edge{{From: "a.ts", To: "c.ts"}}))

	must(t, e.store.ResolveReferences(ctx, e.repo, false))
	if got := declFiles(t, e)["y"]; got != "b.ts" {
		t.Errorf("decl_file of y without reset = %q, want the stale %q", got, "b.ts")
	}
	must(t, e.store.ResolveReferences(ctx, e.repo, true))
	want := map[string]string{"x": "c.ts", "y": "", "z": ""}
	if diff := cmp.Diff(want, declFiles(t, e)); diff != "" {
		t.Errorf("decl_file after reset mismatch (-want +got):\n%s", diff)
	}
}

// The rows of a full index are written and resolved in one transaction, so the planner has no
// statistics for them. A resolution that re-ran an aggregate per candidate row took 67 s for the
// 138k references of a 5,000-file clone (AC3066); this size took 16 s with it and about a second
// without.
func TestResolveReferences_FreshRowsResolveInTime(t *testing.T) {
	const files, fanout = 2500, 4
	e := newEnv(t)
	var (
		syms  []domain.SymbolRow
		refs  []domain.ReferenceRow
		edges []domain.Edge
	)
	file := func(i int) string { return fmt.Sprintf("src/f%05d.ts", i) }
	for i := range files {
		syms = append(syms, sym(file(i), fmt.Sprintf("s%05d", i), 1, true))
		for d := 1; d <= fanout && i+d < files; d++ {
			edges = append(edges, domain.Edge{From: file(i), To: file(i + d)})
			refs = append(refs, domain.ReferenceRow{FromPath: file(i), ToSymbol: fmt.Sprintf("s%05d", i+d), Line: d})
		}
	}
	var took time.Duration
	err := e.tx.InTx(t.Context(), func(ctx context.Context) error {
		must(t, e.store.InsertSymbols(ctx, e.repo, syms))
		must(t, e.store.InsertReferences(ctx, e.repo, refs))
		must(t, e.store.ReplaceEdges(ctx, e.repo, edges))
		start := time.Now()
		defer func() { took = time.Since(start) }()
		return e.store.ResolveReferences(ctx, e.repo, false)
	})
	must(t, err)
	if took > 10*time.Second {
		t.Errorf("ResolveReferences of %d fresh references took %v, want under 10s", len(refs), took)
	}
	var resolved int
	must(t, e.db.Raw(`SELECT count(*) FROM "references" WHERE repo_id = ? AND decl_file = 'src/f' || substr(to_symbol, 2) || '.ts'`, e.repo).Scan(&resolved).Error)
	if resolved != len(refs) {
		t.Errorf("references resolved to the file of their symbol = %d, want %d", resolved, len(refs))
	}
}

func TestReplaceEdgesRankFacts_ReplaceTheRepoRows(t *testing.T) {
	e := newEnv(t)
	other := e.addRepo(t, "o", "other", "/clones/o/other")
	ctx := t.Context()
	must(t, e.store.ReplaceEdges(ctx, other, []domain.Edge{{From: "k", To: "l"}}))
	for round := range 2 { // the second round replaces the first
		must(t, e.store.ReplaceEdges(ctx, e.repo, []domain.Edge{{From: "a", To: "b"}, {From: "b", To: fmt.Sprint("c", round)}}))
		must(t, e.store.ReplaceFileRank(ctx, e.repo, []domain.FileRankRow{{Path: "a", PageRank: 0.5, Rank: 0.5, Percentile: 100}, {Path: "b", PageRank: 0.25, Rank: 0.25, Percentile: 50}}))
		must(t, e.store.ReplaceFileFacts(ctx, e.repo, []domain.FileFacts{{Path: "a", Endpoints: []string{"GET /x"}}}))
	}
	for table, want := range map[string]int64{"file_edges": 2, "file_rank": 2, "file_facts": 1} {
		if got := count(t, e, table, e.repo); got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	if got := count(t, e, "file_edges", other); got != 1 {
		t.Errorf("file_edges rows of another repo = %d, want 1 (untouched)", got)
	}
}

func TestFileFacts_JSONArraysAndPatch(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	must(t, e.store.ReplaceFileFacts(ctx, e.repo, []domain.FileFacts{
		{Path: "a.ts", Endpoints: []string{"GET /x"}},
		{Path: "b.ts", Crons: []string{"0 * * * *"}},
		{Path: "none.ts"}, // no facts: no row
	}))
	facts := func() map[string][2]string {
		var rows []struct {
			FilePath  string
			Endpoints string
			Crons     string
		}
		if err := e.db.Raw(`SELECT file_path, endpoints::text AS endpoints, crons::text AS crons FROM file_facts WHERE repo_id = ?`, e.repo).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out := map[string][2]string{}
		for _, r := range rows {
			out[r.FilePath] = [2]string{r.Endpoints, r.Crons}
		}
		return out
	}
	want := map[string][2]string{"a.ts": {`["GET /x"]`, `[]`}, "b.ts": {`[]`, `["0 * * * *"]`}}
	if diff := cmp.Diff(want, facts()); diff != "" {
		t.Errorf("file_facts mismatch (-want +got):\n%s", diff)
	}

	// Patch: a.ts changed (now without facts), c.ts is new; b.ts is untouched.
	must(t, e.store.PatchFileFacts(ctx, e.repo, []string{"a.ts", "c.ts"}, []domain.FileFacts{{Path: "c.ts", Endpoints: []string{"POST /y"}}}))
	want = map[string][2]string{"b.ts": {`[]`, `["0 * * * *"]`}, "c.ts": {`["POST /y"]`, `[]`}}
	if diff := cmp.Diff(want, facts()); diff != "" {
		t.Errorf("file_facts after PatchFileFacts mismatch (-want +got):\n%s", diff)
	}
}

func TestRepoMap_PutReplacesSameKeyAndDeleteClearsAll(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	must(t, e.store.PutRepoMap(ctx, e.repo, domain.RepoMap{CommitSHA: "s1", Budget: 1500, Text: "one", Tokens: 3}))
	must(t, e.store.PutRepoMap(ctx, e.repo, domain.RepoMap{CommitSHA: "s1", Budget: 1500, Text: "two", Tokens: 4}))
	must(t, e.store.PutRepoMap(ctx, e.repo, domain.RepoMap{CommitSHA: "s1", Budget: 800, Text: "small", Tokens: 1}))
	if got := count(t, e, "repo_map_cache", e.repo); got != 2 {
		t.Fatalf("repo_map_cache rows = %d, want 2", got)
	}
	m, ok, err := e.store.CachedMap(ctx, e.repo, "s1", 1500)
	if err != nil || !ok || m.Text != "two" || m.Tokens != 4 {
		t.Errorf("CachedMap(s1, 1500) = %+v, %v, %v; want text two, 4 tokens", m, ok, err)
	}
	must(t, e.store.DeleteRepoMaps(ctx, e.repo))
	if got := count(t, e, "repo_map_cache", e.repo); got != 0 {
		t.Errorf("repo_map_cache rows after DeleteRepoMaps = %d, want 0", got)
	}
}

func TestState_EveryWriteSetsUpdatedAtFromTheCaller(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	t0 := time.Date(2026, 10, 22, 8, 0, 0, 123_000_000, time.UTC)
	st := domain.IndexState{
		RepoID: e.repo, LastIndexedSHA: "sha1", IndexerVersion: domain.IndexerVersion, Status: domain.StatusPartial,
		FilesIndexed: 3, FilesSkipped: 1, Stats: map[string]any{"durationMs": 42, "reason": "x"}, UpdatedAt: t0,
	}
	updatedAt := func() time.Time {
		got, ok, err := e.store.GetState(ctx, e.repo)
		if err != nil || !ok {
			t.Fatalf("GetState() = %v, %v", ok, err)
		}
		return got.UpdatedAt.UTC()
	}

	must(t, e.store.UpsertState(ctx, st))
	if got := updatedAt(); !got.Equal(t0) {
		t.Errorf("updated_at after UpsertState = %v, want %v", got, t0)
	}
	t1 := t0.Add(time.Minute)
	st.Status, st.FilesIndexed, st.UpdatedAt = domain.StatusFull, 9, t1
	must(t, e.store.UpsertState(ctx, st)) // second upsert replaces
	got, _, _ := e.store.GetState(ctx, e.repo)
	if got.Status != domain.StatusFull || got.FilesIndexed != 9 || !got.UpdatedAt.Equal(t1) {
		t.Errorf("state after second UpsertState = %+v, want full, 9 files, updated_at %v", got, t1)
	}

	t2 := t1.Add(time.Minute)
	must(t, e.store.TouchState(ctx, e.repo, t2))
	got, _, _ = e.store.GetState(ctx, e.repo)
	if !got.UpdatedAt.Equal(t2) || got.LastIndexedSHA != "sha1" || got.Status != domain.StatusFull {
		t.Errorf("state after TouchState = %+v, want only updated_at %v changed", got, t2)
	}

	t3 := t2.Add(time.Minute)
	must(t, e.store.AdvanceSHA(ctx, e.repo, "sha2", t3))
	got, _, _ = e.store.GetState(ctx, e.repo)
	if !got.UpdatedAt.Equal(t3) || got.LastIndexedSHA != "sha2" || got.FilesIndexed != 9 {
		t.Errorf("state after AdvanceSHA = %+v, want sha2 and updated_at %v", got, t3)
	}

	// No row, no effect.
	must(t, e.store.TouchState(ctx, unknownRepo, t3))
	must(t, e.store.AdvanceSHA(ctx, unknownRepo, "s", t3))
	if _, ok, err := e.store.GetState(ctx, unknownRepo); ok || err != nil {
		t.Errorf("GetState(unknown) = ok %v, err %v, want no row", ok, err)
	}
}

func TestState_NilStatsStoreAnEmptyDocument(t *testing.T) {
	e := newEnv(t)
	must(t, e.store.UpsertState(t.Context(), domain.IndexState{
		RepoID: e.repo, IndexerVersion: 2, Status: domain.StatusDegraded, UpdatedAt: time.Now(),
	}))
	var stats string
	if err := e.db.Raw(`SELECT stats::text FROM repo_index_state WHERE repo_id = ?`, e.repo).Scan(&stats).Error; err != nil {
		t.Fatal(err)
	}
	if stats != "{}" {
		t.Errorf("stored stats = %q, want {}", stats)
	}
}

func TestWrites_JoinTheCallersTransaction(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("boom")
	err := e.tx.InTx(t.Context(), func(ctx context.Context) error {
		must(t, e.store.LockRepo(ctx, e.repo))
		must(t, e.store.InsertSymbols(ctx, e.repo, []domain.SymbolRow{sym("a.ts", "f", 1, true)}))
		must(t, e.store.ReplaceFileRank(ctx, e.repo, []domain.FileRankRow{{Path: "a.ts", Rank: 1, Percentile: 100}}))
		must(t, e.store.UpsertState(ctx, domain.IndexState{RepoID: e.repo, IndexerVersion: 2, Status: domain.StatusFull, UpdatedAt: time.Now()}))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx() error = %v, want boom", err)
	}
	for _, table := range []string{"symbols", "file_rank", "repo_index_state"} {
		if got := count(t, e, table, e.repo); got != 0 {
			t.Errorf("%s rows after rollback = %d, want 0", table, got)
		}
	}
}

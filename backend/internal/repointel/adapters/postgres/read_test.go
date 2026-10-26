package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/repointel/domain"
)

func TestGetRepo(t *testing.T) {
	e := newEnv(t)
	uncloned := e.addRepo(t, "o", "bare", "")

	got, err := e.store.GetRepo(t.Context(), e.repo)
	want := domain.Repo{ID: e.repo, WorkspaceID: e.ws, Owner: "o", Name: "r", Provider: "github", Host: "github.com", DefaultBranch: "trunk", ClonePath: "/clones/o/r"}
	if err != nil {
		t.Fatalf("GetRepo(cloned) error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetRepo(cloned) mismatch (-want +got):\n%s", diff)
	}
	if got, err := e.store.GetRepo(t.Context(), uncloned); err != nil || got.ClonePath != "" {
		t.Errorf("GetRepo(uncloned) = %+v, %v, want empty ClonePath", got, err)
	}
	if _, err := e.store.GetRepo(t.Context(), unknownRepo); !errors.Is(err, domain.ErrRepoNotFound) {
		t.Errorf("GetRepo(unknown) error = %v, want ErrRepoNotFound", err)
	}
}

func TestGetRepo_GitLab(t *testing.T) {
	e := newEnv(t)
	id := e.addGitLabRepo(t, "gitlab.example.com:8443", 42, "grp/sub", "web", "/clones/@gitlab/gitlab.example.com_8443/42")

	got, err := e.store.GetRepo(t.Context(), id)
	if err != nil {
		t.Fatalf("GetRepo(gitlab) error = %v", err)
	}
	want := domain.Repo{
		ID: id, WorkspaceID: e.ws, Owner: "grp/sub", Name: "web", Provider: "gitlab", Host: "gitlab.example.com:8443", ProjectID: 42,
		DefaultBranch: "trunk", ClonePath: "/clones/@gitlab/gitlab.example.com_8443/42",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetRepo(gitlab) mismatch (-want +got):\n%s", diff)
	}
	if owner, name := got.CloneKey(); owner != "@gitlab/gitlab.example.com_8443" || name != "42" {
		t.Errorf("CloneKey() = (%q, %q), want (@gitlab/gitlab.example.com_8443, 42)", owner, name)
	}
}

func TestGetState_RoundTripsStats(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 10, 22, 9, 30, 0, 0, time.UTC)
	in := domain.IndexState{
		RepoID: e.repo, LastIndexedSHA: "abc", IndexerVersion: 2, Status: domain.StatusPartial, FilesIndexed: 5,
		FilesSkipped: 2, UpdatedAt: at,
		Stats: map[string]any{"durationMs": 1234, "reason": "r", "parseDegraded": []string{"a.ts"}, "softBudgetReached": true},
	}
	must(t, e.store.UpsertState(t.Context(), in))
	got, ok, err := e.store.GetState(t.Context(), e.repo)
	if err != nil || !ok {
		t.Fatalf("GetState() = %v, %v", ok, err)
	}
	want := in
	want.Stats = map[string]any{"durationMs": 1234.0, "reason": "r", "parseDegraded": []any{"a.ts"}, "softBudgetReached": true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetState() mismatch (-want +got):\n%s", diff)
	}
	if v := domain.NewStateView(got); v.DurationMs != 1234 {
		t.Errorf("NewStateView(stored).DurationMs = %d, want 1234", v.DurationMs)
	}
	if _, ok, err := e.store.GetState(t.Context(), unknownRepo); ok || err != nil {
		t.Errorf("GetState(unknown) = %v, %v, want no row", ok, err)
	}
}

func TestListBackfill(t *testing.T) {
	e := newEnv(t) // e.repo: cloned, no state row
	oldVersion := e.addRepo(t, "o", "old", "/clones/o/old")
	current := e.addRepo(t, "o", "current", "/clones/o/current")
	e.addRepo(t, "o", "bare", "") // not cloned: never listed
	must(t, e.store.UpsertState(t.Context(), domain.IndexState{RepoID: oldVersion, IndexerVersion: 1, Status: domain.StatusFull, UpdatedAt: time.Now()}))
	must(t, e.store.UpsertState(t.Context(), domain.IndexState{RepoID: current, IndexerVersion: domain.IndexerVersion, Status: domain.StatusFull, UpdatedAt: time.Now()}))

	got, err := e.store.ListBackfill(t.Context())
	if err != nil {
		t.Fatalf("ListBackfill() error = %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []string{e.repo, oldVersion}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("ListBackfill() ids mismatch (-want +got), ordered by repo id:\n%s", diff)
	}
	for _, r := range got {
		if r.ClonePath == "" || r.Owner != "o" || r.WorkspaceID != e.ws {
			t.Errorf("ListBackfill() repo = %+v, want clone path, owner and workspace filled", r)
		}
	}
}

func TestListBackfill_GitLabRepoCarriesItsCloneKey(t *testing.T) {
	e := newEnv(t)
	id := e.addGitLabRepo(t, "gitlab.example.com", 7, "grp", "web", "/clones/@gitlab/gitlab.example.com/7")

	got, err := e.store.ListBackfill(t.Context())
	if err != nil {
		t.Fatalf("ListBackfill() error = %v", err)
	}
	for _, r := range got {
		if r.ID != id {
			continue
		}
		if owner, name := r.CloneKey(); owner != "@gitlab/gitlab.example.com" || name != "7" {
			t.Errorf("ListBackfill() gitlab repo CloneKey() = (%q, %q), want (@gitlab/gitlab.example.com, 7)", owner, name)
		}
		return
	}
	t.Errorf("ListBackfill() = %+v, want the gitlab repo %s listed", got, id)
}

func TestMapCandidates_OrderAndFilter(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	with := func(path, name string, line int, exported bool) domain.SymbolRow {
		r := sym(path, name, line, exported)
		r.Signature = "sig " + name
		return r
	}
	rows := []domain.SymbolRow{
		with("hi.ts", "late", 9, true),
		with("hi.ts", "early", 2, true),
		with("hi.ts", "hidden", 1, false), // exported DESC: after the exported ones
		with("lo.ts", "top", 1, true),     // lower rank: after hi.ts
		with("B/x.ts", "tie", 5, true),    // same rank, exported, line, name as a/x.ts:
		with("a/x.ts", "tie", 5, true),    // byte order puts "B/x.ts" first
		with("hi.ts", "Upper", 20, true),  // names compare by bytes: "Upper" < "lower"
		with("hi.ts", "lower", 20, true),
		with("ranked-less.ts", "ghost", 1, true), // no file_rank row: not a candidate
	}
	nosig := sym("hi.ts", "nosig", 30, true)
	rows = append(rows, nosig)
	must(t, e.store.InsertSymbols(ctx, e.repo, rows))
	// Clear the signature of one symbol: it must not be a candidate.
	if err := e.db.Exec(`UPDATE symbols SET signature = NULL WHERE repo_id = ? AND name = 'nosig'`, e.repo).Error; err != nil {
		t.Fatal(err)
	}
	must(t, e.store.ReplaceFileRank(ctx, e.repo, []domain.FileRankRow{
		{Path: "hi.ts", Rank: 0.9, Percentile: 100}, {Path: "lo.ts", Rank: 0.1, Percentile: 50},
		{Path: "B/x.ts", Rank: 0.5, Percentile: 75}, {Path: "a/x.ts", Rank: 0.5, Percentile: 75},
	}))

	got, err := e.store.MapCandidates(ctx, e.repo)
	if err != nil {
		t.Fatalf("MapCandidates() error = %v", err)
	}
	var order []string
	for _, c := range got {
		order = append(order, c.Path+":"+c.Name)
	}
	// hidden is not exported: it follows every exported symbol of its file regardless of line.
	want := []string{
		"hi.ts:early", "hi.ts:late", "hi.ts:Upper", "hi.ts:lower", "hi.ts:hidden",
		"B/x.ts:tie", "a/x.ts:tie", "lo.ts:top",
	}
	if diff := cmp.Diff(want, order); diff != "" {
		t.Errorf("MapCandidates() order mismatch (-want +got):\n%s", diff)
	}
	if first := got[0]; first.Rank != 0.9 || first.Line != 2 || first.Signature != "sig early" || !first.Exported {
		t.Errorf("MapCandidates()[0] = %+v, want rank 0.9, line 2, signature, exported", first)
	}
}

func TestMapCandidates_SeesTheTransactionsWrites(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("rollback")
	err := e.tx.InTx(t.Context(), func(ctx context.Context) error {
		must(t, e.store.InsertSymbols(ctx, e.repo, []domain.SymbolRow{sym("a.ts", "f", 1, true)}))
		must(t, e.store.ReplaceFileRank(ctx, e.repo, []domain.FileRankRow{{Path: "a.ts", Rank: 1, Percentile: 100}}))
		inside, err := e.store.MapCandidates(ctx, e.repo)
		if err != nil || len(inside) != 1 {
			t.Errorf("MapCandidates(tx ctx) = %d rows, %v, want the uncommitted symbol", len(inside), err)
		}
		outside, err := e.store.MapCandidates(context.WithoutCancel(t.Context()), e.repo)
		if err != nil || len(outside) != 0 {
			t.Errorf("MapCandidates(plain ctx) = %d rows, %v, want none before commit", len(outside), err)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx() error = %v", err)
	}
}

func TestCachedMap(t *testing.T) {
	e := newEnv(t)
	must(t, e.store.PutRepoMap(t.Context(), e.repo, domain.RepoMap{CommitSHA: "s1", Budget: 1500, Text: "map", Tokens: 7}))
	got, ok, err := e.store.CachedMap(t.Context(), e.repo, "s1", 1500)
	want := domain.RepoMap{CommitSHA: "s1", Budget: 1500, Text: "map", Tokens: 7}
	if err != nil || !ok || got != want {
		t.Errorf("CachedMap(s1, 1500) = %+v, %v, %v, want %+v", got, ok, err, want)
	}
	for name, c := range map[string]struct {
		sha    string
		budget int
	}{"other budget": {"s1", 1000}, "other sha": {"s2", 1500}, "empty sha": {"", 1500}} {
		if _, ok, err := e.store.CachedMap(t.Context(), e.repo, c.sha, c.budget); ok || err != nil {
			t.Errorf("CachedMap(%s) = ok %v, err %v, want a miss", name, ok, err)
		}
	}
}

func TestFileRanksAndRankedPaths(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	must(t, e.store.ReplaceFileRank(ctx, e.repo, []domain.FileRankRow{
		{Path: "b.ts", Rank: 0.5, Percentile: 50}, {Path: "B.ts", Rank: 0.5, Percentile: 50},
		{Path: "top.ts", Rank: 0.9, Percentile: 100}, {Path: "low.ts", Rank: 0.1, Percentile: 1},
	}))

	ranks, err := e.store.FileRanks(ctx, e.repo, []string{"top.ts", "low.ts", "missing.ts"})
	if err != nil {
		t.Fatalf("FileRanks() error = %v", err)
	}
	byPath := map[string]int{}
	for _, r := range ranks {
		byPath[r.Path] = r.Percentile
	}
	if diff := cmp.Diff(map[string]int{"top.ts": 100, "low.ts": 1}, byPath); diff != "" {
		t.Errorf("FileRanks() mismatch (-want +got):\n%s", diff)
	}
	if got, err := e.store.FileRanks(ctx, e.repo, nil); err != nil || len(got) != 0 {
		t.Errorf("FileRanks(nil) = %v, %v, want empty", got, err)
	}

	top, err := e.store.RankedPaths(ctx, e.repo, 3)
	if err != nil {
		t.Fatalf("RankedPaths() error = %v", err)
	}
	want := []domain.RankedPath{{Path: "top.ts", Rank: 0.9}, {Path: "B.ts", Rank: 0.5}, {Path: "b.ts", Rank: 0.5}}
	if diff := cmp.Diff(want, top); diff != "" {
		t.Errorf("RankedPaths(3) mismatch (-want +got), ties by byte order:\n%s", diff)
	}
	if got, err := e.store.RankedPaths(ctx, e.repo, 0); err != nil || len(got) != 0 {
		t.Errorf("RankedPaths(0) = %v, %v, want empty", got, err)
	}
}

func TestSymbolsInFiles(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	must(t, e.store.InsertSymbols(ctx, e.repo, []domain.SymbolRow{
		sym("b.ts", "second", 5, true), sym("b.ts", "first", 1, false), sym("B.ts", "upper", 1, true),
		sym("c.ts", "elsewhere", 1, true),
	}))
	// A row without lines or signature, and one with a line but no end line.
	must(t, e.db.Exec(`INSERT INTO symbols (repo_id, path, name, kind, exported) VALUES (?, 'b.ts', 'nolines', 'type', true)`, e.repo).Error)
	must(t, e.db.Exec(`INSERT INTO symbols (repo_id, path, name, kind, line, signature) VALUES (?, 'b.ts', 'noend', 'type', 3, NULL)`, e.repo).Error)

	got, err := e.store.SymbolsInFiles(ctx, e.repo, []string{"b.ts", "B.ts", "none.ts"})
	if err != nil {
		t.Fatalf("SymbolsInFiles() error = %v", err)
	}
	sig := func(s string) *string { return &s }
	want := []domain.SymbolInfo{
		{File: "B.ts", Name: "upper", Kind: "function", Exported: true, StartLine: 1, EndLine: 3, Signature: sig("function upper()")},
		{File: "b.ts", Name: "first", Kind: "function", StartLine: 1, EndLine: 3, Signature: sig("function first()")},
		{File: "b.ts", Name: "noend", Kind: "type", StartLine: 3, EndLine: 3},
		{File: "b.ts", Name: "second", Kind: "function", Exported: true, StartLine: 5, EndLine: 7, Signature: sig("function second()")},
		{File: "b.ts", Name: "nolines", Kind: "type", Exported: true},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SymbolsInFiles() mismatch (-want +got):\n%s", diff)
	}
	if got, err := e.store.SymbolsInFiles(ctx, e.repo, nil); err != nil || len(got) != 0 {
		t.Errorf("SymbolsInFiles(nil) = %v, %v, want empty", got, err)
	}
}

func TestEdges_ByteOrder(t *testing.T) {
	e := newEnv(t)
	in := []domain.Edge{{From: "b.ts", To: "a.ts"}, {From: "B.ts", To: "z.ts"}, {From: "b.ts", To: "A.ts"}}
	must(t, e.store.ReplaceEdges(t.Context(), e.repo, in))
	got, err := e.store.Edges(t.Context(), e.repo)
	if err != nil {
		t.Fatalf("Edges() error = %v", err)
	}
	want := []domain.Edge{{From: "B.ts", To: "z.ts"}, {From: "b.ts", To: "A.ts"}, {From: "b.ts", To: "a.ts"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Edges() mismatch (-want +got):\n%s", diff)
	}
}

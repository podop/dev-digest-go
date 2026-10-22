package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

const (
	repoID    = "repo-1"
	clonePath = "/clones/acme/widgets"
)

// env wires an Indexer to fakes around one cloned repo "repo-1".
type env struct {
	store *fakeStore
	clone *fakeClone
	an    *fakeAnalyzer
	git   *fakeGit
	graph *fakeGraph
	clock *fakeClock
	ix    *app.Indexer
}

type envOption func(*envConfig)

type envConfig struct {
	log     *slog.Logger
	workers int
}

func withLog(l *slog.Logger) envOption { return func(c *envConfig) { c.log = l } }

func withWorkers(n int) envOption { return func(c *envConfig) { c.workers = n } }

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	cfg := envConfig{workers: 1}
	for _, o := range opts {
		o(&cfg)
	}
	e := &env{
		store: newFakeStore(),
		clone: &fakeClone{files: map[string][]byte{}, unreadable: map[string]bool{}},
		an:    &fakeAnalyzer{ex: map[string]domain.Extraction{}, imports: map[string][]string{}},
		git:   &fakeGit{head: "head-sha"},
		graph: &fakeGraph{},
		clock: newClock(),
	}
	e.store.repos[repoID] = domain.Repo{ID: repoID, WorkspaceID: "ws", Owner: "acme", Name: "widgets", DefaultBranch: "main", ClonePath: clonePath}
	e.ix = app.NewIndexer(app.IndexerDeps{
		Reader: e.store, Writer: e.store, Tx: e.store, Git: e.git, Files: e.clone,
		Analyzer: e.an, Graph: e.graph, Tokens: lenTokens{}, Redact: maskKey{},
	}, app.IndexerOptions{Now: e.clock.Now, Workers: cfg.workers, Log: cfg.log})
	return e
}

// addFile puts a source file in the clone with one exported function symbol named after it.
func (e *env) addFile(path, content string, symbols ...domain.Symbol) {
	e.clone.files[path] = []byte(content)
	e.an.ex[path] = domain.Extraction{Symbols: symbols}
}

func fn(name string, line int) domain.Symbol {
	return domain.Symbol{Name: name, Kind: "function", Line: line, EndLine: line + 2, Exported: true, Signature: "function " + name + "()"}
}

// seedIndex stores a previous index: a state of the given status at sha "old-sha", with one row
// in every table.
func (e *env) seedIndex(status domain.Status) {
	e.store.states[repoID] = domain.IndexState{
		RepoID: repoID, LastIndexedSHA: "old-sha", IndexerVersion: domain.IndexerVersion, Status: status,
		FilesIndexed: 5, FilesSkipped: 2, Stats: map[string]any{"filesSeen": 7}, UpdatedAt: e.clock.Now().Add(-time.Hour),
	}
	e.store.data = storeData{
		symbols: []domain.SymbolRow{{Path: "src/a.ts", Name: "old", Kind: "function", Line: 1}, {Path: "src/b.ts", Name: "gone", Kind: "function", Line: 1}},
		refs:    []domain.ReferenceRow{{FromPath: "src/b.ts", ToSymbol: "old", Line: 4}},
		edges:   []domain.Edge{{From: "src/b.ts", To: "src/a.ts"}},
		ranks:   []domain.FileRankRow{{Path: "src/a.ts", Rank: 0.6, Percentile: 100}, {Path: "src/b.ts", Rank: 0.4, Percentile: 50}},
		facts:   []domain.FileFacts{{Path: "src/b.ts", Endpoints: []string{"GET /old"}}},
		maps:    []domain.RepoMap{{CommitSHA: "old-sha", Budget: domain.DefaultTokenBudget, Text: "old map", Tokens: 2}},
	}
}

func (e *env) state(t *testing.T) domain.IndexState {
	t.Helper()
	st, ok := e.store.states[repoID]
	if !ok {
		t.Fatalf("no state row for %s", repoID)
	}
	return st
}

func (e *env) noState(t *testing.T) {
	t.Helper()
	if st, ok := e.store.states[repoID]; ok {
		t.Errorf("state row written: %+v", st)
	}
}

func statInt(t *testing.T, st domain.IndexState, key string) int {
	t.Helper()
	v, ok := st.Stats[key].(int)
	if !ok {
		t.Fatalf("stats[%q] = %#v, want an int", key, st.Stats[key])
	}
	return v
}

func statStr(st domain.IndexState, key string) string {
	s, _ := st.Stats[key].(string)
	return s
}

func degradedFiles(t *testing.T, st domain.IndexState) []string {
	t.Helper()
	raw, err := json.Marshal(st.Stats["parseDegraded"])
	if err != nil {
		t.Fatalf("marshal stats[parseDegraded]: %v", err)
	}
	var list []struct{ File, Reason string }
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("stats[parseDegraded] = %s, want a list: %v", raw, err)
	}
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.File + ": " + f.Reason
	}
	return out
}

func manyFiles(e *env, n int) []string {
	files := make([]string, n)
	for i := range files {
		files[i] = fmt.Sprintf("src/f%02d.ts", i)
		e.addFile(files[i], "x", fn(fmt.Sprintf("f%02d", i), 1))
	}
	return files
}

// blockUntilDone is a hook that waits for the context to end and returns its error.
func blockUntilDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// state0 returns the state row or nil.
func (e *env) state0(id string) *domain.IndexState {
	st, ok := e.store.states[id]
	if !ok {
		return nil
	}
	return &st
}

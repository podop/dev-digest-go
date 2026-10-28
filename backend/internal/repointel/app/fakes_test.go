package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/repointel/app"
	"dev-digest-go/backend/internal/repointel/domain"
)

// fakeStore is an in-memory IndexReader and IndexWriter with real transaction semantics: InTx
// restores the data on an error, Savepoint likewise. Hooks inject failures by method name.
type fakeStore struct {
	mu sync.Mutex

	repos  map[string]domain.Repo
	states map[string]domain.IndexState
	data   storeData

	// calls records the writer and map-candidate calls in order.
	calls []string
	// hooks run before the named method; a non-nil error is returned instead of the effect.
	hooks map[string]func(ctx context.Context) error
	inTx  bool

	// callers and factRows are what ResolvedCallers and FileFacts answer (blast radius tests);
	// the fake filters them by the names and files asked for.
	callers  []domain.ResolvedCaller
	factRows []domain.FileFacts
}

type storeData struct {
	symbols []domain.SymbolRow
	refs    []domain.ReferenceRow
	edges   []domain.Edge
	ranks   []domain.FileRankRow
	facts   []domain.FileFacts
	maps    []domain.RepoMap
	// resolved records the reset flag of each ResolveReferences call.
	resolved []bool
	states   map[string]domain.IndexState
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		repos:  map[string]domain.Repo{},
		states: map[string]domain.IndexState{},
		hooks:  map[string]func(context.Context) error{},
	}
}

func (s *fakeStore) snapshot() storeData {
	d := s.data
	d.symbols = slices.Clone(d.symbols)
	d.refs = slices.Clone(d.refs)
	d.edges = slices.Clone(d.edges)
	d.ranks = slices.Clone(d.ranks)
	d.facts = slices.Clone(d.facts)
	d.maps = slices.Clone(d.maps)
	d.resolved = slices.Clone(d.resolved)
	d.states = maps.Clone(s.states)
	return d
}

func (s *fakeStore) restore(d storeData) {
	s.states = d.states
	d.states = nil
	s.data = d
}

func (s *fakeStore) hook(ctx context.Context, name string) error {
	s.calls = append(s.calls, name)
	if h := s.hooks[name]; h != nil {
		// Hooks may block (waiting for the context); release the store while they do.
		s.mu.Unlock()
		defer s.mu.Lock()
		return h(ctx)
	}
	return nil
}

func (s *fakeStore) InTx(ctx context.Context, fn func(context.Context) error) (err error) {
	s.mu.Lock()
	before := s.snapshot()
	s.inTx = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inTx = false
		p := recover()
		if err != nil || p != nil {
			s.restore(before)
		}
		s.mu.Unlock()
		if p != nil {
			panic(p)
		}
	}()
	return fn(ctx)
}

func (s *fakeStore) Savepoint(ctx context.Context, fn func(context.Context) error) error {
	s.mu.Lock()
	if !s.inTx {
		s.mu.Unlock()
		return errors.New("no transaction")
	}
	before := s.snapshot()
	s.mu.Unlock()
	err := fn(ctx)
	if err != nil {
		s.mu.Lock()
		s.restore(before)
		s.mu.Unlock()
	}
	return err
}

// --- IndexWriter ---

func (s *fakeStore) LockRepo(ctx context.Context, repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hook(ctx, "LockRepo")
}

func (s *fakeStore) DeleteRepoRows(ctx context.Context, repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "DeleteRepoRows"); err != nil {
		return err
	}
	s.data.symbols, s.data.refs = nil, nil
	return nil
}

func (s *fakeStore) DeleteFileRows(ctx context.Context, repoID string, paths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "DeleteFileRows"); err != nil {
		return err
	}
	s.data.symbols = slices.DeleteFunc(s.data.symbols, func(r domain.SymbolRow) bool { return slices.Contains(paths, r.Path) })
	s.data.refs = slices.DeleteFunc(s.data.refs, func(r domain.ReferenceRow) bool { return slices.Contains(paths, r.FromPath) })
	return nil
}

func (s *fakeStore) InsertSymbols(ctx context.Context, repoID string, rows []domain.SymbolRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "InsertSymbols"); err != nil {
		return err
	}
	s.data.symbols = append(s.data.symbols, rows...)
	return nil
}

func (s *fakeStore) InsertReferences(ctx context.Context, repoID string, rows []domain.ReferenceRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "InsertReferences"); err != nil {
		return err
	}
	s.data.refs = append(s.data.refs, rows...)
	return nil
}

func (s *fakeStore) ReplaceEdges(ctx context.Context, repoID string, edges []domain.Edge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "ReplaceEdges"); err != nil {
		return err
	}
	s.data.edges = slices.Clone(edges)
	return nil
}

func (s *fakeStore) ResolveReferences(ctx context.Context, repoID string, reset bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "ResolveReferences"); err != nil {
		return err
	}
	s.data.resolved = append(s.data.resolved, reset)
	return nil
}

func (s *fakeStore) ReplaceFileRank(ctx context.Context, repoID string, rows []domain.FileRankRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "ReplaceFileRank"); err != nil {
		return err
	}
	s.data.ranks = slices.Clone(rows)
	return nil
}

func (s *fakeStore) ReplaceFileFacts(ctx context.Context, repoID string, rows []domain.FileFacts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "ReplaceFileFacts"); err != nil {
		return err
	}
	s.data.facts = slices.Clone(rows)
	return nil
}

func (s *fakeStore) PatchFileFacts(ctx context.Context, repoID string, paths []string, rows []domain.FileFacts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "PatchFileFacts"); err != nil {
		return err
	}
	s.data.facts = slices.DeleteFunc(s.data.facts, func(f domain.FileFacts) bool { return slices.Contains(paths, f.Path) })
	s.data.facts = append(s.data.facts, rows...)
	return nil
}

func (s *fakeStore) DeleteRepoMaps(ctx context.Context, repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "DeleteRepoMaps"); err != nil {
		return err
	}
	s.data.maps = nil
	return nil
}

func (s *fakeStore) PutRepoMap(ctx context.Context, repoID string, m domain.RepoMap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "PutRepoMap"); err != nil {
		return err
	}
	s.data.maps = append(s.data.maps, m)
	return nil
}

func (s *fakeStore) UpsertState(ctx context.Context, st domain.IndexState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "UpsertState"); err != nil {
		return err
	}
	if _, ok := s.repos[st.RepoID]; !ok {
		return fmt.Errorf("upsert state: %w", domain.ErrRepoNotFound)
	}
	s.states[st.RepoID] = st
	return nil
}

func (s *fakeStore) TouchState(ctx context.Context, repoID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "TouchState"); err != nil {
		return err
	}
	if st, ok := s.states[repoID]; ok {
		st.UpdatedAt = at
		s.states[repoID] = st
	}
	return nil
}

func (s *fakeStore) AdvanceSHA(ctx context.Context, repoID, sha string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "AdvanceSHA"); err != nil {
		return err
	}
	if st, ok := s.states[repoID]; ok {
		st.LastIndexedSHA, st.UpdatedAt = sha, at
		s.states[repoID] = st
	}
	return nil
}

// --- IndexReader ---

func (s *fakeStore) GetRepo(ctx context.Context, repoID string) (domain.Repo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "GetRepo"); err != nil {
		return domain.Repo{}, err
	}
	r, ok := s.repos[repoID]
	if !ok {
		return domain.Repo{}, domain.ErrRepoNotFound
	}
	return r, nil
}

func (s *fakeStore) GetState(ctx context.Context, repoID string) (domain.IndexState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "GetState"); err != nil {
		return domain.IndexState{}, false, err
	}
	st, ok := s.states[repoID]
	return st, ok, nil
}

func (s *fakeStore) ListBackfill(ctx context.Context) ([]domain.Repo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "ListBackfill"); err != nil {
		return nil, err
	}
	var out []domain.Repo
	for _, r := range s.repos {
		st, ok := s.states[r.ID]
		if r.ClonePath != "" && (!ok || st.IndexerVersion != domain.IndexerVersion) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b domain.Repo) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (s *fakeStore) MapCandidates(ctx context.Context, repoID string) ([]domain.MapCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "MapCandidates"); err != nil {
		return nil, err
	}
	rankOf := map[string]float64{}
	for _, r := range s.data.ranks {
		rankOf[r.Path] = r.Rank
	}
	var out []domain.MapCandidate
	for _, sym := range s.data.symbols {
		rk, ok := rankOf[sym.Path]
		if sym.Signature == "" || !ok {
			continue
		}
		out = append(out, domain.MapCandidate{Path: sym.Path, Name: sym.Name, Exported: sym.Exported,
			Line: sym.Line, Signature: sym.Signature, Rank: rk})
	}
	slices.SortStableFunc(out, func(a, b domain.MapCandidate) int {
		switch {
		case a.Rank != b.Rank:
			if a.Rank > b.Rank {
				return -1
			}
			return 1
		case a.Exported != b.Exported:
			if a.Exported {
				return -1
			}
			return 1
		case a.Line != b.Line:
			return a.Line - b.Line
		case a.Name != b.Name:
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out, nil
}

func (s *fakeStore) CachedMap(ctx context.Context, repoID, sha string, budget int) (domain.RepoMap, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "CachedMap"); err != nil {
		return domain.RepoMap{}, false, err
	}
	for _, m := range s.data.maps {
		if m.CommitSHA == sha && m.Budget == budget {
			return m, true, nil
		}
	}
	return domain.RepoMap{}, false, nil
}

func (s *fakeStore) FileRanks(ctx context.Context, repoID string, paths []string) ([]domain.PathPercentile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "FileRanks"); err != nil {
		return nil, err
	}
	var out []domain.PathPercentile
	for _, r := range s.data.ranks {
		if slices.Contains(paths, r.Path) {
			out = append(out, domain.PathPercentile{Path: r.Path, Percentile: r.Percentile})
		}
	}
	return out, nil
}

func (s *fakeStore) SymbolsInFiles(ctx context.Context, repoID string, paths []string) ([]domain.SymbolInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "SymbolsInFiles"); err != nil {
		return nil, err
	}
	var out []domain.SymbolInfo
	for _, r := range s.data.symbols {
		if slices.Contains(paths, r.Path) {
			sig := r.Signature
			out = append(out, domain.SymbolInfo{File: r.Path, Name: r.Name, Kind: r.Kind, Exported: r.Exported,
				StartLine: r.Line, EndLine: r.EndLine, Signature: &sig})
		}
	}
	return out, nil
}

func (s *fakeStore) ResolvedCallers(ctx context.Context, repoID string, declFiles, names []string) ([]domain.ResolvedCaller, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "ResolvedCallers"); err != nil {
		return nil, err
	}
	var out []domain.ResolvedCaller
	for _, c := range s.callers {
		if slices.Contains(names, c.ToSymbol) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *fakeStore) FileFacts(ctx context.Context, repoID string, files []string) ([]domain.FileFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "FileFacts"); err != nil {
		return nil, err
	}
	var out []domain.FileFacts
	for _, f := range s.factRows {
		if slices.Contains(files, f.Path) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *fakeStore) RankedPaths(ctx context.Context, repoID string, limit int) ([]domain.RankedPath, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "RankedPaths"); err != nil {
		return nil, err
	}
	var out []domain.RankedPath
	for _, r := range s.data.ranks {
		out = append(out, domain.RankedPath{Path: r.Path, Rank: r.Rank})
	}
	slices.SortStableFunc(out, func(a, b domain.RankedPath) int {
		if a.Rank != b.Rank {
			if a.Rank > b.Rank {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) Edges(ctx context.Context, repoID string) ([]domain.Edge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.hook(ctx, "Edges"); err != nil {
		return nil, err
	}
	return slices.Clone(s.data.edges), nil
}

var (
	_ app.IndexReader = (*fakeStore)(nil)
	_ app.IndexWriter = (*fakeStore)(nil)
	_ app.Tx          = (*fakeStore)(nil)
)

// --- clone, parser, git, graph, clock ---

// fakeClone is a SourceFS over an in-memory file map.
type fakeClone struct {
	files   map[string][]byte
	walk    *domain.WalkResult
	walkErr error
	// unreadable paths fail to read.
	unreadable map[string]bool
}

func (c *fakeClone) Walk(ctx context.Context, root string) (domain.WalkResult, error) {
	if c.walkErr != nil {
		return domain.WalkResult{}, c.walkErr
	}
	if c.walk != nil {
		return *c.walk, nil
	}
	var files []string
	for p := range c.files {
		files = append(files, p)
	}
	slices.Sort(files)
	return domain.WalkResult{Files: files, Stats: domain.WalkStats{TotalCandidates: len(files)}}, nil
}

func (c *fakeClone) ReadFile(root, rel string) ([]byte, error) {
	b, ok := c.files[rel]
	if !ok || c.unreadable[rel] {
		return nil, fmt.Errorf("read %s: %w", rel, errors.New("no such file"))
	}
	return b, nil
}

// fakeAnalyzer returns canned extractions by path; a delay entry blocks until the context ends.
type fakeAnalyzer struct {
	ex      map[string]domain.Extraction
	imports map[string][]string
	// packages is the package key Imports reports per path ("" when absent).
	packages map[string]string
	// hang paths block in Extract until ctx ends, then return its error.
	hang map[string]bool
	// panicOn paths panic in Extract.
	panicOn map[string]bool
	// hangImports and panicImports make Imports block until ctx ends or panic.
	hangImports, panicImports map[string]bool
	// onExtract runs when Extract is called for a path (before it answers).
	onExtract func(path string)
	// seen records the text Extract was given, per path.
	mu   sync.Mutex
	seen map[string]string
}

func (a *fakeAnalyzer) Extract(ctx context.Context, path, text string) (domain.Extraction, error) {
	a.mu.Lock()
	if a.seen == nil {
		a.seen = map[string]string{}
	}
	a.seen[path] = text
	a.mu.Unlock()
	if a.onExtract != nil {
		a.onExtract(path)
	}
	if a.panicOn[path] {
		panic("boom")
	}
	if a.hang[path] {
		<-ctx.Done()
		return domain.Extraction{}, fmt.Errorf("parse: %w", ctx.Err())
	}
	return a.ex[path], nil
}

func (a *fakeAnalyzer) Imports(ctx context.Context, path, text string) (domain.FileImports, error) {
	if a.panicImports[path] {
		panic("imports exploded")
	}
	if a.hangImports[path] {
		<-ctx.Done()
		return domain.FileImports{}, fmt.Errorf("parse: %w", ctx.Err())
	}
	return domain.FileImports{Package: a.packages[path], Specs: a.imports[path]}, nil
}

// fakeGit serves HEAD and diffs.
type fakeGit struct {
	head    string
	headErr error
	diff    []string
	diffErr error
	// diffBase records the base asked for.
	diffBase string
	// keys records the "owner/name" every call located the clone under.
	keys []string

	// remote default branch and sync behaviour for the resync job.
	branch    string
	branchErr error
	syncErr   error
	// synced records the branches Sync was called with.
	synced []string
	// onSync runs inside Sync (before it answers).
	onSync func(ctx context.Context) error
}

func (g *fakeGit) DefaultBranch(ctx context.Context, owner, name string) (string, error) {
	g.keys = append(g.keys, owner+"/"+name)
	return g.branch, g.branchErr
}

func (g *fakeGit) Sync(ctx context.Context, owner, name, branch string) (string, error) {
	g.keys = append(g.keys, owner+"/"+name)
	g.synced = append(g.synced, branch)
	if g.onSync != nil {
		if err := g.onSync(ctx); err != nil {
			return "", err
		}
	}
	return g.head, g.syncErr
}

func (g *fakeGit) Head(ctx context.Context, owner, name string) (string, error) {
	g.keys = append(g.keys, owner+"/"+name)
	return g.head, g.headErr
}

func (g *fakeGit) DiffNames(ctx context.Context, owner, name, base, head string) ([]string, error) {
	g.keys = append(g.keys, owner+"/"+name)
	g.diffBase = base
	return g.diff, g.diffErr
}

// fakeGraph returns a graph of canned edges.
type fakeGraph struct {
	edges []domain.Edge
	err   error
	// panics makes BuildGraph panic.
	panics bool
}

func (g *fakeGraph) BuildGraph(ctx context.Context, root string, files []string) (domain.Graph, error) {
	if g.panics {
		panic("graph exploded")
	}
	if g.err != nil {
		return domain.Graph{}, g.err
	}
	return domain.Graph{Edges: g.edges, Packages: []domain.FilePackage{}}, nil
}

// fakeClock is a settable clock safe for concurrent use; every Now call may advance it.
type fakeClock struct {
	mu   sync.Mutex
	t    time.Time
	tick func(calls int) time.Duration
	n    int
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	if c.tick != nil {
		c.t = c.t.Add(c.tick(c.n))
	}
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// lenTokens counts one token per four bytes.
type lenTokens struct{}

func (lenTokens) Count(text string) int { return (len(text) + 3) / 4 }

// maskKey redacts "sk-secret".
type maskKey struct{}

func (maskKey) Redact(s string) string { return strings.ReplaceAll(s, "sk-secret", "[redacted]") }

// fakeQueue records enqueued jobs.
type fakeQueue struct {
	mu   sync.Mutex
	jobs []queuedJob
	err  error
	// failFor makes Enqueue fail for payloads containing the text.
	failFor string
}

type queuedJob struct {
	Workspace, Kind, Payload string
}

func (q *fakeQueue) Enqueue(ctx context.Context, workspaceID, kind string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil || (q.failFor != "" && strings.Contains(string(raw), q.failFor)) {
		if q.err != nil {
			return "", q.err
		}
		return "", errors.New("queue full")
	}
	q.jobs = append(q.jobs, queuedJob{workspaceID, kind, string(raw)})
	return fmt.Sprintf("job-%d", len(q.jobs)), nil
}

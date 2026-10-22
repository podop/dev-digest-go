package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

// runStore serves the calls RunContext makes; any other call panics on the nil embedded port.
type runStore struct {
	store

	agentPaths []string
	skillPaths map[string][]string
	files      map[string]string
	repo       Repo
	repoFound  bool
	err        error
}

func (s *runStore) AgentPaths(context.Context, string, string) ([]string, error) {
	return s.agentPaths, s.err
}
func (s *runStore) SkillPaths(_ context.Context, skillID, _ string) ([]string, error) {
	return s.skillPaths[skillID], s.err
}
func (s *runStore) FindFile(_ context.Context, _, path string) (File, bool, error) {
	content, ok := s.files[path]
	return File{Path: path, Content: content, Version: 1}, ok, s.err
}
func (s *runStore) FindRepo(context.Context, string, string) (Repo, bool, error) {
	return s.repo, s.repoFound, s.err
}

// fakeGit resolves commit and serves blobs by path; blobErr beats blobs.
type fakeGit struct {
	commit    string
	commitErr error
	blobs     map[string][]byte
	blobErr   map[string]error
	onBlob    func() // runs at the start of every BlobAt

	mu        sync.Mutex
	baseArgs  []string
	blobCalls []string
	maxArg    int64
	commitArg string
}

func (g *fakeGit) BaseCommit(_ context.Context, owner, name, baseRef, headSHA string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.baseArgs = []string{owner, name, baseRef, headSHA}
	return g.commit, g.commitErr
}

func (g *fakeGit) BlobAt(_ context.Context, _, _, commit, path string, max int64) ([]byte, error) {
	g.mu.Lock()
	g.blobCalls = append(g.blobCalls, path)
	g.maxArg, g.commitArg = max, commit
	g.mu.Unlock()
	if g.onBlob != nil {
		g.onBlob()
	}
	if err := g.blobErr[path]; err != nil {
		return nil, err
	}
	b, ok := g.blobs[path]
	if !ok {
		return nil, gitcli.ErrBlobMissing
	}
	return b, nil
}

// panicGit panics on the blobs named in panics and serves the others.
type panicGit struct {
	fakeGit
	panics map[string]any
}

func (g *panicGit) BlobAt(ctx context.Context, owner, name, commit, path string, max int64) ([]byte, error) {
	if v, ok := g.panics[path]; ok {
		panic(v)
	}
	return g.fakeGit.BlobAt(ctx, owner, name, commit, path, max)
}

func newRunService(t *testing.T, st *runStore, g gitReader) *Service {
	t.Helper()
	svc, err := NewService(st, nil, nil, domain.DefaultGlobs, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if g == nil {
		return svc
	}
	return svc.WithGit(g)
}

var runIn = RunContextInput{
	WorkspaceID: "w", RepoID: "r", Owner: "acme", Name: "app", Base: "main", HeadSHA: "abcdef1234", AgentID: "a1",
	Skills: []RunSkill{{ID: "s1", Name: "Style"}},
}

func docStatus(docs []domain.RunDoc) map[string]domain.RunStatus {
	out := make(map[string]domain.RunStatus, len(docs))
	for _, d := range docs {
		out[d.Path] = d.Status
	}
	return out
}

func TestRunContext_NothingAttached(t *testing.T) {
	st := &runStore{}
	svc := newRunService(t, st, nil) // no git: it must not be needed
	got, err := svc.RunContext(t.Context(), runIn)
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	want := RunContext{BudgetTokens: 16_000}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("RunContext() mismatch (-want +got):\n%s", diff)
	}
}

// AC4053: store file included, 300,000 bytes too large, absent path missing, skill origin kept.
func TestRunContext_StatusesAndOrigins(t *testing.T) {
	big := []byte(strings.Repeat("x", 300_000))
	st := &runStore{
		agentPaths: []string{".devdigest/specs/d1.md", "docs/d2.md"},
		skillPaths: map[string][]string{"s1": {"docs/d2.md", "docs/d3.md"}},
		files:      map[string]string{".devdigest/specs/d1.md": "store text"},
	}
	g := &fakeGit{
		commit:  strings.Repeat("a", 40),
		blobs:   map[string][]byte{"docs/d2.md": big},
		blobErr: map[string]error{"docs/d2.md": gitcli.ErrBlobTooLarge},
	}
	svc := newRunService(t, st, g)

	got, err := svc.RunContext(t.Context(), runIn)
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	text := "store text"
	want := []domain.RunDoc{
		{Path: ".devdigest/specs/d1.md", DocType: domain.DocTypeSpecs, Origin: domain.RunOrigin{Kind: "agent"},
			Tokens: 3, Status: domain.RunIncluded, Source: "store", Text: &text},
		{Path: "docs/d2.md", DocType: domain.DocTypeDocs, Origin: domain.RunOrigin{Kind: "agent"},
			Status: domain.RunTooLarge, Source: "repo"},
		{Path: "docs/d3.md", DocType: domain.DocTypeDocs,
			Origin: domain.RunOrigin{Kind: "skill", SkillID: "s1", SkillName: "Style"},
			Status: domain.RunMissing, Source: "repo"},
	}
	if diff := cmp.Diff(want, got.Docs); diff != "" {
		t.Errorf("RunContext().Docs mismatch (-want +got):\n%s", diff)
	}
	if got.TokensTotal != 3 || got.BudgetTokens != 16_000 {
		t.Errorf("RunContext() tokens = %d budget = %d, want 3 and 16000", got.TokensTotal, got.BudgetTokens)
	}
	if wantLine := "project context: 1 included, 2 skipped · ~3 tokens"; got.LogLine != wantLine {
		t.Errorf("RunContext().LogLine = %q, want %q", got.LogLine, wantLine)
	}
	if diff := cmp.Diff([]string{"acme", "app", "main", "abcdef1234"}, g.baseArgs); diff != "" {
		t.Errorf("BaseCommit args mismatch (-want +got):\n%s", diff)
	}
	if g.maxArg != 262_144 || g.commitArg != g.commit {
		t.Errorf("BlobAt(max = %d, commit = %q), want 262144 and %q", g.maxArg, g.commitArg, g.commit)
	}
	for _, p := range g.blobCalls {
		if strings.HasPrefix(p, ".devdigest/") {
			t.Errorf("BlobAt(%q): a store file was read through git", p)
		}
	}
}

// AC4054: budget applied in list order.
func TestRunContext_Budget(t *testing.T) {
	doc := func(tokens int) []byte { return []byte(strings.Repeat("x", tokens*4)) }
	tests := []struct {
		name       string
		tokens     [3]int
		want       [3]domain.RunStatus
		wantTotal  int
		wantTextAt [3]bool
	}{
		{
			name: "all fit", tokens: [3]int{7000, 7000, 1000}, wantTotal: 15_000,
			want:       [3]domain.RunStatus{domain.RunIncluded, domain.RunIncluded, domain.RunIncluded},
			wantTextAt: [3]bool{true, true, true},
		},
		{
			name: "second cuts the rest", tokens: [3]int{7000, 10_000, 1000}, wantTotal: 7000,
			want:       [3]domain.RunStatus{domain.RunIncluded, domain.RunOverBudget, domain.RunOverBudget},
			wantTextAt: [3]bool{true, false, false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{"docs/a.md", "docs/b.md", "docs/c.md"}
			g := &fakeGit{commit: strings.Repeat("b", 40), blobs: map[string][]byte{}}
			for i, p := range paths {
				g.blobs[p] = doc(tc.tokens[i])
			}
			svc := newRunService(t, &runStore{agentPaths: paths}, g)
			got, err := svc.RunContext(t.Context(), runIn)
			if err != nil {
				t.Fatalf("RunContext() error = %v", err)
			}
			if got.TokensTotal != tc.wantTotal {
				t.Errorf("RunContext().TokensTotal = %d, want %d", got.TokensTotal, tc.wantTotal)
			}
			for i, d := range got.Docs {
				if d.Status != tc.want[i] || (d.Text != nil) != tc.wantTextAt[i] {
					t.Errorf("RunContext().Docs[%d] = status %s text %v, want %s text %v",
						i, d.Status, d.Text != nil, tc.want[i], tc.wantTextAt[i])
				}
			}
		})
	}
}

// EC4037 and the no-base-commit rule: store files do not need a clone.
func TestRunContext_NoBaseCommit(t *testing.T) {
	clone := "/clones/acme/app"
	tests := []struct {
		name      string
		repo      Repo
		repoFound bool
		want      domain.RunStatus
	}{
		{name: "never cloned", repo: Repo{ID: "r"}, repoFound: true, want: domain.RunMissing},
		{name: "repo row gone", want: domain.RunMissing},
		{name: "cloned but base unresolved", repo: Repo{ID: "r", ClonePath: &clone}, repoFound: true, want: domain.RunUnreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &runStore{
				agentPaths: []string{".devdigest/specs/s.md", "docs/a.md"},
				files:      map[string]string{".devdigest/specs/s.md": "kept"},
				repo:       tc.repo, repoFound: tc.repoFound,
			}
			g := &fakeGit{} // BaseCommit answers ""
			svc := newRunService(t, st, g)
			got, err := svc.RunContext(t.Context(), runIn)
			if err != nil {
				t.Fatalf("RunContext() error = %v", err)
			}
			want := map[string]domain.RunStatus{".devdigest/specs/s.md": domain.RunIncluded, "docs/a.md": tc.want}
			if diff := cmp.Diff(want, docStatus(got.Docs)); diff != "" {
				t.Errorf("RunContext() statuses mismatch (-want +got):\n%s", diff)
			}
			if len(g.blobCalls) != 0 {
				t.Errorf("BlobAt called for %v without a base commit", g.blobCalls)
			}
		})
	}
}

func TestRunContext_BaseCommitFailureIsNoCommit(t *testing.T) {
	st := &runStore{agentPaths: []string{"docs/a.md"}, repoFound: true}
	g := &fakeGit{commitErr: errors.New("git exploded")}
	got, err := newRunService(t, st, g).RunContext(t.Context(), runIn)
	if err != nil {
		t.Fatalf("RunContext() error = %v, want nil", err)
	}
	if s := got.Docs[0].Status; s != domain.RunMissing {
		t.Errorf("RunContext().Docs[0].Status = %s, want missing (no clone)", s)
	}
}

func TestRunContext_UnreadableCases(t *testing.T) {
	st := &runStore{agentPaths: []string{"../escape.md", "docs/odd.md", "docs/ok.md"}}
	g := &fakeGit{
		commit:  strings.Repeat("c", 40),
		blobs:   map[string][]byte{"docs/ok.md": []byte("ok")},
		blobErr: map[string]error{"docs/odd.md": errors.New("git: bad object")},
	}
	got, err := newRunService(t, st, g).RunContext(t.Context(), runIn)
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	want := map[string]domain.RunStatus{
		"../escape.md": domain.RunUnreadable, "docs/odd.md": domain.RunUnreadable, "docs/ok.md": domain.RunIncluded,
	}
	if diff := cmp.Diff(want, docStatus(got.Docs)); diff != "" {
		t.Errorf("RunContext() statuses mismatch (-want +got):\n%s", diff)
	}
	for _, p := range g.blobCalls {
		if p == "../escape.md" {
			t.Errorf("BlobAt(%q): an unsafe path reached git", p)
		}
	}
}

func TestRunContext_DecodesLikeNode(t *testing.T) {
	st := &runStore{agentPaths: []string{"docs/a.md"}}
	g := &fakeGit{commit: strings.Repeat("d", 40), blobs: map[string][]byte{"docs/a.md": []byte("a\xe2\x82b")}}
	got, err := newRunService(t, st, g).RunContext(t.Context(), runIn)
	if err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	if text := *got.Docs[0].Text; text != "a�b" { // E2 82 is one maximal invalid subpart
		t.Errorf("RunContext() text = %q, want %q", text, "a�b")
	}
}

func TestRunContext_Errors(t *testing.T) {
	boom := errors.New("db down")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name string
		st   *runStore
		g    gitReader
		ctx  context.Context
		want error
	}{
		{name: "store failure", st: &runStore{err: boom}, g: &fakeGit{}, ctx: t.Context(), want: boom},
		{name: "store file lookup failure", st: &runStore{agentPaths: []string{".devdigest/specs/a.md"}, err: boom}, g: &fakeGit{}, ctx: t.Context(), want: boom},
		{name: "git not configured", st: &runStore{agentPaths: []string{"docs/a.md"}}, g: nil, ctx: t.Context()},
		{
			name: "cancelled while resolving", ctx: cancelled, want: context.Canceled,
			st: &runStore{agentPaths: []string{"docs/a.md"}}, g: &fakeGit{commitErr: context.Canceled},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newRunService(t, tc.st, tc.g).RunContext(tc.ctx, runIn)
			if err == nil {
				t.Fatalf("RunContext() error = nil, want an error; docs = %v", got.Docs)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("RunContext() error = %v, want it to wrap %v", err, tc.want)
			}
			if len(got.Docs) != 0 || got.LogLine != "" || got.BudgetTokens != 16_000 {
				t.Errorf("RunContext() = %+v on error, want no documents", got)
			}
		})
	}
}

// A read that fails because the run was cancelled is an error, not an unreadable document.
func TestRunContext_CancelledWhileReading(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st := &runStore{agentPaths: []string{"docs/a.md"}}
	g := &fakeGit{
		commit:  strings.Repeat("e", 40),
		blobErr: map[string]error{"docs/a.md": errors.New("signal: killed")},
		onBlob:  cancel,
	}
	_, err := newRunService(t, st, g).RunContext(ctx, runIn)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunContext() error = %v, want it to wrap context.Canceled", err)
	}
}

// gateGit blocks every BlobAt until release closes and counts the reads in flight.
type gateGit struct {
	fakeGit
	release  chan struct{}
	inflight atomic.Int32
	peak     atomic.Int32
}

func (g *gateGit) BlobAt(_ context.Context, _, _, _, _ string, _ int64) ([]byte, error) {
	n := g.inflight.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-g.release
	g.inflight.Add(-1)
	return []byte("text"), nil
}

func TestRunContext_ReadConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		paths := make([]string, 20)
		for i := range paths {
			paths[i] = fmt.Sprintf("docs/d%02d.md", i)
		}
		g := &gateGit{fakeGit: fakeGit{commit: strings.Repeat("f", 40)}, release: make(chan struct{})}
		svc := newRunService(t, &runStore{agentPaths: paths}, g)

		var (
			got RunContext
			err error
			wg  sync.WaitGroup
		)
		wg.Go(func() { got, err = svc.RunContext(t.Context(), runIn) })
		synctest.Wait() // every goroutine is blocked: the limiter has let in all it will
		if n := g.inflight.Load(); n != 8 {
			t.Errorf("reads in flight while blocked = %d, want 8", n)
		}
		close(g.release)
		wg.Wait()

		if err != nil {
			t.Fatalf("RunContext() error = %v", err)
		}
		if peak := g.peak.Load(); peak != 8 {
			t.Errorf("peak reads in flight = %d, want 8", peak)
		}
		if len(got.Docs) != 20 {
			t.Fatalf("RunContext() returned %d docs, want 20", len(got.Docs))
		}
		for i, d := range got.Docs {
			if d.Path != paths[i] || d.Status != domain.RunIncluded {
				t.Errorf("RunContext().Docs[%d] = %s %s, want %s included", i, d.Path, d.Status, paths[i])
			}
		}
	})
}

// A panic in one reader must neither crash the process nor lose the other documents: the document
// becomes unreadable and the log line names the panic's type, not its value.
func TestRunContext_ReaderPanicMarksTheDocUnreadable(t *testing.T) {
	st := &runStore{agentPaths: []string{"docs/ok.md", "docs/boom.md", "docs/boom2.md"}}
	g := &panicGit{
		fakeGit: fakeGit{commit: "c0ffee", blobs: map[string][]byte{"docs/ok.md": []byte("fine")}},
		panics:  map[string]any{"docs/boom.md": "secret document content", "docs/boom2.md": errors.New("also secret")},
	}
	var logs strings.Builder
	svc, err := NewService(st, nil, nil, domain.DefaultGlobs, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc = svc.WithGit(g)
	in := runIn
	in.Skills = nil

	got, err := svc.RunContext(t.Context(), in)
	if err != nil {
		t.Fatalf("RunContext() error = %v, want the panics absorbed", err)
	}
	want := map[string]domain.RunStatus{
		"docs/ok.md": domain.RunIncluded, "docs/boom.md": domain.RunUnreadable, "docs/boom2.md": domain.RunUnreadable,
	}
	if diff := cmp.Diff(want, docStatus(got.Docs)); diff != "" {
		t.Errorf("RunContext() statuses mismatch (-want +got):\n%s", diff)
	}
	out := logs.String()
	if strings.Count(out, "reading a run document panicked") != 2 {
		t.Errorf("log = %s, want one line per panic", out)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("log = %s, want no panic value", out)
	}
}

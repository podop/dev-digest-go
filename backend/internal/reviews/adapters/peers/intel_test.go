package peers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	pcapp "dev-digest-go/backend/internal/projectcontext/app"
	pcdomain "dev-digest-go/backend/internal/projectcontext/domain"
	repointel "dev-digest-go/backend/internal/repointel/domain"
	"dev-digest-go/backend/internal/reviews/adapters/peers"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

type fakeIntel struct {
	callers []repointel.CallerSignature
	mapRes  repointel.RepoMapResult
	ranks   []repointel.PathPercentile
	err     error
	// got records the arguments of the last call.
	repoID  string
	changed []string
	limit   int
	budget  int
	paths   []string
}

func (f *fakeIntel) CallerSignatures(_ context.Context, repoID string, changed []string, limit int) ([]repointel.CallerSignature, error) {
	f.repoID, f.changed, f.limit = repoID, changed, limit
	return f.callers, f.err
}

func (f *fakeIntel) RepoMap(_ context.Context, repoID string, budget int) (repointel.RepoMapResult, error) {
	f.repoID, f.budget = repoID, budget
	return f.mapRes, f.err
}

func (f *fakeIntel) FileRank(_ context.Context, repoID string, paths []string) ([]repointel.PathPercentile, error) {
	f.repoID, f.paths = repoID, paths
	return f.ranks, f.err
}

func TestRepoIntel_MapsRowsAndArguments(t *testing.T) {
	f := &fakeIntel{
		callers: []repointel.CallerSignature{{File: "a.ts", Symbol: "run", Signature: "function run()", Rank: 90}},
		mapRes:  repointel.RepoMapResult{Text: "map", Tokens: 12, Cached: true, Degraded: true},
		ranks:   []repointel.PathPercentile{{Path: "a.ts", Percentile: 97}},
	}
	a := peers.NewRepoIntel(f)

	gotCallers, err := a.CallerSignatures(t.Context(), "repo", []string{"a.ts"}, 20)
	if err != nil {
		t.Fatalf("CallerSignatures() error = %v", err)
	}
	if diff := cmp.Diff([]domain.CallerSignature{{File: "a.ts", Symbol: "run", Signature: "function run()"}}, gotCallers); diff != "" {
		t.Errorf("CallerSignatures() mismatch (-want +got):\n%s", diff)
	}
	if f.repoID != "repo" || f.limit != 20 || len(f.changed) != 1 {
		t.Errorf("CallerSignatures() passed repo %q changed %v limit %d", f.repoID, f.changed, f.limit)
	}

	gotMap, err := a.RepoMap(t.Context(), "repo", 2048)
	if err != nil {
		t.Fatalf("RepoMap() error = %v", err)
	}
	if want := (domain.RepoMap{Text: "map", Tokens: 12, Cached: true, Degraded: true}); gotMap != want || f.budget != 2048 {
		t.Errorf("RepoMap() = %+v (budget %d), want %+v (2048)", gotMap, f.budget, want)
	}

	gotRank, err := a.FileRank(t.Context(), "repo", []string{"a.ts", "b.ts"})
	if err != nil {
		t.Fatalf("FileRank() error = %v", err)
	}
	if diff := cmp.Diff([]domain.PathRank{{Path: "a.ts", Percentile: 97}}, gotRank); diff != "" {
		t.Errorf("FileRank() mismatch (-want +got):\n%s", diff)
	}
	if len(f.paths) != 2 {
		t.Errorf("FileRank() passed paths %v", f.paths)
	}
}

func TestRepoIntel_ErrorsPassThrough(t *testing.T) {
	boom := errors.New("index unreadable")
	a := peers.NewRepoIntel(&fakeIntel{err: boom})
	if _, err := a.CallerSignatures(t.Context(), "r", nil, 1); !errors.Is(err, boom) {
		t.Errorf("CallerSignatures() error = %v, want %v", err, boom)
	}
	if _, err := a.RepoMap(t.Context(), "r", 1); !errors.Is(err, boom) {
		t.Errorf("RepoMap() error = %v, want %v", err, boom)
	}
	if _, err := a.FileRank(t.Context(), "r", nil); !errors.Is(err, boom) {
		t.Errorf("FileRank() error = %v, want %v", err, boom)
	}
}

type fakeRunContext struct {
	rc  pcapp.RunContext
	err error
	in  pcapp.RunContextInput
}

func (f *fakeRunContext) RunContext(_ context.Context, in pcapp.RunContextInput) (pcapp.RunContext, error) {
	f.in = in
	return f.rc, f.err
}

func TestProjectContext_MapsInputAndDocuments(t *testing.T) {
	text, leaked := "the spec", "must not travel"
	f := &fakeRunContext{rc: pcapp.RunContext{
		Docs: []pcdomain.RunDoc{
			{Path: "docs/a.md", DocType: pcdomain.DocType("spec"), Origin: pcdomain.RunOrigin{Kind: pcdomain.OriginAgent}, Tokens: 3,
				Status: pcdomain.RunIncluded, Source: pcdomain.RunSourceRepo, Text: &text},
			{Path: "docs/b.md", DocType: pcdomain.DocType("adr"), Origin: pcdomain.RunOrigin{Kind: pcdomain.OriginSkill, SkillID: "s1", SkillName: "Sk"},
				Status: pcdomain.RunOverBudget, Source: pcdomain.RunSourceStore, Text: &leaked},
		},
		TokensTotal: 3, BudgetTokens: 16000, LogLine: "project context: 1 included, 1 skipped · ~3 tokens",
	}}
	a := peers.NewProjectContext(f)
	got, err := a.ResolveForRun(t.Context(), reviewsapp.ProjectContextInput{
		WorkspaceID: "w", Repo: domain.ReviewRepo{ID: "repo", Owner: "acme", Name: "api"}, Base: "main", HeadSHA: "abc1234",
		AgentID: "ag", Skills: []domain.ReviewSkill{{ID: "s1", Name: "Sk", Body: "ignored"}},
	})
	if err != nil {
		t.Fatalf("ResolveForRun() error = %v", err)
	}
	wantIn := pcapp.RunContextInput{
		WorkspaceID: "w", RepoID: "repo", Owner: "acme", Name: "api", Base: "main", HeadSHA: "abc1234", AgentID: "ag",
		Skills: []pcapp.RunSkill{{ID: "s1", Name: "Sk"}},
	}
	if diff := cmp.Diff(wantIn, f.in); diff != "" {
		t.Errorf("RunContext input mismatch (-want +got):\n%s", diff)
	}
	want := reviewsapp.ProjectContextResult{
		Docs: []domain.ContextDoc{
			{Path: "docs/a.md", DocType: "spec", Origin: domain.ContextOrigin{Kind: "agent"}, Tokens: 3,
				Status: domain.ContextIncluded, Source: "repo", Text: &text},
			{Path: "docs/b.md", DocType: "adr", Origin: domain.ContextOrigin{Kind: "skill", SkillID: "s1", SkillName: "Sk"},
				Status: domain.ContextOverBudget, Source: "store"},
		},
		TokensTotal: 3, BudgetTokens: 16000, LogLine: f.rc.LogLine,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ResolveForRun() mismatch (-want +got):\n%s", diff)
	}
}

func TestProjectContext_GitLabRepoPassesTheCloneKey(t *testing.T) {
	f := &fakeRunContext{}
	_, err := peers.NewProjectContext(f).ResolveForRun(t.Context(), reviewsapp.ProjectContextInput{
		WorkspaceID: "w", Base: "main", HeadSHA: "abc1234",
		Repo: domain.ReviewRepo{ID: "repo", Owner: "grp/sub", Name: "web", Provider: "gitlab", Host: "git.corp.example:8443", ProjectID: 4242},
	})
	if err != nil {
		t.Fatalf("ResolveForRun() error = %v", err)
	}
	if f.in.Owner != "@gitlab/git.corp.example_8443" || f.in.Name != "4242" {
		t.Errorf("RunContext(Owner, Name) = %q, %q, want the clone key %q, %q", f.in.Owner, f.in.Name, "@gitlab/git.corp.example_8443", "4242")
	}
}

func TestProjectContext_NothingAttachedAndErrors(t *testing.T) {
	a := peers.NewProjectContext(&fakeRunContext{rc: pcapp.RunContext{BudgetTokens: 16000}})
	got, err := a.ResolveForRun(t.Context(), reviewsapp.ProjectContextInput{})
	if err != nil || len(got.Docs) != 0 || got.LogLine != "" || got.BudgetTokens != 16000 {
		t.Errorf("ResolveForRun() = %+v, %v, want no docs, no line, budget 16000", got, err)
	}
	boom := errors.New("db down")
	_, err = peers.NewProjectContext(&fakeRunContext{err: boom}).ResolveForRun(t.Context(), reviewsapp.ProjectContextInput{})
	if !errors.Is(err, boom) {
		t.Errorf("ResolveForRun() error = %v, want %v", err, boom)
	}
}

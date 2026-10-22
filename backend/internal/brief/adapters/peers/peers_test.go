package peers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	blastdomain "dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/brief/adapters/peers"
	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/platform/github"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	reviewsdomain "dev-digest-go/backend/internal/reviews/domain"
)

type fakeProvider struct {
	res     blastdomain.BlastRadius
	err     error
	repo    string
	changed []string
}

func (f *fakeProvider) RadiusFor(_ context.Context, repoID string, changed []string) (blastdomain.BlastRadius, error) {
	f.repo, f.changed = repoID, changed
	return f.res, f.err
}

func TestBlast_MapsTheRadius(t *testing.T) {
	fp := &fakeProvider{res: blastdomain.BlastRadius{
		ChangedSymbols: []blastdomain.ChangedSymbol{{Name: "limit", File: "a.ts", Kind: "function"}},
		Downstream: []blastdomain.Downstream{{
			Symbol: "limit", Callers: []blastdomain.CallerRef{{Name: "register", File: "b.ts", Line: 22}},
			EndpointsAffected: []string{"GET /health"},
		}},
		Summary: "1 changed symbol", Degraded: true, Reason: blastdomain.ReasonIndexPartial,
	}}
	got, err := peers.NewBlast(fp).RadiusFor(t.Context(), "repo-1", []string{"a.ts"})
	if err != nil {
		t.Fatalf("RadiusFor() error = %v", err)
	}
	want := domain.Blast{
		ChangedSymbols: []domain.BlastSymbol{{Name: "limit", File: "a.ts", Kind: "function"}},
		Downstream: []domain.BlastGroup{{
			Symbol: "limit", Callers: []domain.BlastCaller{{Name: "register", File: "b.ts", Line: 22}},
			EndpointsAffected: []string{"GET /health"}, CronsAffected: []string{},
		}},
		Summary: "1 changed symbol", Degraded: true, Reason: "index_partial",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("RadiusFor() mismatch (-want +got):\n%s", diff)
	}
	if fp.repo != "repo-1" || !cmp.Equal(fp.changed, []string{"a.ts"}) {
		t.Errorf("provider asked for %q %v, want repo-1 [a.ts]", fp.repo, fp.changed)
	}
}

func TestBlast_ErrorPassesThrough(t *testing.T) {
	boom := errors.New("db down")
	_, err := peers.NewBlast(&fakeProvider{err: boom}).RadiusFor(t.Context(), "r", nil)
	if !errors.Is(err, boom) {
		t.Errorf("RadiusFor() error = %v, want the provider's", err)
	}
}

type fakeSkills struct {
	skills []reviewsdomain.ReviewSkill
	err    error
	agent  string
}

func (f *fakeSkills) EnabledSkills(_ context.Context, agentID string) ([]reviewsdomain.ReviewSkill, error) {
	f.agent = agentID
	return f.skills, f.err
}

type fakeResolver struct {
	res reviewsapp.ProjectContextResult
	err error
	in  reviewsapp.ProjectContextInput
}

func (f *fakeResolver) ResolveForRun(_ context.Context, in reviewsapp.ProjectContextInput) (reviewsapp.ProjectContextResult, error) {
	f.in = in
	return f.res, f.err
}

func TestDocuments_ForAgent(t *testing.T) {
	text, secretish := "A text", "not sent"
	skills := &fakeSkills{skills: []reviewsdomain.ReviewSkill{{ID: "s1", Name: "x", Version: 3}}}
	res := &fakeResolver{res: reviewsapp.ProjectContextResult{Docs: []reviewsdomain.ContextDoc{
		{Path: "docs/a.md", Status: reviewsdomain.ContextIncluded, Text: &text},
		{Path: "docs/b.md", Status: reviewsdomain.ContextOverBudget, Text: &secretish},
		{Path: "docs/c.md", Status: reviewsdomain.ContextMissing},
		{Path: "docs/d.md", Status: reviewsdomain.ContextIncluded}, // no text: included but empty
	}}}
	pull := domain.Pull{ID: "pr", RepoID: "repo-1", Owner: "acme", Name: "api", Base: "main", HeadSHA: "abc"}
	got, err := peers.NewDocuments(skills, res).ForAgent(t.Context(), app.DocumentsInput{WorkspaceID: "ws", Pull: pull, AgentID: "a1"})
	if err != nil {
		t.Fatalf("ForAgent() error = %v", err)
	}
	want := []domain.Document{
		{Path: "docs/a.md", Status: domain.DocIncluded, Text: "A text"},
		{Path: "docs/b.md", Status: domain.DocOverBudget},
		{Path: "docs/c.md", Status: domain.DocMissing},
		{Path: "docs/d.md", Status: domain.DocIncluded},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ForAgent() mismatch (-want +got):\n%s", diff)
	}
	wantIn := reviewsapp.ProjectContextInput{
		WorkspaceID: "ws", Repo: reviewsdomain.ReviewRepo{ID: "repo-1", Owner: "acme", Name: "api"},
		Base: "main", HeadSHA: "abc", AgentID: "a1", Skills: skills.skills,
	}
	if diff := cmp.Diff(wantIn, res.in, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("resolver input mismatch (-want +got):\n%s", diff)
	}
	if skills.agent != "a1" {
		t.Errorf("skills asked for agent %q, want a1", skills.agent)
	}
}

func TestDocuments_Errors(t *testing.T) {
	boom := errors.New("boom")
	in := app.DocumentsInput{AgentID: "a1"}
	if _, err := peers.NewDocuments(&fakeSkills{err: boom}, &fakeResolver{}).ForAgent(t.Context(), in); !errors.Is(err, boom) {
		t.Errorf("ForAgent(skills fail) error = %v, want the cause", err)
	}
	if _, err := peers.NewDocuments(&fakeSkills{}, &fakeResolver{err: boom}).ForAgent(t.Context(), in); !errors.Is(err, boom) {
		t.Errorf("ForAgent(resolver fails) error = %v, want the cause", err)
	}
}

type fakeGitHub struct {
	issue github.Issue
	err   error
}

func (f fakeGitHub) GetIssue(context.Context, string, string, int) (github.Issue, error) {
	return f.issue, f.err
}

func TestIssues(t *testing.T) {
	body := "Users hammer it."
	got, err := peers.NewIssues(fakeGitHub{issue: github.Issue{Number: 12, Title: "Rate limit", Body: &body, State: "open"}}).
		Issue(t.Context(), "acme", "api", 12)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if diff := cmp.Diff(domain.Issue{Number: 12, Title: "Rate limit", Body: &body}, got); diff != "" {
		t.Errorf("Issue() mismatch (-want +got):\n%s", diff)
	}

	_, err = peers.NewIssues(fakeGitHub{err: github.ErrNoToken}).Issue(t.Context(), "acme", "api", 12)
	if !errors.Is(err, peers.ErrNoToken) || err.Error() != "GITHUB_TOKEN is not configured" {
		t.Errorf("Issue(no token) error = %v, want %q", err, "GITHUB_TOKEN is not configured")
	}

	other := errors.New("404")
	if _, err := peers.NewIssues(fakeGitHub{err: other}).Issue(t.Context(), "a", "b", 1); !errors.Is(err, other) {
		t.Errorf("Issue(not found) error = %v, want the cause", err)
	}
}

func TestRoles(t *testing.T) {
	tests := []struct{ path, want string }{
		{"src/server.ts", "core"},
		{"src/server.test.ts", "tests"},
		{"README.md", "docs"},
		{"package-lock.json", "boilerplate"},
	}
	for _, tc := range tests {
		if got := (peers.Roles{}).Role(tc.path); got != tc.want {
			t.Errorf("Role(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

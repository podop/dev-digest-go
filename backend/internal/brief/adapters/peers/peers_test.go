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
	"dev-digest-go/backend/internal/platform/gitlab"
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

type fakeGitLab struct {
	issue gitlab.Issue
	err   error
	conn  gitlab.Conn
	pid   int64
	iid   int
	calls int
}

func (f *fakeGitLab) Issue(_ context.Context, conn gitlab.Conn, pid int64, iid int) (gitlab.Issue, error) {
	f.calls++
	f.conn, f.pid, f.iid = conn, pid, iid
	return f.issue, f.err
}

func resolver(b gitlab.Binding, ok bool, err error, hosts *[]string) gitlab.ResolveFunc {
	return func(_ context.Context, host string) (gitlab.Binding, bool, error) {
		*hosts = append(*hosts, host)
		return b, ok, err
	}
}

func TestGitLabIssues(t *testing.T) { // FR23, FR25
	body := "Throttle per key."
	b := gitlab.Binding{ID: "c1", Name: "corp", BaseURL: "https://git.example.com/gl", Host: "git.example.com", Token: "glpat-secret"}
	var hosts []string
	fg := &fakeGitLab{issue: gitlab.Issue{Number: 7, Title: "Rate limit", Body: &body, State: "open"}}
	got, err := peers.NewGitLabIssues(resolver(b, true, nil, &hosts), fg).Issue(t.Context(), "git.example.com", 42, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if diff := cmp.Diff(domain.Issue{Number: 7, Title: "Rate limit", Body: &body}, got); diff != "" {
		t.Errorf("Issue() mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"git.example.com"}, hosts); diff != "" {
		t.Errorf("resolved hosts mismatch (-want +got):\n%s", diff)
	}
	if fg.conn != b.Conn() || fg.pid != 42 || fg.iid != 7 {
		t.Errorf("client asked with conn %v project %d issue %d, want the binding's conn, 42, 7", fg.conn.BaseURL, fg.pid, fg.iid)
	}
}

func TestGitLabIssues_NoRequestWithoutAConnectionOrToken(t *testing.T) { // FR23
	cause := errors.New("db down")
	tests := []struct {
		name string
		b    gitlab.Binding
		ok   bool
		err  error
		want error
	}{
		{name: "not connected", ok: false, want: peers.ErrGitLabNotConnected},
		{name: "no token", b: gitlab.Binding{BaseURL: "https://git.example.com"}, ok: true, want: peers.ErrGitLabNoToken},
		{name: "lookup failed", err: cause, want: cause},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hosts []string
			fg := &fakeGitLab{}
			_, err := peers.NewGitLabIssues(resolver(tc.b, tc.ok, tc.err, &hosts), fg).Issue(t.Context(), "git.example.com", 42, 7)
			if !errors.Is(err, tc.want) {
				t.Errorf("Issue() error = %v, want %v", err, tc.want)
			}
			if fg.calls != 0 {
				t.Errorf("GitLab client calls = %d, want 0", fg.calls)
			}
		})
	}
}

func TestGitLabIssues_ClientErrorPassesThrough(t *testing.T) {
	cause := errors.New("404 Not found")
	var hosts []string
	b := gitlab.Binding{BaseURL: "https://git.example.com", Token: "t"}
	_, err := peers.NewGitLabIssues(resolver(b, true, nil, &hosts), &fakeGitLab{err: cause}).Issue(t.Context(), "git.example.com", 42, 7)
	if !errors.Is(err, cause) {
		t.Errorf("Issue() error = %v, want the cause", err)
	}
}

func TestRoles(t *testing.T) {
	tests := []struct{ provider, path, want string }{
		{"github", "src/server.ts", "core"},
		{"github", "src/server.test.ts", "tests"},
		{"github", "README.md", "docs"},
		{"github", "package-lock.json", "boilerplate"},
		{"github", ".github/workflows/ci.yml", "wiring"},
		{"github", ".gitlab-ci.yml", "core"}, // GitHub classifies exactly as before (D25)
		{"", ".gitlab-ci.yml", "core"},
		{"gitlab", ".gitlab-ci.yml", "wiring"},
		{"gitlab", "svc/.gitlab-ci.yml", "wiring"},
		{"gitlab", ".gitlab/ci/build.yml", "wiring"},
		{"gitlab", "src/gitlab/x.ts", "core"},
		{"gitlab", "src/server.ts", "core"},
	}
	for _, tc := range tests {
		if got := (peers.Roles{}).RoleFor(tc.provider, tc.path); got != tc.want {
			t.Errorf("RoleFor(%q, %q) = %q, want %q", tc.provider, tc.path, got, tc.want)
		}
	}
}

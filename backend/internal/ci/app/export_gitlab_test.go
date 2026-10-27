package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// This file tests the gitlab_ci export (GitLab C S11) over hand-written fakes: a GitLab stub that
// keeps branches, files and merge requests, a connections fake and an in-memory clone.

const geToken = "glpat-SECRET-TOKEN-VALUE"

// geStore is the GitLab part of fakeStore: the installations by (agent, host, project id).
type geStore struct {
	mu        sync.Mutex
	rows      map[string]domain.Installation
	upserts   []app.NewGitLabInstallation
	upsertErr error
	// slugOwner is the agent that owns every slug in every project ("" = nobody).
	slugOwner string
	finds     int
}

func geKey(agent, host string, project int64) string {
	return fmt.Sprintf("%s|%s|%d", agent, host, project)
}

func (f *fakeStore) FindGitLabInstallation(_ context.Context, _, agentID, host string, projectID int64) (domain.Installation, bool, error) {
	f.ge.mu.Lock()
	defer f.ge.mu.Unlock()
	f.ge.finds++
	i, ok := f.ge.rows[geKey(agentID, host, projectID)]
	return i, ok, nil
}

func (f *fakeStore) GitLabSlugOwner(_ context.Context, host string, projectID int64, slug string) (string, bool, error) {
	f.ge.mu.Lock()
	defer f.ge.mu.Unlock()
	if f.ge.slugOwner != "" {
		return f.ge.slugOwner, true, nil
	}
	for _, i := range f.ge.rows {
		if i.Host == host && i.ProjectID == projectID && i.Slug != nil && *i.Slug == slug {
			return i.AgentID, true, nil
		}
	}
	return "", false, nil
}

func (f *fakeStore) UpsertGitLabInstallation(_ context.Context, in app.NewGitLabInstallation) error {
	f.ge.mu.Lock()
	defer f.ge.mu.Unlock()
	if f.ge.upsertErr != nil {
		return f.ge.upsertErr
	}
	f.ge.upserts = append(f.ge.upserts, in)
	if f.ge.rows == nil {
		f.ge.rows = map[string]domain.Installation{}
	}
	slug, version := in.Slug, in.ManifestVersion
	f.ge.rows[geKey(in.AgentID, in.Host, in.ProjectID)] = domain.Installation{
		ID: "inst-gl", AgentID: in.AgentID, Repo: in.Repo, TargetType: domain.TargetGitLabCI, InstalledAt: installedAt,
		Slug: &slug, ManifestVersion: &version, Provider: in.Provider, Host: in.Host, ProjectID: in.ProjectID,
	}
	return nil
}

// geConns is a GitLabConns over a map keyed by host key.
type geConns struct {
	conns map[string]app.GitLabConn
	err   error
}

func (f geConns) Resolve(_ context.Context, host string) (app.GitLabConn, bool, error) {
	if f.err != nil {
		return app.GitLabConn{}, false, f.err
	}
	c, ok := f.conns[host]
	return c, ok, nil
}

// geClones is a CloneFiles over "<clone path>/<rel>" names.
type geClones struct {
	files map[string]bool
	asked *[]string
}

func (f geClones) Exists(_ context.Context, clonePath, rel string) (bool, error) {
	if f.asked != nil {
		*f.asked = append(*f.asked, clonePath+"/"+rel)
	}
	return f.files[clonePath+"/"+rel], nil
}

// geStub is a GitLabPublisher that behaves like a small GitLab project. Every call is logged
// as one line in log.
type geStub struct {
	mu      sync.Mutex
	log     []string
	project app.GitLabProject
	// branches maps a branch name to its head commit; files maps a branch name to its files.
	branches map[string]string
	files    map[string]map[string]string
	mrs      []geMR
	commits  []app.GitLabCommit
	created  []app.GitLabNewMR
	// fail maps a step to the queue of errors its next calls answer with (consumed in order; nil
	// entries pass).
	fail map[string][]error
	// conflictOnCreate makes the first CreateMR answer 409 after opening the MR (one opened meanwhile).
	conflictOnCreate bool
	// raceOnCommit makes the first commit that carries start_branch lose a race: another export
	// creates the branch with the same files first, and GitLab answers this commit 400.
	raceOnCommit bool
	nextIID      int
	nextSHA      int
}

type geMR struct {
	iid            int
	source, target string
	open           bool
}

func newGEStub() *geStub {
	return &geStub{
		project:  app.GitLabProject{PathWithNamespace: "acme/web", DefaultBranch: "main"},
		branches: map[string]string{"main": geSHA40("main")},
		files:    map[string]map[string]string{"main": {}},
		fail:     map[string][]error{},
		nextIID:  7,
	}
}

func geSHA40(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])[:40]
}

// next records the call and returns the queued failure of its step.
func (g *geStub) next(step, line string) error {
	g.log = append(g.log, line)
	q := g.fail[step]
	if len(q) == 0 {
		return nil
	}
	g.fail[step] = q[1:]
	return q[0]
}

func (g *geStub) refFiles(gitRef string) (map[string]string, bool) {
	if f, ok := g.files[gitRef]; ok {
		return f, true
	}
	for name, head := range g.branches {
		if head == gitRef {
			return g.files[name], true
		}
	}
	return nil, false
}

func (g *geStub) Project(_ context.Context, _ app.GitLabRef) (app.GitLabProject, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.next(app.StepProject, "project"); err != nil {
		return app.GitLabProject{}, err
	}
	return g.project, nil
}

func (g *geStub) Branch(_ context.Context, _ app.GitLabRef, name string) (app.GitLabBranch, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.next(app.StepBranch, "branch "+name); err != nil {
		return app.GitLabBranch{}, false, err
	}
	head, ok := g.branches[name]
	return app.GitLabBranch{Name: name, HeadSHA: head}, ok, nil
}

func (g *geStub) FileSHA256(_ context.Context, _ app.GitLabRef, path, gitRef string) (string, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	shown := gitRef
	for name, head := range g.branches {
		if head == gitRef {
			shown = "branch:" + name
		}
	}
	if err := g.next(app.StepFileMeta, "file_meta "+path+" @"+shown); err != nil {
		return "", false, err
	}
	files, ok := g.refFiles(gitRef)
	if !ok {
		return "", false, nil
	}
	content, ok := files[path]
	if !ok {
		return "", false, nil
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:]), true, nil
}

func (g *geStub) Commit(_ context.Context, _ app.GitLabRef, in app.GitLabCommit) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.next(app.StepCommit, "commit "+in.Branch); err != nil {
		return err
	}
	if g.raceOnCommit && in.StartBranch != "" {
		g.raceOnCommit = false
		g.files[in.Branch] = geCopy(g.files[in.StartBranch])
		for _, a := range in.Actions {
			g.files[in.Branch][a.Path] = a.Content
		}
		g.nextSHA++
		g.branches[in.Branch] = geSHA40(fmt.Sprintf("%s-%d", in.Branch, g.nextSHA))
		return &app.GitLabError{Status: 400, Step: app.StepCommit}
	}
	g.commits = append(g.commits, in)
	if _, ok := g.branches[in.Branch]; !ok {
		g.files[in.Branch] = geCopy(g.files[in.StartBranch])
	}
	for _, a := range in.Actions {
		g.files[in.Branch][a.Path] = a.Content
	}
	g.nextSHA++
	g.branches[in.Branch] = geSHA40(fmt.Sprintf("%s-%d", in.Branch, g.nextSHA))
	return nil
}

func geCopy(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (g *geStub) OpenMR(_ context.Context, _ app.GitLabRef, source, target string) (int, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.next(app.StepOpenMRs, "open_mrs "+source+" "+target); err != nil {
		return 0, false, err
	}
	for _, m := range g.mrs {
		if m.open && m.source == source && m.target == target {
			return m.iid, true, nil
		}
	}
	return 0, false, nil
}

func (g *geStub) CreateMR(_ context.Context, _ app.GitLabRef, in app.GitLabNewMR) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.next(app.StepCreateMR, "create_mr"); err != nil {
		return 0, err
	}
	g.created = append(g.created, in)
	g.mrs = append(g.mrs, geMR{iid: g.nextIID, source: in.SourceBranch, target: in.TargetBranch, open: true})
	g.nextIID++
	if g.conflictOnCreate {
		g.conflictOnCreate = false
		return 0, &app.GitLabError{Status: 409, Step: app.StepCreateMR}
	}
	return g.nextIID - 1, nil
}

func (g *geStub) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.log)
}

func (g *geStub) count(prefix string) int {
	n := 0
	for _, l := range g.calls() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// geRig is one Export service over the GitLab fakes.
type geRig struct {
	svc      *app.ExportService
	store    *fakeStore
	gh       *fakeGitHub
	stub     *geStub
	logs     *logCapture
	rec      *calls
	followed *[]string
	asked    *[]string
	pauses   *int
	agents   map[string]domain.ExportAgent
}

type geOpts struct {
	agent     domain.ExportAgent
	repos     []app.ExportRepo
	github    map[string]domain.ImportedRepo
	conns     map[string]app.GitLabConn
	noConns   bool
	clones    map[string]bool
	noRunner  bool
	custom    app.CustomProviders
	followErr error
}

func geRepo() app.ExportRepo {
	return app.ExportRepo{
		ID: "gl-1", Provider: app.ProviderGitLab, Host: "gitlab.com", Owner: "acme", Name: "web",
		FullName: "acme/web", DefaultBranch: "main", ProjectID: 42, WebURL: "https://gitlab.com/acme/web",
	}
}

func newGERig(t *testing.T, o geOpts) *geRig {
	t.Helper()
	if o.agent.ID == "" {
		o.agent = defaultAgent()
	}
	if o.repos == nil {
		o.repos = []app.ExportRepo{geRepo()}
	}
	if o.conns == nil && !o.noConns {
		o.conns = map[string]app.GitLabConn{"gitlab.com": {Name: "gitlab.com", HasToken: true}}
	}
	rec := &calls{}
	store := &fakeStore{calls: rec}
	gh := &fakeGitHub{calls: rec, configured: true}
	stub := newGEStub()
	logs := newLogCapture()
	followed, asked, pauses := &[]string{}, &[]string{}, new(int)
	agents := map[string]domain.ExportAgent{o.agent.ID: o.agent}
	deps := app.ExportDeps{
		Agents: fakeAgents{agents: agents},
		Repos:  fakeRepos{repos: o.github, gitlab: o.repos, followed: followed, followErr: o.followErr},
		Store:  store,
		Runner: fakeRunner{artifact: testRunner, ok: !o.noRunner},
		GitHub: gh,
		Custom: o.custom,
		Clones: geClones{files: o.clones, asked: asked},
		GitLab: stub,
		RetryPause: func(context.Context) error {
			*pauses++
			return nil
		},
		Log: logs.logger(),
	}
	if !o.noConns {
		deps.GitLabConns = geConns{conns: o.conns}
	}
	return &geRig{
		svc: app.NewExportService(deps), store: store, gh: gh, stub: stub, logs: logs, rec: rec,
		followed: followed, asked: asked, pauses: pauses, agents: agents,
	}
}

func geRequest() app.ExportRequest {
	return app.ExportRequest{
		Repo: "acme/web", Target: domain.TargetGitLabCI, Action: domain.ActionOpenPR, PostAs: domain.PostAsGitHubReview,
		Triggers: []string{"opened", "synchronize", "reopened"},
	}
}

func gePaths(files []domain.File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func geFilePaths(slug string, root bool) []string {
	out := []string{
		".devdigest/agents/" + slug + ".yaml",
		".devdigest/skills/api-safety.md",
		".devdigest/skills/no-secrets.md",
		".devdigest/memory.jsonl",
		domain.RunnerPath,
		domain.RunnerSumPath,
		".gitlab/devdigest/review-" + slug + ".yml",
	}
	if root {
		out = append(out, ".gitlab-ci.yml")
	}
	return out
}

// TestExportGitLab_RepositoryResolution covers FR1, EC1, AC1.
func TestExportGitLab_RepositoryResolution(t *testing.T) {
	second := geRepo()
	second.ID, second.Host = "gl-2", "git.corp.example"
	second.ProjectID = 43
	gh := map[string]domain.ImportedRepo{"acme/web": {Owner: "acme", Name: "web", FullName: "acme/web", DefaultBranch: "main"}}
	conns := map[string]app.GitLabConn{
		"gitlab.com":       {Name: "gitlab.com", HasToken: true},
		"git.corp.example": {Name: "corp", HasToken: true},
	}
	t.Run("a GitHub repository with the name wins", func(t *testing.T) {
		rig := newGERig(t, geOpts{repos: []app.ExportRepo{geRepo(), second}, github: gh, conns: conns})
		req := geRequest()
		req.Target, req.Action = "", domain.ActionFiles
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil {
			t.Fatalf("Export(acme/web) error = %v", err)
		}
		if got.GitLab != nil {
			t.Errorf("Export(acme/web).GitLab = %+v, want nil (GitHub repository)", got.GitLab)
		}
	})
	t.Run("two GitLab hosts are ambiguous", func(t *testing.T) {
		rig := newGERig(t, geOpts{repos: []app.ExportRepo{geRepo(), second}, conns: conns})
		_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", geRequest())
		ae := wantError(t, err, apperr.KindValidation, "repo_ambiguous", "acme/web exists on several hosts; choose the repository by id")
		if diff := cmp.Diff(map[string]any{"repo_ids": []string{"gl-1", "gl-2"}}, ae.Details); diff != "" {
			t.Errorf("repo_ambiguous details mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("repo_id picks the repository", func(t *testing.T) {
		rig := newGERig(t, geOpts{repos: []app.ExportRepo{geRepo(), second}, conns: conns})
		req := geRequest()
		req.RepoID, req.Action = "gl-2", domain.ActionFiles
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil || got.GitLab == nil {
			t.Fatalf("Export(repo_id gl-2) = %+v, %v, want gitlab_ci files", got, err)
		}
	})
	t.Run("repo_id wins over repo", func(t *testing.T) {
		rig := newGERig(t, geOpts{repos: []app.ExportRepo{geRepo(), second}, conns: conns})
		req := geRequest()
		req.Repo, req.RepoID, req.Action = "nobody/nothing", "gl-1", domain.ActionFiles
		if _, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req); err != nil {
			t.Errorf("Export(repo nobody/nothing, repo_id gl-1) error = %v, want none", err)
		}
	})
	t.Run("an unknown repo_id is repo_not_found", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		req := geRequest()
		req.RepoID = "nope"
		_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		wantError(t, err, apperr.KindNotFound, "repo_not_found", "")
	})
	t.Run("an unknown name is repo_not_found", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		req := geRequest()
		req.Repo = "acme/other"
		_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		wantError(t, err, apperr.KindNotFound, "repo_not_found", "")
	})
}

// TestExportGitLab_Target covers FR2, EC2, AC2.
func TestExportGitLab_Target(t *testing.T) {
	gh := map[string]domain.ImportedRepo{"acme/widgets": {Owner: "acme", Name: "widgets", FullName: "acme/widgets", DefaultBranch: "main"}}
	tests := []struct {
		name, repo, target string
		wantGitLab         bool
		wantErr            string // message; "" = success
		wantCode           string
	}{
		{name: "omitted on GitLab is gitlab_ci", repo: "acme/web", target: "", wantGitLab: true},
		{name: "gitlab_ci on GitLab", repo: "acme/web", target: "gitlab_ci", wantGitLab: true},
		{name: "gha on GitLab", repo: "acme/web", target: "gha",
			wantErr: "GitHub Actions export needs a GitHub repository", wantCode: "unsupported_target"},
		{name: "circle on GitLab", repo: "acme/web", target: "circle",
			wantErr: "Only GitHub Actions can be exported", wantCode: "unsupported_target"},
		{name: "omitted on GitHub is gha", repo: "acme/widgets", target: ""},
		{name: "gitlab_ci on GitHub", repo: "acme/widgets", target: "gitlab_ci",
			wantErr: "GitLab CI export needs a GitLab repository", wantCode: "unsupported_target"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newGERig(t, geOpts{github: gh})
			req := geRequest()
			req.Repo, req.Target, req.Action = tc.repo, tc.target, domain.ActionFiles
			got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			if tc.wantErr != "" {
				wantError(t, err, apperr.KindValidation, tc.wantCode, tc.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Export(%s, %q) error = %v", tc.repo, tc.target, err)
			}
			if (got.GitLab != nil) != tc.wantGitLab {
				t.Errorf("Export(%s, %q).GitLab set = %v, want %v", tc.repo, tc.target, got.GitLab != nil, tc.wantGitLab)
			}
		})
	}
}

// TestExportGitLab_NotConnected covers FR2, EC3, AC3 (export part).
func TestExportGitLab_NotConnected(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts geOpts
	}{
		{"connection deleted", geOpts{conns: map[string]app.GitLabConn{"other.example": {Name: "other", HasToken: true}}}},
		{"no connections port", geOpts{noConns: true}},
	} {
		for _, action := range []domain.ExportAction{domain.ActionFiles, domain.ActionOpenPR} {
			t.Run(tc.name+"/"+string(action), func(t *testing.T) {
				rig := newGERig(t, tc.opts)
				req := geRequest()
				req.Action = action
				_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
				ae := wantError(t, err, apperr.KindValidation, "repo_host_not_connected", "")
				if diff := cmp.Diff(map[string]string{"host": "gitlab.com", "repo_id": "gl-1"}, ae.Details); diff != "" {
					t.Errorf("details mismatch (-want +got):\n%s", diff)
				}
				if got := rig.stub.calls(); len(got) != 0 {
					t.Errorf("Export made GitLab calls %v, want none", got)
				}
			})
		}
	}
	t.Run("a resolve failure is not a not-connected answer", func(t *testing.T) {
		boom := errors.New("settings down")
		rig := newGERig(t, geOpts{})
		rig.svc = app.NewExportService(app.ExportDeps{
			Agents: fakeAgents{agents: rig.agents}, Repos: fakeRepos{gitlab: []app.ExportRepo{geRepo()}}, Store: rig.store,
			Runner: fakeRunner{artifact: testRunner, ok: true}, GitHub: rig.gh, GitLabConns: geConns{err: boom},
		})
		_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", geRequest())
		if !errors.Is(err, boom) {
			t.Errorf("Export error = %v, want it to wrap %v", err, boom)
		}
	})
}

// TestExportGitLab_CheckOrder covers FR3, AC4: every failing input answers its own error, in the
// order of the spec, with no request to GitLab. Case i breaks every input from step i on, so the
// answer is the first of them.
func TestExportGitLab_CheckOrder(t *testing.T) {
	insecure := "devdigest-review-security-reviewer:\n  stage: test\n  script: [\"echo $CI_COMMIT_MESSAGE\"]\n"
	steps := []struct {
		name string
		kind apperr.Kind
		code string
		msg  string
	}{
		{"agent", apperr.KindNotFound, "agent_not_found", ""},
		{"plugin", apperr.KindConflict, "plugin_disabled", ""},
		{"target", apperr.KindValidation, "unsupported_target", "Only GitHub Actions can be exported"},
		{"provider", apperr.KindValidation, "provider_not_configured", ""},
		{"triggers", apperr.KindValidation, "invalid_triggers", ""},
		{"repo", apperr.KindNotFound, "repo_not_found", ""},
		{"host match", apperr.KindValidation, "unsupported_target", "GitHub Actions export needs a GitHub repository"},
		{"connection", apperr.KindValidation, "repo_host_not_connected", ""},
		{"slug", apperr.KindConflict, "ci_slug_conflict", ""},
		{"skills", apperr.KindValidation, "skill_slug_conflict", ""},
		{"job file", apperr.KindValidation, "workflow_insecure", ""},
		{"runner", apperr.KindInternal, "runner_bundle_missing", ""},
		{"manifest", apperr.KindValidation, "manifest_invalid", ""},
		{"token", apperr.KindConfig, "config_error", "GitLab connection gitlab.com has no token"},
	}
	for i, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			agent := defaultAgent()
			if i <= 1 {
				agent.Plugin = &domain.PluginRef{Name: "acme-pack", Enabled: false}
			}
			if i <= 3 {
				agent.Provider = "custom:nowhere"
			}
			if i <= 9 {
				agent.Skills = []domain.ExportSkill{{Name: "Same", Body: "a"}, {Name: "same", Body: "b"}}
			}
			if i <= 12 {
				agent.Model = ""
			}
			req := geRequest()
			switch {
			case i <= 2:
				req.Target = "circle"
			case i <= 6:
				req.Target = domain.TargetGHA
			}
			if i <= 4 {
				req.Triggers = []string{"closed"}
			}
			if i <= 5 {
				req.Repo = "acme/missing"
			}
			if i <= 10 {
				req.Workflow = &insecure
			}
			conn := app.GitLabConn{Name: "gitlab.com", HasToken: i > 13}
			conns := map[string]app.GitLabConn{"gitlab.com": conn}
			if i <= 7 {
				conns = map[string]app.GitLabConn{"elsewhere": conn}
			}
			rig := newGERig(t, geOpts{agent: agent, conns: conns, noRunner: i <= 11})
			if i <= 8 {
				rig.store.ge.slugOwner = "someone-else"
			}
			id := agent.ID
			if i == 0 {
				id = "unknown-agent"
			}
			_, err := rig.svc.Export(t.Context(), "ws-1", id, req)
			wantError(t, err, st.kind, st.code, st.msg)
			if got := rig.stub.calls(); len(got) != 0 {
				t.Errorf("step %s: GitLab calls %v, want none", st.name, got)
			}
		})
	}
}

// TestExportGitLab_FilesAnswer covers FR4–FR7, AC5, AC6, AC7 (app part), AC9 (secrets).
func TestExportGitLab_FilesAnswer(t *testing.T) {
	t.Run("files, secrets and the gitlab object", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		req := geRequest()
		req.Action = domain.ActionFiles
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil {
			t.Fatalf("Export error = %v", err)
		}
		if diff := cmp.Diff(geFilePaths("security-reviewer", true), gePaths(got.Files)); diff != "" {
			t.Errorf("files mismatch (-want +got):\n%s", diff)
		}
		for _, f := range got.Files {
			if want := f.Path == ".gitlab/devdigest/review-security-reviewer.yml"; f.Editable != want {
				t.Errorf("file %s editable = %v, want %v", f.Path, f.Editable, want)
			}
		}
		wantGL := &domain.GitLabExport{
			JobFile: ".gitlab/devdigest/review-security-reviewer.yml", RootConfig: domain.RootConfigCreate,
			ConfigPath: ".gitlab-ci.yml", IncludeSnippet: domain.IncludeSnippet("security-reviewer"),
		}
		if diff := cmp.Diff(wantGL, got.GitLab); diff != "" {
			t.Errorf("gitlab object mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"}, got.RequiredSecrets); diff != "" {
			t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
		}
		if len(got.RequiredVariables) != 0 {
			t.Errorf("required_variables = %v, want none", got.RequiredVariables)
		}
		if got.Installation != nil || got.PRURL != nil {
			t.Errorf("files action answered installation %v / pr_url %v, want nil", got.Installation, got.PRURL)
		}
		if calls := rig.stub.calls(); len(calls) != 0 {
			t.Errorf("files action made GitLab calls %v, want none", calls)
		}
		if len(rig.store.ge.upserts) != 0 {
			t.Errorf("files action wrote %d installations, want 0", len(rig.store.ge.upserts))
		}
	})
	t.Run("manifest, skills, memory and runner equal the gha export", func(t *testing.T) {
		gh := map[string]domain.ImportedRepo{"acme/widgets": {Owner: "acme", Name: "widgets", FullName: "acme/widgets", DefaultBranch: "main"}}
		rig := newGERig(t, geOpts{github: gh})
		glReq := geRequest()
		glReq.Action = domain.ActionFiles
		glOut, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", glReq)
		if err != nil {
			t.Fatal(err)
		}
		ghReq := request("acme/widgets")
		ghReq.Action = domain.ActionFiles
		ghOut, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", ghReq)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(ghOut.Files[:6], glOut.Files[:6]); diff != "" {
			t.Errorf("shared files mismatch (-gha +gitlab_ci):\n%s", diff)
		}
	})
	t.Run("the job file is the generated text", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		req := geRequest()
		req.Action, req.Runner = domain.ActionFiles, domain.RunnerSelfHosted
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil {
			t.Fatal(err)
		}
		want, err := domain.BuildGitLabJob(domain.GitLabJobOptions{Slug: "security-reviewer", PostAs: domain.PostAsGitHubReview, Runner: domain.RunnerSelfHosted})
		if err != nil {
			t.Fatal(err)
		}
		if got.Files[6].Contents != want {
			t.Errorf("job file = %q, want %q", got.Files[6].Contents, want)
		}
	})
	t.Run("post_as none leaves the token out", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		req := geRequest()
		req.Action, req.PostAs = domain.ActionFiles, domain.PostAsNone
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"OPENROUTER_API_KEY"}, got.RequiredSecrets); diff != "" {
			t.Errorf("required_secrets mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("a custom provider maps its variables", func(t *testing.T) {
		agent := defaultAgent()
		agent.Provider = "custom:my-gw"
		custom := fakeCustom{defined: map[string]app.CustomExport{"custom:my-gw": {
			Config:        domain.CustomProviderConfig{Protocol: "openai", AuthMode: "bearer", ExtraHeaders: []string{"X-A", "X-B"}, StructuredOutput: "strict"},
			KeyConfigured: true,
		}}}
		rig := newGERig(t, geOpts{agent: agent, custom: custom})
		req := geRequest()
		req.Action = domain.ActionFiles
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{
			"DEVDIGEST_LLM_BASE_URL: $DEVDIGEST_MY_GW_BASE_URL",
			"DEVDIGEST_LLM_API_KEY: $DEVDIGEST_MY_GW_API_KEY",
			"DEVDIGEST_LLM_HEADER_1: $DEVDIGEST_MY_GW_HEADER_1",
			"DEVDIGEST_LLM_HEADER_2: $DEVDIGEST_MY_GW_HEADER_2",
		} {
			if !strings.Contains(got.Files[6].Contents, line) {
				t.Errorf("job file lacks %q", line)
			}
		}
		if diff := cmp.Diff([]string{"DEVDIGEST_MY_GW_BASE_URL"}, got.RequiredVariables); diff != "" {
			t.Errorf("required_variables mismatch (-want +got):\n%s", diff)
		}
		if !slices.Contains(got.RequiredSecrets, "DEVDIGEST_GITLAB_TOKEN") || !slices.Contains(got.RequiredSecrets, "DEVDIGEST_MY_GW_API_KEY") {
			t.Errorf("required_secrets = %v, want the provider key and DEVDIGEST_GITLAB_TOKEN", got.RequiredSecrets)
		}
	})
	t.Run("triggers note", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			triggers []string
			want     string
		}{
			{"one trigger", []string{"opened"}, domain.TriggersNoteText},
			{"all three", []string{"opened", "synchronize", "reopened"}, ""},
		} {
			rig := newGERig(t, geOpts{})
			req := geRequest()
			req.Action, req.Triggers = domain.ActionFiles, tc.triggers
			got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			if err != nil {
				t.Fatal(err)
			}
			if got.GitLab.TriggersNote != tc.want {
				t.Errorf("%s: triggers_note = %q, want %q", tc.name, got.GitLab.TriggersNote, tc.want)
			}
		}
	})
	t.Run("an edited job file is checked", func(t *testing.T) {
		for _, tc := range []struct {
			name, text, rule string
		}{
			{"empty", "", "invalid_yaml"},
			{"include", "include:\n  - remote: https://x/y.yml\n", "structure"},
			{"untrusted", "devdigest-review-security-reviewer:\n  script: [\"echo ${CI_COMMIT_MESSAGE}\"]\n", "structure"},
		} {
			rig := newGERig(t, geOpts{})
			req := geRequest()
			req.Action, req.Workflow = domain.ActionFiles, &tc.text
			_, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			ae := wantError(t, err, apperr.KindValidation, "workflow_insecure", "")
			if diff := cmp.Diff(map[string]string{"rule": tc.rule}, ae.Details); diff != "" {
				t.Errorf("%s: details mismatch (-want +got):\n%s", tc.name, diff)
			}
		}
	})
	t.Run("an edited, valid job file is exported as sent", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		generated, err := domain.BuildGitLabJob(domain.GitLabJobOptions{Slug: "security-reviewer", PostAs: domain.PostAsGitHubReview})
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.Replace(generated, "stage: test", "stage: review", 1)
		req := geRequest()
		req.Action, req.Workflow = domain.ActionFiles, &edited
		got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil {
			t.Fatalf("Export(edited job file) error = %v", err)
		}
		if got.Files[6].Contents != edited {
			t.Errorf("job file = %q, want the edited text", got.Files[6].Contents)
		}
	})
}

// TestExportGitLab_FilesRootConfigFromClone covers FR5, AC6.
func TestExportGitLab_FilesRootConfigFromClone(t *testing.T) {
	tests := []struct {
		name     string
		clones   map[string]bool
		clonePth string
		want     domain.RootConfig
		wantRoot bool
	}{
		{"clone with .gitlab-ci.yml", map[string]bool{"/data/clones/gl/.gitlab-ci.yml": true}, "/data/clones/gl", domain.RootConfigKeepExisting, false},
		{"clone without it", map[string]bool{}, "/data/clones/gl", domain.RootConfigCreate, true},
		{"no clone", nil, "", domain.RootConfigCreate, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := geRepo()
			repo.ClonePath = tc.clonePth
			rig := newGERig(t, geOpts{repos: []app.ExportRepo{repo}, clones: tc.clones})
			req := geRequest()
			req.Action = domain.ActionFiles
			got, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
			if err != nil {
				t.Fatal(err)
			}
			if got.GitLab.RootConfig != tc.want {
				t.Errorf("root_config = %q, want %q", got.GitLab.RootConfig, tc.want)
			}
			if has := slices.Contains(gePaths(got.Files), ".gitlab-ci.yml"); has != tc.wantRoot {
				t.Errorf("root file listed = %v, want %v", has, tc.wantRoot)
			}
			if calls := rig.stub.calls(); len(calls) != 0 {
				t.Errorf("GitLab calls %v, want none", calls)
			}
			if tc.clonePth == "" && len(*rig.asked) != 0 {
				t.Errorf("clone asked %v without a clone, want none", *rig.asked)
			}
		})
	}
}

func geOpenPR(t *testing.T, rig *geRig, mut func(*app.ExportRequest)) (domain.Export, error) {
	t.Helper()
	req := geRequest()
	if mut != nil {
		mut(&req)
	}
	return rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
}

// TestExportGitLab_PublishFresh covers FR11–FR13, AC10, NFR1 (app part).
func TestExportGitLab_PublishFresh(t *testing.T) {
	rig := newGERig(t, geOpts{})
	got, err := geOpenPR(t, rig, nil)
	if err != nil {
		t.Fatalf("Export(open_pr) error = %v", err)
	}
	wantCalls := []string{
		"project",
		"file_meta .gitlab-ci.yml @main",
		"branch devdigest/ci",
		"file_meta .devdigest/agents/security-reviewer.yaml @main",
		"file_meta .devdigest/skills/api-safety.md @main",
		"file_meta .devdigest/skills/no-secrets.md @main",
		"file_meta .devdigest/memory.jsonl @main",
		"file_meta " + domain.RunnerPath + " @main",
		"file_meta " + domain.RunnerSumPath + " @main",
		"file_meta .gitlab/devdigest/review-security-reviewer.yml @main",
		"commit devdigest/ci",
		"open_mrs devdigest/ci main",
		"create_mr",
	}
	if diff := cmp.Diff(wantCalls, rig.stub.calls()); diff != "" {
		t.Errorf("GitLab call order mismatch (-want +got):\n%s", diff)
	}
	if len(rig.stub.commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(rig.stub.commits))
	}
	c := rig.stub.commits[0]
	if c.Branch != "devdigest/ci" || c.StartBranch != "main" || c.Message != "Add DevDigest CI review" {
		t.Errorf("commit = branch %q start %q message %q, want devdigest/ci, main, Add DevDigest CI review", c.Branch, c.StartBranch, c.Message)
	}
	for _, a := range c.Actions {
		if a.Action != "create" {
			t.Errorf("action for %s = %s, want create", a.Path, a.Action)
		}
	}
	if diff := cmp.Diff(geFilePaths("security-reviewer", true), gePaths(got.Files)); diff != "" {
		t.Errorf("answered files mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(func() []string {
		var p []string
		for _, a := range c.Actions {
			p = append(p, a.Path)
		}
		return p
	}(), gePaths(got.Files)); diff != "" {
		t.Errorf("committed paths mismatch (-want +got):\n%s", diff)
	}
	if len(rig.stub.created) != 1 {
		t.Fatalf("created MRs = %d, want 1", len(rig.stub.created))
	}
	mr := rig.stub.created[0]
	wantDesc := domain.BuildMRDescription(domain.MRDescInput{
		Slug: "security-reviewer", Paths: gePaths(got.Files), RunnerSHA256: testRunner.SHA256, BuildID: testRunner.BuildID,
		Secrets:    []string{"OPENROUTER_API_KEY", "DEVDIGEST_GITLAB_TOKEN"},
		RootConfig: domain.RootConfigCreate, ConfigPath: ".gitlab-ci.yml",
	})
	if mr.Title != "Add DevDigest CI review" || mr.SourceBranch != "devdigest/ci" || mr.TargetBranch != "main" ||
		!mr.RemoveSourceBranch || mr.Description != wantDesc {
		t.Errorf("created MR = %+v, want title, devdigest/ci -> main, remove source branch, the §7.4 description", mr)
	}
	if got.PRURL == nil || *got.PRURL != "https://gitlab.com/acme/web/-/merge_requests/7" {
		t.Errorf("pr_url = %v, want https://gitlab.com/acme/web/-/merge_requests/7", got.PRURL)
	}
	wantUpsert := app.NewGitLabInstallation{
		AgentID: "agent-1", Host: "gitlab.com", ProjectID: 42, Repo: "acme/web", Slug: "security-reviewer",
		ManifestVersion: 3, Provider: "openrouter",
	}
	if diff := cmp.Diff([]app.NewGitLabInstallation{wantUpsert}, rig.store.ge.upserts); diff != "" {
		t.Errorf("installation upserts mismatch (-want +got):\n%s", diff)
	}
	if got.Installation == nil || got.Installation.TargetType != "gitlab_ci" || got.Installation.Host != "gitlab.com" || got.Installation.ProjectID != 42 {
		t.Errorf("installation = %+v, want a gitlab_ci installation of gitlab.com project 42", got.Installation)
	}
	if got.GitLab.RootConfig != domain.RootConfigCreate {
		t.Errorf("root_config = %q, want create", got.GitLab.RootConfig)
	}
	if n := len(rig.rec.snapshot(t)); n != 0 {
		t.Errorf("GitHub-side store/client calls = %d, want 0", n)
	}
}

// TestExportGitLab_RootConfig covers FR11 step 2, AC11, EC4–EC6.
func TestExportGitLab_RootConfig(t *testing.T) {
	t.Run("keep_existing", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.files["main"][".gitlab-ci.yml"] = "stages: [test]\n"
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.GitLab.RootConfig != domain.RootConfigKeepExisting || got.GitLab.ConfigPath != ".gitlab-ci.yml" {
			t.Errorf("gitlab = %+v, want keep_existing .gitlab-ci.yml", got.GitLab)
		}
		for _, a := range rig.stub.commits[0].Actions {
			if a.Path == ".gitlab-ci.yml" {
				t.Errorf("commit touches .gitlab-ci.yml")
			}
		}
		if slices.Contains(gePaths(got.Files), ".gitlab-ci.yml") {
			t.Errorf("answered files list the root file: %v", gePaths(got.Files))
		}
		if desc := rig.stub.created[0].Description; !strings.Contains(desc, "Add this to `.gitlab-ci.yml`:") ||
			!strings.Contains(desc, domain.WarnWorkflowRules) || !strings.Contains(desc, domain.WarnStagesTest) {
			t.Errorf("description lacks the include step and its warnings:\n%s", desc)
		}
		if rig.stub.files["main"][".gitlab-ci.yml"] != "stages: [test]\n" {
			t.Errorf("default branch .gitlab-ci.yml changed")
		}
	})
	t.Run("custom_path", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.project.CIConfigPath = "ci/main.yml"
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.GitLab.RootConfig != domain.RootConfigCustomPath || got.GitLab.ConfigPath != "ci/main.yml" {
			t.Errorf("gitlab = %+v, want custom_path ci/main.yml", got.GitLab)
		}
		if slices.Contains(gePaths(got.Files), ".gitlab-ci.yml") || rig.stub.count("file_meta .gitlab-ci.yml") != 0 {
			t.Errorf("custom_path touched .gitlab-ci.yml: files %v calls %v", gePaths(got.Files), rig.stub.calls())
		}
		if desc := rig.stub.created[0].Description; !strings.Contains(desc, "Add this to `ci/main.yml`:") {
			t.Errorf("description lacks the include step for ci/main.yml:\n%s", desc)
		}
	})
	t.Run("a stale clone that said create", func(t *testing.T) {
		repo := geRepo()
		repo.ClonePath = "/data/clones/gl"
		rig := newGERig(t, geOpts{repos: []app.ExportRepo{repo}, clones: map[string]bool{}})
		req := geRequest()
		req.Action = domain.ActionFiles
		preview, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req)
		if err != nil || preview.GitLab.RootConfig != domain.RootConfigCreate {
			t.Fatalf("preview = %+v, %v, want create", preview.GitLab, err)
		}
		rig.stub.files["main"][".gitlab-ci.yml"] = "stages: [test]\n"
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.GitLab.RootConfig != domain.RootConfigKeepExisting || slices.Contains(gePaths(got.Files), ".gitlab-ci.yml") {
			t.Errorf("open_pr after a stale preview = %+v files %v, want keep_existing without the root file", got.GitLab, gePaths(got.Files))
		}
	})
}

// TestExportGitLab_Idempotent covers FR11, FR12, AC12, EC7, EC8.
func TestExportGitLab_Idempotent(t *testing.T) {
	t.Run("identical re-export commits nothing and reuses the MR", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(rig.stub.commits) != 1 || len(rig.stub.created) != 1 {
			t.Errorf("commits = %d, created MRs = %d, want 1 and 1 after two identical exports", len(rig.stub.commits), len(rig.stub.created))
		}
		if got.PRURL == nil || !strings.HasSuffix(*got.PRURL, "/-/merge_requests/7") {
			t.Errorf("pr_url = %v, want the first MR reused", got.PRURL)
		}
		if diff := cmp.Diff(geFilePaths("security-reviewer", true), gePaths(got.Files)); diff != "" {
			t.Errorf("files mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("a skill change commits one update", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		a := rig.agents["agent-1"]
		a.Skills = []domain.ExportSkill{{Name: "API Safety", Body: "Check auth.\nCheck input.\nCheck CSRF.\n"}, a.Skills[1]}
		rig.agents["agent-1"] = a
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		if len(rig.stub.commits) != 2 {
			t.Fatalf("commits = %d, want 2", len(rig.stub.commits))
		}
		second := rig.stub.commits[1]
		want := []app.GitLabCommitAction{{Action: "update", Path: ".devdigest/skills/api-safety.md", Content: "Check auth.\nCheck input.\nCheck CSRF.\n"}}
		if diff := cmp.Diff(want, second.Actions); diff != "" {
			t.Errorf("second commit actions mismatch (-want +got):\n%s", diff)
		}
		if second.StartBranch != "" {
			t.Errorf("second commit start_branch = %q, want empty (the branch exists)", second.StartBranch)
		}
	})
	t.Run("a new file is a create next to the unchanged ones", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		a := rig.agents["agent-1"]
		a.Skills = append(slices.Clone(a.Skills), domain.ExportSkill{Name: "Extra", Body: "x"})
		rig.agents["agent-1"] = a
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, act := range rig.stub.commits[1].Actions {
			got = append(got, act.Action+" "+act.Path)
		}
		want := []string{"update .devdigest/agents/security-reviewer.yaml", "create .devdigest/skills/extra.md"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("second commit actions mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("a closed MR gets a new one", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		rig.stub.mrs[0].open = false
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(rig.stub.created) != 2 || got.PRURL == nil || !strings.HasSuffix(*got.PRURL, "/-/merge_requests/8") {
			t.Errorf("created MRs = %d, pr_url = %v, want a second MR !8", len(rig.stub.created), got.PRURL)
		}
	})
	t.Run("a 409 on create lists again and reuses", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.conflictOnCreate = true
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatalf("Export error = %v", err)
		}
		if got.PRURL == nil || !strings.HasSuffix(*got.PRURL, "/-/merge_requests/7") {
			t.Errorf("pr_url = %v, want the MR opened meanwhile (!7)", got.PRURL)
		}
		if rig.stub.count("open_mrs") != 2 {
			t.Errorf("open_mrs calls = %d, want 2", rig.stub.count("open_mrs"))
		}
	})
	t.Run("a 409 with no MR to be found is unavailable", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.fail[app.StepCreateMR] = []error{&app.GitLabError{Status: 409, Step: app.StepCreateMR}}
		_, err := geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later")
	})
	t.Run("an existing CI branch is the parent", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.branches["devdigest/ci"] = geSHA40("ci")
		rig.stub.files["devdigest/ci"] = map[string]string{}
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		if got := rig.stub.calls()[3]; got != "file_meta .devdigest/agents/security-reviewer.yaml @branch:devdigest/ci" {
			t.Errorf("first file meta after the branch lookup = %q, want it read at the CI branch head", got)
		}
		if c := rig.stub.commits[0]; c.StartBranch != "" {
			t.Errorf("commit start_branch = %q, want empty for an existing branch", c.StartBranch)
		}
	})
}

// TestExportGitLab_Failures covers FR13, EC9, AC13.
func TestExportGitLab_Failures(t *testing.T) {
	denied := "GitLab refused the write. The connection token needs the api scope and at least the Developer role in acme/web."
	tests := []struct {
		name      string
		err       error
		wantKind  apperr.Kind
		wantCode  string
		wantMsg   string
		wantTries int
		wantPause int
	}{
		{"403", &app.GitLabError{Status: 403, Step: app.StepCommit}, apperr.KindValidation, "gitlab_permission_denied", denied, 1, 0},
		{"404", &app.GitLabError{Status: 404, Step: app.StepCommit}, apperr.KindValidation, "gitlab_permission_denied", denied, 1, 0},
		{"401", &app.GitLabError{Status: 401, Step: app.StepCommit}, apperr.KindValidation, "gitlab_permission_denied", denied, 1, 0},
		{"500 twice", &app.GitLabError{Status: 500, Step: app.StepCommit}, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later", 2, 1},
		{"timeout twice", &app.GitLabError{Transport: true, Step: app.StepCommit}, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later", 2, 1},
		{"429 is not retried", &app.GitLabError{Status: 429, Step: app.StepCommit}, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later", 1, 0},
		// The branch is absent at the lookup, so the commit carries start_branch: a 400 is the lost race
		// of two first exports and the sequence is repeated once, without a pause.
		{"400 on a start_branch commit is repeated once", &app.GitLabError{Status: 400, Step: app.StepCommit}, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later", 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newGERig(t, geOpts{})
			rig.stub.fail[app.StepCommit] = []error{tc.err, tc.err}
			_, err := geOpenPR(t, rig, nil)
			wantError(t, err, tc.wantKind, tc.wantCode, tc.wantMsg)
			if got := rig.stub.count("commit"); got != tc.wantTries {
				t.Errorf("commit attempts = %d, want %d", got, tc.wantTries)
			}
			if *rig.pauses != tc.wantPause {
				t.Errorf("retry pauses = %d, want %d", *rig.pauses, tc.wantPause)
			}
			if len(rig.store.ge.upserts) != 0 {
				t.Errorf("installations written = %d, want 0", len(rig.store.ge.upserts))
			}
			lines := rig.logs.snapshot(t)
			if len(lines) != 1 {
				t.Fatalf("log lines = %v, want exactly one", lines)
			}
			line := lines[0].(map[string]any)
			obj := line["obj"].(map[string]any)
			if line["msg"] != "ci export: GitLab write failed" || line["level"] != "error" || obj["step"] != "commit" ||
				obj["repo_id"] != "gl-1" || obj["host"] != "gitlab.com" {
				t.Errorf("log line = %v, want the error line with repo_id, host and step commit", line)
			}
			if strings.Contains(fmt.Sprint(lines), geToken) {
				t.Errorf("log line carries the token: %v", lines)
			}
		})
	}
	t.Run("a transient failure that passes on the retry succeeds", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.fail[app.StepProject] = []error{&app.GitLabError{Status: 502, Step: app.StepProject}}
		got, err := geOpenPR(t, rig, nil)
		if err != nil || got.PRURL == nil {
			t.Fatalf("Export = %+v, %v, want success after one repeat", got, err)
		}
		if *rig.pauses != 1 || len(rig.logs.snapshot(t)) != 0 {
			t.Errorf("pauses = %d, log lines = %v, want 1 pause and no log line", *rig.pauses, rig.logs.snapshot(t))
		}
	})
	t.Run("400 on a commit without start_branch is not retried", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		a := rig.agents["agent-1"]
		a.Skills = []domain.ExportSkill{{Name: "API Safety", Body: "Changed.\n"}, a.Skills[1]}
		rig.agents["agent-1"] = a
		before := rig.stub.count("commit")
		rig.stub.fail[app.StepCommit] = []error{&app.GitLabError{Status: 400, Step: app.StepCommit}, &app.GitLabError{Status: 400, Step: app.StepCommit}}
		_, err := geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later")
		if got := rig.stub.count("commit") - before; got != 1 {
			t.Errorf("commit attempts = %d, want 1 (the branch existed, so the 400 is not the race)", got)
		}
	})
	t.Run("a lost first-commit race is repeated once and succeeds", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.raceOnCommit = true
		got, err := geOpenPR(t, rig, nil)
		if err != nil || got.PRURL == nil || got.Installation == nil {
			t.Fatalf("Export = %+v, %v, want success after one repeat", got, err)
		}
		if n := rig.stub.count("commit"); n != 1 {
			t.Errorf("commit attempts = %d, want 1 (the repeat finds the winner's files identical)", n)
		}
		if n := rig.stub.count("project"); n != 2 {
			t.Errorf("sequences = %d, want 2", n)
		}
		if len(rig.stub.created) != 1 || *rig.pauses != 0 || len(rig.logs.snapshot(t)) != 0 {
			t.Errorf("created MRs = %d, pauses = %d, log lines = %v, want 1, 0 and none", len(rig.stub.created), *rig.pauses, rig.logs.snapshot(t))
		}
	})
	t.Run("the race and a transient failure share one repeat", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.raceOnCommit = true
		// The repeat meets a 502: no third sequence.
		rig.stub.fail[app.StepProject] = []error{nil, &app.GitLabError{Status: 502, Step: app.StepProject}}
		_, err := geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later")
		if n := rig.stub.count("project"); n != 2 {
			t.Errorf("sequences = %d, want 2 in all", n)
		}
		// The other way round: a 502 first, then the race on the repeat.
		rig = newGERig(t, geOpts{})
		rig.stub.fail[app.StepProject] = []error{&app.GitLabError{Status: 502, Step: app.StepProject}}
		rig.stub.raceOnCommit = true
		_, err = geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindExternalService, "gitlab_unavailable", "GitLab is unavailable, try again later")
		if n := rig.stub.count("project"); n != 2 {
			t.Errorf("sequences = %d, want 2 in all", n)
		}
	})
	t.Run("a commit that landed before its answer was lost is found identical", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		// The stub applies the commit, then the first answer is lost: emulate by a failing step after it.
		rig.stub.fail[app.StepOpenMRs] = []error{&app.GitLabError{Transport: true, Step: app.StepOpenMRs}}
		got, err := geOpenPR(t, rig, nil)
		if err != nil || got.PRURL == nil {
			t.Fatalf("Export = %+v, %v, want success", got, err)
		}
		if len(rig.stub.commits) != 1 {
			t.Errorf("commits = %d, want 1 (the repeat finds the files identical)", len(rig.stub.commits))
		}
	})
	t.Run("every GitLab step maps its error", func(t *testing.T) {
		for _, step := range []string{app.StepProject, app.StepBranch, app.StepFileMeta, app.StepOpenMRs, app.StepCreateMR} {
			rig := newGERig(t, geOpts{})
			rig.stub.fail[step] = []error{&app.GitLabError{Status: 403, Step: step}}
			_, err := geOpenPR(t, rig, nil)
			ae := wantError(t, err, apperr.KindValidation, "gitlab_permission_denied", "")
			if !strings.Contains(ae.Message, "in acme/web") {
				t.Errorf("step %s: message %q lacks the repository", step, ae.Message)
			}
			var ge *app.GitLabError
			if !errors.As(ae, &ge) || ge.Step != step {
				t.Errorf("step %s: cause = %v, want the *GitLabError of the step", step, errors.Unwrap(ae))
			}
		}
	})
	t.Run("a cancelled context is not retried", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		ctx, cancel := context.WithCancel(t.Context())
		rig.stub.fail[app.StepProject] = []error{&app.GitLabError{Transport: true, Step: app.StepProject}}
		cancel()
		req := geRequest()
		_, err := rig.svc.Export(ctx, "ws-1", "agent-1", req)
		if err == nil {
			t.Fatal("Export with a cancelled context succeeded")
		}
		if *rig.pauses != 0 {
			t.Errorf("pauses = %d, want 0", *rig.pauses)
		}
	})
	t.Run("a path-follow failure is not a GitLab answer", func(t *testing.T) {
		boom := errors.New("bad path")
		rig := newGERig(t, geOpts{followErr: boom})
		_, err := geOpenPR(t, rig, nil)
		if !errors.Is(err, boom) {
			t.Errorf("Export error = %v, want it to wrap %v", err, boom)
		}
		if len(rig.logs.snapshot(t)) != 0 {
			t.Errorf("a non-GitLab failure logged %v", rig.logs.snapshot(t))
		}
	})
	t.Run("no token", func(t *testing.T) {
		rig := newGERig(t, geOpts{conns: map[string]app.GitLabConn{"gitlab.com": {Name: "main-gl", HasToken: false}}})
		_, err := geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindConfig, "config_error", "GitLab connection main-gl has no token")
		if got := rig.stub.calls(); len(got) != 0 {
			t.Errorf("GitLab calls %v, want none", got)
		}
		// files needs no token
		req := geRequest()
		req.Action = domain.ActionFiles
		if _, err := rig.svc.Export(t.Context(), "ws-1", "agent-1", req); err != nil {
			t.Errorf("files without a token error = %v, want none", err)
		}
	})
}

// TestExportGitLab_Installation covers FR13, EC10, AC14.
func TestExportGitLab_Installation(t *testing.T) {
	t.Run("a renamed project: the stored path, the installation and pr_url follow it", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.stub.project.PathWithNamespace = "acme/web-renamed"
		got, err := geOpenPR(t, rig, nil)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]string{"gl-1 acme/web-renamed"}, *rig.followed); diff != "" {
			t.Errorf("followed paths mismatch (-want +got):\n%s", diff)
		}
		if got.PRURL == nil || *got.PRURL != "https://gitlab.com/acme/web-renamed/-/merge_requests/7" {
			t.Errorf("pr_url = %v, want the renamed path", got.PRURL)
		}
		if got.Installation.Repo != "acme/web-renamed" || got.Installation.ProjectID != 42 {
			t.Errorf("installation = repo %q project %d, want acme/web-renamed, 42", got.Installation.Repo, got.Installation.ProjectID)
		}
	})
	t.Run("pr_url never comes from a response", func(t *testing.T) {
		repo := geRepo()
		repo.WebURL = "https://gitlab.com/acme/web/"
		rig := newGERig(t, geOpts{repos: []app.ExportRepo{repo}})
		got, err := geOpenPR(t, rig, nil)
		if err != nil || got.PRURL == nil || *got.PRURL != "https://gitlab.com/acme/web/-/merge_requests/7" {
			t.Errorf("pr_url = %v, %v, want https://gitlab.com/acme/web/-/merge_requests/7", got.PRURL, err)
		}
	})
	t.Run("an installation's slug is kept", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		slug, version := "kept-slug", 1
		rig.store.ge.rows = map[string]domain.Installation{geKey("agent-1", "gitlab.com", 42): {
			ID: "old", AgentID: "agent-1", Slug: &slug, ManifestVersion: &version, Host: "gitlab.com", ProjectID: 42,
		}}
		if _, err := geOpenPR(t, rig, nil); err != nil {
			t.Fatal(err)
		}
		if got := rig.store.ge.upserts[0].Slug; got != "kept-slug" {
			t.Errorf("upserted slug = %q, want kept-slug", got)
		}
		if rig.stub.count("file_meta .gitlab/devdigest/review-kept-slug.yml") != 1 {
			t.Errorf("calls %v, want the job file of the kept slug", rig.stub.calls())
		}
	})
	t.Run("a lost slug race is ci_slug_conflict", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.store.ge.upsertErr = app.ErrSlugTaken
		_, err := geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindConflict, "ci_slug_conflict", "")
	})
	t.Run("a vanished row is an internal error", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		rig.store.ge.upsertErr = nil
		rig.svc = app.NewExportService(app.ExportDeps{
			Agents: fakeAgents{agents: rig.agents}, Repos: fakeRepos{gitlab: []app.ExportRepo{geRepo()}}, Store: &geVanishStore{fakeStore: rig.store},
			Runner: fakeRunner{artifact: testRunner, ok: true}, GitHub: rig.gh,
			GitLabConns: geConns{conns: map[string]app.GitLabConn{"gitlab.com": {Name: "g", HasToken: true}}},
			GitLab:      rig.stub, RetryPause: func(context.Context) error { return nil },
		})
		_, err := geOpenPR(t, rig, nil)
		wantError(t, err, apperr.KindInternal, "internal_error", "")
	})
	t.Run("two concurrent exports leave one installation", func(t *testing.T) {
		rig := newGERig(t, geOpts{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Go(func() {
				_, errs[i] = rig.svc.Export(t.Context(), "ws-1", "agent-1", geRequest())
			})
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("concurrent export %d error = %v", i, err)
			}
		}
		if n := len(rig.store.ge.rows); n != 1 {
			t.Errorf("installations = %d, want 1", n)
		}
	})
}

// geVanishStore is a Store whose GitLab installation is never found after the upsert.
type geVanishStore struct{ *fakeStore }

func (s *geVanishStore) FindGitLabInstallation(context.Context, string, string, string, int64) (domain.Installation, bool, error) {
	return domain.Installation{}, false, nil
}

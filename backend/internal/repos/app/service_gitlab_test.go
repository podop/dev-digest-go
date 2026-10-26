package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

var gitlabCom = app.GitLabConn{
	ID: "c-1", Name: "GitLab.com", BaseURL: "https://gitlab.com", Host: "gitlab.com", Token: "glpat-secret-token-value",
}

var paymentsProject = gitlab.Project{
	ID: 42, PathWithNamespace: "acme/platform/payments", Path: "payments", NamespaceFullPath: "acme/platform",
	DefaultBranch: "develop", WebURL: "https://evil.example/not-used",
}

type gitlabEnv struct {
	store    *memStore
	queue    *fakeQueue
	conns    *fakeConns
	projects *fakeProjects
	svc      *app.Service
	logs     *bytes.Buffer
}

func newGitLabEnv(conns ...app.GitLabConn) *gitlabEnv {
	e := &gitlabEnv{
		store: &memStore{}, queue: &fakeQueue{}, projects: &fakeProjects{project: paymentsProject},
		conns: &fakeConns{conns: map[string]app.GitLabConn{}}, logs: &bytes.Buffer{},
	}
	for _, c := range conns {
		e.conns.conns[c.Host] = c
	}
	log := slog.New(slog.NewTextHandler(e.logs, nil))
	e.svc = app.NewService(e.store, e.queue, "/data", app.WithGitLab(e.conns, e.projects), app.WithLog(log))
	return e
}

func (e *gitlabEnv) add(t *testing.T, url string) (domain.Repo, bool, error) {
	t.Helper()
	return e.svc.Add(t.Context(), ws, "user-1", mustParsed(t, url))
}

func TestService_Add_GitLab(t *testing.T) {
	e := newGitLabEnv(gitlabCom)

	repo, created, err := e.add(t, "https://gitlab.com/acme/platform/payments")
	if err != nil || !created {
		t.Fatalf("Add() = %+v, %v, %v, want created", repo, created, err)
	}
	want := domain.Repo{
		Provider: "gitlab", Host: "gitlab.com", ProjectID: 42, Owner: "acme/platform", Name: "payments",
		FullName: "acme/platform/payments", DefaultBranch: "develop", WebURL: "https://gitlab.com/acme/platform/payments",
	}
	got := repo
	got.ID, got.WorkspaceID, got.CreatedBy = "", "", nil
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Add() repo mismatch (-want +got):\n%s", diff)
	}
	wantCalls := []projectCall{{
		Conn: gitlab.Conn{BaseURL: "https://gitlab.com/api/v4", Token: gitlabCom.Token}, Path: "acme/platform/payments",
	}}
	if diff := cmp.Diff(wantCalls, e.projects.calls); diff != "" {
		t.Errorf("project reads mismatch (-want +got):\n%s", diff)
	}
	wantJobs := []enqueued{{ws, "clone", app.ClonePayload{RepoID: repo.ID, Owner: "acme/platform", Name: "payments"}}}
	if diff := cmp.Diff(wantJobs, e.queue.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}

	// The SSH form resolves to the same project (the path case differs): 200, same repo, no job.
	e.projects.calls = nil
	again, created, err := e.add(t, "git@gitlab.com:Acme/Platform/Payments.git")
	if err != nil || created || again.ID != repo.ID {
		t.Errorf("Add(ssh) = %+v, %v, %v, want the same repo, created=false", again, created, err)
	}
	if len(e.queue.jobs) != 1 {
		t.Errorf("jobs after duplicate add = %d, want 1", len(e.queue.jobs))
	}
	if len(e.projects.calls) != 1 || e.projects.calls[0].Path != "Acme/Platform/Payments" {
		t.Errorf("project reads of the duplicate add = %+v, want one read of the path as typed", e.projects.calls)
	}
}

func TestService_Add_GitLab_NoTokenAndNoBranch(t *testing.T) {
	anon := gitlabCom
	anon.Token = ""
	e := newGitLabEnv(anon)
	e.projects.project.DefaultBranch = ""

	repo, created, err := e.add(t, "https://gitlab.com/acme/platform/payments")
	if err != nil || !created {
		t.Fatalf("Add() = %+v, %v, %v, want created", repo, created, err)
	}
	if got := e.projects.calls[0].Conn.Token; got != "" {
		t.Errorf("token sent = %q, want none for a connection without a token", got)
	}
	if repo.DefaultBranch != "main" {
		t.Errorf("default branch = %q, want main when GitLab reports none", repo.DefaultBranch)
	}
}

func TestService_Add_GitLab_PathPrefix(t *testing.T) {
	corp := app.GitLabConn{ID: "c-2", Name: "Corp", BaseURL: "https://git.corp.example/gitlab", Host: "git.corp.example"}
	e := newGitLabEnv(corp)
	e.projects.project = gitlab.Project{ID: 7, PathWithNamespace: "group/proj", Path: "proj", NamespaceFullPath: "group"}

	repo, _, err := e.add(t, "https://git.corp.example/gitlab/group/proj")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	call := e.projects.calls[0]
	if call.Conn.BaseURL != "https://git.corp.example/gitlab/api/v4" || call.Path != "group/proj" {
		t.Errorf("project read = %+v, want the prefixed REST root and the path without the prefix", call)
	}
	if repo.WebURL != "https://git.corp.example/gitlab/group/proj" {
		t.Errorf("web url = %q, want it built from the connection base", repo.WebURL)
	}

	e.projects.calls = nil
	_, _, err = e.add(t, "https://git.corp.example/other/group/proj")
	assertURLIssue(t, err, "This URL is outside https://git.corp.example/gitlab")
	if len(e.projects.calls) != 0 {
		t.Errorf("project reads for a URL outside the prefix = %d, want none", len(e.projects.calls))
	}
}

func TestService_Add_GitLab_NotConnected(t *testing.T) {
	e := newGitLabEnv()
	for _, url := range []string{"https://gitlab.com/acme/web", "git@gitlab.com:acme/web.git"} {
		t.Run(url, func(t *testing.T) {
			_, created, err := e.add(t, url)
			ae, ok := apperr.As(err)
			if !ok || created || ae.Kind != apperr.KindValidation || ae.Code != "repo_host_not_connected" {
				t.Fatalf("Add(%q) = %v, %v, want 422 repo_host_not_connected", url, created, err)
			}
			if want := "No GitLab connection for gitlab.com. Add one in Settings → GitLab Integration."; ae.Message != want {
				t.Errorf("message = %q, want %q", ae.Message, want)
			}
			if diff := cmp.Diff(map[string]string{"host": "gitlab.com"}, ae.Details); diff != "" {
				t.Errorf("details mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if len(e.projects.calls)+len(e.queue.jobs)+len(e.store.adds) != 0 {
		t.Errorf("calls %d, jobs %d, inserts %d, want none", len(e.projects.calls), len(e.queue.jobs), len(e.store.adds))
	}

	// Without the GitLab port every GitLab URL is not connected, too.
	svc := app.NewService(&memStore{}, &fakeQueue{}, "/data")
	if _, _, err := svc.Add(t.Context(), ws, "u", mustParsed(t, "https://gitlab.com/acme/web")); err == nil {
		t.Error("Add() without a GitLab port succeeded, want repo_host_not_connected")
	}
}

func TestService_Add_GitLab_SSHHostBinding(t *testing.T) {
	t.Run("several connections on one host name", func(t *testing.T) {
		a := app.GitLabConn{ID: "a", Name: "A", BaseURL: "https://git.corp.example", Host: "git.corp.example"}
		b := app.GitLabConn{ID: "b", Name: "B", BaseURL: "https://git.corp.example:8443", Host: "git.corp.example:8443"}
		e := newGitLabEnv(a, b)
		_, _, err := e.add(t, "git@git.corp.example:group/proj.git")
		assertURLIssue(t, err, "Several GitLab connections use git.corp.example; paste the HTTPS URL")
		if len(e.projects.calls) != 0 {
			t.Errorf("project reads = %d, want none", len(e.projects.calls))
		}
	})
	t.Run("one connection on another port stores its host key", func(t *testing.T) {
		b := app.GitLabConn{ID: "b", Name: "B", BaseURL: "https://git.corp.example:8443", Host: "git.corp.example:8443"}
		e := newGitLabEnv(b)
		e.projects.project = gitlab.Project{ID: 7, PathWithNamespace: "group/proj", Path: "proj", NamespaceFullPath: "group"}
		repo, _, err := e.add(t, "git@git.corp.example:group/proj.git")
		if err != nil {
			t.Fatalf("Add() error = %v", err)
		}
		if repo.Host != "git.corp.example:8443" || repo.WebURL != "https://git.corp.example:8443/group/proj" {
			t.Errorf("repo host/web = %q %q, want the connection's host key and base", repo.Host, repo.WebURL)
		}
	})
}

func TestService_Add_GitLab_ProjectErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind apperr.Kind
		wantCode string
		wantMsg  string
	}{
		{"404", &gitlab.StatusError{Status: 404}, apperr.KindValidation, "gitlab_project_not_found",
			"Project acme/platform/payments was not found on gitlab.com, or the token cannot see it"},
		{"redirect", fmt.Errorf("gitlab project: %w", gitlab.ErrRedirect), apperr.KindValidation, "gitlab_project_not_found",
			"GitLab reports that acme/platform/payments has moved; paste the project's current URL"},
		{"401", &gitlab.StatusError{Status: 401}, apperr.KindValidation, "gitlab_permission_denied",
			"GitLab rejected the token of connection GitLab.com"},
		{"403", &gitlab.StatusError{Status: 403}, apperr.KindValidation, "gitlab_permission_denied",
			"GitLab rejected the token of connection GitLab.com"},
		{"429", &gitlab.StatusError{Status: 429}, apperr.KindExternalService, "gitlab_rate_limited",
			"GitLab rate limit reached, try again later"},
		{"500", &gitlab.StatusError{Status: 500}, apperr.KindExternalService, "gitlab_unavailable",
			"GitLab is unavailable, try again later"},
		{"other 4xx", &gitlab.StatusError{Status: 418}, apperr.KindExternalService, "gitlab_unavailable",
			"GitLab is unavailable, try again later"},
		{"not the API", fmt.Errorf("gitlab project: %w", gitlab.ErrNotAPI), apperr.KindExternalService, "gitlab_unavailable",
			"GitLab is unavailable, try again later"},
		{"connection error", errors.New("dial tcp: connection refused"), apperr.KindExternalService, "gitlab_unavailable",
			"GitLab is unavailable, try again later"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newGitLabEnv(gitlabCom)
			e.projects.err = tc.err
			repo, created, err := e.add(t, "https://gitlab.com/acme/platform/payments")
			ae, ok := apperr.As(err)
			if !ok || created || repo.ID != "" {
				t.Fatalf("Add() = %+v, %v, %v, want an application error", repo, created, err)
			}
			if ae.Kind != tc.wantKind || ae.Code != tc.wantCode || ae.Message != tc.wantMsg {
				t.Errorf("error = %v %q %q, want %v %q %q", ae.Kind, ae.Code, ae.Message, tc.wantKind, tc.wantCode, tc.wantMsg)
			}
			if strings.Contains(err.Error(), gitlabCom.Token) {
				t.Errorf("error %q contains the token", err)
			}
			if len(e.store.adds) != 0 || len(e.queue.jobs) != 0 {
				t.Errorf("inserts %d, jobs %d, want nothing stored", len(e.store.adds), len(e.queue.jobs))
			}
		})
	}
}

func TestService_Add_GitLab_CanceledContextIsNotUnavailable(t *testing.T) {
	e := newGitLabEnv(gitlabCom)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	e.projects.err = context.Canceled
	_, _, err := e.svc.Add(ctx, ws, "u", mustParsed(t, "https://gitlab.com/acme/platform/payments"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Add() error = %v, want context.Canceled", err)
	}
	if ae, ok := apperr.As(err); ok {
		t.Errorf("Add() error is the client error %q, want the context error", ae.Code)
	}
}

func TestService_Add_GitLab_RefusesUntrustedProjectPaths(t *testing.T) {
	tests := []struct {
		name    string
		project gitlab.Project
	}{
		{"traversal", gitlab.Project{ID: 42, PathWithNamespace: "acme/../x", Path: "x", NamespaceFullPath: "acme/.."}},
		{"query", gitlab.Project{ID: 42, PathWithNamespace: "acme/p?x=1", Path: "p?x=1", NamespaceFullPath: "acme"}},
		{"userinfo", gitlab.Project{ID: 42, PathWithNamespace: "acme/p@evil", Path: "p@evil", NamespaceFullPath: "acme"}},
		{"one segment", gitlab.Project{ID: 42, PathWithNamespace: "payments", Path: "payments"}},
		{"git suffix", gitlab.Project{ID: 42, PathWithNamespace: "acme/p.git", Path: "p.git", NamespaceFullPath: "acme"}},
		{"namespace contradicts path", gitlab.Project{ID: 42, PathWithNamespace: "acme/p", Path: "p", NamespaceFullPath: "other"}},
		{"project path contradicts path", gitlab.Project{ID: 42, PathWithNamespace: "acme/p", Path: "q", NamespaceFullPath: "acme"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newGitLabEnv(gitlabCom)
			e.projects.project = tc.project
			_, _, err := e.add(t, "https://gitlab.com/acme/p")
			if ae, ok := apperr.As(err); !ok || ae.Code != "gitlab_unavailable" {
				t.Errorf("Add() error = %v, want gitlab_unavailable", err)
			}
			if len(e.store.adds) != 0 {
				t.Errorf("inserts = %d, want nothing stored", len(e.store.adds))
			}
		})
	}
}

func TestService_Add_GitLab_PathConflict(t *testing.T) {
	e := newGitLabEnv(gitlabCom)
	// A stored repo holds the path, but it is project 7: the project was renamed or moved, and
	// the path now belongs to project 42.
	stale, _, err := e.store.Add(t.Context(), app.NewRepo{
		WorkspaceID: ws, Ref: domain.Ref{Owner: "acme/platform", Name: "payments"},
		Provider: codehost.GitLab, Host: "gitlab.com", ProjectID: 7, WebURL: "https://gitlab.com/acme/platform/payments",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, created, err := e.add(t, "https://gitlab.com/acme/platform/payments")
	ae, ok := apperr.As(err)
	if !ok || created || ae.Kind != apperr.KindConflict || ae.Code != "gitlab_path_conflict" {
		t.Fatalf("Add() = %v, %v, want 409 gitlab_path_conflict", created, err)
	}
	if want := "acme/platform/payments in this workspace is a different GitLab project that was renamed or moved; sync it first"; ae.Message != want {
		t.Errorf("message = %q, want %q", ae.Message, want)
	}
	if diff := cmp.Diff(map[string]string{"repo_id": stale.ID}, ae.Details); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
	if len(e.queue.jobs) != 0 {
		t.Errorf("jobs = %d, want none", len(e.queue.jobs))
	}
}

func TestService_Add_GitLab_FollowsRename(t *testing.T) {
	e := newGitLabEnv(gitlabCom)
	old, _, err := e.store.Add(t.Context(), app.NewRepo{
		WorkspaceID: ws, Ref: domain.Ref{Owner: "acme/platform", Name: "old"},
		Provider: codehost.GitLab, Host: "gitlab.com", ProjectID: 42, WebURL: "https://gitlab.com/acme/platform/old",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, created, err := e.add(t, "https://gitlab.com/acme/platform/payments")
	if err != nil || created || got.ID != old.ID {
		t.Fatalf("Add() = %+v, %v, %v, want the existing repo, created=false", got, created, err)
	}
	if got.FullName != "acme/platform/payments" || got.Name != "payments" || got.WebURL != "https://gitlab.com/acme/platform/payments" {
		t.Errorf("returned repo = %+v, want the current path", got)
	}
	if stored := e.store.get(old.ID); stored.FullName != "acme/platform/payments" || stored.Owner != "acme/platform" {
		t.Errorf("stored repo = %+v, want the current path", stored)
	}
	if len(e.queue.jobs) != 0 {
		t.Errorf("jobs = %d, want none", len(e.queue.jobs))
	}
}

func TestService_Add_GitLab_RenameCollisionKeepsPathAndWarns(t *testing.T) {
	e := newGitLabEnv(gitlabCom)
	old, _, _ := e.store.Add(t.Context(), app.NewRepo{
		WorkspaceID: ws, Ref: domain.Ref{Owner: "acme/platform", Name: "old"},
		Provider: codehost.GitLab, Host: "gitlab.com", ProjectID: 42, WebURL: "https://gitlab.com/acme/platform/old",
	})
	// Another repo holds the new full name, so the new path is skipped; the lookup by project id
	// still finds the first repo.
	_, _, err := e.store.Add(t.Context(), app.NewRepo{
		WorkspaceID: ws, Ref: domain.Ref{Owner: "acme/platform", Name: "payments"},
		Provider: codehost.GitLab, Host: "gitlab.com", ProjectID: 7, WebURL: "https://gitlab.com/acme/platform/payments",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, created, err := e.add(t, "https://gitlab.com/acme/platform/payments")
	if err != nil || created || got.ID != old.ID {
		t.Fatalf("Add() = %+v, %v, %v, want the first repo unchanged", got, created, err)
	}
	if got.FullName != "acme/platform/old" {
		t.Errorf("full name = %q, want the stored path kept", got.FullName)
	}
	if n := strings.Count(e.logs.String(), "level=WARN"); n != 1 {
		t.Errorf("WARN lines = %d, want 1:\n%s", n, e.logs.String())
	}
	if log := e.logs.String(); !strings.Contains(log, "repo="+old.ID) || strings.Contains(log, gitlabCom.Token) {
		t.Errorf("log %q must name the repo and no token", log)
	}
}

func TestService_Add_GitLab_SameNameAsGitHub(t *testing.T) {
	e := newGitLabEnv(gitlabCom)
	e.projects.project = gitlab.Project{ID: 5, PathWithNamespace: "acme/web", Path: "web", NamespaceFullPath: "acme"}
	gh, created, err := e.add(t, "https://github.com/acme/web")
	if err != nil || !created {
		t.Fatalf("Add(github) = %+v, %v, %v", gh, created, err)
	}
	gl, created, err := e.add(t, "https://gitlab.com/acme/web")
	if err != nil || !created || gl.ID == gh.ID {
		t.Errorf("Add(gitlab) = %+v, %v, %v, want a separate repo", gl, created, err)
	}
	if len(e.projects.calls) != 1 {
		t.Errorf("project reads = %d, want 1 (none for GitHub)", len(e.projects.calls))
	}
	want := []app.NewRepo{
		{WorkspaceID: ws, CreatedBy: "user-1", Ref: domain.Ref{Owner: "acme", Name: "web"}, Provider: "github", Host: "github.com"},
		{WorkspaceID: ws, CreatedBy: "user-1", Ref: domain.Ref{Owner: "acme", Name: "web"}, Provider: "gitlab", Host: "gitlab.com",
			ProjectID: 5, WebURL: "https://gitlab.com/acme/web", DefaultBranch: "main"},
	}
	if diff := cmp.Diff(want, e.store.adds[:2]); diff != "" {
		t.Errorf("inserts mismatch (-want +got):\n%s", diff)
	}
}

func TestGitLabConn_NeverPrintsTheToken(t *testing.T) {
	for _, s := range []string{fmt.Sprintf("%v", gitlabCom), fmt.Sprintf("%+v", gitlabCom), fmt.Sprintf("%#v", gitlabCom)} {
		if strings.Contains(s, gitlabCom.Token) {
			t.Errorf("formatted conn %q contains the token", s)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "conn", gitlabCom)
	if strings.Contains(buf.String(), gitlabCom.Token) {
		t.Errorf("log line %q contains the token", buf.String())
	}
}

func assertURLIssue(t *testing.T, err error, message string) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Kind != apperr.KindValidation || ae.Code != "validation_error" {
		t.Fatalf("error = %v, want 422 validation_error", err)
	}
	raw, jerr := json.Marshal(ae.Details)
	if jerr != nil {
		t.Fatal(jerr)
	}
	var details []struct {
		Message string `json:"message"`
		Params  struct {
			Issue struct {
				Path []any `json:"path"`
			} `json:"issue"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &details); err != nil || len(details) != 1 {
		t.Fatalf("details = %s, want one issue", raw)
	}
	if details[0].Message != message || len(details[0].Params.Issue.Path) != 1 || details[0].Params.Issue.Path[0] != "url" {
		t.Errorf("issue = %+v, want %q at [\"url\"]", details[0], message)
	}
}

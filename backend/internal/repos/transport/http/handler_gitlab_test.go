package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/repos/app"
)

const glHost = "gitlab.example.com"

// fakeConns is the connection port: it resolves the connections of the test by host key.
type fakeConns struct {
	conns map[string]app.GitLabConn
}

func (f *fakeConns) Resolve(_ context.Context, hostKey string) (app.GitLabConn, bool, error) {
	c, ok := f.conns[hostKey]
	return c, ok, nil
}

func (f *fakeConns) HostKeysByName(_ context.Context, name string) ([]string, error) {
	var keys []string
	for k := range f.conns {
		if strings.SplitN(k, ":", 2)[0] == name {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// fakeProjects answers the one project read of the add.
type fakeProjects struct {
	project gitlab.Project
	err     error
	paths   []string
}

func (f *fakeProjects) ProjectByPath(_ context.Context, _ gitlab.Conn, path string) (gitlab.Project, error) {
	f.paths = append(f.paths, path)
	return f.project, f.err
}

// glEnv is an env whose service knows one GitLab connection on glHost (a row and a port entry).
type glEnv struct {
	*env
	projects *fakeProjects
	connID   string
}

func newGitLabEnv(t *testing.T, base string) *glEnv {
	t.Helper()
	projects := &fakeProjects{project: gitlab.Project{
		ID: 42, PathWithNamespace: "acme/platform/web", Path: "web", NamespaceFullPath: "acme/platform", DefaultBranch: "trunk",
	}}
	conns := &fakeConns{conns: map[string]app.GitLabConn{}}
	v := newEnv(t, app.WithGitLab(conns, projects))
	g := &glEnv{env: v, projects: projects}
	g.connect(t, conns, glHost, base)
	return g
}

// connect inserts the connection row and registers it in the port.
func (g *glEnv) connect(t *testing.T, conns *fakeConns, host, base string) {
	t.Helper()
	if err := g.db.Raw(`INSERT INTO gitlab_connections (name, base_url, host) VALUES ('corp', ?, ?) RETURNING id`, base, host).
		Scan(&g.connID).Error; err != nil {
		t.Fatal(err)
	}
	conns.conns[host] = app.GitLabConn{ID: g.connID, Name: "corp", BaseURL: base, Host: host, Token: "glpat-secret-token-value"}
}

func TestAdd_GitLab_Created(t *testing.T) {
	g := newGitLabEnv(t, "https://"+glHost)
	res := post(t, g.env, "https://gitlab.example.com/acme/platform/web.git")
	if res.status != http.StatusCreated {
		t.Fatalf("POST /repos status = %d (%s), want 201", res.status, res.body)
	}
	got := decode[map[string]any](t, res)
	id, _ := got["id"].(string)
	want := map[string]any{
		"id": id, "workspace_id": g.ws, "owner": "acme/platform", "name": "web", "full_name": "acme/platform/web",
		"default_branch": "trunk", "clone_path": nil, "last_polled_at": nil, "created_by": g.user,
		"provider": "gitlab", "host": glHost, "web_url": "https://gitlab.example.com/acme/platform/web",
		"connection_id": g.connID,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("POST /repos body mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"acme/platform/web"}, g.projects.paths); diff != "" {
		t.Errorf("project reads mismatch (-want +got):\n%s", diff)
	}
	wantJobs := []queued{{g.ws, "clone", app.ClonePayload{RepoID: id, Owner: "acme/platform", Name: "web"}}}
	if diff := cmp.Diff(wantJobs, g.queue.jobs); diff != "" {
		t.Errorf("enqueued jobs mismatch (-want +got):\n%s", diff)
	}

	// The stored project is the repository: adding it again, by SSH, answers 200 and enqueues nothing.
	g.queue.reset()
	again := post(t, g.env, "git@gitlab.example.com:acme/platform/web.git")
	if again.status != http.StatusOK || decode[map[string]any](t, again)["id"] != id {
		t.Errorf("second POST /repos = %d (%s), want 200 with the same repo", again.status, again.body)
	}
	if len(g.queue.jobs) != 0 {
		t.Errorf("jobs after a duplicate add = %v, want none", g.queue.kinds())
	}
}

func TestAdd_GitLab_RenamedProjectKeepsItsRepo(t *testing.T) {
	g := newGitLabEnv(t, "https://"+glHost)
	first := decode[map[string]any](t, post(t, g.env, "https://gitlab.example.com/acme/platform/web"))
	g.queue.reset()

	g.projects.project.PathWithNamespace, g.projects.project.Path = "acme/platform/web-next", "web-next"
	res := post(t, g.env, "https://gitlab.example.com/acme/platform/web-next")
	if res.status != http.StatusOK {
		t.Fatalf("POST /repos status = %d (%s), want 200", res.status, res.body)
	}
	got := decode[map[string]any](t, res)
	if got["id"] != first["id"] || got["full_name"] != "acme/platform/web-next" || got["name"] != "web-next" ||
		got["web_url"] != "https://gitlab.example.com/acme/platform/web-next" {
		t.Errorf("POST /repos renamed = %v, want the first repo on the new path (FR47)", got)
	}
	if len(g.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", g.queue.kinds())
	}
}

func TestAdd_GitLab_PathConflict(t *testing.T) {
	g := newGitLabEnv(t, "https://"+glHost)
	first := decode[map[string]any](t, post(t, g.env, "https://gitlab.example.com/acme/platform/web"))
	g.queue.reset()

	// Another project now owns the stored path of the first one.
	g.projects.project.ID = 43
	res := post(t, g.env, "https://gitlab.example.com/acme/platform/web")
	if res.status != http.StatusConflict {
		t.Fatalf("POST /repos status = %d (%s), want 409", res.status, res.body)
	}
	got := decode[httpx.ErrorBody](t, res).Error
	if got.Code != "gitlab_path_conflict" {
		t.Errorf("code = %q, want gitlab_path_conflict", got.Code)
	}
	if diff := cmp.Diff(map[string]any{"repo_id": first["id"]}, got.Details); diff != "" {
		t.Errorf("details mismatch (-want +got):\n%s", diff)
	}
	if n := g.count(t, "repos"); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
	if len(g.queue.jobs) != 0 {
		t.Errorf("jobs = %v, want none", g.queue.kinds())
	}
}

func TestAdd_GitLab_Errors(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
		wantDetail map[string]any
	}{
		{
			name: "no connection for the host", url: "https://other.example.com/g/p",
			wantStatus: http.StatusUnprocessableEntity, wantCode: "repo_host_not_connected",
			wantMsg:    "No GitLab connection for other.example.com. Add one in Settings → GitLab Integration.",
			wantDetail: map[string]any{"host": "other.example.com"},
		},
		{
			name: "no connection for an ssh host", url: "git@other.example.com:g/p",
			wantStatus: http.StatusUnprocessableEntity, wantCode: "repo_host_not_connected",
			wantMsg:    "No GitLab connection for other.example.com. Add one in Settings → GitLab Integration.",
			wantDetail: map[string]any{"host": "other.example.com"},
		},
		{
			name: "project not found", url: "https://gitlab.example.com/g/p",
			err:        &gitlab.StatusError{Status: http.StatusNotFound},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "gitlab_project_not_found",
			wantMsg: "Project g/p was not found on gitlab.example.com, or the token cannot see it",
		},
		{
			name: "project moved", url: "https://gitlab.example.com/g/p",
			err:        gitlab.ErrRedirect,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "gitlab_project_not_found",
			wantMsg: "GitLab reports that g/p has moved; paste the project's current URL",
		},
		{
			name: "token rejected", url: "https://gitlab.example.com/g/p",
			err:        &gitlab.StatusError{Status: http.StatusForbidden},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "gitlab_permission_denied",
			wantMsg: "GitLab rejected the token of connection corp",
		},
		{
			name: "rate limited", url: "https://gitlab.example.com/g/p",
			err:        &gitlab.StatusError{Status: http.StatusTooManyRequests},
			wantStatus: http.StatusBadGateway, wantCode: "gitlab_rate_limited",
			wantMsg: "GitLab rate limit reached, try again later",
		},
		{
			name: "unavailable", url: "https://gitlab.example.com/g/p",
			err:        &gitlab.StatusError{Status: http.StatusBadGateway, Message: "boom glpat-secret-token-value"},
			wantStatus: http.StatusBadGateway, wantCode: "gitlab_unavailable",
			wantMsg: "GitLab is unavailable, try again later",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newGitLabEnv(t, "https://"+glHost)
			g.projects.err = tc.err
			res := post(t, g.env, tc.url)
			if res.status != tc.wantStatus {
				t.Fatalf("POST /repos %q status = %d (%s), want %d", tc.url, res.status, res.body, tc.wantStatus)
			}
			got := decode[httpx.ErrorBody](t, res).Error
			if got.Code != tc.wantCode || got.Message != tc.wantMsg {
				t.Errorf("error = %q %q, want %q %q", got.Code, got.Message, tc.wantCode, tc.wantMsg)
			}
			if tc.wantDetail != nil {
				if diff := cmp.Diff(tc.wantDetail, got.Details); diff != "" {
					t.Errorf("details mismatch (-want +got):\n%s", diff)
				}
			}
			if strings.Contains(res.body, "glpat-") {
				t.Errorf("body %s leaks the token", res.body)
			}
			if n := g.count(t, "repos"); n != 0 {
				t.Errorf("repo rows = %d, want 0", n)
			}
			if len(g.queue.jobs) != 0 {
				t.Errorf("jobs = %v, want none", g.queue.kinds())
			}
		})
	}
}

func TestAdd_GitLab_NoGitLabWiring(t *testing.T) {
	v := newEnv(t)
	res := post(t, v, "https://gitlab.com/o/r")
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("POST /repos status = %d (%s), want 422", res.status, res.body)
	}
	got := decode[httpx.ErrorBody](t, res).Error
	if got.Code != "repo_host_not_connected" || !cmp.Equal(got.Details, map[string]any{"host": "gitlab.com"}) {
		t.Errorf("error = %+v, want repo_host_not_connected for gitlab.com", got)
	}
}

func TestAdd_GitLab_OutsideBasePath(t *testing.T) {
	g := newGitLabEnv(t, "https://"+glHost+"/gitlab")
	res := post(t, g.env, "https://gitlab.example.com/other/g/p")
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("POST /repos status = %d (%s), want 422", res.status, res.body)
	}
	got := decode[httpx.ErrorBody](t, res).Error
	if got.Code != "validation_error" || !strings.Contains(res.body, "This URL is outside https://gitlab.example.com/gitlab") {
		t.Errorf("error = %+v (%s), want validation_error naming the base URL", got, res.body)
	}
	if len(g.projects.paths) != 0 {
		t.Errorf("project reads = %v, want none", g.projects.paths)
	}

	g.projects.project.PathWithNamespace, g.projects.project.Path, g.projects.project.NamespaceFullPath = "g/p", "p", "g"
	if res := post(t, g.env, "https://gitlab.example.com/gitlab/g/p"); res.status != http.StatusCreated {
		t.Fatalf("POST /repos inside the base path status = %d (%s), want 201", res.status, res.body)
	}
	if diff := cmp.Diff([]string{"g/p"}, g.projects.paths); diff != "" {
		t.Errorf("project reads mismatch (-want +got):\n%s", diff)
	}
}

// TestList_GitLabBinding is AC5 on the repos side: deleting a connection leaves the repo
// listed, not connected, with its stored web URL; a connection for the same host re-binds it.
func TestList_GitLabBinding(t *testing.T) {
	g := newGitLabEnv(t, "https://"+glHost)
	post(t, g.env, "https://gitlab.example.com/acme/platform/web")
	post(t, g.env, "https://github.com/acme/web")

	list := func() map[string]map[string]any {
		t.Helper()
		res := g.do(t, http.MethodGet, "/repos", "")
		if res.status != http.StatusOK {
			t.Fatalf("GET /repos status = %d (%s), want 200", res.status, res.body)
		}
		out := map[string]map[string]any{}
		for _, r := range decode[[]map[string]any](t, res) {
			out[r["provider"].(string)] = r
		}
		return out
	}

	got := list()
	if got["gitlab"]["connection_id"] != g.connID || got["github"]["connection_id"] != nil ||
		got["github"]["web_url"] != "https://github.com/acme/web" || got["github"]["host"] != "github.com" {
		t.Errorf("GET /repos = %v, want the GitLab repo bound and the GitHub one not", got)
	}

	if err := g.db.Exec(`DELETE FROM gitlab_connections WHERE id = ?`, g.connID).Error; err != nil {
		t.Fatal(err)
	}
	got = list()
	if got["gitlab"]["connection_id"] != nil || got["gitlab"]["web_url"] != "https://gitlab.example.com/acme/platform/web" {
		t.Errorf("GET /repos after the delete = %v, want connection_id null and the stored web_url", got["gitlab"])
	}

	var again string
	if err := g.db.Raw(`INSERT INTO gitlab_connections (name, base_url, host) VALUES ('new', 'https://gitlab.example.com', ?) RETURNING id`, glHost).
		Scan(&again).Error; err != nil {
		t.Fatal(err)
	}
	if got = list(); got["gitlab"]["connection_id"] != again {
		t.Errorf("GET /repos after a new connection = %v, want it re-bound to %s", got["gitlab"], again)
	}
}

func TestAdd_SameNameOnTwoHosts(t *testing.T) {
	g := newGitLabEnv(t, "https://"+glHost)
	g.projects.project = gitlab.Project{ID: 7, PathWithNamespace: "acme/web", Path: "web", NamespaceFullPath: "acme"}
	if res := post(t, g.env, "https://github.com/acme/web"); res.status != http.StatusCreated {
		t.Fatalf("POST GitHub acme/web = %d (%s), want 201", res.status, res.body)
	}
	if res := post(t, g.env, "https://gitlab.example.com/acme/web"); res.status != http.StatusCreated {
		t.Fatalf("POST GitLab acme/web = %d (%s), want 201 next to the GitHub repo", res.status, res.body)
	}
	if n := g.count(t, "repos"); n != 2 {
		t.Errorf("repo rows = %d, want 2", n)
	}
}

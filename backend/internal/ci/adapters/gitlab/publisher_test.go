package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/ci/app"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

const (
	sha  = "0123456789abcdef0123456789abcdef01234567"
	hash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

func TestPublisher_Project(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{
		"id": 7, "path": "web", "path_with_namespace": "acme/web", "default_branch": "trunk",
		"web_url": "https://elsewhere.example/acme/web", "ci_config_path": "ci/main.yml",
		"namespace": map[string]any{"full_path": "acme"},
	}))
	got, err := s.publisher().Project(t.Context(), ref)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	want := app.GitLabProject{PathWithNamespace: "acme/web", DefaultBranch: "trunk", CIConfigPath: "ci/main.yml"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Project() mismatch (-want +got):\n%s", diff)
	}
	r := s.req(0)
	if r.Method != "GET" || r.URI != "/api/v4/projects/7" || r.Header.Get("PRIVATE-TOKEN") != testToken {
		t.Errorf("request = %s %s token %q, want GET /api/v4/projects/7 with the token", r.Method, r.URI, r.Header.Get("PRIVATE-TOKEN"))
	}
}

func TestPublisher_Branch(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.RequestURI, "/branches/missing") {
			writeJSON(w, 404, map[string]any{"message": "404 Branch Not Found"})
			return
		}
		writeJSON(w, 200, map[string]any{"name": "devdigest/ci", "commit": map[string]any{"id": sha}})
	})
	p := s.publisher()
	got, found, err := p.Branch(t.Context(), ref, "devdigest/ci")
	if err != nil || !found || got != (app.GitLabBranch{Name: "devdigest/ci", HeadSHA: sha}) {
		t.Errorf("Branch(devdigest/ci) = %+v, %v, %v, want the branch", got, found, err)
	}
	if _, found, err := p.Branch(t.Context(), ref, "missing"); err != nil || found {
		t.Errorf("Branch(missing) = found %v, err %v, want not found and no error", found, err)
	}
}

func TestPublisher_FileSHA256(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.RequestURI, "gone") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("X-Gitlab-Content-Sha256", strings.ToUpper(hash))
		w.WriteHeader(200)
	})
	p := s.publisher()
	got, found, err := p.FileSHA256(t.Context(), ref, ".gitlab/devdigest/a.yml", "main")
	if err != nil || !found || got != hash {
		t.Fatalf("FileSHA256() = %q, %v, %v, want the lower-case hash", got, found, err)
	}
	r := s.req(0)
	if r.Method != "HEAD" || r.URI != "/api/v4/projects/7/repository/files/.gitlab%2Fdevdigest%2Fa.yml?ref=main" {
		t.Errorf("request = %s %s, want a HEAD of the escaped file with ref=main", r.Method, r.URI)
	}
	if _, found, err := p.FileSHA256(t.Context(), ref, "gone.yml", "main"); err != nil || found {
		t.Errorf("FileSHA256(gone) = found %v, err %v, want not found and no error", found, err)
	}
}

func TestPublisher_Commit(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, map[string]any{"id": sha}) })
	err := s.publisher().Commit(t.Context(), ref, app.GitLabCommit{
		Branch: "devdigest/ci", StartBranch: "main", Message: "Add DevDigest review",
		Actions: []app.GitLabCommitAction{{Action: "create", Path: ".gitlab/devdigest/a.yml", Content: "x: 1\n"}},
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	r := s.req(0)
	if r.Method != "POST" || r.URI != "/api/v4/projects/7/repository/commits" {
		t.Errorf("request = %s %s, want POST …/repository/commits", r.Method, r.URI)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(r.Body), &raw); err != nil {
		t.Fatalf("body %q is not JSON: %v", r.Body, err)
	}
	if raw["branch"] != "devdigest/ci" || raw["start_branch"] != "main" || raw["commit_message"] != "Add DevDigest review" {
		t.Errorf("body = %v, want branch, start_branch and commit_message", raw)
	}
	actions, _ := raw["actions"].([]any)
	if len(actions) != 1 {
		t.Fatalf("actions = %v, want one", raw["actions"])
	}
	a, _ := actions[0].(map[string]any)
	want := map[string]any{"action": "create", "file_path": ".gitlab/devdigest/a.yml", "content": "x: 1\n", "encoding": "text"}
	if diff := cmp.Diff(want, a); diff != "" {
		t.Errorf("action mismatch (-want +got):\n%s", diff)
	}
}

func TestPublisher_OpenMR(t *testing.T) {
	var list []any
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, list) })
	mr := func(iid int) map[string]any {
		return map[string]any{"iid": iid, "title": "t", "state": "opened", "source_branch": "devdigest/ci", "target_branch": "main",
			"source_project_id": 7, "target_project_id": 7, "web_url": "https://other.example/x/-/merge_requests/1"}
	}
	p := s.publisher()

	list = []any{mr(12), mr(15)}
	iid, found, err := p.OpenMR(t.Context(), ref, "devdigest/ci", "main")
	if err != nil || !found || iid != 12 {
		t.Errorf("OpenMR() = %d, %v, %v, want the first, 12", iid, found, err)
	}
	u, _ := url.ParseRequestURI(s.req(0).URI)
	if u.Path != "/api/v4/projects/7/merge_requests" || u.Query().Get("state") != "opened" ||
		u.Query().Get("source_branch") != "devdigest/ci" || u.Query().Get("target_branch") != "main" {
		t.Errorf("request = %s, want the opened MRs from devdigest/ci to main", s.req(0).URI)
	}

	list = []any{}
	if iid, found, err := p.OpenMR(t.Context(), ref, "devdigest/ci", "main"); err != nil || found || iid != 0 {
		t.Errorf("OpenMR(none) = %d, %v, %v, want 0, false, nil", iid, found, err)
	}
}

func TestPublisher_CreateMR(t *testing.T) {
	conflict := false
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		if conflict {
			writeJSON(w, 409, map[string]any{"message": []string{"Another open merge request already exists for this source branch"}})
			return
		}
		writeJSON(w, 201, map[string]any{"iid": 21, "title": "Add review", "state": "opened",
			"source_branch": "devdigest/ci", "target_branch": "main", "source_project_id": 7, "target_project_id": 7})
	})
	p := s.publisher()
	in := app.GitLabNewMR{SourceBranch: "devdigest/ci", TargetBranch: "main", Title: "Add review", Description: "d", RemoveSourceBranch: true}

	iid, err := p.CreateMR(t.Context(), ref, in)
	if err != nil || iid != 21 {
		t.Fatalf("CreateMR() = %d, %v, want 21", iid, err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(s.req(0).Body), &sent); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"source_branch": "devdigest/ci", "target_branch": "main", "title": "Add review", "description": "d", "remove_source_branch": true}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}

	conflict = true
	_, err = p.CreateMR(t.Context(), ref, in)
	if ge := gitLabError(t, err); ge.Status != 409 || ge.Step != app.StepCreateMR || ge.Transport {
		t.Errorf("CreateMR(conflict) error = %+v, want Status 409, Step %s", ge, app.StepCreateMR)
	}
}

// publishCalls runs every publisher method once and returns its error with the step it must name.
func publishCalls(p *cigitlabPublisher) map[string]func(context.Context) (string, error) {
	return map[string]func(context.Context) (string, error){
		"project": func(ctx context.Context) (string, error) {
			_, err := p.Project(ctx, ref)
			return app.StepProject, err
		},
		"branch": func(ctx context.Context) (string, error) {
			_, _, err := p.Branch(ctx, ref, "devdigest/ci")
			return app.StepBranch, err
		},
		"file_meta": func(ctx context.Context) (string, error) {
			_, _, err := p.FileSHA256(ctx, ref, "a.yml", "main")
			return app.StepFileMeta, err
		},
		"commit": func(ctx context.Context) (string, error) {
			err := p.Commit(ctx, ref, app.GitLabCommit{Branch: "b", Message: "m",
				Actions: []app.GitLabCommitAction{{Action: "create", Path: "a.yml", Content: "x"}}})
			return app.StepCommit, err
		},
		"open_merge_requests": func(ctx context.Context) (string, error) {
			_, _, err := p.OpenMR(ctx, ref, "devdigest/ci", "main")
			return app.StepOpenMRs, err
		},
		"create_merge_request": func(ctx context.Context) (string, error) {
			_, err := p.CreateMR(ctx, ref, app.GitLabNewMR{SourceBranch: "devdigest/ci", TargetBranch: "main", Title: "t"})
			return app.StepCreateMR, err
		},
	}
}

func TestPublisher_StatusMapsToGitLabError(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 422, 429, 500, 503} {
		s := newStub(t, status(code, "boom "+testToken))
		for name, call := range publishCalls(s.publisher()) {
			t.Run(name+"/"+http.StatusText(code), func(t *testing.T) {
				before := s.count()
				step, err := call(t.Context())
				if name == "branch" && code == 404 || name == "file_meta" && code == 404 {
					if err != nil {
						t.Fatalf("%s 404 error = %v, want found=false and no error", name, err)
					}
					return
				}
				ge := gitLabError(t, err)
				if ge.Status != code || ge.Transport || ge.Step != step {
					t.Errorf("%s answered %d: error = %+v, want Status %d, Step %s, no Transport", name, code, ge, code, step)
				}
				if n := s.count() - before; n != 1 {
					t.Errorf("%s made %d requests for a %d, want exactly one attempt", name, n, code)
				}
				assertNoToken(t, "error text", ge.Error())
				assertNoToken(t, "cause text", ge.Cause.Error())
			})
		}
	}
}

func TestPublisher_RedirectIsRefusedAndTokenStays(t *testing.T) {
	other := newStub(t, okJSON(map[string]any{}))
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", other.URL+"/steal?sig=SECRET")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	for name, call := range publishCalls(s.publisher()) {
		t.Run(name, func(t *testing.T) {
			step, err := call(t.Context())
			ge := gitLabError(t, err)
			if ge.Status != 0 || ge.Transport || ge.Step != step || !errors.Is(err, glclient.ErrRedirect) {
				t.Errorf("%s redirected: error = %+v (%v), want Status 0, Step %s, ErrRedirect", name, ge, err, step)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(ge.Cause.Error(), "SECRET") {
				t.Errorf("error %q shows the redirect target", err)
			}
		})
	}
	if other.count() != 0 {
		t.Errorf("redirect target saw %d requests, want 0 (the token must not travel)", other.count())
	}
}

func TestPublisher_ConnectionVanished(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name       string
		resolve    glclient.ResolveFunc
		wantStatus int
	}{
		{name: "no connection", resolve: fixedResolve(glclient.Binding{}, false, nil), wantStatus: 401},
		{name: "token removed", resolve: fixedResolve(glclient.Binding{BaseURL: "https://unused", Token: " "}, true, nil), wantStatus: 401},
		{name: "invalid token", resolve: fixedResolve(glclient.Binding{BaseURL: "https://unused", Token: "bad token"}, true, nil), wantStatus: 401},
		{name: "read failure", resolve: fixedResolve(glclient.Binding{}, false, boom)},
	}
	for _, tc := range tests {
		s := newStub(t, okJSON(map[string]any{}))
		tc.resolve = withBase(tc.resolve, s.URL)
		p := newPublisher(s, tc.resolve)
		for name, call := range publishCalls(p) {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				step, err := call(t.Context())
				ge := gitLabError(t, err)
				if ge.Status != tc.wantStatus || ge.Step != step {
					t.Errorf("error = %+v, want Status %d, Step %s", ge, tc.wantStatus, step)
				}
				if tc.name == "read failure" && !errors.Is(err, boom) {
					t.Errorf("error = %v, want it to wrap %v", err, boom)
				}
			})
		}
		if s.count() != 0 {
			t.Errorf("%s: %d requests were sent, want none", tc.name, s.count())
		}
	}
}

func TestPublisher_TransportAndCancellation(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{}))
	p := s.publisher()
	s.Close() // nothing listens any more: a failure before a response

	_, err := p.Project(t.Context(), ref)
	ge := gitLabError(t, err)
	if !ge.Transport || ge.Status != 0 || ge.Step != app.StepProject {
		t.Errorf("Project(server gone) error = %+v, want Transport, Status 0", ge)
	}
	if got := ge.Error(); got != "gitlab project: transport error" {
		t.Errorf("error text = %q, want the fixed transport text", got)
	}
	assertNoToken(t, "error text", ge.Error()+ge.Cause.Error())
	if strings.Contains(ge.Cause.Error(), "/api/v4") {
		t.Errorf("cause %q shows the request URL", ge.Cause)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = p.Project(ctx, ref)
	ge = gitLabError(t, err)
	if ge.Transport || ge.Status != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("Project(cancelled) error = %+v (%v), want not Transport and context.Canceled", ge, err)
	}
}

func TestPublisher_ReadsTheBindingPerCall(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"id": 7, "path": "web", "path_with_namespace": "acme/web"}))
	token := "glpat-first-token-0000000"
	p := newPublisher(s, func(context.Context, string) (glclient.Binding, bool, error) {
		return glclient.Binding{BaseURL: s.URL, Token: token}, true, nil
	})
	for _, tok := range []string{"glpat-first-token-0000000", "glpat-rotated-token-111111"} {
		token = tok
		if _, err := p.Project(t.Context(), ref); err != nil {
			t.Fatalf("Project() error = %v", err)
		}
	}
	if a, b := s.req(0).Header.Get("PRIVATE-TOKEN"), s.req(1).Header.Get("PRIVATE-TOKEN"); a != "glpat-first-token-0000000" || b != "glpat-rotated-token-111111" {
		t.Errorf("tokens sent = %q, %q, want the current one on each call", a, b)
	}
}

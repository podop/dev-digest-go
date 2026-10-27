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

	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

const (
	ciSHA  = "0123456789abcdef0123456789abcdef01234567"
	ciHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

func TestProject_CIConfigPath(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"custom path", map[string]any{"ci_config_path": "ci/main.yml"}, "ci/main.yml"},
		{"null", map[string]any{"ci_config_path": nil}, ""},
		{"absent", nil, ""},
		{"empty", map[string]any{"ci_config_path": ""}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := projectJSON()
			for k, v := range tc.extra {
				body[k] = v
			}
			s := newStub(t, okJSON(body))
			p, err := s.client(gitlab.Options{}).Project(t.Context(), s.conn(), 7)
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if p.CIConfigPath != tc.want {
				t.Errorf("Project().CIConfigPath = %q, want %q", p.CIConfigPath, tc.want)
			}
		})
	}
}

func TestBranch(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.RequestURI, "/branches/missing") {
			writeJSON(w, 404, map[string]any{"message": "404 Branch Not Found"})
			return
		}
		writeJSON(w, 200, map[string]any{"name": "devdigest/ci", "commit": map[string]any{"id": ciSHA}})
	})
	c := s.client(gitlab.Options{})

	b, found, err := c.Branch(t.Context(), s.conn(), 7, "devdigest/ci")
	if err != nil || !found {
		t.Fatalf("Branch(devdigest/ci) = %+v, %v, %v; want found", b, found, err)
	}
	if want := (gitlab.Branch{Name: "devdigest/ci", HeadSHA: ciSHA}); b != want {
		t.Errorf("Branch() = %+v, want %+v", b, want)
	}
	r := s.req(0)
	if r.Method != "GET" || r.URI != "/api/v4/projects/7/repository/branches/devdigest%2Fci" {
		t.Errorf("request = %s %s, want GET …/branches/devdigest%%2Fci (one escaped segment)", r.Method, r.URI)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken {
		t.Errorf("PRIVATE-TOKEN = %q, want the token", r.Header.Get("PRIVATE-TOKEN"))
	}

	if _, found, err := c.Branch(t.Context(), s.conn(), 7, "missing"); err != nil || found {
		t.Errorf("Branch(missing) found = %v, err = %v; want not found and no error", found, err)
	}
}

func TestBranch_RefusesBadArgumentsWithoutRequest(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{}))
	c := s.client(gitlab.Options{})
	for _, name := range []string{"", ".", "..", "a\nb"} {
		if _, _, err := c.Branch(t.Context(), s.conn(), 7, name); err == nil {
			t.Errorf("Branch(%q) succeeded", name)
		}
	}
	if _, _, err := c.Branch(t.Context(), s.conn(), 0, "main"); err == nil {
		t.Error("Branch(project 0) succeeded")
	}
	if s.count() != 0 {
		t.Errorf("%d requests for invalid input, want 0", s.count())
	}
}

func TestBranch_AnswerWithoutCommitIsNotAPI(t *testing.T) {
	s := newStub(t, okJSON(map[string]any{"name": "main"}))
	if _, _, err := s.client(gitlab.Options{}).Branch(t.Context(), s.conn(), 7, "main"); !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("Branch() error = %v, want ErrNotAPI", err)
	}
}

func TestFileMeta(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.RequestURI, "absent") {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("X-Gitlab-Content-Sha256", strings.ToUpper(ciHash))
		w.Header().Set("X-Gitlab-Size", "42")
		w.WriteHeader(200)
	})
	c := s.client(gitlab.Options{})

	m, found, err := c.FileMeta(t.Context(), s.conn(), 7, ".gitlab/devdigest/review-x.yml", "devdigest/ci")
	if err != nil || !found {
		t.Fatalf("FileMeta() = %+v, %v, %v; want found", m, found, err)
	}
	if want := (gitlab.FileMeta{SHA256: ciHash, Size: 42}); m != want {
		t.Errorf("FileMeta() = %+v, want %+v (hash lower-cased)", m, want)
	}
	r := s.req(0)
	if r.Method != "HEAD" {
		t.Errorf("method = %s, want HEAD (no body transferred)", r.Method)
	}
	u, _ := url.ParseRequestURI(r.URI)
	if u.EscapedPath() != "/api/v4/projects/7/repository/files/.gitlab%2Fdevdigest%2Freview-x.yml" || u.Query().Get("ref") != "devdigest/ci" {
		t.Errorf("request URI = %q, want the escaped file path and ref=devdigest/ci", r.URI)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken {
		t.Errorf("PRIVATE-TOKEN = %q, want the token", r.Header.Get("PRIVATE-TOKEN"))
	}

	if _, found, err := c.FileMeta(t.Context(), s.conn(), 7, "absent.yml", "main"); err != nil || found {
		t.Errorf("FileMeta(absent) found = %v, err = %v; want not found and no error", found, err)
	}
}

func TestFileMeta_BadHashHeaderIsNotAPI(t *testing.T) {
	for _, hash := range []string{"", "abc", strings.Repeat("g", 64), ciHash + "00"} {
		s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
			if hash != "" {
				w.Header().Set("X-Gitlab-Content-Sha256", hash)
			}
			w.WriteHeader(200)
		})
		if _, _, err := s.client(gitlab.Options{}).FileMeta(t.Context(), s.conn(), 7, "a.yml", "main"); !errors.Is(err, gitlab.ErrNotAPI) {
			t.Errorf("FileMeta() with hash %q error = %v, want ErrNotAPI", hash, err)
		}
	}
}

func TestFileMeta_RetriesReadsAndRefusesRedirects(t *testing.T) {
	n := 0
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("X-Gitlab-Content-Sha256", ciHash)
		w.WriteHeader(200)
	})
	if _, found, err := s.client(gitlab.Options{}).FileMeta(t.Context(), s.conn(), 7, "a.yml", "main"); err != nil || !found {
		t.Fatalf("FileMeta() after a 503 = found %v, err %v; want a retried success", found, err)
	}
	if s.count() != 2 {
		t.Errorf("%d requests, want 2", s.count())
	}

	r := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://elsewhere.example/x")
		w.WriteHeader(302)
	})
	_, _, err := r.client(gitlab.Options{}).FileMeta(t.Context(), r.conn(), 7, "a.yml", "main")
	if !errors.Is(err, gitlab.ErrRedirect) {
		t.Errorf("FileMeta() on a 302 error = %v, want ErrRedirect", err)
	}
	if r.count() != 1 {
		t.Errorf("%d requests on a redirect, want 1", r.count())
	}
}

func TestFileMeta_TransportErrorHasNoURLOrToken(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom " + testToken)
	})}
	c := gitlab.New(gitlab.Options{HTTP: hc, Retry: &retry.Options{}})
	_, _, err := c.FileMeta(t.Context(), gitlab.Conn{BaseURL: "https://secret-host.example/api/v4", Token: testToken}, 7, "a.yml", "main")
	if err == nil {
		t.Fatal("FileMeta() succeeded")
	}
	assertNoToken(t, err.Error())
	if strings.Contains(err.Error(), "secret-host") {
		t.Errorf("error %q embeds the request URL", err)
	}
}

func TestCommit(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, map[string]any{"id": ciSHA}) })
	sha, err := s.client(gitlab.Options{}).Commit(t.Context(), s.conn(), 7, gitlab.CommitInput{
		Branch: "devdigest/ci", StartBranch: "main", Message: "Add DevDigest CI review",
		Actions: []gitlab.CommitAction{
			{Action: "create", FilePath: ".gitlab/devdigest/review-x.yml", Content: "a: 1\n"},
			{Action: "update", FilePath: ".gitlab-ci.yml", Content: "ünï\n"},
		},
	})
	if err != nil || sha != ciSHA {
		t.Fatalf("Commit() = %q, %v; want %q", sha, err, ciSHA)
	}
	r := s.req(0)
	if r.Method != "POST" || r.URI != "/api/v4/projects/7/repository/commits" {
		t.Errorf("request = %s %s, want POST …/repository/commits", r.Method, r.URI)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := map[string]any{
		"branch": "devdigest/ci", "start_branch": "main", "commit_message": "Add DevDigest CI review",
		"actions": []any{
			map[string]any{"action": "create", "file_path": ".gitlab/devdigest/review-x.yml", "content": "a: 1\n", "encoding": "text"},
			map[string]any{"action": "update", "file_path": ".gitlab-ci.yml", "content": "ünï\n", "encoding": "text"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("commit body mismatch (-want +got):\n%s", diff)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken || strings.Contains(r.Body+r.URI, testToken) {
		t.Errorf("token must be in the header only")
	}
}

func TestCommit_OmitsStartBranchWhenEmpty(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, map[string]any{"id": ciSHA}) })
	_, err := s.client(gitlab.Options{}).Commit(t.Context(), s.conn(), 7, gitlab.CommitInput{
		Branch: "devdigest/ci", Message: "m", Actions: []gitlab.CommitAction{{Action: "create", FilePath: "a", Content: ""}},
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if strings.Contains(s.req(0).Body, "start_branch") {
		t.Errorf("body %q has start_branch for an existing branch", s.req(0).Body)
	}
}

func TestCommit_IsSentOnceWhateverTheAnswer(t *testing.T) {
	for _, code := range []int{500, 502, 503, 429, 409} {
		s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, code, map[string]any{"message": "no"})
		})
		_, err := s.client(gitlab.Options{}).Commit(t.Context(), s.conn(), 7, gitlab.CommitInput{
			Branch: "b", Message: "m", Actions: []gitlab.CommitAction{{Action: "create", FilePath: "a", Content: "x"}},
		})
		var se *gitlab.StatusError
		if !errors.As(err, &se) || se.Status != code {
			t.Errorf("Commit() on %d error = %v, want a StatusError %d", code, err, code)
		}
		if s.count() != 1 {
			t.Errorf("Commit() on %d made %d attempts, want 1 (a write is never retried)", code, s.count())
		}
	}
}

func TestCommit_OnceOnTransportFailure(t *testing.T) {
	calls := 0
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, context.DeadlineExceeded
	})}
	c := gitlab.New(gitlab.Options{HTTP: hc}) // default retry policy
	_, err := c.Commit(t.Context(), gitlab.Conn{BaseURL: "https://gl.example/api/v4", Token: testToken}, 7, gitlab.CommitInput{
		Branch: "b", Message: "m", Actions: []gitlab.CommitAction{{Action: "create", FilePath: "a", Content: "x"}},
	})
	if err == nil || calls != 1 {
		t.Errorf("Commit() error = %v after %d calls, want an error after exactly 1", err, calls)
	}
}

func TestCommit_RefusesBadInputWithoutRequest(t *testing.T) {
	ok := gitlab.CommitAction{Action: "create", FilePath: "a.yml", Content: "x"}
	tests := []struct {
		name string
		in   gitlab.CommitInput
	}{
		{"no actions", gitlab.CommitInput{Branch: "b", Message: "m"}},
		{"no branch", gitlab.CommitInput{Message: "m", Actions: []gitlab.CommitAction{ok}}},
		{"dot branch", gitlab.CommitInput{Branch: "..", Message: "m", Actions: []gitlab.CommitAction{ok}}},
		{"no message", gitlab.CommitInput{Branch: "b", Actions: []gitlab.CommitAction{ok}}},
		{"delete action", gitlab.CommitInput{Branch: "b", Message: "m", Actions: []gitlab.CommitAction{{Action: "delete", FilePath: "a"}}}},
		{"traversal path", gitlab.CommitInput{Branch: "b", Message: "m", Actions: []gitlab.CommitAction{{Action: "create", FilePath: "../a", Content: "x"}}}},
		{"invalid utf-8", gitlab.CommitInput{Branch: "b", Message: "m", Actions: []gitlab.CommitAction{{Action: "create", FilePath: "a", Content: "\xff"}}}},
		{"too many", gitlab.CommitInput{Branch: "b", Message: "m", Actions: make([]gitlab.CommitAction, gitlab.MaxCommitActions+1)}},
	}
	s := newStub(t, okJSON(map[string]any{"id": ciSHA}))
	c := s.client(gitlab.Options{})
	for _, tc := range tests {
		if _, err := c.Commit(t.Context(), s.conn(), 7, tc.in); err == nil {
			t.Errorf("Commit(%s) succeeded", tc.name)
		}
	}
	if s.count() != 0 {
		t.Errorf("%d requests for invalid input, want 0", s.count())
	}
}

func TestCommit_AnswerWithoutIDIsNotAPI(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 201, map[string]any{"id": "x"}) })
	_, err := s.client(gitlab.Options{}).Commit(t.Context(), s.conn(), 7, gitlab.CommitInput{
		Branch: "b", Message: "m", Actions: []gitlab.CommitAction{{Action: "create", FilePath: "a", Content: "x"}},
	})
	if !errors.Is(err, gitlab.ErrNotAPI) {
		t.Errorf("Commit() error = %v, want ErrNotAPI", err)
	}
}

func TestOpenMRs(t *testing.T) {
	s := newStub(t, okJSON([]any{mrJSON(5, "opened", nil), mrJSON(6, "opened", nil)}))
	mrs, err := s.client(gitlab.Options{}).OpenMRs(t.Context(), s.conn(), 7, "devdigest/ci", "main")
	if err != nil {
		t.Fatalf("OpenMRs() error = %v", err)
	}
	if len(mrs) != 2 || mrs[0].IID != 5 || mrs[0].Status != "open" {
		t.Errorf("OpenMRs() = %+v, want 2 open MRs, first iid 5", mrs)
	}
	u, _ := url.ParseRequestURI(s.req(0).URI)
	q := u.Query()
	if u.Path != "/api/v4/projects/7/merge_requests" || q.Get("state") != "opened" ||
		q.Get("source_branch") != "devdigest/ci" || q.Get("target_branch") != "main" || q.Get("page") != "1" {
		t.Errorf("request URI = %q, want state/source_branch/target_branch/page=1", s.req(0).URI)
	}
	if _, err := s.client(gitlab.Options{}).OpenMRs(t.Context(), s.conn(), 7, "", "main"); err == nil {
		t.Error("OpenMRs() with an empty source succeeded")
	}
}

func TestCreateMR(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 201, mrJSON(9, "opened", nil))
	})
	mr, err := s.client(gitlab.Options{}).CreateMR(t.Context(), s.conn(), 7, gitlab.CreateMRInput{
		SourceBranch: "devdigest/ci", TargetBranch: "main", Title: "Add DevDigest CI review",
		Description: "body\n", RemoveSourceBranch: true,
	})
	if err != nil || mr.IID != 9 {
		t.Fatalf("CreateMR() = %+v, %v; want iid 9", mr, err)
	}
	r := s.req(0)
	if r.Method != "POST" || r.URI != "/api/v4/projects/7/merge_requests" {
		t.Errorf("request = %s %s, want POST …/merge_requests", r.Method, r.URI)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	want := map[string]any{
		"source_branch": "devdigest/ci", "target_branch": "main", "title": "Add DevDigest CI review",
		"description": "body\n", "remove_source_branch": true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("create MR body mismatch (-want +got):\n%s", diff)
	}
}

func TestCreateMR_IsSentOnceAndConflictIsAStatusError(t *testing.T) {
	for _, code := range []int{409, 500, 503} {
		s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, code, map[string]any{"message": "Another open merge request already exists"})
		})
		_, err := s.client(gitlab.Options{}).CreateMR(t.Context(), s.conn(), 7, gitlab.CreateMRInput{
			SourceBranch: "a", TargetBranch: "b", Title: "t",
		})
		var se *gitlab.StatusError
		if !errors.As(err, &se) || se.Status != code {
			t.Errorf("CreateMR() on %d error = %v, want a StatusError %d", code, err, code)
		}
		if s.count() != 1 {
			t.Errorf("CreateMR() on %d made %d attempts, want 1", code, s.count())
		}
	}
}

func TestCreateMR_RefusesBadInputWithoutRequest(t *testing.T) {
	s := newStub(t, okJSON(mrJSON(1, "opened", nil)))
	c := s.client(gitlab.Options{})
	for _, in := range []gitlab.CreateMRInput{
		{TargetBranch: "b", Title: "t"},
		{SourceBranch: "a", Title: "t"},
		{SourceBranch: "a", TargetBranch: "b", Title: "  "},
		{SourceBranch: "a", TargetBranch: "b", Title: "t", Description: "\xff"},
	} {
		if _, err := c.CreateMR(t.Context(), s.conn(), 7, in); err == nil {
			t.Errorf("CreateMR(%+v) succeeded", in)
		}
	}
	if s.count() != 0 {
		t.Errorf("%d requests for invalid input, want 0", s.count())
	}
}

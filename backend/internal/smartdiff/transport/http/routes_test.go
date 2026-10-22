package smartdiffhttp_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/smartdiff/app"
	"dev-digest-go/backend/internal/smartdiff/domain"
	smartdiffhttp "dev-digest-go/backend/internal/smartdiff/transport/http"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	prID        = "7e1d0000-0000-4000-8000-000000000001"
)

type fakeAPI struct {
	result  domain.SmartDiff
	err     error
	gotWS   string
	gotPR   string
	nCalled int
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) Get(_ context.Context, ws, pr string) (domain.SmartDiff, error) {
	f.nCalled++
	f.gotWS, f.gotPR = ws, pr
	return f.result, f.err
}

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func get(t *testing.T, api *fakeAPI, path string) (int, string) {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		AllowedHosts: []string{"127.0.0.1"},
	})
	smartdiffhttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func TestGet_WireShape(t *testing.T) {
	api := &fakeAPI{result: domain.Build(
		[]domain.PullFile{
			{Path: "src/a.go", Additions: 3, Deletions: 4},
			{Path: "README.md", Additions: 1},
		},
		[]domain.FindingRef{{File: "src/a.go", StartLine: 9}, {File: "src/a.go", StartLine: 2}},
	)}
	status, body := get(t, api, "/pulls/"+prID+"/smart-diff")
	want := `{"groups":[` +
		`{"role":"core","files":[{"path":"src/a.go","additions":3,"deletions":4,"finding_lines":[2,9]}]},` +
		`{"role":"tests","files":[]},` +
		`{"role":"wiring","files":[]},` +
		`{"role":"docs","files":[{"path":"README.md","additions":1,"deletions":0,"finding_lines":[]}]},` +
		`{"role":"boilerplate","files":[]}],` +
		`"split_suggestion":{"too_big":false,"total_lines":8,"proposed_splits":[]}}`
	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", status, body)
	}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("body mismatch (-want +got):\n%s", diff)
	}
	if api.gotWS != workspaceID || api.gotPR != prID {
		t.Errorf("Get(%s, %s), want (%s, %s)", api.gotWS, api.gotPR, workspaceID, prID)
	}
}

func TestGet_PullRequestWithoutFilesIsFiveEmptyGroups(t *testing.T) {
	api := &fakeAPI{result: domain.Build(nil, nil)}
	status, body := get(t, api, "/pulls/"+prID+"/smart-diff")
	want := `{"groups":[{"role":"core","files":[]},{"role":"tests","files":[]},{"role":"wiring","files":[]},` +
		`{"role":"docs","files":[]},{"role":"boilerplate","files":[]}],` +
		`"split_suggestion":{"too_big":false,"total_lines":0,"proposed_splits":[]}}`
	if status != http.StatusOK || body != want {
		t.Errorf("GET = %d %s, want 200 %s", status, body, want)
	}
}

func TestGet_NoPseudocodeSummaryKey(t *testing.T) {
	api := &fakeAPI{result: domain.Build([]domain.PullFile{{Path: "a.go"}}, nil)}
	_, body := get(t, api, "/pulls/"+prID+"/smart-diff")
	if strings.Contains(body, "pseudocode_summary") {
		t.Errorf("body %s holds pseudocode_summary, want it absent", body)
	}
}

func TestGet_NonUUIDIs422(t *testing.T) {
	api := &fakeAPI{}
	status, body := get(t, api, "/pulls/x/smart-diff")
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, `"code":"validation_error"`) ||
		!strings.Contains(body, `"instancePath":"/id"`) {
		t.Errorf("GET /pulls/x/smart-diff = %d %s, want 422 validation_error at /id", status, body)
	}
	if api.nCalled != 0 {
		t.Errorf("use case called %d times, want 0", api.nCalled)
	}
}

func TestGet_NotFoundIs404Envelope(t *testing.T) {
	api := &fakeAPI{err: apperr.NotFound("Pull request not found")}
	status, body := get(t, api, "/pulls/"+prID+"/smart-diff")
	want := `{"error":{"code":"not_found","message":"Pull request not found"}}`
	if status != http.StatusNotFound || body != want {
		t.Errorf("GET = %d %s, want 404 %s", status, body, want)
	}
}

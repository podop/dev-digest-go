package blasthttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	blasthttp "dev-digest-go/backend/internal/blast/transport/http"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/tenancy"
)

const (
	workspaceID = "11111111-2222-4333-8444-555555555555"
	prID        = "7e1d0000-0000-4000-8000-000000000001"
)

type fakeTenants struct{}

func (fakeTenants) Resolve(context.Context) (tenancy.Identity, error) {
	return tenancy.Identity{WorkspaceID: workspaceID}, nil
}

func serve(t *testing.T, api app.API) *httptest.Server {
	t.Helper()
	e := httpx.New(httpx.Options{
		Log:          slog.New(slog.DiscardHandler),
		AllowedHosts: []string{"127.0.0.1"},
	})
	blasthttp.AddRoutes(e, api, fakeTenants{})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string) (status int, contentType, body string) {
	t.Helper()
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
	return resp.StatusCode, resp.Header.Get("Content-Type"), strings.TrimSpace(string(b))
}

type fakeAPI struct {
	blast   domain.BlastRadius
	history []domain.PriorPR
	err     error
	gotWS   string
	gotPR   string
}

var _ app.API = (*fakeAPI)(nil)

func (f *fakeAPI) Radius(_ context.Context, ws, pr string) (domain.BlastRadius, error) {
	f.gotWS, f.gotPR = ws, pr
	return f.blast, f.err
}

func (f *fakeAPI) History(_ context.Context, ws, pr string) ([]domain.PriorPR, error) {
	f.gotWS, f.gotPR = ws, pr
	return f.history, f.err
}

func TestBlast_WireShape(t *testing.T) {
	api := &fakeAPI{blast: domain.BlastRadius{
		ChangedSymbols: []domain.ChangedSymbol{{Name: "foo", File: "src/a.ts", Kind: "function"}},
		Downstream: []domain.Downstream{
			{Symbol: "foo", Callers: []domain.CallerRef{{Name: "h", File: "src/x.ts", Line: 4}}, EndpointsAffected: []string{"GET /x"}},
		},
		Summary: "1 changed symbol",
	}}
	status, ct, body := get(t, serve(t, api), "/pulls/"+prID+"/blast")

	const want = `{"changed_symbols":[{"name":"foo","file":"src/a.ts","kind":"function"}],` +
		`"downstream":[{"symbol":"foo","callers":[{"name":"h","file":"src/x.ts","line":4}],"endpoints_affected":["GET /x"],"crons_affected":[]}],` +
		`"summary":"1 changed symbol","degraded":false}`
	if status != http.StatusOK || ct != "application/json" && ct != "application/json; charset=utf-8" || body != want {
		t.Errorf("GET blast = %d %q %s, want 200 JSON %s", status, ct, body, want)
	}
	if api.gotWS != workspaceID || api.gotPR != prID {
		t.Errorf("use case saw workspace %q pull %q, want %q %q", api.gotWS, api.gotPR, workspaceID, prID)
	}
}

// reason is on the wire only when the answer is degraded; degraded always is.
func TestBlast_ReasonOnlyWhenDegraded(t *testing.T) {
	tests := []struct {
		name string
		in   domain.BlastRadius
		want string
	}{
		{"degraded", domain.BlastRadius{Degraded: true, Reason: domain.ReasonFlagOff}, `"degraded":true,"reason":"flag_off"}`},
		{"not degraded, reason set", domain.BlastRadius{Reason: domain.ReasonNoData}, `"degraded":false}`},
		{"not degraded", domain.BlastRadius{}, `"degraded":false}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, body := get(t, serve(t, &fakeAPI{blast: tc.in}), "/pulls/"+prID+"/blast")
			if !strings.HasPrefix(body, `{"changed_symbols":[],"downstream":[],"summary":"",`) || !strings.HasSuffix(body, tc.want) {
				t.Errorf("GET blast body = %s, want empty lists ending in %s", body, tc.want)
			}
		})
	}
}

func TestHistory_WireShape(t *testing.T) {
	api := &fakeAPI{history: []domain.PriorPR{{
		PRNumber: 4, Title: "Touches a", MergedAt: "2026-01-01T00:00:00Z", Author: "ann", FilesOverlap: []string{"src/a.ts"},
	}}}
	status, _, body := get(t, serve(t, api), "/pulls/"+prID+"/history")

	const want = `{"history":[{"pr_number":4,"title":"Touches a","merged_at":"2026-01-01T00:00:00Z","author":"ann","files_overlap":["src/a.ts"],"notes":""}]}`
	if status != http.StatusOK || body != want {
		t.Errorf("GET history = %d %s, want 200 %s", status, body, want)
	}
}

func TestHistory_EmptyIsAnEmptyListNotNull(t *testing.T) {
	_, _, body := get(t, serve(t, &fakeAPI{}), "/pulls/"+prID+"/history")
	if body != `{"history":[]}` {
		t.Errorf("GET history (nil list) = %s, want {\"history\":[]}", body)
	}
}

func TestRoutes_InvalidIDAndUseCaseErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"blast bad id", "/pulls/x/blast", nil, http.StatusUnprocessableEntity, "validation_error"},
		{"history bad id", "/pulls/x/history", nil, http.StatusUnprocessableEntity, "validation_error"},
		{"blast not found", "/pulls/" + prID + "/blast", apperr.NotFound("Pull request not found"), http.StatusNotFound, "not_found"},
		{"history not found", "/pulls/" + prID + "/history", apperr.NotFound("Pull request not found"), http.StatusNotFound, "not_found"},
		{"blast internal", "/pulls/" + prID + "/blast", errors.New("connection reset"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{err: tc.err}
			status, _, body := get(t, serve(t, api), tc.path)
			var env struct {
				Error struct{ Code string }
			}
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("body %q: %v", body, err)
			}
			if status != tc.wantStatus || env.Error.Code != tc.wantCode {
				t.Errorf("GET %s = %d %q, want %d %q", tc.path, status, env.Error.Code, tc.wantStatus, tc.wantCode)
			}
			if tc.err == nil && api.gotPR != "" {
				t.Errorf("use case called with %q for an invalid id", api.gotPR)
			}
		})
	}
}

// ---- the Node golden (routes.json, group "blast"): the real service over fake ports ----

type goldenStep struct {
	Request *struct{ Method, URL string } `json:"request"`
	Status  int                           `json:"status"`
	CT      string                        `json:"contentType"`
	Body    string                        `json:"body"`
	World   *struct {
		Files  *[]string        `json:"files"`
		Index  *json.RawMessage `json:"index"`
		Merged *struct {
			Throws string `json:"throws"`
		} `json:"merged"`
	} `json:"world"`
}

type goldenIndex struct {
	ChangedSymbols []struct{ File, Name, Kind string } `json:"changedSymbols"`
	Callers        []struct {
		File, Symbol, ViaSymbol string
		Line                    int
		Rank                    float64
	} `json:"callers"`
	FactsByFile map[string]struct{ Endpoints, Crons []string } `json:"factsByFile"`
	Degraded    bool                                           `json:"degraded"`
	Reason      string                                         `json:"reason"`
	Throws      string                                         `json:"throws"`
}

type world struct {
	files    []string
	index    domain.PortResult
	indexErr error
	merged   []domain.MergedPR
	mergeErr error
}

func (w *world) FindPull(_ context.Context, ws, id string) (domain.Pull, error) {
	if ws != workspaceID || id != "11111111-1111-4111-8111-111111111111" {
		return domain.Pull{}, domain.ErrNotFound
	}
	return domain.Pull{ID: id, RepoID: "33333333-3333-4333-8333-333333333333", Number: 5, Owner: "acme", Name: "api"}, nil
}
func (w *world) ChangedPaths(context.Context, string) ([]string, error) { return w.files, nil }
func (w *world) BlastRadius(context.Context, string, []string) (domain.PortResult, error) {
	return w.index, w.indexErr
}
func (w *world) ListMergedPulls(context.Context, string, string, int) ([]domain.MergedPR, error) {
	return w.merged, w.mergeErr
}

func (w *world) setIndex(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var in goldenIndex
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("golden index %s: %v", raw, err)
	}
	w.indexErr = nil
	if in.Throws != "" {
		w.indexErr = errors.New(in.Throws)
		return
	}
	res := domain.PortResult{Degraded: in.Degraded, Reason: domain.Reason(in.Reason)}
	for _, s := range in.ChangedSymbols {
		res.ChangedSymbols = append(res.ChangedSymbols, domain.ChangedSymbol{Name: s.Name, File: s.File, Kind: s.Kind})
	}
	for _, c := range in.Callers {
		res.Callers = append(res.Callers, domain.Caller{File: c.File, Symbol: c.Symbol, ViaSymbol: c.ViaSymbol, Line: c.Line, Rank: c.Rank})
	}
	if in.FactsByFile != nil {
		res.FactsByFile = map[string]domain.Facts{}
		for f, facts := range in.FactsByFile {
			res.FactsByFile[f] = domain.Facts{Endpoints: facts.Endpoints, Crons: facts.Crons}
		}
	}
	w.index = res
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var env struct {
		Error struct{ Code string }
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("error body %q: %v", body, err)
	}
	return env.Error.Code
}

func TestGolden_NodeRoutesBlastGroup(t *testing.T) {
	raw, err := os.ReadFile("../../../parity/testdata/goldens/routes.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden struct {
		Groups struct {
			Blast []goldenStep `json:"blast"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode golden: %v", err)
	}

	// The default world of testdata/gen/routes.mts.
	w := &world{files: []string{"src/a.ts", "src/b.ts"}}
	w.setIndex(t, json.RawMessage(`{
		"changedSymbols": [{"file":"src/a.ts","name":"foo","kind":"function"},{"file":"src/b.ts","name":"bar","kind":"class"}],
		"callers": [
			{"file":"src/x.ts","symbol":"handleX","viaSymbol":"foo","line":10,"rank":0.5},
			{"file":"src/y.ts","symbol":"handleY","viaSymbol":"bar","line":3,"rank":0.9}],
		"factsByFile": {"src/x.ts":{"endpoints":["GET /x"],"crons":[]},"src/y.ts":{"endpoints":["POST /y"],"crons":["nightly"]}},
		"degraded": false}`))
	w.merged = []domain.MergedPR{
		{Number: 4, Title: "Touches a", Author: "ann", MergedAt: "2026-01-01T00:00:00Z", Files: []string{"src/a.ts", "zzz.ts"}},
		{Number: 3, Title: "Unrelated", Author: "bob", MergedAt: "2026-01-02T00:00:00Z", Files: []string{"other.ts"}},
		{Number: 2, Title: "Both files", Author: "cy", MergedAt: "2026-02-01T00:00:00Z", Files: []string{"src/b.ts", "src/a.ts"}},
	}
	svc := app.NewService(w, w, w, app.Options{})
	srv := serve(t, svc)

	if len(golden.Groups.Blast) == 0 {
		t.Fatal("golden group blast is empty")
	}
	var requests int
	for _, step := range golden.Groups.Blast {
		if step.World != nil {
			if step.World.Files != nil {
				w.files = *step.World.Files
			}
			if step.World.Index != nil {
				w.setIndex(t, *step.World.Index)
			}
			if step.World.Merged != nil {
				w.mergeErr = errors.New(step.World.Merged.Throws)
			}
			continue
		}
		requests++
		status, ct, body := get(t, srv, step.Request.URL)
		if status != step.Status {
			t.Errorf("GET %s status = %d, want %d", step.Request.URL, status, step.Status)
		}
		// The media type is the golden's; the charset suffix is Fastify's and httpx-wide not
		// emitted by Echo (same for every route of the server).
		if mediaType, _, _ := strings.Cut(ct, ";"); mediaType != "application/json" {
			t.Errorf("GET %s content type = %q, want application/json", step.Request.URL, ct)
		}
		if step.Status == http.StatusUnprocessableEntity {
			// The validation envelope is httpx's (its detail layout is shared by all routes and
			// tested there); here only the code is the golden's.
			if got, want := errorCode(t, body), errorCode(t, step.Body); got != want {
				t.Errorf("GET %s error code = %q, want %q", step.Request.URL, got, want)
			}
			continue
		}
		if body != step.Body {
			t.Errorf("GET %s body = %s, want %s", step.Request.URL, body, step.Body)
		}
	}
	if requests == 0 {
		t.Error("golden group blast holds no request")
	}
}

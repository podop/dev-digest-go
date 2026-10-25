package abouthttp_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	abouthttp "dev-digest-go/backend/internal/about/transport/http"
	"dev-digest-go/backend/internal/platform/httpx"
)

var info = abouthttp.Info{
	Version:       "1.4.0",
	BuildID:       "abc123",
	RunnerVersion: "2",
	DataDir:       "/data",
	SecretsFile:   "/data/secrets.json",
}

func serve(t *testing.T, opts httpx.Options) *httptest.Server {
	t.Helper()
	opts.Log = slog.New(slog.DiscardHandler)
	if opts.AllowedHosts == nil {
		opts.AllowedHosts = []string{"127.0.0.1"}
	}
	e := httpx.New(opts)
	abouthttp.AddRoutes(e, info)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, header map[string]string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(""))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range header {
		if k == "Host" { // net/http ignores a Host header; it is taken from req.Host
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
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

func TestAbout_Shape(t *testing.T) {
	srv := serve(t, httpx.Options{})
	status, body := do(t, srv, http.MethodGet, "/about", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /about status = %d, want 200 (body %s)", status, body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	want := map[string]any{
		"version":       "1.4.0",
		"build_id":      "abc123",
		"runner":        map[string]any{"kind": "go", "version": "2"},
		"config_format": "devdigest-plugin/v1",
		"data_dir":      "/data",
		"secrets_file":  "/data/secrets.json",
	}
	// cmp.Diff over the decoded maps also proves there are no extra fields, top level or in runner.
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GET /about mismatch (-want +got):\n%s", diff)
	}
}

func TestAbout_KeyOrderAndContentType(t *testing.T) {
	srv := serve(t, httpx.Options{})
	resp, err := srv.Client().Get(srv.URL + "/about")
	if err != nil {
		t.Fatalf("GET /about: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /about Content-Type = %q, want application/json", ct)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := `{"version":"1.4.0","build_id":"abc123","runner":{"kind":"go","version":"2"},` +
		`"config_format":"devdigest-plugin/v1","data_dir":"/data","secrets_file":"/data/secrets.json"}`
	if got := strings.TrimSpace(string(b)); got != want {
		t.Errorf("GET /about body = %s, want %s", got, want)
	}
}

func TestAbout_GetOnly(t *testing.T) {
	srv := serve(t, httpx.Options{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			status, body := do(t, srv, method, "/about", nil)
			if status == http.StatusOK {
				t.Errorf("%s /about status = 200, want a refusal (body %s)", method, body)
			}
			if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
				t.Errorf("%s /about status = %d, want 404 or 405", method, status)
			}
			if strings.Contains(body, "build_id") {
				t.Errorf("%s /about leaked the About body: %s", method, body)
			}
		})
	}
}

// NFR5: the route sits behind the global guards of httpx.New.
func TestAbout_Guards(t *testing.T) {
	t.Run("foreign Host is refused", func(t *testing.T) {
		srv := serve(t, httpx.Options{})
		status, body := do(t, srv, http.MethodGet, "/about", map[string]string{"Host": "evil.example"})
		if status != http.StatusForbidden {
			t.Errorf("GET /about with Host evil.example = %d, want 403 (body %s)", status, body)
		}
	})
	t.Run("foreign Origin on a state-changing method is refused", func(t *testing.T) {
		srv := serve(t, httpx.Options{})
		status, body := do(t, srv, http.MethodPost, "/about", map[string]string{"Origin": "https://evil.example"})
		if status != http.StatusForbidden {
			t.Errorf("POST /about with foreign Origin = %d, want 403 (body %s)", status, body)
		}
	})
	t.Run("rate limit applies", func(t *testing.T) {
		now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
		srv := serve(t, httpx.Options{
			RateLimit: httpx.RateLimit{Enabled: true, Limit: 2, Window: time.Minute},
			Now:       func() time.Time { return now },
		})
		var got []int
		for range 3 {
			status, _ := do(t, srv, http.MethodGet, "/about", nil)
			got = append(got, status)
		}
		if diff := cmp.Diff([]int{200, 200, http.StatusTooManyRequests}, got); diff != "" {
			t.Errorf("GET /about x3 statuses mismatch (-want +got):\n%s", diff)
		}
	})
}

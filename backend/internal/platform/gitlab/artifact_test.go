package gitlab_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"

	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/platform/retry"
)

const (
	artFile  = "devdigest-result.json"
	artSig   = "X-Amz-Signature=SECRETSIGNATURE0123"
	artLimit = 65536
)

// artStorage is a recording TLS double of the artifact object storage.
func artStorage(t *testing.T, handler http.HandlerFunc) *stub {
	t.Helper()
	return newStub(t, handler)
}

// artRedirect makes the GitLab double answer with code and a Location on the storage double.
func artRedirect(code int, location string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(code)
	}
}

func assertArtifactSecretsAbsent(t *testing.T, text string) {
	t.Helper()
	assertNoToken(t, text)
	for _, secret := range []string{"SECRETSIGNATURE", "X-Amz", "/blob"} {
		if strings.Contains(text, secret) {
			t.Errorf("text %q contains %q (part of the signed storage URL)", text, secret)
		}
	}
}

func TestJobArtifactFile_Direct(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	got, err := s.client(gitlab.Options{}).JobArtifactFile(t.Context(), s.conn(), nil, 7, 501, artFile, artLimit)
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("JobArtifactFile() = %q, %v; want the body", got, err)
	}
	r := s.req(0)
	if r.Method != "GET" || r.URI != "/api/v4/projects/7/jobs/501/artifacts/devdigest-result.json" {
		t.Errorf("request = %s %s, want GET …/jobs/501/artifacts/devdigest-result.json", r.Method, r.URI)
	}
	if r.Header.Get("PRIVATE-TOKEN") != testToken {
		t.Errorf("PRIVATE-TOKEN = %q, want the token", r.Header.Get("PRIVATE-TOKEN"))
	}
}

func TestJobArtifactFile_NestedPathIsEscapedPerSegment(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "x") })
	if _, err := s.client(gitlab.Options{}).JobArtifactFile(t.Context(), s.conn(), nil, 7, 5, "out dir/r#1.json", artLimit); err != nil {
		t.Fatalf("JobArtifactFile() error = %v", err)
	}
	if want := "/api/v4/projects/7/jobs/5/artifacts/out%20dir/r%231.json"; s.req(0).URI != want {
		t.Errorf("request URI = %q, want %q", s.req(0).URI, want)
	}
}

func TestJobArtifactFile_StorageHopCarriesNoCredential(t *testing.T) {
	storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"from":"storage"}`)
	})
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob?"+artSig))
	got, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 501, artFile, artLimit)
	if err != nil || string(got) != `{"from":"storage"}` {
		t.Fatalf("JobArtifactFile() = %q, %v; want the storage body", got, err)
	}
	if api.count() != 1 || storage.count() != 1 {
		t.Fatalf("api saw %d requests, storage %d; want 1 and 1", api.count(), storage.count())
	}
	r := storage.req(0)
	if r.URI != "/blob?"+artSig {
		t.Errorf("storage URI = %q, want the Location as given", r.URI)
	}
	for _, h := range []string{"Private-Token", "Authorization", "Cookie", "Proxy-Authorization"} {
		if v := r.Header.Get(h); v != "" {
			t.Errorf("storage request carries %s = %q, want no credential", h, v)
		}
	}
	if strings.Contains(storage.req(0).URI+r.Body, testToken) {
		t.Error("token reached the storage")
	}
}

func TestJobArtifactFile_FollowsOnly302_303_307(t *testing.T) {
	for _, code := range []int{302, 303, 307} {
		storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
		api := newStub(t, artRedirect(code, storage.URL+"/blob"))
		if got, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, artLimit); err != nil || string(got) != "ok" {
			t.Errorf("redirect %d: JobArtifactFile() = %q, %v; want followed", code, got, err)
		}
	}
	for _, code := range []int{300, 301, 308, 304} {
		storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
		api := newStub(t, artRedirect(code, storage.URL+"/blob"))
		_, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, artLimit)
		if !errors.Is(err, gitlab.ErrRedirect) {
			t.Errorf("redirect %d: error = %v, want ErrRedirect", code, err)
		}
		if storage.count() != 0 {
			t.Errorf("redirect %d: storage was contacted", code)
		}
	}
}

func TestJobArtifactFile_RefusesUnsafeLocations(t *testing.T) {
	tests := []struct{ name, location string }{
		{"http", "http://storage.example/blob?" + artSig},
		{"missing", ""},
		{"relative", "/blob?" + artSig},
		{"scheme-relative", "//storage.example/blob?" + artSig},
		{"userinfo", "https://user:pw@storage.example/blob?" + artSig},
		{"no host", "https:///blob?" + artSig},
		{"opaque", "https:blob?" + artSig},
		{"ftp", "ftp://storage.example/blob?" + artSig},
		{"unparsable", "https://storage.example/%zz?" + artSig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			var mu sync.Mutex
			storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				hits++
				mu.Unlock()
			})
			api := newStub(t, artRedirect(http.StatusFound, tc.location))
			_, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, artLimit)
			if !errors.Is(err, gitlab.ErrRedirect) {
				t.Fatalf("error = %v, want ErrRedirect", err)
			}
			assertArtifactSecretsAbsent(t, err.Error())
			if storage.count() != 0 || hits != 0 {
				t.Errorf("storage contacted %d times, want 0", storage.count())
			}
			if api.count() != 1 {
				t.Errorf("api saw %d requests, want 1 (a refusal is not retried)", api.count())
			}
		})
	}
}

func TestJobArtifactFile_StorageRedirectIsRefused(t *testing.T) {
	other := artStorage(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "never") })
	storage := artStorage(t, artRedirect(http.StatusFound, other.URL+"/second?"+artSig))
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob?"+artSig))
	// storage.Client() follows redirects by default: the package must switch that off on its copy.
	_, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, artLimit)
	if !errors.Is(err, gitlab.ErrRedirect) {
		t.Fatalf("error = %v, want ErrRedirect", err)
	}
	assertArtifactSecretsAbsent(t, err.Error())
	if other.count() != 0 {
		t.Error("the storage leg followed a redirect")
	}
}

func TestJobArtifactFile_StorageCookieJarIsNotUsed(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	u := mustParse(t, storage.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "secret", Path: "/"}})
	hc := storage.Client()
	hc.Jar = jar
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob"))
	if _, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), hc, 7, 1, artFile, artLimit); err != nil {
		t.Fatalf("JobArtifactFile() error = %v", err)
	}
	if c := storage.req(0).Header.Get("Cookie"); c != "" {
		t.Errorf("storage request carries Cookie %q, want none", c)
	}
}

func TestJobArtifactFile_StorageUsesItsOwnClient(t *testing.T) {
	storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob"))

	var mu sync.Mutex
	var apiClientHosts, storageClientHosts []string
	record := func(list *[]string, base http.RoundTripper) roundTripFunc {
		return func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			*list = append(*list, r.URL.Host)
			mu.Unlock()
			return base.RoundTrip(r)
		}
	}
	apiHC := &http.Client{Transport: record(&apiClientHosts, api.Client().Transport)}
	storageHC := &http.Client{Transport: record(&storageClientHosts, storage.Client().Transport)}
	c := gitlab.New(gitlab.Options{HTTP: apiHC, Retry: &retry.Options{}})

	if _, err := c.JobArtifactFile(t.Context(), api.conn(), storageHC, 7, 1, artFile, artLimit); err != nil {
		t.Fatalf("JobArtifactFile() error = %v", err)
	}
	apiHost, storageHost := mustParse(t, api.URL).Host, mustParse(t, storage.URL).Host
	if len(apiClientHosts) != 1 || apiClientHosts[0] != apiHost {
		t.Errorf("the connection client saw %v, want only %s", apiClientHosts, apiHost)
	}
	if len(storageClientHosts) != 1 || storageClientHosts[0] != storageHost {
		t.Errorf("the storage client saw %v, want only %s", storageClientHosts, storageHost)
	}

	// Without a storage client the Client's own (egress) client makes the second hop.
	apiClientHosts = nil
	if _, err := c.JobArtifactFile(t.Context(), api.conn(), nil, 7, 1, artFile, artLimit); err != nil {
		t.Fatalf("JobArtifactFile(nil storage) error = %v", err)
	}
	if len(apiClientHosts) != 2 {
		t.Errorf("with a nil storage client the connection client saw %v, want both hops", apiClientHosts)
	}
}

func TestJobArtifactFile_Limit(t *testing.T) {
	body := strings.Repeat("a", 100)
	serve := func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }

	s := newStub(t, serve)
	c := s.client(gitlab.Options{})
	if got, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, artFile, 100); err != nil || len(got) != 100 {
		t.Errorf("a body of exactly the limit: %d bytes, %v; want accepted", len(got), err)
	}
	got, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, artFile, 99)
	if !errors.Is(err, gitlab.ErrTooLarge) || got != nil {
		t.Errorf("a body of limit+1: %d bytes, %v; want ErrTooLarge and nothing returned", len(got), err)
	}

	storage := artStorage(t, serve)
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob"))
	got, err = api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, 99)
	if !errors.Is(err, gitlab.ErrTooLarge) || got != nil {
		t.Errorf("storage body of limit+1: %d bytes, %v; want ErrTooLarge", len(got), err)
	}
}

func TestJobArtifactFile_StatusErrors(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]any{"message": "404 Not Found"})
	})
	_, err := s.client(gitlab.Options{}).JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, artFile, artLimit)
	var se *gitlab.StatusError
	if !errors.As(err, &se) || !se.IsNotFound() {
		t.Errorf("error = %v, want a 404 StatusError", err)
	}
	if s.count() != 1 {
		t.Errorf("%d requests on a 404, want 1", s.count())
	}

	// An expired signed URL: the storage's text and URL stay out of the error.
	storage := artStorage(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error>Request has expired "+r.RequestURI+"</Error>")
	})
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob?"+artSig))
	_, err = api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, artLimit)
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("error = %v, want a 403 StatusError", err)
	}
	assertArtifactSecretsAbsent(t, err.Error())
}

func TestJobArtifactFile_ReadsRetryTransientFailures(t *testing.T) {
	n := 0
	storage := artStorage(t, func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	api := newStub(t, artRedirect(http.StatusFound, storage.URL+"/blob"))
	got, err := api.client(gitlab.Options{}).JobArtifactFile(t.Context(), api.conn(), storage.Client(), 7, 1, artFile, artLimit)
	if err != nil || string(got) != "ok" {
		t.Fatalf("JobArtifactFile() = %q, %v; want a retried success", got, err)
	}
	if storage.count() != 2 || api.count() != 1 {
		t.Errorf("storage saw %d, api %d requests; want 2 and 1 (only the failed leg repeats)", storage.count(), api.count())
	}
}

func TestJobArtifactFile_TransportErrorsLeakNothing(t *testing.T) {
	api := newStub(t, artRedirect(http.StatusFound, "https://storage.example/blob?"+artSig))
	storageHC := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}
	c := gitlab.New(gitlab.Options{HTTP: api.Client(), Retry: &retry.Options{}})
	_, err := c.JobArtifactFile(t.Context(), api.conn(), storageHC, 7, 1, artFile, artLimit)
	if err == nil {
		t.Fatal("JobArtifactFile() succeeded")
	}
	assertArtifactSecretsAbsent(t, err.Error())
	if strings.Contains(err.Error(), "storage.example") {
		t.Errorf("error %q embeds the storage URL", err)
	}

	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom " + testToken)
	})}
	c = gitlab.New(gitlab.Options{HTTP: hc, Retry: &retry.Options{}})
	_, err = c.JobArtifactFile(t.Context(), gitlab.Conn{BaseURL: "https://secret-host.example/api/v4", Token: testToken}, nil, 7, 1, artFile, artLimit)
	if err == nil {
		t.Fatal("JobArtifactFile() succeeded")
	}
	assertNoToken(t, err.Error())
	if strings.Contains(err.Error(), "secret-host") {
		t.Errorf("error %q embeds the request URL", err)
	}
}

func TestJobArtifactFile_RefusesBadArgumentsWithoutRequest(t *testing.T) {
	s := newStub(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "x") })
	c := s.client(gitlab.Options{})
	calls := map[string]func() error{
		"project 0":     func() error { _, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 0, 1, artFile, 10); return err },
		"job 0":         func() error { _, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 0, artFile, 10); return err },
		"limit 0":       func() error { _, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, artFile, 0); return err },
		"empty path":    func() error { _, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, "", 10); return err },
		"traversal":     func() error { _, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, "../x", 10); return err },
		"empty segment": func() error { _, err := c.JobArtifactFile(t.Context(), s.conn(), nil, 7, 1, "a//b", 10); return err },
	}
	for name, call := range calls {
		if call() == nil {
			t.Errorf("%s: JobArtifactFile() succeeded", name)
		}
	}
	if s.count() != 0 {
		t.Errorf("%d requests for invalid input, want 0", s.count())
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

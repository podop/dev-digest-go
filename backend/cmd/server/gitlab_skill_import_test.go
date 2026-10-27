package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
)

// Component tests of the skills URL import for GitLab (GitLab B plan S23: AC26, AC27; FR30, FR31,
// EC23-EC25, NFR3) through the real server: POST /skills/import/preview as the UI calls it.
//
// Two TLS stubs stand in for the outside. The "files" stub is the GitLab REST API of a
// connection: the connection URL is https://git.corp.example:<port>/gitlab (a self-managed host
// on a non-443 port; a fake name server makes the name resolve to loopback, which the connection's
// address rules allow like a private address). The "public" stub is whatever the public fetcher
// reaches: the importer's resolver answers a public address and its dialer is redirected to the
// stub, so the test sees the request exactly as the fetcher built it and the address it dialled.
// Helpers are prefixed gk (the other GitLab component tests share the package).

const (
	gkToken   = "glpat-KKKKKKKKKKKKKKKKKKKK"
	gkHost    = "git.corp.example"
	gkPrefix  = "/gitlab"
	gkProject = "acme/skills"
	gkSkill   = "---\nname: gk-limiter\ndescription: Rate limiting advice\n---\n\n# Limiter\n\nUse a token bucket.\n"
)

// ---- certificates and TLS stubs ----

// gkCert makes a self-signed certificate valid for the stub host names, writes it to a PEM file
// (the server's extra CA) and returns it with the pool the public fetcher trusts.
func gkCert(t *testing.T) (cert tls.Certificate, pool *x509.CertPool, caFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(26), Subject: pkix.Name{CommonName: "gk stub"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{gkHost, "gitlab.com", "other.corp.example"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(leaf)
	caFile = filepath.Join(t.TempDir(), "gk-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool, caFile
}

// gkReq is one request a stub saw.
type gkReq struct {
	Host, Method, Path, Query string
	Header                    http.Header
}

// gkAnswer is what a stub replies.
type gkAnswer struct {
	Status   int
	Body     string
	Location string
}

// gkStub is a TLS server that records every request and answers through its handler (default 404).
type gkStub struct {
	srv *httptest.Server

	mu     sync.Mutex
	reqs   []gkReq
	answer func(gkReq) gkAnswer
}

func gkNewStub(t *testing.T, cert tls.Certificate, answer func(gkReq) gkAnswer) *gkStub {
	t.Helper()
	g := &gkStub{answer: answer}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	g.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gkStub) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	req := gkReq{Host: r.Host, Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone()}
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	answer := g.answer
	g.mu.Unlock()
	a := gkAnswer{Status: http.StatusNotFound, Body: `{"message":"404 File Not Found"}`}
	if answer != nil {
		a = answer(req)
	}
	if a.Location != "" {
		w.Header().Set("Location", a.Location)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(a.Status)
	_, _ = io.WriteString(w, a.Body)
}

func (g *gkStub) requests() []gkReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gkReq(nil), g.reqs...)
}

func (g *gkStub) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = nil
}

func (g *gkStub) setAnswer(answer func(gkReq) gkAnswer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answer = answer
}

func (g *gkStub) port() string {
	_, p, _ := net.SplitHostPort(g.srv.Listener.Addr().String())
	return p
}
func (g *gkStub) addr() string { return g.srv.Listener.Addr().String() }
func (g *gkStub) url() string  { return g.srv.URL }

// gkResolver is the public fetcher's resolver: the listed names answer the given address, any
// other name a public one.
type gkResolver map[string]string

func (r gkResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return []netip.Addr{netip.MustParseAddr(a)}, nil
	}
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

// ---- the environment ----

// gkEnv is a server with the two stubs and the record of the addresses the public fetcher dialled.
type gkEnv struct {
	s      *cpStack
	files  *gkStub // the GitLab REST API of a connection
	public *gkStub // what the public fetcher reaches
	other  *gkStub // a third party that must never be contacted

	mu    sync.Mutex
	dials []string
}

func (e *gkEnv) dialled() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.dials...)
}

// gkBoot starts the server. The fake name server maps the host names to loopback, so the GitLab
// client (egress guard: loopback and private allowed) reaches the files stub; the public
// fetcher's own resolver maps names per the given table.
func gkBoot(t *testing.T, publicResolver gkResolver) *gkEnv {
	t.Helper()
	cert, pool, caFile := gkCert(t)
	ccFakeDNS(t, map[string]string{gkHost: "127.0.0.1", "gitlab.com": "127.0.0.1"})
	e := &gkEnv{
		files: gkNewStub(t, cert, nil),
		public: gkNewStub(t, cert, func(r gkReq) gkAnswer {
			if r.Path == "/acme/skills/-/raw/main/a.md" {
				return gkAnswer{Status: http.StatusOK, Body: gkSkill}
			}
			return gkAnswer{Status: http.StatusNotFound, Body: "not found"}
		}),
		other: gkNewStub(t, cert, nil),
	}
	e.s = cpBoot(t, func(sp *spec) {
		sp.config = func(c *config.Config) { c.ExtraCAFile = caFile }
		sp.urlfetch = urlfetch.Options{
			Resolver:  publicResolver,
			TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				e.mu.Lock()
				e.dials = append(e.dials, address)
				e.mu.Unlock()
				return (&net.Dialer{}).DialContext(ctx, network, e.public.addr())
			},
		}
	})
	e.s.leakCheck(gkToken)
	return e
}

// connect creates a connection for base and returns its id.
func (e *gkEnv) connect(t *testing.T, base string) string {
	t.Helper()
	return gcID(gcCreate(t, e.s, base, gkToken))
}

// connectCorp is the self-managed connection of AC26: private name, the stub's port, a path prefix.
func (e *gkEnv) connectCorp(t *testing.T) (base, key string) {
	t.Helper()
	key = gkHost + ":" + e.files.port()
	base = "https://" + key + gkPrefix
	e.connect(t, base)
	return base, key
}

// preview posts a URL import the way the UI does.
func (e *gkEnv) preview(raw string) response {
	e.s.t.Helper()
	resp := e.s.post("/skills/import/preview", map[string]any{"kind": "url", "url": raw})
	assertNoSecret(e.s.t, "response of the import of "+raw, string(resp.Body), gkToken)
	return resp
}

// wantInvalid checks a 422 invalid_import with exactly the message.
func gkWantInvalid(t *testing.T, resp response, message string) {
	t.Helper()
	if resp.Status != http.StatusUnprocessableEntity || resp.errorCode(t) != "invalid_import" {
		t.Fatalf("import preview = %d %s, want 422 invalid_import", resp.Status, resp.Body)
	}
	got, _ := resp.Obj(t)["error"].(map[string]any)["message"].(string)
	if got != message {
		t.Errorf("import preview error message = %q, want %q", got, message)
	}
}

// wantPreview checks a 200 preview of the sample skill whose source is the typed URL.
func gkWantPreview(t *testing.T, resp response, typed string) {
	t.Helper()
	if resp.Status != http.StatusOK {
		t.Fatalf("import preview of %s = %d %s, want 200", typed, resp.Status, resp.Body)
	}
	got := resp.Obj(t)
	if got["source"] != "imported_url" || got["source_ref"] != typed {
		t.Errorf("preview source = %v, source_ref = %v, want imported_url and the URL as typed %q", got["source"], got["source_ref"], typed)
	}
	if got["name"] != "gk-limiter" || !strings.Contains(fmt.Sprint(got["body"]), "token bucket") {
		t.Errorf("preview = %v, want the skill gk-limiter with its body", got)
	}
}

// gkFilesPath is the path a files API read of the connection prefix, project and file has.
func gkFilesPath(file string) string {
	return gkPrefix + "/api/v4/projects/acme%2Fskills/repository/files/" + url.PathEscape(file) + "/raw"
}

// gkNoCredentials fails when a request carries any credential-looking header or the token anywhere.
func gkNoCredentials(t *testing.T, what string, reqs []gkReq) {
	t.Helper()
	for _, r := range reqs {
		for k, vs := range r.Header {
			for _, v := range vs {
				switch {
				case strings.EqualFold(k, "Authorization"), strings.EqualFold(k, "PRIVATE-TOKEN"), strings.EqualFold(k, "Cookie"):
					t.Errorf("%s: request %s %s carried header %s", what, r.Method, r.Path, k)
				case strings.Contains(v, gkToken):
					t.Errorf("%s: request %s %s carried the token in header %s", what, r.Method, r.Path, k)
				}
			}
		}
		if strings.Contains(r.Path+r.Query, gkToken) {
			t.Errorf("%s: request %s %s?%s carried the token in its URL", what, r.Method, r.Path, r.Query)
		}
	}
}

// ---- AC26 ----

// AC26, EC23: a blob URL of a connected self-managed host (private name, non-443 port, path
// prefix) is read through the files API with the connection's token. Ref "feature" answers 404 and
// "feature/x" 200: shortest ref first, two requests, the preview source is the typed URL, and the
// public fetcher and its dialer are never involved.
func TestGitLabSkillImport_AC26_ConnectedHostImportsThroughTheFilesAPI(t *testing.T) {
	e := gkBoot(t, gkResolver{})
	_, key := e.connectCorp(t)
	ghBefore := len(e.s.gh.requests())
	e.files.setAnswer(func(r gkReq) gkAnswer {
		if r.Query == "ref=feature%2Fx" {
			return gkAnswer{Status: http.StatusOK, Body: gkSkill}
		}
		return gkAnswer{Status: http.StatusNotFound, Body: `{"message":"404 File Not Found"}`}
	})

	typed := "https://" + key + gkPrefix + "/acme/skills/-/blob/feature/x/skills/a.md?ref_type=heads"
	resp := e.preview(typed)
	gkWantPreview(t, resp, typed)

	type seen struct{ Method, Path, Query, Token, Authorization string }
	var got []seen
	for _, r := range e.files.requests() {
		got = append(got, seen{r.Method, r.Path, r.Query, r.Header.Get("PRIVATE-TOKEN"), r.Header.Get("Authorization")})
	}
	want := []seen{
		{http.MethodGet, gkFilesPath("x/skills/a.md"), "ref=feature", gkToken, ""},
		{http.MethodGet, gkFilesPath("skills/a.md"), "ref=feature%2Fx", gkToken, ""},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the files API saw (-want +got):\n%s", diff)
	}
	if n := len(e.public.requests()) + len(e.other.requests()); n != 0 || len(e.dialled()) != 0 {
		t.Errorf("the public fetcher saw %d request(s) and dialled %v, want nothing for a connection's URL", n, e.dialled())
	}
	if n := len(e.s.gh.requests()) - ghBefore; n != 0 {
		t.Errorf("the import sent %d GitHub request(s), want none", n)
	}
}

// AC26, FR30, D28: the first answer that is not a 404 ends the search (200, 401/403, 429, other),
// and an all-404 URL tries the first five splits of a longer link, or every split but the last
// segment of a short one, and then says so (retries of a 429 or 5xx repeat one ref, SCR2).
func TestGitLabSkillImport_AC26_RefSplitsStopAtTheFirstAnswerThatIsNot404(t *testing.T) {
	e := gkBoot(t, gkResolver{})
	_, key := e.connectCorp(t)
	typed := func(rest string) string { return "https://" + key + gkPrefix + "/acme/skills/-/blob/" + rest }
	notFound := gkAnswer{Status: http.StatusNotFound, Body: `{"message":"404 File Not Found"}`}
	ok := gkAnswer{Status: http.StatusOK, Body: gkSkill}

	tests := []struct {
		name    string
		rest    string
		answer  func(n int) gkAnswer // by the 1-based number of the request
		wantRef []string             // the ref of every request, in order
		message string               // "" = a 200 preview
	}{
		{"first split answers 200", "main/skills/a.md", func(int) gkAnswer { return ok }, []string{"main"}, ""},
		{"third split answers 200", "a/b/c/d/e.md", func(n int) gkAnswer {
			if n == 3 {
				return ok
			}
			return notFound
		}, []string{"a", "a%2Fb", "a%2Fb%2Fc"}, ""},
		{"401 on the first split stops", "main/skills/a.md", func(int) gkAnswer { return gkAnswer{Status: http.StatusUnauthorized, Body: "{}"} },
			[]string{"main"}, "GitLab rejected the token of connection " + key},
		{"403 on the second split stops", "a/b/c.md", func(n int) gkAnswer {
			if n == 1 {
				return notFound
			}
			return gkAnswer{Status: http.StatusForbidden, Body: "{}"}
		}, []string{"a", "a%2Fb"}, "GitLab rejected the token of connection " + key},
		{"429 stops", "main/skills/a.md", func(int) gkAnswer { return gkAnswer{Status: http.StatusTooManyRequests, Body: "{}"} },
			[]string{"main"}, "GitLab rate limit reached; try again later"},
		{"500 stops", "main/skills/a.md", func(int) gkAnswer { return gkAnswer{Status: http.StatusInternalServerError, Body: "oops"} },
			[]string{"main"}, "The server answered HTTP 500"},
		{"all 404 on a long link tries five splits", "a/b/c/d/e/f/g.md", func(int) gkAnswer { return notFound },
			[]string{"a", "a%2Fb", "a%2Fb%2Fc", "a%2Fb%2Fc%2Fd", "a%2Fb%2Fc%2Fd%2Fe"}, "File not found on " + key + ", or the token cannot see it"},
		{"all 404 on a short link tries every split but the last segment", "a/b/c.md", func(int) gkAnswer { return notFound },
			[]string{"a", "a%2Fb"}, "File not found on " + key + ", or the token cannot see it"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(e.files.requests())
			var n int
			e.files.setAnswer(func(gkReq) gkAnswer { n++; return tc.answer(n) })
			resp := e.preview(typed(tc.rest))
			if tc.message == "" {
				gkWantPreview(t, resp, typed(tc.rest))
			} else {
				gkWantInvalid(t, resp, tc.message)
			}
			// A 429 or 5xx is retried by the GitLab client (SCR2: the budget counts logical requests),
			// so a ref may be requested several times in a row; the logical sequence is what counts.
			var refs []string
			for _, r := range e.files.requests()[before:] {
				if ref := strings.TrimPrefix(r.Query, "ref="); len(refs) == 0 || refs[len(refs)-1] != ref {
					refs = append(refs, ref)
				}
				if r.Header.Get("PRIVATE-TOKEN") != gkToken {
					t.Errorf("request %s?%s carried PRIVATE-TOKEN %q, want the connection's token", r.Path, r.Query, r.Header.Get("PRIVATE-TOKEN"))
				}
			}
			if diff := cmp.Diff(tc.wantRef, refs); diff != "" {
				t.Errorf("refs requested (-want +got):\n%s", diff)
			}
		})
	}
	if len(e.public.requests()) != 0 || len(e.dialled()) != 0 {
		t.Errorf("the public fetcher was used for a connection's URL: %v", e.dialled())
	}
}

// AC26, EC25: a URL of a connection's host that is not a blob or raw file link answers 422 with
// the fixed message and sends nothing; a 302 of the files API is reported as moved and its target
// receives no request (and no token).
func TestGitLabSkillImport_AC26_OtherLinksAreRefusedAndARedirectIsNotFollowed(t *testing.T) {
	e := gkBoot(t, gkResolver{})
	_, key := e.connectCorp(t)
	root := "https://" + key + gkPrefix

	for _, tc := range []struct{ name, raw string }{
		{"tree", root + "/acme/skills/-/tree/main/skills"},
		{"snippet", root + "/acme/skills/-/snippets/1"},
		{"bare project", root + "/acme/skills"},
		{"blob without a file", root + "/acme/skills/-/blob/main"},
		{"project of one segment", root + "/skills/-/blob/main/a.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gkWantInvalid(t, e.preview(tc.raw), "Only GitLab blob or raw file links can be imported from "+key)
		})
	}
	if n := len(e.files.requests()) + len(e.public.requests()); n != 0 || len(e.dialled()) != 0 {
		t.Errorf("refused links caused %d request(s) and dials %v, want none", n, e.dialled())
	}

	t.Run("a redirect is not followed", func(t *testing.T) {
		e.files.setAnswer(func(gkReq) gkAnswer {
			return gkAnswer{Status: http.StatusFound, Location: e.other.url() + "/acme/skills/-/raw/main/a.md"}
		})
		gkWantInvalid(t, e.preview(root+"/acme/skills/-/blob/main/a.md"), "GitLab reports that this project has moved; use its current URL")
		if got := e.files.requests(); len(got) != 1 {
			t.Errorf("the files API saw %d request(s), want 1 (the redirect ends the search)", len(got))
		}
		if got := e.other.requests(); len(got) != 0 {
			t.Errorf("the redirect target saw %v, want no request", got)
		}
		if len(e.public.requests()) != 0 || len(e.dialled()) != 0 {
			t.Errorf("the public fetcher was used after a redirect: %v", e.dialled())
		}
	})
}

// ---- AC27 ----

// AC27, EC24, FR31: with no connection for gitlab.com a blob URL is rewritten to /-/raw/ (query
// dropped) and fetched publicly: one request to the public address, no credential of any kind even
// though another connection with a token exists, nothing to the connection's API.
func TestGitLabSkillImport_AC27_WithoutAConnectionABlobURLIsFetchedPubliclyWithoutAToken(t *testing.T) {
	e := gkBoot(t, gkResolver{})
	e.connectCorp(t) // a token exists, for another host

	typed := "https://gitlab.com/acme/skills/-/blob/main/a.md?ref_type=heads"
	gkWantPreview(t, e.preview(typed), typed)

	got := e.public.requests()
	if len(got) != 1 {
		t.Fatalf("the public fetcher sent %d request(s) %+v, want 1", len(got), got)
	}
	if r := got[0]; r.Method != http.MethodGet || r.Host != "gitlab.com" || r.Path != "/acme/skills/-/raw/main/a.md" || r.Query != "" {
		t.Errorf("public request = %s https://%s%s?%q, want GET https://gitlab.com/acme/skills/-/raw/main/a.md with no query", r.Method, r.Host, r.Path, r.Query)
	}
	gkNoCredentials(t, "public fetch", got)
	if diff := cmp.Diff([]string{"93.184.216.34:443"}, e.dialled()); diff != "" {
		t.Errorf("addresses dialled by the public fetcher (-want +got):\n%s", diff)
	}
	if n := len(e.files.requests()); n != 0 {
		t.Errorf("the connection's API saw %d request(s) for a URL of another host, want none", n)
	}
}

// AC27, FR31, EC23: the public rules still refuse what is not public. No connection for the host:
// a non-443 port, a private literal address, a name resolving to a private address and a
// non-https URL are refused before any request or dial; the same URL that a connection would
// import (EC23) is refused once the connection is gone.
func TestGitLabSkillImport_AC27_PublicRulesStillRefuseNonPublicTargets(t *testing.T) {
	e := gkBoot(t, gkResolver{"other.corp.example": "10.0.0.5"})
	corpKey := gkHost + ":" + e.files.port()
	id := e.connect(t, "https://"+corpKey+gkPrefix)
	corp := "https://" + corpKey + gkPrefix + "/acme/skills/-/blob/feature/x/skills/a.md"
	e.files.setAnswer(func(gkReq) gkAnswer { return gkAnswer{Status: http.StatusOK, Body: gkSkill} })
	gkWantPreview(t, e.preview(corp), corp) // with the connection the URL imports (EC23)
	e.files.reset()

	if resp := e.s.do(http.MethodDelete, gcPath+"/"+id, nil); resp.Status != http.StatusNoContent && resp.Status != http.StatusOK {
		t.Fatalf("DELETE %s/%s = %d %s, want success", gcPath, id, resp.Status, resp.Body)
	}

	for _, tc := range []struct{ name, raw, message string }{
		{"the connection's URL once it is gone", corp, "Only the default https port (443) is allowed"},
		{"another non-443 port", "https://gitlab.com:8443/acme/skills/-/blob/main/a.md", "Only the default https port (443) is allowed"},
		{"private literal address", "https://10.0.0.5/acme/skills/-/blob/main/a.md", "10.0.0.5 is not a public address"},
		{"name resolving to a private address", "https://other.corp.example/acme/skills/-/blob/main/a.md", "other.corp.example resolves to a non-public address (10.0.0.5)"},
		{"plain http", "http://gitlab.com/acme/skills/-/blob/main/a.md", "Only https:// URLs can be imported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gkWantInvalid(t, e.preview(tc.raw), tc.message)
		})
	}
	if n := len(e.files.requests()) + len(e.public.requests()); n != 0 || len(e.dialled()) != 0 {
		t.Errorf("refused imports caused %d request(s) and dials %v, want none", n, e.dialled())
	}
}

// AC27, FR30, NFR3: the same host key under another path prefix is not the connection's URL: it
// takes the public path and no token is sent; a URL under the prefix goes to the connection's API
// and never falls back to the public fetcher (asserted by the AC26 tests). A stub cannot listen on
// port 443, so the connection of the first half is gitlab.com/gitlab and its API is not exercised.
func TestGitLabSkillImport_AC27_SameHostOtherPathPrefixTakesThePublicPathWithoutAToken(t *testing.T) {
	e := gkBoot(t, gkResolver{})
	e.connect(t, "https://gitlab.com"+gkPrefix)

	typed := "https://gitlab.com/acme/skills/-/blob/main/a.md?ref_type=heads"
	gkWantPreview(t, e.preview(typed), typed)
	got := e.public.requests()
	if len(got) != 1 || got[0].Host != "gitlab.com" || got[0].Path != "/acme/skills/-/raw/main/a.md" {
		t.Fatalf("public requests = %+v, want one GET of https://gitlab.com/acme/skills/-/raw/main/a.md", got)
	}
	gkNoCredentials(t, "public fetch beside a connection of the same host key", got)
	if n := len(e.files.requests()); n != 0 {
		t.Errorf("the connection's API saw %d request(s) for a URL outside its prefix, want none", n)
	}

	// The same holds on the self-managed connection (non-443 port): another prefix is not its URL, so
	// the public rules apply (port refused) and the files API sees no request and no token.
	corp := gkHost + ":" + e.files.port()
	e.connect(t, "https://"+corp+gkPrefix)
	gkWantInvalid(t, e.preview("https://"+corp+"/other/acme/skills/-/blob/main/a.md"), "Only the default https port (443) is allowed")
	if n := len(e.files.requests()); n != 0 || len(e.public.requests()) != 1 || len(e.dialled()) != 1 {
		t.Errorf("a URL outside the prefix caused %d files request(s), public %d, dials %v, want none beyond the first import", n, len(e.public.requests()), e.dialled())
	}
}

// AC27, EC24, NFR3: with a connection for the host the same kind of URL goes through the files API
// with its token (the connection is gitlab.com:<port>, as a stub cannot listen on 443): the URL's
// host key must match the connection's, so a URL of gitlab.com on the default port stays public.
func TestGitLabSkillImport_AC27_WithAConnectionTheFilesAPIAndItsTokenAreUsed(t *testing.T) {
	e := gkBoot(t, gkResolver{})
	key := "gitlab.com:" + e.files.port()
	e.connect(t, "https://"+key)
	e.files.setAnswer(func(r gkReq) gkAnswer {
		if r.Query == "ref=main" {
			return gkAnswer{Status: http.StatusOK, Body: gkSkill}
		}
		return gkAnswer{Status: http.StatusNotFound, Body: "{}"}
	})

	typed := "https://" + key + "/acme/skills/-/blob/main/a.md?ref_type=heads"
	gkWantPreview(t, e.preview(typed), typed)
	got := e.files.requests()
	if len(got) != 1 {
		t.Fatalf("the files API saw %+v, want one request", got)
	}
	if r := got[0]; r.Path != "/api/v4/projects/acme%2Fskills/repository/files/a.md/raw" || r.Query != "ref=main" || r.Header.Get("PRIVATE-TOKEN") != gkToken {
		t.Errorf("files request = %s?%s token %q, want the files API of acme/skills at ref main with the token", r.Path, r.Query, r.Header.Get("PRIVATE-TOKEN"))
	}
	if n := len(e.public.requests()); n != 0 || len(e.dialled()) != 0 {
		t.Errorf("the public fetcher saw %d request(s), dials %v, want none for a connection's URL", n, e.dialled())
	}

	// The default port of the same name is another host key: public path, no token.
	plain := "https://gitlab.com/acme/skills/-/blob/main/a.md"
	gkWantPreview(t, e.preview(plain), plain)
	pub := e.public.requests()
	if len(pub) != 1 {
		t.Fatalf("the public fetcher saw %+v, want one request", pub)
	}
	gkNoCredentials(t, "public fetch of the default-port host key", pub)
	if n := len(e.files.requests()); n != 1 {
		t.Errorf("the files API saw %d request(s) in total, want still 1", n)
	}
}

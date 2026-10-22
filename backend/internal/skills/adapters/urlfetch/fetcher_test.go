package urlfetch_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/skills/adapters/urlfetch"
	"dev-digest-go/backend/internal/skills/domain"
)

// fakeResolver answers from a table and counts lookups per host.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][]string
	lookups map[string]int
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lookups == nil {
		r.lookups = map[string]int{}
	}
	r.lookups[host]++
	answer, ok := r.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	out := make([]netip.Addr, len(answer))
	for i, s := range answer {
		out[i] = netip.MustParseAddr(s)
	}
	return out, nil
}

func (r *fakeResolver) count(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lookups[host]
}

// harness is a TLS test server reachable as a "public" host through an injected resolver and dialer.
type harness struct {
	srv      *httptest.Server
	resolver *fakeResolver
	fetcher  *urlfetch.Fetcher
	mu       sync.Mutex
	dialed   []string
}

const publicIP = "93.184.216.34"

// newHarness serves handler over TLS; "example.com" (the name in httptest's certificate) resolves to
// publicIP, and every dial to it is redirected to the test server.
func newHarness(t *testing.T, handler http.Handler, tweak func(*urlfetch.Options)) *harness {
	t.Helper()
	h := &harness{srv: httptest.NewTLSServer(handler)}
	t.Cleanup(h.srv.Close)
	h.resolver = &fakeResolver{answers: map[string][]string{"example.com": {publicIP}}}
	pool := x509.NewCertPool()
	pool.AddCert(h.srv.Certificate())
	opts := urlfetch.Options{
		Resolver: h.resolver,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			h.mu.Lock()
			h.dialed = append(h.dialed, address)
			h.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, h.srv.Listener.Addr().String())
		},
	}
	opts.TLSConfig = tlsConfig(pool)
	if tweak != nil {
		tweak(&opts)
	}
	h.fetcher = urlfetch.New(opts)
	return h
}

func (h *harness) dials() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.dialed...)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := domain.ParseImportURL(raw)
	if err != nil {
		t.Fatalf("ParseImportURL(%q) error = %v", raw, err)
	}
	return u
}

func importMessage(t *testing.T, err error) string {
	t.Helper()
	var ie *domain.ImportError
	if !errors.As(err, &ie) {
		t.Fatalf("error = %v (%T), want *domain.ImportError", err, err)
	}
	return ie.Message
}

func TestFetch_Success(t *testing.T) {
	var gotUA, gotAccept, gotPath, gotQuery string
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept, gotPath, gotQuery = r.Header.Get("User-Agent"), r.Header.Get("Accept"), r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		fmt.Fprint(w, "# hello")
	}), nil)

	doc, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/skills/x.md?plain=1#frag"))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if string(doc.Bytes) != "# hello" || doc.ContentType != "text/markdown; charset=utf-8" {
		t.Errorf("Fetch() = %q (%s), want the served document", doc.Bytes, doc.ContentType)
	}
	if got := doc.FinalURL.String(); got != "https://example.com/skills/x.md?plain=1" {
		t.Errorf("FinalURL = %q, want the fetched URL without fragment", got)
	}
	if gotUA != "DevDigest-skill-import" || gotAccept != "text/markdown, text/plain, application/zip, */*" {
		t.Errorf("request headers user-agent/accept = %q/%q, want the import headers", gotUA, gotAccept)
	}
	if gotPath != "/skills/x.md" || gotQuery != "plain=1" {
		t.Errorf("request path/query = %q/%q, want /skills/x.md and plain=1", gotPath, gotQuery)
	}
	if diff := cmp.Diff([]string{publicIP + ":443"}, h.dials()); diff != "" {
		t.Errorf("dialed addresses mismatch (-want +got):\n%s", diff)
	}
}

func TestFetch_AC256_NonPublicResolution(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), nil)
	h.resolver.answers["a.test"] = []string{publicIP, "10.0.0.5"}
	h.resolver.answers["b.test"] = []string{"127.0.0.1"}
	h.resolver.answers["v6.test"] = []string{"::ffff:192.168.1.1"}
	h.resolver.answers["localhost"] = []string{"127.0.0.1", "::1"}

	tests := []struct{ url, want string }{
		{"https://a.test/x.md", "a.test resolves to a non-public address (10.0.0.5)"},
		{"https://b.test/x.md", "b.test resolves to a non-public address (127.0.0.1)"},
		{"https://v6.test/x.md", "v6.test resolves to a non-public address (::ffff:192.168.1.1)"},
		{"https://localhost/x.md", "localhost resolves to a non-public address (127.0.0.1)"},
		{"https://unknown.test/x.md", "Could not resolve unknown.test"},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			_, err := h.fetcher.Fetch(t.Context(), mustURL(t, tc.url))
			if got := importMessage(t, err); got != tc.want {
				t.Errorf("Fetch(%s) message = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
	if dials := h.dials(); len(dials) != 0 {
		t.Errorf("dialed %v, want no connection at all", dials)
	}
}

func TestFetch_EmptyResolution(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), nil)
	h.resolver.answers["empty.test"] = nil
	_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://empty.test/x.md"))
	if got, want := importMessage(t, err), "Could not resolve empty.test"; got != want {
		t.Errorf("Fetch() message = %q, want %q", got, want)
	}
}

func TestFetch_RevalidatesTheStartURL(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), nil)
	// A URL that skipped domain.ParseImportURL is still refused.
	for in, want := range map[string]string{
		"http://example.com/x.md":      "Only https:// URLs can be imported",
		"https://127.0.0.1/x.md":       "127.0.0.1 is not a public address",
		"https://example.com:8443/x":   "Only the default https port (443) is allowed",
		"https://u:p@example.com/x":    "URLs with credentials are not allowed",
		"https://0x7f.0.0.1/x":         "127.0.0.1 is not a public address",
		"https://[::ffff:7f00:1]/x":    "::ffff:7f00:1 is not a public address",
		"https://[64:ff9b::a9fe:a9fe]": "64:ff9b::a9fe:a9fe is not a public address",
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatalf("url.Parse(%q) error = %v", in, err)
		}
		_, err = h.fetcher.Fetch(t.Context(), u)
		if got := importMessage(t, err); got != want {
			t.Errorf("Fetch(%s) message = %q, want %q", in, got, want)
		}
	}
	if dials := h.dials(); len(dials) != 0 {
		t.Errorf("dialed %v, want no connection", dials)
	}
}

func TestFetch_IgnoresProxyEnvironment(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer proxy.Close()
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			c, err := proxy.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			c.Close()
		}
	}()
	t.Setenv("HTTPS_PROXY", "http://"+proxy.Addr().String())
	t.Setenv("https_proxy", "http://"+proxy.Addr().String())
	t.Setenv("ALL_PROXY", "http://"+proxy.Addr().String())
	t.Setenv("NO_PROXY", "")

	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "direct") }), nil)
	doc, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/x.md"))
	if err != nil || string(doc.Bytes) != "direct" {
		t.Fatalf("Fetch() = %q, %v; want the document fetched directly", doc.Bytes, err)
	}
	select {
	case <-accepted:
		t.Error("the proxy accepted a connection, want the fetch to bypass it")
	default:
	}
	if diff := cmp.Diff([]string{publicIP + ":443"}, h.dials()); diff != "" {
		t.Errorf("dialed addresses mismatch (-want +got):\n%s", diff)
	}
}

func TestFetch_Redirects(t *testing.T) {
	newChain := func(t *testing.T) *harness {
		return newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var n int
			if _, err := fmt.Sscanf(r.URL.Path, "/r/%d", &n); err == nil && n > 0 {
				http.Redirect(w, r, fmt.Sprintf("/r/%d", n-1), http.StatusFound)
				return
			}
			fmt.Fprint(w, "end")
		}), nil)
	}

	t.Run("three redirects succeed", func(t *testing.T) {
		h := newChain(t)
		doc, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/r/3"))
		if err != nil || string(doc.Bytes) != "end" {
			t.Fatalf("Fetch() = %q, %v; want end", doc.Bytes, err)
		}
		if got := doc.FinalURL.String(); got != "https://example.com/r/0" {
			t.Errorf("FinalURL = %q, want https://example.com/r/0", got)
		}
		if got := h.resolver.count("example.com"); got != 4 {
			t.Errorf("lookups of example.com = %d, want one per request (4)", got)
		}
	})
	t.Run("four redirects fail", func(t *testing.T) {
		h := newChain(t)
		_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/r/4"))
		if got, want := importMessage(t, err), "More than 3 redirects"; got != want {
			t.Errorf("Fetch() message = %q, want %q", got, want)
		}
	})
}

func TestFetch_RedirectTargetsAreGuarded(t *testing.T) {
	tests := []struct {
		name, location, want string
	}{
		{"loopback literal", "https://127.0.0.1/x", "127.0.0.1 is not a public address"},
		{"numeric loopback", "https://0x7f.1/x", "127.0.0.1 is not a public address"},
		{"mapped IPv6 literal", "https://[::ffff:a00:1]/x", "::ffff:a00:1 is not a public address"},
		{"metadata address", "https://169.254.169.254/latest", "169.254.169.254 is not a public address"},
		{"host resolving privately", "https://internal.test/x", "internal.test resolves to a non-public address (10.1.1.1)"},
		{"downgrade to http", "http://example.com/x", "Only https:// URLs can be imported"},
		{"other port", "https://example.com:8443/x", "Only the default https port (443) is allowed"},
		{"credentials", "https://u:p@example.com/x", "URLs with credentials are not allowed"},
		{"missing location", "", "Redirect (302) without a location"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(http.StatusFound)
			}), nil)
			h.resolver.answers["internal.test"] = []string{"10.1.1.1"}
			_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/start"))
			if got := importMessage(t, err); got != tc.want {
				t.Errorf("Fetch() message = %q, want %q", got, tc.want)
			}
			if diff := cmp.Diff([]string{publicIP + ":443"}, h.dials()); diff != "" {
				t.Errorf("dialed addresses mismatch (-want +got), only the first hop may connect:\n%s", diff)
			}
		})
	}
}

func TestFetch_RedirectStatuses(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "https://example.com/final", status)
					return
				}
				fmt.Fprint(w, "ok")
			}), nil)
			doc, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/start"))
			if err != nil || string(doc.Bytes) != "ok" {
				t.Errorf("Fetch() = %q, %v; want ok after a %d", doc.Bytes, err, status)
			}
		})
	}
}

// A DNS answer that changes between requests must not matter: each hop resolves once and dials
// only what it validated.
func TestFetch_RebindingBetweenHops(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "https://rebind.test/x", http.StatusFound)
			return
		}
		fmt.Fprint(w, "should not be fetched")
	}), nil)
	// The first answer for rebind.test would be fine; the guard must not reuse it for another host.
	h.resolver.answers["rebind.test"] = []string{"127.0.0.1"}
	_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/start"))
	if got, want := importMessage(t, err), "rebind.test resolves to a non-public address (127.0.0.1)"; got != want {
		t.Errorf("Fetch() message = %q, want %q", got, want)
	}
	if got := h.resolver.count("rebind.test"); got != 1 {
		t.Errorf("lookups of rebind.test = %d, want 1", got)
	}
}

func TestFetch_DialsOnlyValidatedAddresses(t *testing.T) {
	var dialed []string
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	resolver := &fakeResolver{answers: map[string][]string{"example.com": {"203.0.114.1", "2606:2800:220:1::1", "8.8.8.8"}}}
	f := urlfetch.New(urlfetch.Options{
		Resolver:  resolver,
		TLSConfig: tlsConfig(pool),
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, address)
			first := len(dialed) == 1
			mu.Unlock()
			if first {
				return nil, errors.New("connection refused")
			}
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
	if _, err := f.Fetch(t.Context(), mustURL(t, "https://example.com/x")); err != nil {
		t.Fatalf("Fetch() error = %v, want the second validated address to be used", err)
	}
	want := []string{"203.0.114.1:443", "[2606:2800:220:1::1]:443"}
	if diff := cmp.Diff(want, dialed); diff != "" {
		t.Errorf("dialed addresses mismatch (-want +got):\n%s", diff)
	}
}

func TestFetch_Status(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}), nil)
	_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/x"))
	if got, want := importMessage(t, err), "The server answered HTTP 404"; got != want {
		t.Errorf("Fetch() message = %q, want %q", got, want)
	}
}

func TestFetch_SizeCap(t *testing.T) {
	serve := func(size int, chunked bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if !chunked {
				w.Header().Set("Content-Length", fmt.Sprint(size))
			}
			w.Write(bytes.Repeat([]byte("a"), size))
		})
	}
	tests := []struct {
		name    string
		size    int
		chunked bool
		wantErr string
	}{
		{"exactly at the cap", 1 << 20, false, ""},
		{"one over, declared", 1<<20 + 1, false, "The document is larger than 1048576 bytes"},
		{"one over, undeclared", 1<<20 + 1, true, "The document is larger than 1048576 bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, serve(tc.size, tc.chunked), nil)
			doc, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/x"))
			if tc.wantErr == "" {
				if err != nil || len(doc.Bytes) != tc.size {
					t.Errorf("Fetch() = %d bytes, %v; want %d bytes", len(doc.Bytes), err, tc.size)
				}
				return
			}
			if got := importMessage(t, err); got != tc.wantErr {
				t.Errorf("Fetch() message = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

// The cap counts the bytes after decompression, so a small gzip body cannot inflate past it.
func TestFetch_CapAppliesToDecompressedBody(t *testing.T) {
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	zw.Write(make([]byte, 8<<20))
	zw.Close()
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(packed.Bytes())
	}), nil)
	_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/x"))
	if got, want := importMessage(t, err), "The document is larger than 1048576 bytes"; got != want {
		t.Errorf("Fetch() message = %q, want %q (compressed size %d)", got, want, packed.Len())
	}
}

func TestFetch_Timeouts(t *testing.T) {
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/body" {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	})
	short := func(o *urlfetch.Options) { o.Timeout = 300 * time.Millisecond }

	tests := []struct{ name, path, want string }{
		{"no response headers", "/headers", "Fetching example.com timed out"},
		{"stalled body", "/body", "The download timed out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, handler, short)
			start := time.Now()
			_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com"+tc.path))
			if got := importMessage(t, err); got != tc.want {
				t.Errorf("Fetch() message = %q, want %q", got, tc.want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("Fetch() took %v, want it bounded by the timeout", elapsed)
			}
		})
	}
}

func TestFetch_CallerCancellation(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	h := newHarness(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-block:
		}
	}), nil)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := h.fetcher.Fetch(ctx, mustURL(t, "https://example.com/x"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch() error = %v, want context.Canceled", err)
	}
	var ie *domain.ImportError
	if errors.As(err, &ie) {
		t.Errorf("Fetch() error = %q is an import error, want the cancellation to pass through", ie.Message)
	}
}

func TestFetch_ConnectionFailure(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler(), func(o *urlfetch.Options) {
		o.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") }
	})
	_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/x"))
	msg := importMessage(t, err)
	if !strings.HasPrefix(msg, "Could not fetch example.com: ") || !strings.Contains(msg, "connection refused") {
		t.Errorf("Fetch() message = %q, want %q", msg, "Could not fetch example.com: ... connection refused")
	}
}

func TestFetch_RejectsUntrustedCertificate(t *testing.T) {
	// No root CA is configured, so the test server's certificate must be refused.
	h := newHarness(t, http.NotFoundHandler(), func(o *urlfetch.Options) { o.TLSConfig = nil })
	_, err := h.fetcher.Fetch(t.Context(), mustURL(t, "https://example.com/x"))
	if msg := importMessage(t, err); !strings.HasPrefix(msg, "Could not fetch example.com: ") {
		t.Errorf("Fetch() message = %q, want a fetch failure", msg)
	}
}

func tlsConfig(roots *x509.CertPool) *tls.Config {
	return &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}

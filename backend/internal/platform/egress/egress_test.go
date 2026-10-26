package egress_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"dev-digest-go/backend/internal/platform/egress"
)

func TestForbiddenAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"fe80::1", true},
		{"fe80::1%eth0", true},
		{"::ffff:169.254.169.254", true},
		{"::ffff:0.0.0.0", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.5", false},
		{"172.16.3.4", false},
		{"192.168.1.1", false},
		{"fd00::1", false},
		{"93.184.216.34", false},
		{"2606:4700::1111", false},
		{"::ffff:10.0.0.5", false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			if got := egress.ForbiddenAddr(netip.MustParseAddr(tc.addr)); got != tc.want {
				t.Errorf("ForbiddenAddr(%s) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
	if !egress.ForbiddenAddr(netip.Addr{}) {
		t.Error("ForbiddenAddr(invalid) = false, want true (fail closed)")
	}
}

func TestUnwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("boom")
	ue := &url.Error{Op: "Post", URL: "https://gw.example/v1/chat/completions?secret=x", Err: inner}
	if got := egress.Unwrap(ue); got != inner || strings.Contains(got.Error(), "secret") {
		t.Errorf("Unwrap(*url.Error) = %v, want the inner error", got)
	}
	if got := egress.Unwrap(inner); got != inner {
		t.Errorf("Unwrap(plain) = %v, want unchanged", got)
	}
}

func TestClient_RefusesForbiddenLiteralAtConnect(t *testing.T) {
	t.Parallel()
	// 0.0.0.0 reaches the local machine on Linux: the listener below would accept the connection.
	var hits atomic.Int32
	ln := listen(t, &hits)
	port := ln.Addr().(*net.TCPAddr).Port

	for _, host := range []string{"0.0.0.0", "[::]", "169.254.169.254"} {
		hc := egress.Client{}.HTTPClient()
		resp, err := hc.Get("http://" + host + ":" + itoa(port) + "/v1/models")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("GET %s = no error, want a refusal", host)
		}
		if !egress.IsForbidden(err) {
			t.Errorf("GET %s error = %v, want a forbidden-address refusal", host, err)
		}
		if got := egress.Unwrap(err).Error(); !strings.Contains(got, egress.ForbiddenMessage) || strings.Contains(got, "/v1/models") {
			t.Errorf("Unwrap(err) = %q, want the message without the URL", got)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("listener accepted %d connection(s), want 0", n)
	}
}

func TestClient_GuardChecksTheResolvedAddress(t *testing.T) {
	t.Parallel()
	// "localhost" is a host name: the URL looks harmless, the address it resolves to is judged.
	// This is the DNS-rebinding shape: only the post-resolution address decides.
	var hits atomic.Int32
	ln := listen(t, &hits)
	port := ln.Addr().(*net.TCPAddr).Port

	var seen []netip.Addr
	hc := egress.Client{Guard: func(a netip.Addr) bool {
		seen = append(seen, a)
		return a.IsLoopback()
	}}.HTTPClient()
	resp, err := hc.Get("http://localhost:" + itoa(port) + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("GET localhost = no error, want a refusal")
	}
	if !egress.IsForbidden(err) {
		t.Errorf("error = %v, want a forbidden-address refusal", err)
	}
	if len(seen) == 0 || !seen[0].IsLoopback() {
		t.Errorf("guard saw %v, want the resolved loopback address", seen)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("listener accepted %d connection(s), want 0", n)
	}
}

func TestClient_AllowsLoopback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	resp, err := egress.Client{}.HTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET loopback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()
			var targetHits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits.Add(1) }))
			t.Cleanup(target.Close)
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/steal", code)
			}))
			t.Cleanup(gw.Close)

			req, err := http.NewRequest(http.MethodPost, gw.URL, strings.NewReader(`{"k":1}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("x-api-key", "sk-should-not-travel")
			resp, err := egress.Client{}.HTTPClient().Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != code {
				t.Errorf("status = %d, want the %d itself", resp.StatusCode, code)
			}
			if n := targetHits.Load(); n != 0 {
				t.Errorf("redirect target got %d request(s), want 0", n)
			}
		})
	}
}

func TestClient_IgnoresProxyEnvironment(t *testing.T) {
	// No t.Parallel: t.Setenv. net/http reads the proxy variables once per process and never
	// proxies loopback, so a behavioural check is not possible; the transport must not even
	// carry a proxy function.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	tr, ok := egress.Client{}.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not an *http.Transport")
	}
	if tr.Proxy != nil {
		t.Error("Transport.Proxy is set, want nil (HTTP(S)_PROXY must be ignored)")
	}
}

func TestClient_TrustsRootCAs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)

	// Without the CA the self-signed certificate is refused.
	if resp, err := (egress.Client{}).HTTPClient().Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("GET with the system store = no error, want an unknown-authority failure")
	} else if egress.IsForbidden(err) {
		t.Fatalf("error = %v, want a certificate failure, not the guard", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	resp, err := egress.Client{RootCAs: pool}.HTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatalf("GET with RootCAs: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestLoadExtraCA(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	good := filepath.Join(dir, "ca.pem")
	writeFile(t, good, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	junk := filepath.Join(dir, "junk.pem")
	writeFile(t, junk, []byte("not-a-certificate-SECRET-CONTENT"))
	empty := filepath.Join(dir, "empty.pem")
	writeFile(t, empty, nil)

	t.Run("unset is the system store", func(t *testing.T) {
		t.Parallel()
		pool, err := egress.LoadExtraCA("")
		if pool != nil || err != nil {
			t.Errorf("LoadExtraCA(\"\") = %v, %v, want nil, nil", pool, err)
		}
	})
	t.Run("valid bundle is trusted", func(t *testing.T) {
		t.Parallel()
		pool, err := egress.LoadExtraCA(good)
		if err != nil {
			t.Fatalf("LoadExtraCA(%q): %v", good, err)
		}
		resp, err := egress.Client{RootCAs: pool}.HTTPClient().Get(srv.URL)
		if err != nil {
			t.Fatalf("GET with the loaded CA: %v", err)
		}
		resp.Body.Close()
	})
	for name, path := range map[string]string{
		"missing file":   filepath.Join(dir, "absent.pem"),
		"no certificate": junk,
		"empty file":     empty,
		"directory":      dir,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pool, err := egress.LoadExtraCA(path)
			if err == nil || pool != nil {
				t.Fatalf("LoadExtraCA(%q) = %v, %v, want nil and an error", path, pool, err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the path %q", err, path)
			}
			if strings.Contains(err.Error(), "SECRET-CONTENT") {
				t.Errorf("error %q leaks the file content", err)
			}
		})
	}
}

func TestWithRootCAs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	base := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	got := egress.WithRootCAs(base, pool)
	if got == http.RoundTripper(base) {
		t.Fatal("WithRootCAs returned base itself, want a copy")
	}
	if base.TLSClientConfig.RootCAs != nil {
		t.Error("WithRootCAs modified the base transport")
	}
	resp, err := (&http.Client{Transport: got}).Get(srv.URL)
	if err != nil {
		t.Fatalf("GET through the copy: %v", err)
	}
	resp.Body.Close()

	if egress.WithRootCAs(base, nil) != http.RoundTripper(base) {
		t.Error("WithRootCAs(base, nil) changed the transport, want it unchanged")
	}
	if egress.WithRootCAs(nil, pool) == nil {
		t.Error("WithRootCAs(nil, pool) = nil, want a copy of the default transport")
	}
}

func listen(t *testing.T, hits *atomic.Int32) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return ln
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

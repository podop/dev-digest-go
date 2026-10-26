// Package egress builds the outbound HTTP clients that call user-configured hosts (custom model
// providers) and loads the deploy-level extra CA bundle (DEVDIGEST_EXTRA_CA_FILE).
//
// A client from [Client.HTTPClient] is hardened for an address the user typed:
//   - it never uses a proxy (HTTP_PROXY / HTTPS_PROXY are ignored);
//   - it never follows a redirect (the 3xx response is returned as it is), so a key or an
//     x-api-key header cannot be resent to another host;
//   - it refuses link-local and unspecified addresses at connect time, on the address the host
//     name resolved to, so DNS rebinding cannot reach 169.254.169.254 or 0.0.0.0.
//
// Loopback and private (RFC 1918, ULA) addresses are allowed: the gateway often runs there.
package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"syscall"
	"time"
)

// ForbiddenMessage is the user-facing text of a refused address (spec FR3, EC3).
const ForbiddenMessage = "Base URL resolves to a forbidden address"

const (
	dialTimeout      = 30 * time.Second
	keepAlive        = 30 * time.Second
	tlsHandshakeWait = 10 * time.Second
	idleConnWait     = 90 * time.Second
)

// ForbiddenAddrError is the connect-time refusal of a forbidden address. It carries no address:
// the message is shown to users and written to logs as it is.
type ForbiddenAddrError struct{}

func (ForbiddenAddrError) Error() string { return ForbiddenMessage }

// ErrForbiddenAddr is what [IsForbidden] looks for; errors.Is matches any [ForbiddenAddrError].
var ErrForbiddenAddr error = ForbiddenAddrError{}

// IsForbidden reports whether err (from a client made by [Client.HTTPClient], possibly wrapped
// in *url.Error and *net.OpError) is the refusal of a forbidden address.
func IsForbidden(err error) bool {
	var fe ForbiddenAddrError
	return errors.As(err, &fe)
}

// ForbiddenAddr reports whether a call to a custom provider must not connect to a: link-local
// unicast (169.254.0.0/16, fe80::/10) or unspecified (0.0.0.0, ::). An IPv4-mapped IPv6 address
// (::ffff:169.254.169.254) is judged as its IPv4 form; an invalid address is forbidden.
func ForbiddenAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	a = a.Unmap().WithZone("")
	return a.IsLinkLocalUnicast() || a.IsUnspecified()
}

// Unwrap returns the cause inside a *url.Error. The text of a *url.Error embeds the full request
// URL, which must not reach a message or a log line (backend/INSIGHTS.md); every other error is
// returned unchanged.
func Unwrap(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// LoadExtraCA returns the system trust store plus the certificates of the PEM bundle at path.
// An empty path returns (nil, nil): a nil pool means "system store only". A missing or
// unreadable file, or one without any certificate, is an error that names the path but never
// the file content.
func LoadExtraCA(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, fmt.Errorf("read extra CA file %q: %w", path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("extra CA file %q holds no PEM certificate", path)
	}
	return pool, nil
}

// WithRootCAs returns a copy of base (nil = http.DefaultTransport) that trusts pool. A nil pool
// returns base unchanged. Use it for the outbound clients that are not built by [Client]
// (GitHub, built-in LLM providers) so the extra CA applies to every outbound HTTPS call.
// A base that is not an *http.Transport is returned unchanged.
func WithRootCAs(base http.RoundTripper, pool *x509.CertPool) http.RoundTripper {
	if pool == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	t, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	c := t.Clone()
	if c.TLSClientConfig == nil {
		c.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	c.TLSClientConfig.RootCAs = pool
	return c
}

// Client configures [Client.HTTPClient]. The zero value is usable: system trust store, the
// default [ForbiddenAddr] guard, no timeout (callers bound calls with a context).
type Client struct {
	// RootCAs is the trust store for HTTPS; nil = the system store.
	RootCAs *x509.CertPool
	// Guard reports whether an address must be refused at connect time; nil = [ForbiddenAddr].
	Guard func(netip.Addr) bool
	// Timeout bounds one whole request including the body read; 0 = none.
	Timeout time.Duration
}

// HTTPClient builds a new client and transport on every call.
func (c Client) HTTPClient() *http.Client {
	guard := c.Guard
	if guard == nil {
		guard = ForbiddenAddr
	}
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAlive,
		// Control runs for every connection attempt, after name resolution, with the numeric
		// "ip:port" about to be dialled. Checking here (not on the URL host) is what defeats DNS
		// rebinding and hosts with several A/AAAA records.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || guard(ap.Addr()) {
				return ForbiddenAddrError{}
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy: nil, // A3: HTTP_PROXY / HTTPS_PROXY are ignored
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.RootCAs},
		TLSHandshakeTimeout:   tlsHandshakeWait,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       idleConnWait,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   c.Timeout,
		// The 3xx response is the result; the request (and its key headers) is never resent.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

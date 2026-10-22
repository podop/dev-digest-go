// Package urlfetch downloads skill import documents over HTTPS and is the SSRF guard of the URL
// import. Every hop (the first request and each redirect) resolves the host once, requires every
// answer to be a public address and then dials only those validated addresses; no proxy is used.
package urlfetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"dev-digest-go/backend/internal/skills/domain"
)

// Resolver looks up the addresses of a host. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc opens a connection; net.Dialer.DialContext satisfies it. It is only ever called with a
// validated public IP address.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Options configures a Fetcher. The zero value is the production setup.
type Options struct {
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
	// Dial defaults to a net.Dialer. Tests replace it to reach a local server.
	Dial DialFunc
	// TLSConfig defaults to Go's defaults with TLS 1.2 as the minimum. Tests add a root CA.
	TLSConfig *tls.Config
	// Timeout bounds one fetch end to end; default domain.URLImportTimeout.
	Timeout time.Duration
	// MaxBytes caps the response body; default domain.URLImportMaxBytes.
	MaxBytes int64
	// MaxRedirects caps the redirects followed; default domain.URLImportMaxRedirects.
	MaxRedirects int
}

// Fetcher downloads import documents. It is safe for concurrent use.
type Fetcher struct {
	resolver     Resolver
	dial         DialFunc
	tlsConfig    *tls.Config
	timeout      time.Duration
	maxBytes     int64
	maxRedirects int
}

// New returns a Fetcher.
func New(opts Options) *Fetcher {
	f := &Fetcher{
		resolver:     opts.Resolver,
		dial:         opts.Dial,
		tlsConfig:    opts.TLSConfig,
		timeout:      opts.Timeout,
		maxBytes:     opts.MaxBytes,
		maxRedirects: opts.MaxRedirects,
	}
	if f.resolver == nil {
		f.resolver = net.DefaultResolver
	}
	if f.dial == nil {
		f.dial = (&net.Dialer{}).DialContext
	}
	if f.tlsConfig == nil {
		f.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if f.timeout <= 0 {
		f.timeout = domain.URLImportTimeout
	}
	if f.maxBytes <= 0 {
		f.maxBytes = domain.URLImportMaxBytes
	}
	if f.maxRedirects <= 0 {
		f.maxRedirects = domain.URLImportMaxRedirects
	}
	return f
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// Fetch downloads start. It is revalidated here, so a URL that did not come from
// domain.ParseImportURL is still checked. Every failure that the user can act on is a
// *domain.ImportError; a canceled ctx returns its own error.
func (f *Fetcher) Fetch(ctx context.Context, start *url.URL) (domain.FetchedDocument, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	target, err := domain.ParseImportURL(start.String())
	if err != nil {
		return domain.FetchedDocument{}, err
	}
	for hop := 0; ; hop++ {
		resp, err := f.get(ctx, target)
		if err != nil {
			return domain.FetchedDocument{}, err
		}
		if isRedirect(resp.StatusCode) {
			location := resp.Header.Get("Location")
			closeBody(resp)
			if location == "" {
				return domain.FetchedDocument{}, domain.InvalidImport(fmt.Sprintf("Redirect (%d) without a location", resp.StatusCode))
			}
			if hop >= f.maxRedirects {
				return domain.FetchedDocument{}, domain.InvalidImport(fmt.Sprintf("More than %d redirects", f.maxRedirects))
			}
			next, err := target.Parse(location)
			if err != nil {
				return domain.FetchedDocument{}, domain.InvalidImport("Not a valid URL")
			}
			if target, err = domain.ParseImportURL(next.String()); err != nil {
				return domain.FetchedDocument{}, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			closeBody(resp)
			return domain.FetchedDocument{}, domain.InvalidImport(fmt.Sprintf("The server answered HTTP %d", resp.StatusCode))
		}
		body, err := f.readBody(ctx, resp)
		closeBody(resp)
		if err != nil {
			return domain.FetchedDocument{}, err
		}
		return domain.FetchedDocument{Bytes: body, ContentType: resp.Header.Get("Content-Type"), FinalURL: target}, nil
	}
}

func closeBody(resp *http.Response) { _ = resp.Body.Close() }

// get resolves the host, validates every address and sends one GET without following redirects.
func (f *Fetcher) get(ctx context.Context, target *url.URL) (*http.Response, error) {
	hostname := target.Hostname()
	addrs, err := f.publicAddrs(ctx, hostname)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, domain.InvalidImport("Not a valid URL")
	}
	req.Header.Set("User-Agent", domain.URLImportUserAgent)
	req.Header.Set("Accept", domain.URLImportAccept)

	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             nil, // never through HTTPS_PROXY & co.
			DialContext:       f.dialValidated(hostname, addrs),
			TLSClientConfig:   f.tlsConfig.Clone(),
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			return nil, domain.InvalidImport(fmt.Sprintf("Could not fetch %s: %s", hostname, reason(err)))
		}
		return nil, f.ctxError(ctx, fmt.Sprintf("Fetching %s timed out", hostname))
	}
	return resp, nil
}

// reason is the transport's error text without the request URL that url.Error repeats.
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// ctxError maps a finished context to the user message for our own deadline, or to the caller's
// cancellation.
func (f *Fetcher) ctxError(ctx context.Context, timeoutMessage string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.InvalidImport(timeoutMessage)
	}
	return ctx.Err()
}

// publicAddrs resolves host and requires every address to be public.
func (f *Fetcher) publicAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = f.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			if ctx.Err() != nil {
				return nil, f.ctxError(ctx, fmt.Sprintf("Fetching %s timed out", host))
			}
			return nil, domain.InvalidImport("Could not resolve " + host)
		}
	}
	if len(addrs) == 0 {
		return nil, domain.InvalidImport("Could not resolve " + host)
	}
	for i, a := range addrs {
		a = a.WithZone("")
		if !domain.IsPublicAddress(a) {
			return nil, domain.InvalidImport(fmt.Sprintf("%s resolves to a non-public address (%s)", host, a))
		}
		addrs[i] = a
	}
	return addrs, nil
}

// dialValidated returns a dial function for one hop that connects to the validated addresses only,
// whatever address string the transport hands it, and re-checks each one right before dialing.
func (f *Fetcher) dialValidated(host string, addrs []netip.Addr) DialFunc {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		h, port, err := net.SplitHostPort(address)
		if err != nil || h != host || port != "443" {
			return nil, fmt.Errorf("refusing to dial %q", address)
		}
		var firstErr error
		for _, a := range addrs {
			if !domain.IsPublicAddress(a) {
				return nil, fmt.Errorf("refusing to dial non-public address %s", a)
			}
			conn, err := f.dial(ctx, network, net.JoinHostPort(a.String(), port))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				break
			}
		}
		return nil, firstErr
	}
}

// readBody reads at most maxBytes of the body.
func (f *Fetcher) readBody(ctx context.Context, resp *http.Response) ([]byte, error) {
	tooLarge := domain.InvalidImport("The document is larger than " + strconv.FormatInt(f.maxBytes, 10) + " bytes")
	if resp.ContentLength > f.maxBytes {
		return nil, tooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, f.ctxError(ctx, "The download timed out")
		}
		return nil, domain.InvalidImport(fmt.Sprintf("Could not fetch %s: %s", resp.Request.URL.Hostname(), err))
	}
	if int64(len(body)) > f.maxBytes {
		return nil, tooLarge
	}
	return body, nil
}

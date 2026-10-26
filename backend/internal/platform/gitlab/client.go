// Package gitlab is a small read-only GitLab client over net/http for the calls the backend
// needs: the token owner, token scopes, instance version, projects, merge requests with their
// diffs and commits, issues, and merge request diff stats through one GraphQL query.
//
// The HTTP client is supplied by the caller (platform/egress: no proxy, no redirect, the dial
// guard); the package never builds one. The token goes in the PRIVATE-TOKEN header of each call
// and only to the connection's own scheme, host, port and path prefix. It never appears in a
// URL, an error, a log line or a returned value. Paging uses page numbers; a Link header or
// X-Next-Page value is never followed.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/retry"
)

// Limits and timeouts (NFR2-NFR5).
const (
	DefaultTimeout       = 30 * time.Second // each REST or GraphQL call
	DefaultDetailTimeout = 90 * time.Second // a whole multi-page detail fetch
	TestTimeout          = 15 * time.Second // each call of a connection test; the caller bounds its context with it
	MaxFiles             = 3000             // diff entries read per merge request
	MaxCommits           = 250              // commits read per merge request
	MaxPerPage           = 100              // GitLab's own ceiling for per_page
	MaxPage              = 1000             // sanity bound on a page number
	MaxStatsBatch        = 20               // merge requests per GraphQL stats query (FR26)

	// MaxResponseBytes bounds one response body. A larger body is an error, never a truncation.
	MaxResponseBytes = 32 << 20

	maxMessageBytes = 1000
	maxResetSeconds = 1 << 40 // far beyond any real reset; keeps time.Unix well inside its range
	maxRetryAfter   = 24 * time.Hour
	// defaultRetryAfter stands for a Retry-After header that is present but unreadable.
	defaultRetryAfter = time.Minute
)

var (
	// ErrRedirect is a 3xx answer. It is never followed: the token must not travel on.
	ErrRedirect = errors.New("gitlab answered with a redirect")
	// ErrNotAPI is a 2xx answer that is not the GitLab API: a non-JSON content type (an HTML
	// page), a body that does not decode, or a body without the fields every answer has.
	ErrNotAPI = errors.New("no GitLab API answered")
	// ErrBaseURL is a connection base URL the client refuses to call.
	ErrBaseURL = errors.New("gitlab: invalid base URL")
	// ErrToken is a token that cannot be sent as a header value.
	ErrToken = errors.New("gitlab: token has invalid characters")
)

// StatusError is a non-2xx, non-3xx GitLab answer. Message is GitLab's own message with the token
// scrubbed and at most 1000 bytes.
type StatusError struct {
	Status  int
	Message string
	// RetryAfter is the time the Retry-After header says to wait until (now + its seconds, or its
	// HTTP date); the zero time without such a header. An unreadable value counts as a minute. It
	// is at most a day away.
	RetryAfter time.Time
	// Reset is the time the RateLimit-Reset header (epoch seconds) says the window renews; the
	// zero time without a valid one.
	Reset time.Time
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("gitlab: HTTP %d: %s", e.Status, e.Message)
}

// StatusCode lets retry.IsTransient classify the error.
func (e *StatusError) StatusCode() int { return e.Status }

// ResumeAt is when the host may be called again according to the answer: RetryAfter, else Reset,
// else the zero time (FR27, A4).
func (e *StatusError) ResumeAt() time.Time {
	if !e.RetryAfter.IsZero() {
		return e.RetryAfter
	}
	return e.Reset
}

// Conn is one GitLab connection as a call sees it. BaseURL is the REST root
// (https://host[:port][/prefix]/api/v4); Token may be empty for an anonymous call.
type Conn struct {
	BaseURL string
	Token   string
}

// Options configures a Client.
type Options struct {
	// HTTP is the caller's client (platform/egress). Required: New panics without it.
	HTTP *http.Client
	// Retry is the policy for reads. Nil means retry.Default(); a pointer to the zero value means
	// no retry (the background poll, NFR5).
	Retry *retry.Options
	// Log receives one line per attempt (NFR9). Nil discards.
	Log *slog.Logger
	// Timeout bounds each call (default DefaultTimeout).
	Timeout time.Duration
	// DetailTimeout bounds the page loop of Diffs and of Commits (default DefaultDetailTimeout).
	DetailTimeout time.Duration
}

// Client talks to GitLab. It is safe for concurrent use.
type Client struct {
	http          *http.Client
	retry         retry.Options
	log           *slog.Logger
	timeout       time.Duration
	detailTimeout time.Duration
}

// New builds a Client. It panics if opts.HTTP is nil (a wiring bug): a default client would
// bypass the dial guard and the no-proxy rule.
func New(opts Options) *Client {
	if opts.HTTP == nil {
		panic("gitlab.New: Options.HTTP is required")
	}
	c := &Client{
		http:          withoutRedirects(opts.HTTP),
		retry:         retry.Default(),
		log:           opts.Log,
		timeout:       opts.Timeout,
		detailTimeout: opts.DetailTimeout,
	}
	if opts.Retry != nil {
		c.retry = *opts.Retry
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.detailTimeout <= 0 {
		c.detailTimeout = DefaultDetailTimeout
	}
	return c
}

// WithRetry returns a copy of c that uses policy p. The zero policy means no retry.
func (c *Client) WithRetry(p retry.Options) *Client {
	cc := *c
	cc.retry = p
	return &cc
}

// withoutRedirects returns a copy of hc that hands back a 3xx instead of following it, even when
// the caller's client would. The caller's client is left unchanged.
func withoutRedirects(hc *http.Client) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// target returns the request URL for path (already escaped) below the connection's REST root,
// and the root's host for logs. Only http and https roots without credentials, query or
// fragment are accepted.
func (cn Conn) target(path string, q url.Values) (target, host string, err error) {
	base := strings.TrimRight(cn.BaseURL, "/")
	if base == "" || strings.ContainsAny(base, "?#\\ \t\r\n") {
		return "", "", ErrBaseURL
	}
	u, perr := url.Parse(base)
	if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", "", ErrBaseURL
	}
	target = base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	return target, u.Host, nil
}

// token returns the trimmed token, refusing one that is not a valid header value.
func (cn Conn) token() (string, error) {
	tok := strings.TrimSpace(cn.Token)
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x21 || c > 0x7e {
			return "", ErrToken
		}
	}
	return tok, nil
}

// request is one logical call.
type request struct {
	conn   Conn
	op     string // short name for logs, never a path or a value
	method string
	path   string // escaped, below the REST root
	query  url.Values
	// url overrides conn + path (GraphQL lives beside the REST root, not below it).
	url  string
	body []byte
}

// getJSON decodes the 2xx answer of a GET into T, retrying transient failures.
func getJSON[T any](ctx context.Context, c *Client, conn Conn, op, path string, q url.Values) (T, error) {
	return doJSON[T](ctx, c, request{conn: conn, op: op, method: http.MethodGet, path: path, query: q})
}

func doJSON[T any](ctx context.Context, c *Client, r request) (T, error) {
	tok, err := r.conn.token()
	if err != nil {
		var zero T
		return zero, err
	}
	attempt := 0
	return retry.Value(ctx, c.policy(), func(ctx context.Context) (T, error) {
		attempt++
		var out T
		err := c.once(ctx, r, tok, attempt, &out)
		return out, err
	})
}

// policy is the retry policy with one addition: a 429 whose Retry-After is longer than the
// longest backoff step is not retried, since the retry would fail again inside the pause.
func (c *Client) policy() retry.Options {
	p := c.retry
	base := p.IsRetryable
	if base == nil {
		base = retry.IsTransient
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = retry.DefaultMaxDelay
	}
	p.IsRetryable = func(err error) bool {
		var se *StatusError
		if errors.As(err, &se) && se.Status == http.StatusTooManyRequests {
			if at := se.ResumeAt(); !at.IsZero() && time.Until(at) > maxDelay {
				return false
			}
		}
		return base(err)
	}
	return p
}

// once performs one attempt bounded by c.timeout and decodes a 2xx JSON body into out.
func (c *Client) once(ctx context.Context, r request, token string, attempt int, out any) (err error) {
	target, host, err := r.conn.target(r.path, r.query)
	if err != nil {
		return err
	}
	if r.url != "" {
		target = r.url
	}
	start := time.Now()
	status := 0
	defer func() {
		c.logAttempt(host, r.op, status, err, attempt, time.Since(start))
	}()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var body io.Reader
	if r.body != nil {
		body = strings.NewReader(string(r.body))
	}
	req, err := http.NewRequestWithContext(ctx, r.method, target, body)
	if err != nil {
		return fmt.Errorf("gitlab %s: %w", r.op, ErrBaseURL)
	}
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dev-digest-go")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return transportError(r.op, err, token)
	}
	defer resp.Body.Close()
	status = resp.StatusCode

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		return fmt.Errorf("gitlab %s: %w", r.op, ErrRedirect)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return statusError(resp, token)
	}
	if !isJSON(resp.Header.Get("Content-Type")) {
		return fmt.Errorf("gitlab %s: %w", r.op, ErrNotAPI)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return transportError(r.op, err, token)
	}
	if len(raw) > MaxResponseBytes {
		return fmt.Errorf("gitlab %s: response larger than %d bytes", r.op, MaxResponseBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// The decoder's message can quote a piece of the body; only the sentinel is kept.
		return fmt.Errorf("gitlab %s: %w", r.op, ErrNotAPI)
	}
	return nil
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

// transportError drops the request URL (a *url.Error embeds it) and the token from a transport
// error. The error keeps its type for retry classification unless its text had to change.
func transportError(op string, err error, token string) error {
	cause := egress.Unwrap(err)
	if clean := scrub(cause.Error(), token); clean != cause.Error() {
		return fmt.Errorf("gitlab %s: %w", op, errors.New(clean))
	}
	return fmt.Errorf("gitlab %s: %w", op, cause)
}

// statusError builds a StatusError from GitLab's JSON error body ({"message": …} or
// {"error": …}); a body in any other shape gives the status text.
func statusError(resp *http.Response, token string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Message json.RawMessage `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &payload) == nil {
		msg = rawText(payload.Message)
		if msg == "" {
			msg = rawText(payload.Error)
		}
	}
	if strings.TrimSpace(msg) == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	msg = scrub(msg, token) // before the cut: a cut token is no longer recognisable
	if len(msg) > maxMessageBytes {
		msg = strings.ToValidUTF8(msg[:maxMessageBytes], "")
	}
	return &StatusError{
		Status:     resp.StatusCode,
		Message:    msg,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		Reset:      parseReset(resp.Header.Get("RateLimit-Reset")),
	}
}

// rawText reads a JSON string as it is, or any other JSON value as its compact text.
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// parseRetryAfter reads Retry-After (delay seconds or an HTTP date) as the time to wait until,
// at most maxRetryAfter after now. An absent header is the zero time; an unreadable one is
// defaultRetryAfter after now.
func parseRetryAfter(v string, now time.Time) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	wait := defaultRetryAfter
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs >= 0 {
		wait = time.Duration(min(secs, int64(maxRetryAfter/time.Second))) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		wait = min(max(at.Sub(now), 0), maxRetryAfter)
	}
	return now.Add(wait)
}

// parseReset reads RateLimit-Reset (epoch seconds); an absent or malformed value is the zero time.
func parseReset(v string) time.Time {
	secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || secs <= 0 || secs > maxResetSeconds {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

// logAttempt writes the one line NFR9 asks for: host, operation, status or error class,
// attempt, latency. It carries no token, title, description or diff.
func (c *Client) logAttempt(host, op string, status int, err error, attempt int, took time.Duration) {
	attrs := []any{"host", host, "op", op, "attempt", attempt, "latency_ms", took.Milliseconds()}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	if err != nil {
		attrs = append(attrs, "error_class", errorClass(err))
		c.log.Warn("gitlab call failed", attrs...)
		return
	}
	c.log.Info("gitlab call", attrs...)
}

func errorClass(err error) string {
	var se *StatusError
	var ne net.Error
	switch {
	case errors.As(err, &se):
		return "status"
	case errors.Is(err, ErrRedirect):
		return "redirect"
	case errors.Is(err, ErrNotAPI):
		return "not_api"
	case egress.IsForbidden(err):
		return "forbidden_address"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, ErrBaseURL), errors.Is(err, ErrToken):
		return "config"
	default:
		return "connection"
	}
}

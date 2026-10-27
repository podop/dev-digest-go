// Package glapi is the runner's GitLab client (spec GitLab C FR24-FR28, NFR2, NFR7, NFR9): the
// merge request read, the draft-note calls and the single-note call of the "MR review" and "MR
// comment" posting modes, over net/http. It mirrors adapters/ghapi and is not the server's
// platform/gitlab client: the runner links no retry-policy, secrets-store or server code.
//
// Safety rules, all enforced here:
//   - the token travels in the PRIVATE-TOKEN header with every call and never appears in a returned
//     error; it is sent only to the scheme, host, port and path prefix of CI_API_V4_URL (which must
//     be https), checked on every request before it is sent;
//   - redirects are never followed (the token would go with them): a 3xx answer is an error;
//   - one call takes at most 30 s; a GET that failed on a transient error is retried at most twice,
//     a POST, PUT or DELETE is never retried;
//   - trust is the system store plus CI_SERVER_TLS_CA_FILE (see NewTransport);
//   - a request body never carries reviewer_state, an approval or a quick action of its own
//     (bulk_publish is called without a body, NFR9).
package glapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dev-digest-go/backend/internal/platform/egress"
)

const (
	userAgent = "devdigest-agent-runner"
	// requestTimeout is the time budget of one request (NFR7).
	requestTimeout = 30 * time.Second
	// getRetries is how many times a failed GET is repeated (NFR7).
	getRetries = 2
	// defaultRetryDelay is the pause before a GET is repeated.
	defaultRetryDelay = 500 * time.Millisecond
	// maxResponseBytes caps a success body (a drafts page of 100 notes is far below it).
	maxResponseBytes = 4 << 20
	// errorBodyBytes is how much of an error body reaches the job log (FR28).
	errorBodyBytes = 1000
	// errorReadBytes is how much of an error body is read: the token is redacted before the cut,
	// and a token straddling the cut would survive as a fragment.
	errorReadBytes = 64 << 10
	// redacted replaces the token in a message.
	redacted = "[REDACTED]"
	// minRedactLen is the shortest token the messages scrub (see ci/runner/app.Run).
	minRedactLen = 8

	// whatReview and whatComment name the operation in a message (FR28); every call of the MR
	// review flow (read, list, create, publish, delete) is part of "posting MR review".
	whatReview  = "posting MR review"
	whatComment = "posting MR comment"
)

// Options configure a Client.
type Options struct {
	// APIURL is CI_API_V4_URL: https, no user info, query or fragment, no dot segments. It is the
	// origin and path prefix every request is confined to.
	APIURL string
	// Transport is the round tripper; nil = http.DefaultTransport. Build one that trusts
	// CI_SERVER_TLS_CA_FILE with NewTransport.
	Transport http.RoundTripper
	// RetryDelay is the pause before a GET is repeated; 0 = 500 ms.
	RetryDelay time.Duration
}

// Client is the runner's GitLab client.
type Client struct {
	origin     string // scheme://host[:port]
	host       string
	prefix     string // path prefix without trailing slash, may be ""
	http       *http.Client
	retryDelay time.Duration
}

// APIError is a non-2xx GitLab answer. Its text is the job-log message of FR28,
// "GitLab API error <what> (<path>): <status> <body cut to 1,000 bytes, token redacted>", where
// <path> is the request path without host or query.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// MRRef addresses a merge request: the numeric CI_PROJECT_ID and the merge request IID.
type MRRef struct {
	ProjectID string
	IID       int
}

// DiffRefs are the three SHAs a positioned draft note is anchored to.
type DiffRefs struct {
	BaseSHA, StartSHA, HeadSHA string
}

// MergeRequest is the part of a merge request the runner reads. DiffRefs is nil when GitLab
// answers null or an incomplete diff_refs (FR25: inline threads are then not created).
type MergeRequest struct {
	IID      int
	DiffRefs *DiffRefs
}

// DraftPosition anchors a draft note to a line of the diff (FR26): position_type is always
// "text"; OldPath is the file's old path (equal to NewPath when not renamed); OldLine is 0 unless
// the new-side line is a context line, then it is omitted from the request.
type DraftPosition struct {
	Refs             DiffRefs
	NewPath, OldPath string
	NewLine, OldLine int
}

// NewDraft is a draft note to create: a summary draft has no Position.
type NewDraft struct {
	Note     string
	Position *DraftPosition
}

// Draft is a draft note on a merge request.
type Draft struct {
	ID int
}

// New returns a Client. It fails when APIURL is not a usable https API root.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(opts.APIURL)
	if err != nil {
		return nil, errors.New("CI_API_V4_URL is not a valid URL")
	}
	switch {
	case u.Scheme != "https":
		return nil, errors.New("CI_API_V4_URL must use https")
	case u.Host == "" || u.Hostname() == "":
		return nil, errors.New("CI_API_V4_URL has no host")
	case u.User != nil:
		return nil, errors.New("CI_API_V4_URL must not carry user info")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, errors.New("CI_API_V4_URL must not carry a query or a fragment")
	case u.RawPath != "" && u.RawPath != u.EscapedPath() || strings.ContainsAny(u.Path, "\\\x00"):
		return nil, errors.New("CI_API_V4_URL has an unusable path")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("CI_API_V4_URL has dot segments in its path")
		}
	}
	delay := opts.RetryDelay
	if delay <= 0 {
		delay = defaultRetryDelay
	}
	return &Client{
		origin:     u.Scheme + "://" + u.Host,
		host:       u.Host,
		prefix:     strings.TrimRight(u.Path, "/"),
		retryDelay: delay,
		http: &http.Client{
			Transport: opts.Transport,
			Timeout:   requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// NewTransport returns a round tripper that trusts the system store plus the PEM bundle at
// caFile (CI_SERVER_TLS_CA_FILE); "" = the default transport. A missing or empty bundle is an
// error that names the path, never its content.
func NewTransport(caFile string) (http.RoundTripper, error) {
	pool, err := egress.LoadExtraCA(caFile)
	if err != nil {
		return nil, fmt.Errorf("CI_SERVER_TLS_CA_FILE: %w", err)
	}
	return egress.WithRootCAs(nil, pool), nil
}

// request is one API call.
type request struct {
	method string
	path   string // below the API root, escaped, starts with "/"
	query  string
	what   string
	body   any // JSON-encoded when non-nil
}

// call sends r and returns the status and the (capped) body of a 2xx answer. A non-2xx answer is
// an *APIError, a failure to get an answer an error with the path and no token. GETs are retried
// on transient errors, nothing else.
func (c *Client) call(ctx context.Context, token string, r request) ([]byte, error) {
	if err := validToken(token); err != nil {
		return nil, err
	}
	target := c.origin + c.prefix + r.path
	if r.query != "" {
		target += "?" + r.query
	}
	u, err := url.Parse(target)
	if err != nil || !c.confined(u) {
		return nil, fmt.Errorf("GitLab API error %s (%s): the request leaves the CI_API_V4_URL origin or path prefix", r.what, r.path)
	}
	var payload []byte
	if r.body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // the note body is sent as it is
		if err := enc.Encode(r.body); err != nil {
			return nil, fmt.Errorf("GitLab API error %s (%s): %w", r.what, u.EscapedPath(), err)
		}
		payload = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	}
	attempts := 1
	if r.method == http.MethodGet {
		attempts += getRetries
	}
	var lastErr error
	for i := range attempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, lastErr
			case <-time.After(c.retryDelay):
			}
		}
		body, retry, err := c.once(ctx, token, r, u, payload)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return nil, lastErr
}

// once sends the request one time; retry says whether a GET may be repeated after err.
func (c *Client) once(ctx context.Context, token string, r request, u *url.URL, payload []byte) (body []byte, retry bool, err error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), rd)
	if err != nil {
		return nil, false, fmt.Errorf("GitLab API error %s (%s): %s", r.what, u.EscapedPath(), transportText(err, token))
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// a cancelled or timed-out context is final; a connection failure is transient
		return nil, ctx.Err() == nil, fmt.Errorf("GitLab API error %s (%s): %s", r.what, u.EscapedPath(), transportText(err, token))
	}
	defer closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, transientStatus(resp.StatusCode), &APIError{Status: resp.StatusCode, Message: apiErrorText(r.what, u.EscapedPath(), resp, token)}
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, ctx.Err() == nil, fmt.Errorf("GitLab API error %s (%s): %s", r.what, u.EscapedPath(), transportText(err, token))
	}
	if len(out) > maxResponseBytes {
		return nil, false, fmt.Errorf("GitLab API error %s (%s): the response is larger than %d bytes", r.what, u.EscapedPath(), maxResponseBytes)
	}
	return out, false, nil
}

// confined reports whether u is on the client's origin and under its path prefix.
func (c *Client) confined(u *url.URL) bool {
	if u.Scheme+"://"+u.Host != c.origin || u.User != nil || u.Fragment != "" {
		return false
	}
	if c.prefix == "" {
		return strings.HasPrefix(u.Path, "/")
	}
	return u.Path == c.prefix || strings.HasPrefix(u.Path, c.prefix+"/")
}

func transientStatus(code int) bool {
	switch code {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// validToken refuses a token that cannot be a header value, without echoing it.
func validToken(token string) error {
	if token == "" {
		return errors.New("GitLab API error: the token is empty")
	}
	for i := range len(token) {
		if b := token[i]; b < 0x21 || b > 0x7e {
			return errors.New("GitLab API error: the token is not a valid header value")
		}
	}
	return nil
}

func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// apiErrorText is "GitLab API error <what> (<path>): <status> <body>" with the body redacted of
// the token, then cut to 1,000 bytes (at a character boundary).
func apiErrorText(what, path string, resp *http.Response, token string) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorReadBytes))
	text := redactToken(strings.ToValidUTF8(string(raw), "�"), token)
	if len(text) > errorBodyBytes {
		cut := errorBodyBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return "GitLab API error " + what + " (" + path + "): " + strconv.Itoa(resp.StatusCode) + " " + text
}

// transportText is the text of a failed request without the request URL (the caller adds the
// path) and without the token.
func transportText(err error, token string) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return redactToken(err.Error(), token)
}

func redactToken(s, token string) string {
	if len(token) >= minRedactLen {
		return strings.ReplaceAll(s, token, redacted)
	}
	return s
}

// Package github is a small GitHub REST client over net/http for the calls the
// backend needs: pull requests, issues, review comments and the token owner.
//
// The token is read through a function on every public call, so a key saved at
// runtime is used without a restart. It is sent only in the Authorization
// header and never appears in an error, a log line or a returned value.
package github

import (
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

	"dev-digest-go/backend/internal/platform/retry"
)

// Limits and timeouts (NFR5, Node parity).
const (
	DefaultBaseURL       = "https://api.github.com"
	DefaultTimeout       = 30 * time.Second // each REST call
	DefaultDetailTimeout = 90 * time.Second // the whole PR detail fetch
	MaxFiles             = 3000             // GitHub's own ceiling for a PR's file list
	MaxCommits           = 250              // GitHub's own ceiling for a PR's commit list
	PageSize             = 100

	maxResponseBytes = 64 << 20
	maxMessageBytes  = 1000
	maxResetSeconds  = 1 << 40 // far beyond any real reset; keeps time.Unix well inside its range
)

// ErrNoToken is returned when no GitHub token is configured at call time.
var ErrNoToken = errors.New("github token is not configured")

// StatusError is a non-2xx GitHub response. Message is GitHub's own message.
type StatusError struct {
	Status  int
	Message string
	// RateLimitRemaining is GitHub's x-ratelimit-remaining header, "" when the response had none.
	RateLimitRemaining string
	// RateLimitReset is when GitHub's x-ratelimit-reset header says the window renews; the zero
	// time when the response had no valid one.
	RateLimitReset time.Time
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message)
}

// RateLimited reports whether the response said the rate limit is used up (remaining 0).
func (e *StatusError) RateLimited() bool { return strings.TrimSpace(e.RateLimitRemaining) == "0" }

// StatusCode lets retry.IsTransient classify the error.
func (e *StatusError) StatusCode() int { return e.Status }

// Options configures a Client.
type Options struct {
	// Token returns the current token, "" when none is configured. Required.
	Token func() string
	// BaseURL defaults to DefaultBaseURL (tests point it at an httptest server).
	BaseURL string
	// HTTPClient defaults to a client with no overall timeout; deadlines come
	// from contexts.
	HTTPClient *http.Client
	// ArtifactClient fetches the storage leg of an artifact download. It defaults to HTTPClient;
	// either way the copy used never follows a redirect and never keeps a cookie jar.
	ArtifactClient *http.Client
	// Retry is the policy for idempotent reads. Nil means retry.Default().
	Retry *retry.Options
	// Timeout bounds each REST call (default DefaultTimeout).
	Timeout time.Duration
	// DetailTimeout bounds a whole PR detail fetch (default DefaultDetailTimeout).
	DetailTimeout time.Duration
}

// Client talks to the GitHub REST API. It is safe for concurrent use.
type Client struct {
	token         func() string
	baseURL       string
	http          *http.Client
	strict        *http.Client // copy of http that never follows a redirect (contents, merged PRs, git, actions)
	storage       *http.Client // artifact storage leg: no redirect, no cookie jar
	retry         retry.Options
	timeout       time.Duration
	detailTimeout time.Duration
}

// New builds a Client. It panics if opts.Token is nil (a wiring bug).
func New(opts Options) *Client {
	if opts.Token == nil {
		panic("github.New: Options.Token is required")
	}
	c := &Client{
		token:         opts.Token,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		http:          opts.HTTPClient,
		retry:         retry.Default(),
		timeout:       opts.Timeout,
		detailTimeout: opts.DetailTimeout,
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	c.strict = withoutRedirects(c.http)
	storageBase := opts.ArtifactClient
	if storageBase == nil {
		storageBase = c.http
	}
	c.storage = withoutRedirects(storageBase)
	c.storage.Jar = nil
	if opts.Retry != nil {
		c.retry = *opts.Retry
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.detailTimeout <= 0 {
		c.detailTimeout = DefaultDetailTimeout
	}
	return c
}

// withoutRedirects returns a copy of hc that stops at a 3xx and hands it back as the response, so
// the token header and the query are never resent to wherever a redirect points. The 3xx is then
// reported like any other non-2xx answer. The caller's client is left unchanged.
func withoutRedirects(hc *http.Client) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// noRedirect returns a shallow copy of c whose requests never follow a redirect. Its methods
// share the token function, retry policy and timeouts of c.
func (c *Client) noRedirect() *Client {
	cc := *c
	cc.http = c.strict
	return &cc
}

// currentToken reads the token for one public call.
func (c *Client) currentToken() (string, error) {
	tok := strings.TrimSpace(c.token())
	if tok == "" {
		return "", ErrNoToken
	}
	return tok, nil
}

// repoPath returns /repos/<owner>/<name> with both segments escaped.
func repoPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// getJSON GETs path and decodes the 2xx body, retrying transient failures. Each
// attempt gets its own timeout.
func getJSON[T any](ctx context.Context, c *Client, token, path string, query url.Values) (T, error) {
	return retry.Value(ctx, c.retry, func(ctx context.Context) (T, error) {
		var out T
		err := c.do(ctx, token, http.MethodGet, path, query, nil, &out)
		return out, err
	})
}

// do performs one request attempt bounded by c.timeout and decodes a 2xx JSON
// body into out (when non-nil).
func (c *Client) do(ctx context.Context, token, method, path string, query url.Values, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github %s %s: encode body: %w", method, path, err)
		}
		reader = strings.NewReader(string(raw))
	}
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("github %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dev-digest-go")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github %s %s: %w", method, path, scrub(err, token))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp, token)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("github %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// scrub drops the token from a transport error. Transport errors carry only the
// URL, which has no credentials, so this is a safety net for custom clients.
func scrub(err error, token string) error {
	if token != "" && strings.Contains(err.Error(), token) {
		return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
	}
	return err
}

// statusError builds a StatusError from GitHub's JSON error body.
func statusError(resp *http.Response, token string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	msg := ""
	if json.Unmarshal(raw, &payload) == nil {
		msg = payload.Message
		if details := errorDetails(payload.Errors); details != "" {
			msg += ": " + details
		}
	}
	if strings.TrimSpace(msg) == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[REDACTED]")
	}
	if len(msg) > maxMessageBytes {
		msg = msg[:maxMessageBytes]
	}
	return &StatusError{
		Status:             resp.StatusCode,
		Message:            msg,
		RateLimitRemaining: strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")),
		RateLimitReset:     parseReset(resp.Header.Get("X-RateLimit-Reset")),
	}
}

// parseReset reads x-ratelimit-reset (Unix seconds); an absent or malformed value is the zero time.
func parseReset(v string) time.Time {
	secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || secs <= 0 || secs > maxResetSeconds {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}

// errorDetails flattens GitHub's "errors" array (strings or objects with a
// message) into one line.
func errorDetails(errs []json.RawMessage) string {
	var parts []string
	for _, e := range errs {
		var s string
		if json.Unmarshal(e, &s) == nil && s != "" {
			parts = append(parts, s)
			continue
		}
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e, &obj) == nil && obj.Message != "" {
			parts = append(parts, obj.Message)
		}
	}
	return strings.Join(parts, "; ")
}

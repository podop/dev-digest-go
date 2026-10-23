// Package ghapi is the runner's GitHub client (ci/runner/app.GitHub): the three REST calls of
// spec 7.8.5 and 7.8.9 over net/http. The token travels with every call and never appears in a
// returned error; redirects are not followed (the token would go with them); a response is read
// up to a cap. It is not the server's platform/github client: the runner links no retry, rate
// limit or secrets-store code.
package ghapi

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

	"dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/ci/runner/domain"
)

const (
	// DefaultBaseURL is the GitHub REST API root.
	DefaultBaseURL = "https://api.github.com"

	apiVersion = "2022-11-28"
	userAgent  = "devdigest-agent-runner"
	// requestTimeout is the time budget of one request (spec 7.8.5).
	requestTimeout = 60 * time.Second
	// errorBodyBytes is how much of an error body reaches the job log.
	errorBodyBytes = 1000
	// errorReadBytes is how much of an error body is read: the token is redacted before the cut,
	// and a token straddling the cut would survive as a fragment.
	errorReadBytes = 64 << 10
	// redacted replaces the token in a message.
	redacted = "[REDACTED]"
	// minRedactLen is the shortest token the messages scrub (see app.Run).
	minRedactLen = 8
)

// Options configure a Client.
type Options struct {
	// BaseURL is the API root without a trailing slash; "" = DefaultBaseURL. Tests point it at an
	// httptest server.
	BaseURL string
	// Transport is the round tripper; nil = http.DefaultTransport.
	Transport http.RoundTripper
}

// Client implements app.GitHub.
type Client struct {
	base string
	http *http.Client
}

var _ app.GitHub = (*Client)(nil)

// New returns a Client. Redirects are never followed: a 3xx answer is an error like any non-2xx.
func New(opts Options) *Client {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{base: base, http: &http.Client{
		Transport: opts.Transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// FetchDiff returns the unified diff of the pull request, at most domain.MaxDiffBytes.
func (c *Client) FetchDiff(ctx context.Context, token string, pr app.PullRef) (string, error) {
	u := c.pullURL(pr)
	resp, err := c.do(ctx, token, http.MethodGet, u, "application/vnd.github.v3.diff", nil)
	if err != nil {
		return "", fmt.Errorf("GitHub API error fetching PR diff (%s): %s", u, transportText(err, token))
	}
	defer closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &app.APIError{Status: resp.StatusCode, Message: apiErrorText("fetching PR diff", u, resp, token)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxDiffBytes+1))
	if err != nil {
		return "", fmt.Errorf("GitHub API error fetching PR diff (%s): %s", u, transportText(err, token))
	}
	if len(body) > domain.MaxDiffBytes {
		return "", fmt.Errorf("GitHub API error fetching PR diff (%s): the diff is larger than %d bytes", u, domain.MaxDiffBytes)
	}
	return string(body), nil
}

// reviewBody is the JSON of a pull request review; comments is left out when there are none.
type reviewBody struct {
	Body     string          `json:"body"`
	Event    string          `json:"event"`
	Comments []commentFields `json:"comments,omitempty"`
}

type commentFields struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// PostReview posts a pull request review.
func (c *Client) PostReview(ctx context.Context, token string, pr app.PullRef, post app.ReviewPost) error {
	payload := reviewBody{Body: post.Body, Event: post.Event}
	for _, cm := range post.Comments {
		payload.Comments = append(payload.Comments, commentFields{Path: cm.Path, Line: cm.Line, Body: cm.Body})
	}
	return c.post(ctx, token, c.pullURL(pr)+"/reviews", "posting review", payload)
}

// PostComment posts an issue comment on the pull request.
func (c *Client) PostComment(ctx context.Context, token string, pr app.PullRef, body string) error {
	u := c.base + "/repos/" + url.PathEscape(pr.Owner) + "/" + url.PathEscape(pr.Repo) + "/issues/" + strconv.Itoa(pr.Number) + "/comments"
	return c.post(ctx, token, u, "posting PR comment", struct {
		Body string `json:"body"`
	}{body})
}

func (c *Client) post(ctx context.Context, token, u, what string, payload any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // JSON.stringify does not escape < > &
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("GitHub API error %s (%s): %w", what, u, err)
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	resp, err := c.do(ctx, token, http.MethodPost, u, "application/vnd.github+json", body)
	if err != nil {
		return fmt.Errorf("GitHub API error %s (%s): %s", what, u, transportText(err, token))
	}
	defer closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &app.APIError{Status: resp.StatusCode, Message: apiErrorText(what, u, resp, token)}
	}
	return nil
}

func (c *Client) pullURL(pr app.PullRef) string {
	return c.base + "/repos/" + url.PathEscape(pr.Owner) + "/" + url.PathEscape(pr.Repo) + "/pulls/" + strconv.Itoa(pr.Number)
}

func (c *Client) do(ctx context.Context, token, method, u, accept string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// apiErrorText is "GitHub API error <what> (<url>): <status> <body>" with the body redacted of
// the token, then cut to 1,000 bytes (at a character boundary).
func apiErrorText(what, u string, resp *http.Response, token string) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorReadBytes))
	text := redactToken(strings.ToValidUTF8(string(raw), "�"), token)
	if len(text) > errorBodyBytes {
		cut := errorBodyBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return "GitHub API error " + what + " (" + u + "): " + strconv.Itoa(resp.StatusCode) + " " + text
}

// transportText is the text of a failed request without the request URL (which the caller adds)
// and without the token.
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

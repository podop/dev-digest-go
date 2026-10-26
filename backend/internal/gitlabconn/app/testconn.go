package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/gitlab"
)

// Messages of a connection test (spec FR10). They are fixed text: GitLab's own answer is never
// shown.
const (
	msgRejected     = "GitLab rejected the token (401): it is wrong, expired or revoked"
	msgCannotRead   = "The token cannot read its user (403)"
	msgRedirect     = "GitLab answered with a redirect — check the base URL"
	msgBadToken     = "The token contains characters that cannot be sent to GitLab"
	msgBadBaseURL   = "The base URL cannot be called"
	maxLoginBytes   = 200
	maxVersionBytes = 64
	maxScopes       = 50
	maxScopeBytes   = 64
)

// TestResult is the outcome of a connection test (the settings GitLabConnTestResult without the
// provider). A failed test is data (OK false, a scrubbed Message), not an error; on failure every
// other field is zero (null on the wire). Warnings is never nil on success.
type TestResult struct {
	OK      bool
	Message string
	// LatencyMS is the time of the three calls; set on success only.
	LatencyMS *int
	// Login is the GitLab username of the token's owner.
	Login *string
	// Scopes are the token's scopes; nil when GitLab did not report them (null on the wire).
	Scopes []string
	// ExpiresAt is "YYYY-MM-DD"; nil for a token without expiry or when not reported.
	ExpiresAt *string
	// InstanceVersion is GET /metadata's version, e.g. "17.5.1-ee"; nil when not reported.
	InstanceVersion *string
	// TokenAccess is "full", "read_only" or "" (null).
	TokenAccess domain.TokenAccess
	Warnings    []domain.Warning
}

// TestConnection checks connection id with GET /user, GET /personal_access_tokens/self and GET
// /metadata, in that order, each under gitlab.TestTimeout (spec FR9-FR11). A key, when given, is
// saved as the connection's token first and stays saved whatever the outcome. A malformed or
// unknown id -> 404 gitlab_connection_not_found and nothing is saved; an invalid key -> 422 at
// ["key"]. A connection without a token -> OK false and no request. Only a failure of the store
// or the secrets file, or a canceled ctx, is an error.
func (s *Service) TestConnection(ctx context.Context, id, key string) (TestResult, error) {
	if !validID(id) {
		return TestResult{}, notFound(id)
	}
	if key != "" {
		if msg := domain.CheckToken(key); msg != "" {
			return TestResult{}, validationError([]issue{{path: []any{"key"}, message: msg}})
		}
	}
	c, token, err := s.loadForTest(ctx, id, key)
	if err != nil {
		return TestResult{}, err
	}
	fail := func(msg string) TestResult { return TestResult{Message: scrub(msg, token, key)} }
	if token == "" {
		s.logTest(ctx, c, false, 0)
		return fail("GitLab connection " + c.Name + " has no token"), nil
	}

	conn := gitlab.Conn{BaseURL: c.BaseURL + "/api/v4", Token: token}
	started := s.now()
	latency := func() int { return max(0, int(s.now().Sub(started).Milliseconds())) }

	var user gitlab.User
	if err := bounded(ctx, func(ctx context.Context) (err error) { user, err = s.d.GitLab.User(ctx, conn); return }); err != nil {
		if ctx.Err() != nil {
			return TestResult{}, fmt.Errorf("test gitlab connection %s: %w", id, ctx.Err())
		}
		s.logTest(ctx, c, false, latency())
		return TestResult{Message: scrub(userFailure(err, c.BaseURL), token, key)}, nil
	}

	ev := domain.Evidence{Login: clean(user.Username, maxLoginBytes, token)}
	var info gitlab.TokenInfo
	if err := bounded(ctx, func(ctx context.Context) (err error) { info, err = s.d.GitLab.TokenSelf(ctx, conn); return }); err != nil {
		if ctx.Err() != nil {
			return TestResult{}, fmt.Errorf("test gitlab connection %s: %w", id, ctx.Err())
		}
		s.logCallFailure(ctx, c, "token_self", err)
	} else {
		ev.Scopes = cleanScopes(info.Scopes, token)
		ev.ExpiresAt = parseExpiry(info.ExpiresAt)
	}
	var meta gitlab.Metadata
	if err := bounded(ctx, func(ctx context.Context) (err error) { meta, err = s.d.GitLab.Metadata(ctx, conn); return }); err != nil {
		if ctx.Err() != nil {
			return TestResult{}, fmt.Errorf("test gitlab connection %s: %w", id, ctx.Err())
		}
		s.logCallFailure(ctx, c, "metadata", err)
	} else {
		ev.Version = clean(meta.Version, maxVersionBytes, token)
	}

	took := latency()
	res := domain.Evaluate(ev, s.now())
	if !res.OK {
		s.logTest(ctx, c, false, took)
		return fail(res.Message), nil
	}
	out := TestResult{
		OK:          true,
		Message:     scrub(res.Message, token, key),
		LatencyMS:   &took,
		Login:       &ev.Login,
		TokenAccess: res.TokenAccess,
		Warnings:    make([]domain.Warning, len(res.Warnings)),
	}
	if ev.Scopes != nil {
		out.Scopes = ev.Scopes
	}
	if ev.ExpiresAt != nil {
		d := ev.ExpiresAt.UTC().Format(time.DateOnly)
		out.ExpiresAt = &d
	}
	if ev.Version != "" {
		out.InstanceVersion = &ev.Version
	}
	for i, w := range res.Warnings {
		out.Warnings[i] = domain.Warning{Code: w.Code, Message: scrub(w.Message, token, key)}
	}
	s.logTest(ctx, c, true, took)
	return out, nil
}

// loadForTest returns the connection and its token, saving key first when given. The lookup and
// the save happen under writeMu, so a connection a concurrent delete removed is never given a
// token.
func (s *Service) loadForTest(ctx context.Context, id, key string) (domain.Connection, string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	c, err := s.d.Store.Get(ctx, id)
	if err != nil {
		return domain.Connection{}, "", s.storeError("get gitlab connection", id, "", err)
	}
	if key != "" {
		if _, err := s.d.Secrets.SetGitLab(id, key); err != nil {
			return domain.Connection{}, "", fmt.Errorf("save token of gitlab connection %s: %w", id, err)
		}
		return c, key, nil
	}
	return c, s.d.Secrets.GitLab(id), nil
}

// bounded runs one call of the test under gitlab.TestTimeout.
func bounded(ctx context.Context, call func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, gitlab.TestTimeout)
	defer cancel()
	return call(ctx)
}

// userFailure is the message of a failed GET /user (spec FR10); baseURL is the connection's.
func userFailure(err error, baseURL string) string {
	var se *gitlab.StatusError
	switch {
	case egress.IsForbidden(err):
		return egress.ForbiddenMessage
	case errors.Is(err, gitlab.ErrRedirect):
		return msgRedirect
	case errors.Is(err, gitlab.ErrNotAPI):
		return "No GitLab API found at " + baseURL
	case errors.Is(err, gitlab.ErrToken):
		return msgBadToken
	case errors.Is(err, gitlab.ErrBaseURL):
		return msgBadBaseURL
	case errors.As(err, &se):
		switch {
		case se.Status == http.StatusUnauthorized:
			return msgRejected
		case se.Status == http.StatusForbidden:
			return msgCannotRead
		case se.Status == http.StatusNotFound || se.Status == http.StatusMethodNotAllowed:
			// A server that is not GitLab (or a wrong path prefix) has no /user.
			return "No GitLab API found at " + baseURL
		case se.Status >= 300 && se.Status < 400:
			return msgRedirect
		}
	}
	// A timeout, a 5xx, a connection or TLS error, any other status.
	return "GitLab is not reachable at " + baseURL
}

// logTest writes the one line a test leaves: the connection id and host, never a token or a path.
func (s *Service) logTest(ctx context.Context, c domain.Connection, ok bool, latencyMS int) {
	s.log.InfoContext(ctx, "gitlab connection test", "connection_id", c.ID, "host", c.Host,
		"ok", ok, "latency_ms", latencyMS)
}

// logCallFailure logs why an optional call (scopes, version) failed: the class only, GitLab's
// text could carry a secret.
func (s *Service) logCallFailure(ctx context.Context, c domain.Connection, op string, err error) {
	reason := "error"
	var se *gitlab.StatusError
	switch {
	case egress.IsForbidden(err):
		reason = "forbidden_address"
	case errors.As(err, &se):
		reason = fmt.Sprintf("http_%d", se.Status)
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	}
	s.log.WarnContext(ctx, "gitlab connection test: optional call failed", "connection_id", c.ID,
		"host", c.Host, "op", op, "reason", reason)
}

// parseExpiry reads the expires_at of GET /personal_access_tokens/self: a date, or a timestamp.
// "" or anything unreadable is nil (no expiry known).
func parseExpiry(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

// clean makes a string GitLab sent fit for a response and a log: control characters dropped, the
// secrets masked, cut to n bytes.
func clean(s string, n int, secrets ...string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	return truncate(scrub(s, secrets...), n)
}

// cleanScopes bounds the scope list GitLab reported; nil stays nil (not reported).
func cleanScopes(scopes []string, secrets ...string) []string {
	if scopes == nil {
		return nil
	}
	out := make([]string, 0, min(len(scopes), maxScopes))
	for _, sc := range scopes {
		if len(out) == maxScopes {
			break
		}
		if sc = clean(sc, maxScopeBytes, secrets...); sc != "" {
			out = append(out, sc)
		}
	}
	return out
}

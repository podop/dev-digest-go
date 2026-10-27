// Package gitlab implements the GitLab ports of the ci context (spec C FR1, FR5, FR11-FR13,
// FR32-FR34) over the strict client of platform/gitlab: connection lookups, the calls of a
// gitlab_ci export (project, branch, file hash, commit, merge request) and the calls of the
// refresh (pipelines, jobs, merge request source project, result artifact).
//
// The binding (connection and token) is read from a gitlab.ResolveFunc on every call, so a rotated
// token applies to the next request and no token is ever held here. No error, log line or text of
// this package carries a token: the platform client scrubs what it returns and nothing here adds
// request details.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dev-digest-go/backend/internal/ci/app"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

var (
	_ app.GitLabConns       = (*ExportConns)(nil)
	_ app.GitLabIngestConns = (*IngestConns)(nil)
)

// hasToken reports whether b carries a token that can be sent.
func hasToken(b glclient.Binding) bool { return strings.TrimSpace(b.Token) != "" }

// ExportConns implements app.GitLabConns: the export asks whether a host has a connection and a
// token, and learns nothing else (no address, no token).
type ExportConns struct{ resolve glclient.ResolveFunc }

// NewExportConns builds the lookup over resolve; it panics on a nil resolve (a wiring bug).
func NewExportConns(resolve glclient.ResolveFunc) *ExportConns {
	if resolve == nil {
		panic("ci/adapters/gitlab: NewExportConns needs a ResolveFunc")
	}
	return &ExportConns{resolve: resolve}
}

// Resolve returns the name and token presence of the connection whose host key is host; false when
// the host has none.
func (c *ExportConns) Resolve(ctx context.Context, host string) (app.GitLabConn, bool, error) {
	b, ok, err := c.resolve(ctx, host)
	if err != nil {
		return app.GitLabConn{}, false, fmt.Errorf("resolve gitlab connection: %w", err)
	}
	if !ok {
		return app.GitLabConn{}, false, nil
	}
	return app.GitLabConn{Name: b.Name, HasToken: hasToken(b)}, true, nil
}

// IngestConns implements app.GitLabIngestConns: the refresh asks for the instance URL of a host's
// connection (the job web address of a stored run is built from it); the token stays behind the CI
// adapter.
type IngestConns struct{ resolve glclient.ResolveFunc }

// NewIngestConns builds the lookup over resolve; it panics on a nil resolve (a wiring bug).
func NewIngestConns(resolve glclient.ResolveFunc) *IngestConns {
	if resolve == nil {
		panic("ci/adapters/gitlab: NewIngestConns needs a ResolveFunc")
	}
	return &IngestConns{resolve: resolve}
}

// Resolve returns the base URL of the connection whose host key is host:
// app.ErrGitLabHostNotConnected when there is none, app.ErrGitLabTokenMissing when it has no token.
func (c *IngestConns) Resolve(ctx context.Context, host string) (app.GitLabIngestConn, error) {
	b, ok, err := c.resolve(ctx, host)
	switch {
	case err != nil:
		return app.GitLabIngestConn{}, fmt.Errorf("resolve gitlab connection: %w", err)
	case !ok:
		return app.GitLabIngestConn{}, app.ErrGitLabHostNotConnected
	case !hasToken(b):
		return app.GitLabIngestConn{}, app.ErrGitLabTokenMissing
	}
	return app.GitLabIngestConn{BaseURL: b.BaseURL}, nil
}

// bind reads the binding of host for one call. A connection or token that vanished since the
// caller's check is a *app.GitLabError with Status 401 and no request is made (the same answer as
// GitLab's for a revoked token); a failure to read the connections is a plain failure before any
// response. step names the call for the error.
func bind(ctx context.Context, resolve glclient.ResolveFunc, host, step string) (glclient.Binding, error) {
	b, ok, err := resolve(ctx, host)
	switch {
	case err != nil:
		return glclient.Binding{}, fail(ctx, step, fmt.Errorf("resolve gitlab connection: %w", err), "")
	case !ok || !hasToken(b):
		return glclient.Binding{}, &app.GitLabError{Status: 401, Step: step, Cause: errConnectionGone}
	}
	return b, nil
}

// errConnectionGone is the cause of the 401 answered without a request.
var errConnectionGone = errors.New("gitlab connection or token no longer available")

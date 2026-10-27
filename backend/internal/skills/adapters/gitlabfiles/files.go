// Package gitlabfiles reads skill import files through a GitLab connection (spec B FR30): it finds
// the connection by host key and fetches the file from the connection's REST root with its token.
// The token stays inside this package; the importer sees only the connection's name and address.
package gitlabfiles

import (
	"context"
	"errors"
	"fmt"

	"dev-digest-go/backend/internal/platform/egress"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/skills/app"
)

// api is the part of *glclient.Client the adapter calls.
type api interface {
	RawFile(ctx context.Context, conn glclient.Conn, project, file, ref string, limit int) ([]byte, error)
}

// Adapter implements the importer's GitLab file port.
type Adapter struct {
	api     api
	resolve glclient.ResolveFunc
}

// New builds an Adapter over the platform client and the connection lookup by exact host key. The
// lookup is read on every call, so a rotated token applies to the next request.
func New(c *glclient.Client, resolve glclient.ResolveFunc) *Adapter {
	return &Adapter{api: c, resolve: resolve}
}

// Match returns the connection whose host key is hostKey.
func (a *Adapter) Match(ctx context.Context, hostKey string) (app.GitLabFileConn, bool, error) {
	b, ok, err := a.resolve(ctx, hostKey)
	if err != nil {
		return app.GitLabFileConn{}, false, fmt.Errorf("resolve connection %s: %w", hostKey, err)
	}
	if !ok {
		return app.GitLabFileConn{}, false, nil
	}
	return app.GitLabFileConn{ID: b.ID, Name: b.Name, BaseURL: b.BaseURL, Host: b.Host}, true, nil
}

// Raw reads the file with the token of conn as it is stored now. Failures are mapped to the
// app.ErrGitLabFile* sentinels; the token is never part of an error.
func (a *Adapter) Raw(ctx context.Context, conn app.GitLabFileConn, project, file, ref string, limit int) ([]byte, error) {
	b, ok, err := a.resolve(ctx, conn.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve connection %s: %w", conn.Host, err)
	}
	if !ok || b.ID != conn.ID {
		return nil, app.ErrGitLabFileConnectionGone
	}
	data, err := a.api.RawFile(ctx, b.Conn(), project, file, ref, limit)
	if err != nil {
		return nil, mapError(err)
	}
	return data, nil
}

func mapError(err error) error {
	var se *glclient.StatusError
	switch {
	case errors.Is(err, glclient.ErrTooLarge):
		return app.ErrGitLabFileTooLarge
	case errors.Is(err, glclient.ErrRedirect):
		return app.ErrGitLabFileMoved
	case errors.As(err, &se):
		switch {
		case se.IsNotFound():
			return app.ErrGitLabFileNotFound
		case se.IsUnauthorized(), se.IsForbidden():
			return app.ErrGitLabFileRejected
		case se.IsRedirect():
			return app.ErrGitLabFileMoved
		case se.IsRateLimited():
			return app.ErrGitLabFileRateLimited
		}
		return &app.GitLabFileStatusError{Status: se.Status}
	case egress.IsForbidden(err):
		return app.ErrGitLabFileAddress
	}
	return err
}

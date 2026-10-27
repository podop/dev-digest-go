package peers

import (
	"context"
	"strings"

	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/gitlab"
)

// GitLabReads is the part of *gitlab.Client the GitLab reads of the ticket and doc readers use
// (spec B FR27, FR29): one issue by project id and one raw file at a ref. The composition root
// passes the *gitlab.Client together with a gitlab.ResolveFunc over the connections.
type GitLabReads interface {
	Issue(ctx context.Context, conn gitlab.Conn, projectID int64, iid int) (gitlab.Issue, error)
	RawFile(ctx context.Context, conn gitlab.Conn, project, file, ref string, limit int) ([]byte, error)
}

// gitLabSide is a reader's way to GitLab: the client and the lookup of the connection a repository
// belongs to. The zero value is "GitLab is not wired": every repository reads as not connected.
type gitLabSide struct {
	reads   GitLabReads
	resolve gitlab.ResolveFunc
}

// connState says why a GitLab read is not made.
type connState int

const (
	connReady connState = iota
	// connNone: no connection has the repository's host key (or GitLab is not wired).
	connNone
	// connNoToken: the connection exists but holds no token.
	connNoToken
	// connUnreadable: the connections could not be read.
	connUnreadable
)

// connection finds the connection of the repository by its host key, which is the only thing that
// selects it: a URL in a pull request text never does (FR23). A repository without a project id
// cannot be read either.
func (g gitLabSide) connection(ctx context.Context, repo domain.Repo) (gitlab.Binding, connState) {
	if g.reads == nil || g.resolve == nil || repo.Host == "" || repo.ProjectID <= 0 {
		return gitlab.Binding{}, connNone
	}
	b, ok, err := g.resolve(ctx, repo.Host)
	if err != nil {
		return gitlab.Binding{}, connUnreadable
	}
	if !ok {
		return gitlab.Binding{}, connNone
	}
	if strings.TrimSpace(b.Token) == "" {
		return b, connNoToken
	}
	return b, connReady
}

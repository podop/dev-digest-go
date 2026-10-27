package peers

import (
	"context"
	"fmt"
	"sync"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

// MaxDiffsInFlight bounds the concurrent diffs requests of one history read (spec B FR24).
const MaxDiffsInFlight = 5

// GitLabClient is the part of *platform/gitlab.Client the adapter uses.
type GitLabClient interface {
	MergedMergeRequests(ctx context.Context, conn glclient.Conn, projectID int64, perPage int) ([]glclient.MergedMergeRequest, error)
	DiffPaths(ctx context.Context, conn glclient.Conn, projectID int64, iid int) ([]string, error)
}

// GitLab implements app.GitLabHistory over the platform GitLab client and the connection of the
// repository's host.
type GitLab struct {
	c       GitLabClient
	resolve glclient.ResolveFunc
}

var _ app.GitLabHistory = (*GitLab)(nil)

// NewGitLab wraps the platform client. resolve finds the connection (with its token) by host key
// on every call, so a rotated token applies to the next read.
func NewGitLab(c GitLabClient, resolve glclient.ResolveFunc) *GitLab {
	return &GitLab{c: c, resolve: resolve}
}

// ListMerged reads one page of merged merge requests of the project and then the first page of
// diffs of each, at most MaxDiffsInFlight at a time, in the order GitLab listed them. A host
// without a connection or a connection without a token makes no request and answers
// app.ErrGitLabNotConnected. The first failure cancels the remaining requests and fails the read.
func (a *GitLab) ListMerged(ctx context.Context, host string, projectID int64, limit int) ([]domain.MergedPR, error) {
	b, ok, err := a.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve gitlab connection: %w", err)
	}
	if !ok || b.Token == "" {
		return nil, app.ErrGitLabNotConnected
	}
	conn := b.Conn()
	mrs, err := a.c.MergedMergeRequests(ctx, conn, projectID, limit)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, MaxDiffsInFlight)
		out      = make([]domain.MergedPR, len(mrs))
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	for i, m := range mrs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			files, err := a.c.DiffPaths(ctx, conn, projectID, m.IID)
			if err != nil {
				fail(err)
				return
			}
			out[i] = domain.MergedPR{Number: m.IID, Title: m.Title, Author: m.Author, MergedAt: m.MergedAt, Files: files}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

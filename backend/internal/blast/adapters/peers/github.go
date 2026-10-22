package peers

import (
	"context"
	"errors"

	"dev-digest-go/backend/internal/blast/app"
	"dev-digest-go/backend/internal/blast/domain"
	ghclient "dev-digest-go/backend/internal/platform/github"
)

// GitHubClient is the part of *platform/github.Client the adapter uses.
type GitHubClient interface {
	ListMergedPulls(ctx context.Context, owner, name string, limit int) ([]ghclient.MergedPull, error)
}

// GitHub implements app.GitHub over the platform GitHub client.
type GitHub struct{ c GitHubClient }

var _ app.GitHub = (*GitHub)(nil)

// NewGitHub wraps the platform client.
func NewGitHub(c GitHubClient) *GitHub { return &GitHub{c: c} }

// ListMergedPulls returns the repository's recent merged pull requests with their files, in the
// order GitHub listed them.
func (a *GitHub) ListMergedPulls(ctx context.Context, owner, name string, limit int) ([]domain.MergedPR, error) {
	pulls, err := a.c.ListMergedPulls(ctx, owner, name, limit)
	if err != nil {
		if errors.Is(err, ghclient.ErrNoToken) {
			return nil, noTokenError{}
		}
		return nil, err
	}
	out := make([]domain.MergedPR, len(pulls))
	for i, p := range pulls {
		out[i] = domain.MergedPR{Number: p.Number, Title: p.Title, Author: p.Author, MergedAt: p.MergedAt, Files: p.Files}
	}
	return out, nil
}

// noTokenError is the error for a missing GitHub token as the user knows the setting
// ("GITHUB_TOKEN is not configured"); errors.Is still finds the client's ErrNoToken.
type noTokenError struct{}

func (noTokenError) Error() string { return "GITHUB_TOKEN is not configured" }

func (noTokenError) Unwrap() error { return ghclient.ErrNoToken }

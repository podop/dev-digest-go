// Package peers adapts other parts of the backend to the ports of the ci use cases. github.go
// implements app.GitHub over the strict client of platform/github.
package peers

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	ghclient "dev-digest-go/backend/internal/platform/github"
	"dev-digest-go/backend/internal/platform/retry"
)

// GitHubClient is the part of *platform/github.Client the adapter uses.
type GitHubClient interface {
	GetRef(ctx context.Context, owner, name, branch string) (sha string, found bool, err error)
	GetCommit(ctx context.Context, owner, name, sha string) (ghclient.CommitInfo, error)
	CreateBlob(ctx context.Context, owner, name, contentBase64 string) (string, error)
	CreateTree(ctx context.Context, owner, name, baseTree string, entries []ghclient.TreeEntry) (string, error)
	CreateCommit(ctx context.Context, owner, name, message, tree string, parents []string) (string, error)
	UpdateRef(ctx context.Context, owner, name, branch, sha string) error
	CreateRef(ctx context.Context, owner, name, branch, sha string) error
	FindOpenPull(ctx context.Context, owner, name, branch string) (pr ghclient.PullRef, found bool, err error)
	CreatePull(ctx context.Context, owner, name string, in ghclient.NewPull) (ghclient.PullRef, error)
	ListWorkflowRuns(ctx context.Context, owner, name, workflowFile string) ([]ghclient.WorkflowRun, error)
	ListRunArtifacts(ctx context.Context, owner, name string, runID int64) ([]ghclient.RunArtifact, error)
	DownloadArtifact(ctx context.Context, owner, name string, artifactID, maxBytes int64) ([]byte, error)
}

// GitHub implements app.GitHub.
type GitHub struct {
	c     GitHubClient
	token func() string
	retry retry.Options
}

var (
	_ app.GitHub   = (*GitHub)(nil)
	_ GitHubClient = (*ghclient.Client)(nil)
)

// NewGitHub wraps the platform client. token is the same function the client reads per call; it
// only answers Configured.
func NewGitHub(c GitHubClient, token func() string) *GitHub {
	return &GitHub{
		c:     c,
		token: token,
		// One re-run of a whole publish on a transient error (spec 7.9).
		retry: retry.Options{Retries: 1, BaseDelay: retry.DefaultBaseDelay, MaxDelay: retry.DefaultMaxDelay},
	}
}

// Configured reports whether a GitHub token is set.
func (a *GitHub) Configured() bool { return strings.TrimSpace(a.token()) != "" }

// CommitFiles publishes in.Files as one commit on in.Branch (spec 7.6.7). A transient failure
// (HTTP 429/5xx, connection reset) re-runs the whole sequence once; the single calls inside are
// not retried.
func (a *GitHub) CommitFiles(ctx context.Context, repo app.RepoRef, in app.CommitFilesInput) error {
	err := retry.Do(ctx, a.retry, func(ctx context.Context) error { return a.commitOnce(ctx, repo, in) })
	return mapError(err)
}

func (a *GitHub) commitOnce(ctx context.Context, repo app.RepoRef, in app.CommitFilesInput) error {
	o, n := repo.Owner, repo.Name
	parent, branchExists, err := a.c.GetRef(ctx, o, n, in.Branch)
	if err != nil {
		return err
	}
	if !branchExists {
		var found bool
		if parent, found, err = a.c.GetRef(ctx, o, n, in.Base); err != nil {
			return err
		}
		if !found {
			return &ghclient.StatusError{Status: 404, Message: "base branch not found"}
		}
	}
	parentCommit, err := a.c.GetCommit(ctx, o, n, parent)
	if err != nil {
		return err
	}
	entries := make([]ghclient.TreeEntry, 0, len(in.Files))
	for _, f := range in.Files {
		if f.AsBlob {
			// P21: the blob holds the base64 of the file's text (itself already gzip+base64), so
			// the repository file is that text byte for byte.
			sha, err := a.c.CreateBlob(ctx, o, n, base64.StdEncoding.EncodeToString([]byte(f.Contents)))
			if err != nil {
				return err
			}
			entries = append(entries, ghclient.TreeEntry{Path: f.Path, SHA: sha})
			continue
		}
		contents := f.Contents
		entries = append(entries, ghclient.TreeEntry{Path: f.Path, Content: &contents})
	}
	tree, err := a.c.CreateTree(ctx, o, n, parentCommit.TreeSHA, entries)
	if err != nil {
		return err
	}
	commit, err := a.c.CreateCommit(ctx, o, n, in.Message, tree, []string{parent})
	if err != nil {
		return err
	}
	if branchExists {
		return a.c.UpdateRef(ctx, o, n, in.Branch, commit)
	}
	return a.c.CreateRef(ctx, o, n, in.Branch, commit)
}

// FindOpenPR returns the URL of the first open pull request of branch.
func (a *GitHub) FindOpenPR(ctx context.Context, repo app.RepoRef, branch string) (string, bool, error) {
	pr, found, err := a.c.FindOpenPull(ctx, repo.Owner, repo.Name, branch)
	if err != nil {
		return "", false, mapError(err)
	}
	return pr.HTMLURL, found, nil
}

// OpenPullRequest creates a pull request and returns its html_url.
func (a *GitHub) OpenPullRequest(ctx context.Context, repo app.RepoRef, in app.OpenPullRequestInput) (string, error) {
	pr, err := a.c.CreatePull(ctx, repo.Owner, repo.Name, ghclient.NewPull{Title: in.Title, Head: in.Head, Base: in.Base, Body: in.Body})
	if err != nil {
		return "", mapError(err)
	}
	return pr.HTMLURL, nil
}

// ListWorkflowRuns lists the completed pull_request runs of a workflow file. The client asks for
// one page of domain.RunsPerInstallation; perPage can only shorten the answer.
func (a *GitHub) ListWorkflowRuns(ctx context.Context, repo app.RepoRef, workflowFile string, perPage int) ([]domain.WorkflowRun, error) {
	runs, err := a.c.ListWorkflowRuns(ctx, repo.Owner, repo.Name, workflowFile)
	if err != nil {
		return nil, mapError(err)
	}
	if perPage > 0 && len(runs) > perPage {
		runs = runs[:perPage]
	}
	out := make([]domain.WorkflowRun, len(runs))
	for i, r := range runs {
		w := domain.WorkflowRun{
			ID:           r.ID,
			RunAttempt:   r.RunAttempt,
			HeadSHA:      r.HeadSHA,
			Path:         r.Path,
			Event:        r.Event,
			HTMLURL:      r.HTMLURL,
			RepositoryID: r.RepositoryID,
			PRNumbers:    r.PullNumbers,
			CreatedAt:    timeText(r.CreatedAt),
		}
		if r.Conclusion != "" {
			c := r.Conclusion
			w.Conclusion = &c
		}
		out[i] = w
	}
	return out, nil
}

// ListRunArtifacts returns the first page of a run's artifacts.
func (a *GitHub) ListRunArtifacts(ctx context.Context, repo app.RepoRef, runID int64) ([]domain.RunArtifact, error) {
	list, err := a.c.ListRunArtifacts(ctx, repo.Owner, repo.Name, runID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]domain.RunArtifact, len(list))
	for i, x := range list {
		out[i] = domain.RunArtifact{ID: x.ID, Name: x.Name, SizeInBytes: x.SizeBytes, Expired: x.Expired, CreatedAt: timeText(x.CreatedAt)}
	}
	return out, nil
}

// DownloadArtifact returns the ZIP of an artifact; app.ErrArtifactTooLarge when it is longer than
// maxBytes.
func (a *GitHub) DownloadArtifact(ctx context.Context, repo app.RepoRef, artifactID int64, maxBytes int64) ([]byte, error) {
	body, err := a.c.DownloadArtifact(ctx, repo.Owner, repo.Name, artifactID, maxBytes)
	if err != nil {
		if errors.Is(err, ghclient.ErrArtifactTooLarge) {
			return nil, app.ErrArtifactTooLarge
		}
		return nil, mapError(err)
	}
	return body, nil
}

// timeText renders a parsed GitHub timestamp the way the ingest parses it back (RFC 3339); a zero
// time (GitHub sent none, or garbage) is "", which the ingest treats as unparsable.
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// mapError turns a platform failure into the port's *app.GitHubError: the status and the
// rate-limit flag of a non-2xx answer, and nothing of its message (GitHub's text). Every other
// failure (dial, TLS, timeout, cancellation, a refused redirect, a missing token, an unreadable
// answer) is a Transport error that keeps the already scrubbed cause for errors.Is. A nil error
// stays nil.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var se *ghclient.StatusError
	if errors.As(err, &se) {
		return &app.GitHubError{Status: se.Status, RateLimited: se.RateLimited()}
	}
	return &app.GitHubError{Transport: true, Cause: err}
}

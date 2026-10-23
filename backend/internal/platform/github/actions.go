package github

import (
	"context"
	"time"
)

// WorkflowRun is a completed workflow run as GitHub lists it.
type WorkflowRun struct {
	ID           int64
	RunAttempt   int // 1 when GitHub sends none
	HeadSHA      string
	Path         string
	Event        string
	Conclusion   string // "" when null
	HTMLURL      string
	RepositoryID int64
	PullNumbers  []int
	CreatedAt    time.Time
}

// RunArtifact is one artifact of a workflow run.
type RunArtifact struct {
	ID        int64
	Name      string
	SizeBytes int64
	Expired   bool
	CreatedAt time.Time
}

// ListWorkflowRuns lists the completed pull_request runs of workflow file (first page of 30). A
// missing workflow is a *StatusError with status 404.
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, name, workflowFile string) ([]WorkflowRun, error) {
	return nil, ErrNotImplemented
}

// ListRunArtifacts lists the first page (100) of a run's artifacts.
func (c *Client) ListRunArtifacts(ctx context.Context, owner, name string, runID int64) ([]RunArtifact, error) {
	return nil, ErrNotImplemented
}

// DownloadArtifact downloads an artifact zip of at most maxBytes. The first leg carries the token
// and follows no redirect on its own; an https redirect target is fetched once without any
// credential. A longer body is an error.
func (c *Client) DownloadArtifact(ctx context.Context, owner, name string, artifactID, maxBytes int64) ([]byte, error) {
	return nil, ErrNotImplemented
}

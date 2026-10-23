package github

import (
	"context"
	"net/url"
	"strconv"
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
	CreatedAt    time.Time // zero when GitHub sent none or an unparsable value
}

// RunArtifact is one artifact of a workflow run.
type RunArtifact struct {
	ID        int64
	Name      string
	SizeBytes int64
	Expired   bool
	CreatedAt time.Time // zero when GitHub sent none or an unparsable value
}

type apiRuns struct {
	WorkflowRuns []struct {
		ID         int64   `json:"id"`
		RunAttempt int     `json:"run_attempt"`
		HeadSHA    string  `json:"head_sha"`
		Path       string  `json:"path"`
		Event      string  `json:"event"`
		Conclusion *string `json:"conclusion"`
		HTMLURL    string  `json:"html_url"`
		CreatedAt  string  `json:"created_at"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"workflow_runs"`
}

type apiArtifacts struct {
	Artifacts []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size_in_bytes"`
		Expired   bool   `json:"expired"`
		CreatedAt string `json:"created_at"`
	} `json:"artifacts"`
}

// ListWorkflowRuns lists the completed pull_request runs of workflow file (first page of 30). A
// missing workflow is a *StatusError with status 404.
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, name, workflowFile string) ([]WorkflowRun, error) {
	token, err := c.currentToken()
	if err != nil {
		return nil, err
	}
	q := url.Values{"event": {"pull_request"}, "status": {"completed"}, "per_page": {"30"}}
	path := repoPath(owner, name) + "/actions/workflows/" + url.PathEscape(workflowFile) + "/runs"
	raw, err := getJSON[apiRuns](ctx, c.noRedirect(), token, path, q)
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowRun, 0, len(raw.WorkflowRuns))
	for _, r := range raw.WorkflowRuns {
		run := WorkflowRun{
			ID:           r.ID,
			RunAttempt:   r.RunAttempt,
			HeadSHA:      r.HeadSHA,
			Path:         r.Path,
			Event:        r.Event,
			HTMLURL:      r.HTMLURL,
			RepositoryID: r.Repository.ID,
			CreatedAt:    parseTime(r.CreatedAt),
		}
		if run.RunAttempt <= 0 {
			run.RunAttempt = 1
		}
		if r.Conclusion != nil {
			run.Conclusion = *r.Conclusion
		}
		for _, p := range r.PullRequests {
			run.PullNumbers = append(run.PullNumbers, p.Number)
		}
		out = append(out, run)
	}
	return out, nil
}

// ListRunArtifacts lists the first page (100) of a run's artifacts.
func (c *Client) ListRunArtifacts(ctx context.Context, owner, name string, runID int64) ([]RunArtifact, error) {
	token, err := c.currentToken()
	if err != nil {
		return nil, err
	}
	path := repoPath(owner, name) + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/artifacts"
	raw, err := getJSON[apiArtifacts](ctx, c.noRedirect(), token, path, url.Values{"per_page": {"100"}})
	if err != nil {
		return nil, err
	}
	out := make([]RunArtifact, 0, len(raw.Artifacts))
	for _, a := range raw.Artifacts {
		out = append(out, RunArtifact{ID: a.ID, Name: a.Name, SizeBytes: a.Size, Expired: a.Expired, CreatedAt: parseTime(a.CreatedAt)})
	}
	return out, nil
}

// parseTime reads an RFC 3339 timestamp; the zero time when it is empty or unparsable.
func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

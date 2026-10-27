package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// maxJobsPage is the per_page of the pipeline job list (spec C FR32: the first page of 100).
const maxJobsPage = MaxPerPage

// Pipeline is a pipeline as the project list reports it.
type Pipeline struct {
	ID        int64
	ProjectID int64
	// Ref is the pipeline's ref, e.g. refs/merge-requests/7/head for a merge request pipeline.
	Ref    string
	SHA    string
	Source string
	Status string
	// CreatedAt is the zero time when GitLab sent none.
	CreatedAt time.Time
}

// Pipelines calls GET /projects/:id/pipelines?source=<source>&order_by=id&sort=desc&per_page=<n>&
// page=<page> (spec C FR32). source may be "" for any source. A 404 is a *StatusError.
func (c *Client) Pipelines(ctx context.Context, conn Conn, projectID int64, source string, page, perPage int) ([]Pipeline, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return nil, err
	}
	extra := url.Values{"order_by": {"id"}, "sort": {"desc"}}
	if source != "" {
		if !validToken(source) {
			return nil, fmt.Errorf("gitlab pipelines: invalid source")
		}
		extra.Set("source", source)
	}
	q, err := pageQuery(page, perPage, extra)
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]struct {
		ID        int64     `json:"id"`
		ProjectID int64     `json:"project_id"`
		Ref       string    `json:"ref"`
		SHA       string    `json:"sha"`
		Source    string    `json:"source"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
	}](ctx, c, conn, "pipelines", base+"/pipelines", q)
	if err != nil {
		return nil, err
	}
	out := make([]Pipeline, len(raw))
	for i, p := range raw {
		if p.ID <= 0 {
			return nil, fmt.Errorf("gitlab pipelines: %w", ErrNotAPI)
		}
		out[i] = Pipeline{ID: p.ID, ProjectID: p.ProjectID, Ref: p.Ref, SHA: p.SHA, Source: p.Source, Status: p.Status, CreatedAt: p.CreatedAt}
	}
	return out, nil
}

// ArtifactsFile is the job's artifacts archive as the job list reports it.
type ArtifactsFile struct {
	Filename string
	Size     int64
}

// Job is a pipeline job as the job list reports it.
type Job struct {
	ID     int64
	Name   string
	Status string
	// CreatedAt is the zero time when GitLab sent none.
	CreatedAt time.Time
	// ArtifactsFile is nil when the job has no artifacts archive.
	ArtifactsFile *ArtifactsFile
	// ArtifactsExpireAt is nil when the artifacts never expire or the job has none.
	ArtifactsExpireAt *time.Time
}

// PipelineJobs calls GET /projects/:id/pipelines/:pipeline_id/jobs?scope[]=<scope>…&per_page=100:
// the first page only (spec C FR32). scopes are job statuses such as "success" and "failed"; none
// lists every job.
func (c *Client) PipelineJobs(ctx context.Context, conn Conn, projectID, pipelineID int64, scopes ...string) ([]Job, error) {
	base, err := projectPath(projectID)
	if err != nil {
		return nil, err
	}
	if pipelineID <= 0 {
		return nil, fmt.Errorf("gitlab: pipeline id %d out of range", pipelineID)
	}
	extra := url.Values{}
	for _, s := range scopes {
		if !validToken(s) {
			return nil, fmt.Errorf("gitlab pipeline_jobs: invalid scope")
		}
		extra.Add("scope[]", s)
	}
	q, err := pageQuery(1, maxJobsPage, extra)
	if err != nil {
		return nil, err
	}
	raw, err := getJSON[[]struct {
		ID            int64     `json:"id"`
		Name          string    `json:"name"`
		Status        string    `json:"status"`
		CreatedAt     time.Time `json:"created_at"`
		ArtifactsFile *struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		} `json:"artifacts_file"`
		ArtifactsExpireAt *time.Time `json:"artifacts_expire_at"`
	}](ctx, c, conn, "pipeline_jobs", base+"/pipelines/"+strconv.FormatInt(pipelineID, 10)+"/jobs", q)
	if err != nil {
		return nil, err
	}
	out := make([]Job, len(raw))
	for i, j := range raw {
		if j.ID <= 0 {
			return nil, fmt.Errorf("gitlab pipeline_jobs: %w", ErrNotAPI)
		}
		out[i] = Job{ID: j.ID, Name: j.Name, Status: j.Status, CreatedAt: j.CreatedAt, ArtifactsExpireAt: j.ArtifactsExpireAt}
		if j.ArtifactsFile != nil {
			out[i].ArtifactsFile = &ArtifactsFile{Filename: j.ArtifactsFile.Filename, Size: j.ArtifactsFile.Size}
		}
	}
	return out, nil
}

// validToken accepts a short lower-case identifier ([a-z_]); it keeps a query value to the
// fixed vocabulary of sources and job statuses.
func validToken(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && c != '_' {
			return false
		}
	}
	return true
}

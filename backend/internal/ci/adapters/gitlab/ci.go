package gitlab

import (
	"context"
	"errors"
	"net/http"
	"time"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

var _ app.GitLabCI = (*CI)(nil)

// CI implements app.GitLabCI, the reads of the refresh, over the platform client. Every method
// reads the binding of the ref's host first; its errors carry no Step.
type CI struct {
	client  *glclient.Client
	resolve glclient.ResolveFunc
	storage *http.Client
}

// NewCI builds the reader. storage is the client of the artifact object storage hop (the egress
// client without proxy and with the dial guard); the platform client sends it no credential and
// follows no redirect with it. nil means the platform client's own client. It panics on a nil
// client or resolve (a wiring bug).
func NewCI(client *glclient.Client, resolve glclient.ResolveFunc, storage *http.Client) *CI {
	if client == nil || resolve == nil {
		panic("ci/adapters/gitlab: NewCI needs a client and a ResolveFunc")
	}
	return &CI{client: client, resolve: resolve, storage: storage}
}

// Pipelines lists the project's pipelines of one source, newest id first.
func (c *CI) Pipelines(ctx context.Context, ref app.GitLabRef, source string, page, perPage int) ([]domain.GitLabPipeline, error) {
	b, err := bind(ctx, c.resolve, ref.Host, "")
	if err != nil {
		return nil, err
	}
	list, err := c.client.Pipelines(ctx, b.Conn(), ref.ProjectID, source, page, perPage)
	if err != nil {
		return nil, fail(ctx, "", err, b.Token)
	}
	out := make([]domain.GitLabPipeline, len(list))
	for i, p := range list {
		out[i] = domain.GitLabPipeline{ID: p.ID, ProjectID: p.ProjectID, SHA: p.SHA, Ref: p.Ref, Source: p.Source}
	}
	return out, nil
}

// PipelineJobs lists the first page of a pipeline's jobs whose status is one of scopes. Times are
// mapped to RFC 3339 text, "" when GitLab sent none.
func (c *CI) PipelineJobs(ctx context.Context, ref app.GitLabRef, pipelineID int64, scopes ...string) ([]domain.GitLabJob, error) {
	b, err := bind(ctx, c.resolve, ref.Host, "")
	if err != nil {
		return nil, err
	}
	list, err := c.client.PipelineJobs(ctx, b.Conn(), ref.ProjectID, pipelineID, scopes...)
	if err != nil {
		return nil, fail(ctx, "", err, b.Token)
	}
	out := make([]domain.GitLabJob, len(list))
	for i, j := range list {
		job := domain.GitLabJob{ID: j.ID, Name: j.Name, Status: j.Status, CreatedAt: timeText(j.CreatedAt)}
		if j.ArtifactsExpireAt != nil {
			job.ArtifactsExpireAt = timeText(*j.ArtifactsExpireAt)
		}
		if j.ArtifactsFile != nil {
			job.ArtifactsFile = &domain.GitLabArtifactsFile{Size: j.ArtifactsFile.Size}
		}
		out[i] = job
	}
	return out, nil
}

// MergeRequestSourceProject returns the project the merge request's source branch lives in: the
// ref's own project when the platform client reports the merge request as not from a fork
// (source and target project are the same, both known), 0 otherwise. The client reduces
// source_project_id to that fork flag, which is all the ingest uses it for: a fork, a missing id
// and an unknown source all end the run's inspection the same way (FR33).
func (c *CI) MergeRequestSourceProject(ctx context.Context, ref app.GitLabRef, iid int) (int64, error) {
	b, err := bind(ctx, c.resolve, ref.Host, "")
	if err != nil {
		return 0, err
	}
	mr, err := c.client.MergeRequest(ctx, b.Conn(), ref.ProjectID, iid)
	if err != nil {
		return 0, fail(ctx, "", err, b.Token)
	}
	if mr.HeadIsFork {
		return 0, nil
	}
	return ref.ProjectID, nil
}

// JobArtifactFile returns one file of a job's artifacts, at most limit bytes. The first request
// carries the token to the connection's origin and follows no redirect; one 302, 303 or 307 to an
// https storage address is followed without any credential (the platform client's rule). A body
// longer than limit is returned as limit bytes: the caller only has to see that it is over its own
// limit (the content of an oversized file is never used), and the platform client refuses to hand
// out a truncated body.
func (c *CI) JobArtifactFile(ctx context.Context, ref app.GitLabRef, jobID int64, file string, limit int) ([]byte, error) {
	b, err := bind(ctx, c.resolve, ref.Host, "")
	if err != nil {
		return nil, err
	}
	body, err := c.client.JobArtifactFile(ctx, b.Conn(), c.storage, ref.ProjectID, jobID, file, limit)
	if errors.Is(err, glclient.ErrTooLarge) {
		return make([]byte, limit), nil
	}
	if err != nil {
		return nil, fail(ctx, "", err, b.Token)
	}
	return body, nil
}

func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

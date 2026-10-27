package main

import (
	"context"
	"errors"
	"os"

	"dev-digest-go/backend/internal/ci/runner/adapters/env"
	"dev-digest-go/backend/internal/ci/runner/adapters/gitdiff"
	"dev-digest-go/backend/internal/ci/runner/adapters/glapi"
	runnerapp "dev-digest-go/backend/internal/ci/runner/app"
	"dev-digest-go/backend/internal/ci/runner/domain"
)

// gitLabDeps builds what GitLab mode (DEVDIGEST_PLATFORM=gitlab_ci) needs beyond GitHub mode: the
// GitLab API client of CI_API_V4_URL (trusting CI_SERVER_TLS_CA_FILE) and the diff of the job's
// checkout (CI_PROJECT_DIR, else the working directory). With post_as none the runner posts
// nothing, so the client is nil and no token or API URL is read (FR19). A client that cannot be
// built (an unusable URL or CA bundle) comes back as unusableGitLab: the use case asks it
// (Unusable) right after the environment checks and fails before the diff and the model call, with
// a reason that names a variable and never a value.
func gitLabDeps(lookup func(string) (string, bool)) (runnerapp.GitLab, runnerapp.GitDiff) {
	get := func(name string) string { v, _ := lookup(name); return v }

	dir := get("CI_PROJECT_DIR")
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		} else {
			dir = "."
		}
	}
	diff := gitdiff.New(dir)

	if get("DEVDIGEST_POST_AS") == domain.PostAsNone {
		return nil, diff
	}
	transport, err := glapi.NewTransport(get("CI_SERVER_TLS_CA_FILE"))
	if err != nil {
		return unusableGitLab{err: err}, diff
	}
	redact := func(s string) string { return runnerapp.RedactGitLabSecrets(env.Process{LookupEnv: lookup}, s) }
	client, err := glapi.New(glapi.Options{APIURL: get("CI_API_V4_URL"), Transport: transport, Redact: redact})
	if err != nil {
		return unusableGitLab{err: err}, diff
	}
	return gitLabAdapter{c: client}, diff
}

// gitLabClient is the part of *glapi.Client the adapter calls; it keeps the adapter testable
// against the real client over httptest.
type gitLabClient interface {
	MR(ctx context.Context, token string, ref glapi.MRRef) (glapi.MergeRequest, error)
	ListDrafts(ctx context.Context, token string, ref glapi.MRRef) ([]glapi.Draft, error)
	CreateDraft(ctx context.Context, token string, ref glapi.MRRef, d glapi.NewDraft) (glapi.Draft, error)
	DeleteDraft(ctx context.Context, token string, ref glapi.MRRef, id int) error
	PublishDraft(ctx context.Context, token string, ref glapi.MRRef, id int) error
	BulkPublish(ctx context.Context, token string, ref glapi.MRRef) error
	CreateNote(ctx context.Context, token string, ref glapi.MRRef, body string) error
}

// gitLabAdapter implements runnerapp.GitLab over the glapi client: it maps the use case's types to
// glapi's and glapi's *APIError to *runnerapp.GitLabAPIError (the 400 retry of a positioned draft
// depends on the latter; FR26).
type gitLabAdapter struct{ c gitLabClient }

var _ runnerapp.GitLab = gitLabAdapter{}

func (a gitLabAdapter) MR(ctx context.Context, token string, ref runnerapp.MRRef) (runnerapp.MergeRequest, error) {
	mr, err := a.c.MR(ctx, token, toGLRef(ref))
	if err != nil {
		return runnerapp.MergeRequest{}, appError(err)
	}
	out := runnerapp.MergeRequest{IID: mr.IID}
	if mr.DiffRefs != nil {
		r := fromGLRefs(*mr.DiffRefs)
		out.DiffRefs = &r
	}
	return out, nil
}

func (a gitLabAdapter) ListDrafts(ctx context.Context, token string, ref runnerapp.MRRef) ([]runnerapp.Draft, error) {
	drafts, err := a.c.ListDrafts(ctx, token, toGLRef(ref))
	if err != nil {
		return nil, appError(err)
	}
	out := make([]runnerapp.Draft, len(drafts))
	for i, d := range drafts {
		out[i] = runnerapp.Draft{ID: d.ID}
	}
	return out, nil
}

func (a gitLabAdapter) CreateDraft(ctx context.Context, token string, ref runnerapp.MRRef, d runnerapp.NewDraft) (runnerapp.Draft, error) {
	nd := glapi.NewDraft{Note: d.Note}
	if p := d.Position; p != nil {
		nd.Position = &glapi.DraftPosition{
			Refs:    glapi.DiffRefs{BaseSHA: p.Refs.BaseSHA, StartSHA: p.Refs.StartSHA, HeadSHA: p.Refs.HeadSHA},
			NewPath: p.NewPath, OldPath: p.OldPath, NewLine: p.NewLine, OldLine: p.OldLine,
		}
	}
	created, err := a.c.CreateDraft(ctx, token, toGLRef(ref), nd)
	if err != nil {
		return runnerapp.Draft{}, appError(err)
	}
	return runnerapp.Draft{ID: created.ID}, nil
}

func (a gitLabAdapter) DeleteDraft(ctx context.Context, token string, ref runnerapp.MRRef, id int) error {
	return appError(a.c.DeleteDraft(ctx, token, toGLRef(ref), id))
}

func (a gitLabAdapter) PublishDraft(ctx context.Context, token string, ref runnerapp.MRRef, id int) error {
	return appError(a.c.PublishDraft(ctx, token, toGLRef(ref), id))
}

func (a gitLabAdapter) BulkPublish(ctx context.Context, token string, ref runnerapp.MRRef) error {
	return appError(a.c.BulkPublish(ctx, token, toGLRef(ref)))
}

func (a gitLabAdapter) CreateNote(ctx context.Context, token string, ref runnerapp.MRRef, body string) error {
	return appError(a.c.CreateNote(ctx, token, toGLRef(ref), body))
}

func toGLRef(r runnerapp.MRRef) glapi.MRRef { return glapi.MRRef{ProjectID: r.ProjectID, IID: r.IID} }

func fromGLRefs(r glapi.DiffRefs) runnerapp.DiffRefs {
	return runnerapp.DiffRefs{BaseSHA: r.BaseSHA, StartSHA: r.StartSHA, HeadSHA: r.HeadSHA}
}

// appError turns a glapi *APIError into the use case's *GitLabAPIError (same status and complete
// job-log text); any other error (transport, bad reference) passes through unchanged. nil is nil.
func appError(err error) error {
	if err == nil {
		return nil
	}
	if api, ok := errors.AsType[*glapi.APIError](err); ok {
		return &runnerapp.GitLabAPIError{Status: api.Status, Message: api.Message}
	}
	return err
}

// unusableGitLab is the GitLab port when the client could not be built. The use case reads the
// reason from Unusable before anything runs; every call fails with it as well.
type unusableGitLab struct{ err error }

// Unusable returns why the client could not be built.
func (u unusableGitLab) Unusable() error { return u.err }

var _ runnerapp.GitLab = unusableGitLab{}

func (u unusableGitLab) MR(context.Context, string, runnerapp.MRRef) (runnerapp.MergeRequest, error) {
	return runnerapp.MergeRequest{}, u.err
}

func (u unusableGitLab) ListDrafts(context.Context, string, runnerapp.MRRef) ([]runnerapp.Draft, error) {
	return nil, u.err
}

func (u unusableGitLab) CreateDraft(context.Context, string, runnerapp.MRRef, runnerapp.NewDraft) (runnerapp.Draft, error) {
	return runnerapp.Draft{}, u.err
}

func (u unusableGitLab) DeleteDraft(context.Context, string, runnerapp.MRRef, int) error {
	return u.err
}

func (u unusableGitLab) PublishDraft(context.Context, string, runnerapp.MRRef, int) error {
	return u.err
}

func (u unusableGitLab) BulkPublish(context.Context, string, runnerapp.MRRef) error { return u.err }

func (u unusableGitLab) CreateNote(context.Context, string, runnerapp.MRRef, string) error {
	return u.err
}

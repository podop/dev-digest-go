package gitlab

import (
	"context"

	"dev-digest-go/backend/internal/ci/app"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
)

var _ app.GitLabPublisher = (*Publisher)(nil)

// Publisher implements app.GitLabPublisher over the platform client. Every method makes exactly one
// attempt at its call (writes are never retried; the use case repeats the whole sequence once) and
// reads the binding of the ref's host first.
type Publisher struct {
	client  *glclient.Client
	resolve glclient.ResolveFunc
}

// NewPublisher builds the publisher. client must be a platform client built on the egress client
// (it never follows a redirect and sends the token to the connection's origin only); it panics on
// a nil argument (a wiring bug).
func NewPublisher(client *glclient.Client, resolve glclient.ResolveFunc) *Publisher {
	if client == nil || resolve == nil {
		panic("ci/adapters/gitlab: NewPublisher needs a client and a ResolveFunc")
	}
	return &Publisher{client: client, resolve: resolve}
}

// Project reads GET /projects/:id.
func (p *Publisher) Project(ctx context.Context, ref app.GitLabRef) (app.GitLabProject, error) {
	b, err := bind(ctx, p.resolve, ref.Host, app.StepProject)
	if err != nil {
		return app.GitLabProject{}, err
	}
	proj, err := p.client.Project(ctx, b.Conn(), ref.ProjectID)
	if err != nil {
		return app.GitLabProject{}, fail(ctx, app.StepProject, err, b.Token)
	}
	return app.GitLabProject{
		PathWithNamespace: proj.PathWithNamespace, DefaultBranch: proj.DefaultBranch, CIConfigPath: proj.CIConfigPath,
	}, nil
}

// Branch reads a branch; false = it does not exist.
func (p *Publisher) Branch(ctx context.Context, ref app.GitLabRef, name string) (app.GitLabBranch, bool, error) {
	b, err := bind(ctx, p.resolve, ref.Host, app.StepBranch)
	if err != nil {
		return app.GitLabBranch{}, false, err
	}
	br, found, err := p.client.Branch(ctx, b.Conn(), ref.ProjectID, name)
	if err != nil {
		return app.GitLabBranch{}, false, fail(ctx, app.StepBranch, err, b.Token)
	}
	return app.GitLabBranch{Name: br.Name, HeadSHA: br.HeadSHA}, found, nil
}

// FileSHA256 reads the content hash of path at gitRef from the headers of a HEAD request; false =
// the file (or the ref) is absent.
func (p *Publisher) FileSHA256(ctx context.Context, ref app.GitLabRef, path, gitRef string) (string, bool, error) {
	b, err := bind(ctx, p.resolve, ref.Host, app.StepFileMeta)
	if err != nil {
		return "", false, err
	}
	m, found, err := p.client.FileMeta(ctx, b.Conn(), ref.ProjectID, path, gitRef)
	if err != nil {
		return "", false, fail(ctx, app.StepFileMeta, err, b.Token)
	}
	return m.SHA256, found, nil
}

// Commit makes one commit through the Commits API.
func (p *Publisher) Commit(ctx context.Context, ref app.GitLabRef, in app.GitLabCommit) error {
	b, err := bind(ctx, p.resolve, ref.Host, app.StepCommit)
	if err != nil {
		return err
	}
	actions := make([]glclient.CommitAction, len(in.Actions))
	for i, a := range in.Actions {
		actions[i] = glclient.CommitAction{Action: a.Action, FilePath: a.Path, Content: a.Content}
	}
	_, err = p.client.Commit(ctx, b.Conn(), ref.ProjectID, glclient.CommitInput{
		Branch: in.Branch, StartBranch: in.StartBranch, Message: in.Message, Actions: actions,
	})
	if err != nil {
		return fail(ctx, app.StepCommit, err, b.Token)
	}
	return nil
}

// OpenMR returns the iid of the first open merge request from source to target; false = none.
func (p *Publisher) OpenMR(ctx context.Context, ref app.GitLabRef, source, target string) (int, bool, error) {
	b, err := bind(ctx, p.resolve, ref.Host, app.StepOpenMRs)
	if err != nil {
		return 0, false, err
	}
	mrs, err := p.client.OpenMRs(ctx, b.Conn(), ref.ProjectID, source, target)
	if err != nil {
		return 0, false, fail(ctx, app.StepOpenMRs, err, b.Token)
	}
	if len(mrs) == 0 {
		return 0, false, nil
	}
	return mrs[0].IID, true, nil
}

// CreateMR creates a merge request and returns its iid. A 409 is a *app.GitLabError with Status 409.
func (p *Publisher) CreateMR(ctx context.Context, ref app.GitLabRef, in app.GitLabNewMR) (int, error) {
	b, err := bind(ctx, p.resolve, ref.Host, app.StepCreateMR)
	if err != nil {
		return 0, err
	}
	mr, err := p.client.CreateMR(ctx, b.Conn(), ref.ProjectID, glclient.CreateMRInput{
		SourceBranch: in.SourceBranch, TargetBranch: in.TargetBranch, Title: in.Title,
		Description: in.Description, RemoveSourceBranch: in.RemoveSourceBranch,
	})
	if err != nil {
		return 0, fail(ctx, app.StepCreateMR, err, b.Token)
	}
	return mr.IID, nil
}

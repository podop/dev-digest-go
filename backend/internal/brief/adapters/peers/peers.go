// Package peers adapts the other contexts and the platform to the ports of the brief context
// (brief/app): the blast radius, the project documents of a reviewed agent, the linked GitHub
// issue and the smart-diff role of a path. Each adapter only maps types and holds no rule of its
// own; the peers are taken as small interfaces declared here, so the tests use fakes and the
// composition root passes the real services. The stored intent needs no adapter: the intent
// service's Stored method already has the shape of app.Intents.
package peers

import (
	"context"
	"errors"

	blastapp "dev-digest-go/backend/internal/blast/app"
	blastdomain "dev-digest-go/backend/internal/blast/domain"
	"dev-digest-go/backend/internal/brief/app"
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/platform/github"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	reviewsdomain "dev-digest-go/backend/internal/reviews/domain"
	smartdiff "dev-digest-go/backend/internal/smartdiff/domain"
)

// Blast implements app.Blast over the blast context's provider.
type Blast struct{ p blastapp.Provider }

var _ app.Blast = (*Blast)(nil)

// NewBlast wraps the blast context's RadiusFor (*blast/app.Service).
func NewBlast(p blastapp.Provider) *Blast { return &Blast{p: p} }

// RadiusFor reads the blast radius of the changed paths and maps it field by field.
func (a *Blast) RadiusFor(ctx context.Context, repoID string, changed []string) (domain.Blast, error) {
	r, err := a.p.RadiusFor(ctx, repoID, changed)
	if err != nil {
		return domain.Blast{}, err
	}
	return mapBlast(r), nil
}

func mapBlast(r blastdomain.BlastRadius) domain.Blast {
	b := domain.Blast{
		ChangedSymbols: make([]domain.BlastSymbol, len(r.ChangedSymbols)),
		Downstream:     make([]domain.BlastGroup, len(r.Downstream)),
		Summary:        r.Summary,
		Degraded:       r.Degraded,
		Reason:         string(r.Reason),
	}
	for i, s := range r.ChangedSymbols {
		b.ChangedSymbols[i] = domain.BlastSymbol{Name: s.Name, File: s.File, Kind: s.Kind}
	}
	for i, d := range r.Downstream {
		g := domain.BlastGroup{
			Symbol:            d.Symbol,
			Callers:           make([]domain.BlastCaller, len(d.Callers)),
			EndpointsAffected: append([]string{}, d.EndpointsAffected...),
			CronsAffected:     append([]string{}, d.CronsAffected...),
		}
		for j, c := range d.Callers {
			g.Callers[j] = domain.BlastCaller{Name: c.Name, File: c.File, Line: c.Line}
		}
		b.Downstream[i] = g
	}
	return b
}

// SkillSource is the part of the reviews context's store the documents adapter uses.
type SkillSource interface {
	// EnabledSkills returns the agent's linked, enabled skills in link order.
	EnabledSkills(ctx context.Context, agentID string) ([]reviewsdomain.ReviewSkill, error)
}

// Documents implements app.Documents over the reviews context: the agent's enabled skills, then
// the project-context resolution a Phase 4 run does (the same service, so the same base commit
// reads and 16,000-token budget).
type Documents struct {
	skills SkillSource
	pc     reviewsapp.ProjectContext
}

var _ app.Documents = (*Documents)(nil)

// NewDocuments wraps the skill source (the reviews store) and the project-context resolver (the
// reviews peers adapter over the project-context service bound to its git reader).
func NewDocuments(skills SkillSource, pc reviewsapp.ProjectContext) *Documents {
	return &Documents{skills: skills, pc: pc}
}

// ForAgent returns the documents the agent attaches, in prompt order. The text of a document is
// passed on only when it is included.
func (a *Documents) ForAgent(ctx context.Context, in app.DocumentsInput) ([]domain.Document, error) {
	skills, err := a.skills.EnabledSkills(ctx, in.AgentID)
	if err != nil {
		return nil, err
	}
	res, err := a.pc.ResolveForRun(ctx, reviewsapp.ProjectContextInput{
		WorkspaceID: in.WorkspaceID,
		Repo:        reviewsdomain.ReviewRepo{ID: in.Pull.RepoID, Owner: in.Pull.Owner, Name: in.Pull.Name},
		Base:        in.Pull.Base,
		HeadSHA:     in.Pull.HeadSHA,
		AgentID:     in.AgentID,
		Skills:      skills,
	})
	if err != nil {
		return nil, err
	}
	docs := make([]domain.Document, len(res.Docs))
	for i, d := range res.Docs {
		docs[i] = domain.Document{Path: d.Path, Status: domain.DocStatus(d.Status)}
		if d.Status == reviewsdomain.ContextIncluded && d.Text != nil {
			docs[i].Text = *d.Text
		}
	}
	return docs, nil
}

// ErrNoToken is the error of an issue read without a GitHub token (P7).
var ErrNoToken = errors.New("GITHUB_TOKEN is not configured")

// IssueReader is the part of *platform/github.Client the issues adapter uses.
type IssueReader interface {
	GetIssue(ctx context.Context, owner, name string, number int) (github.Issue, error)
}

// Issues implements app.Issues over GitHub.
type Issues struct{ gh IssueReader }

var _ app.Issues = (*Issues)(nil)

// NewIssues wraps the GitHub client.
func NewIssues(gh IssueReader) *Issues { return &Issues{gh: gh} }

// Issue reads one issue; a missing token is ErrNoToken.
func (a *Issues) Issue(ctx context.Context, owner, name string, number int) (domain.Issue, error) {
	is, err := a.gh.GetIssue(ctx, owner, name, number)
	if errors.Is(err, github.ErrNoToken) {
		return domain.Issue{}, ErrNoToken
	}
	if err != nil {
		return domain.Issue{}, err
	}
	return domain.Issue{Number: is.Number, Title: is.Title, Body: is.Body}, nil
}

// Roles implements app.Roles with the smart-diff classification.
type Roles struct{}

var _ app.Roles = Roles{}

// Role returns the smart-diff role of a path.
func (Roles) Role(path string) string { return string(smartdiff.Classify(path)) }

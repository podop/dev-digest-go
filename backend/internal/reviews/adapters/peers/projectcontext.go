package peers

import (
	"context"

	"dev-digest-go/backend/internal/platform/codehost"
	pcapp "dev-digest-go/backend/internal/projectcontext/app"
	reviewsapp "dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// RunContexter is the part of *projectcontext/app.Service the adapter uses.
type RunContexter interface {
	RunContext(ctx context.Context, in pcapp.RunContextInput) (pcapp.RunContext, error)
}

// ProjectContext implements reviews/app.ProjectContext over the project-context service.
type ProjectContext struct{ s RunContexter }

var _ reviewsapp.ProjectContext = (*ProjectContext)(nil)

// NewProjectContext wraps the project-context service (already bound to its git reader).
func NewProjectContext(s RunContexter) *ProjectContext { return &ProjectContext{s: s} }

// ResolveForRun reads the run's documents, field by field into the reviews vocabulary. The text
// of a document is passed on only when it is included.
func (a *ProjectContext) ResolveForRun(ctx context.Context, in reviewsapp.ProjectContextInput) (reviewsapp.ProjectContextResult, error) {
	skills := make([]pcapp.RunSkill, len(in.Skills))
	for i, s := range in.Skills {
		skills[i] = pcapp.RunSkill{ID: s.ID, Name: s.Name}
	}
	// The service forwards (Owner, Name) to the git reader as the clone location: the clone key
	// (GitHub: the pair itself; GitLab: "@gitlab/<host dir>", "<project id>").
	owner, name := codehost.CloneKey(in.Repo.Provider, in.Repo.Host, in.Repo.ProjectID, in.Repo.Owner, in.Repo.Name)
	rc, err := a.s.RunContext(ctx, pcapp.RunContextInput{
		WorkspaceID: in.WorkspaceID, RepoID: in.Repo.ID, Owner: owner, Name: name,
		Base: in.Base, HeadSHA: in.HeadSHA, AgentID: in.AgentID, Skills: skills,
	})
	if err != nil {
		return reviewsapp.ProjectContextResult{}, err
	}
	docs := make([]domain.ContextDoc, len(rc.Docs))
	for i, d := range rc.Docs {
		doc := domain.ContextDoc{
			Path: d.Path, DocType: string(d.DocType),
			Origin: domain.ContextOrigin{Kind: d.Origin.Kind, SkillID: d.Origin.SkillID, SkillName: d.Origin.SkillName},
			Tokens: d.Tokens, Status: domain.ContextStatus(d.Status), Source: d.Source,
		}
		if doc.Status == domain.ContextIncluded && d.Text != nil {
			text := *d.Text
			doc.Text = &text
		}
		docs[i] = doc
	}
	return reviewsapp.ProjectContextResult{
		Docs: docs, TokensTotal: rc.TokensTotal, BudgetTokens: rc.BudgetTokens, LogLine: rc.LogLine,
	}, nil
}

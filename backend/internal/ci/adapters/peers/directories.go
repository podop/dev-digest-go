package peers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	agentsdomain "dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/codehost"
	glclient "dev-digest-go/backend/internal/platform/gitlab"
	reposapp "dev-digest-go/backend/internal/repos/app"
	reposdomain "dev-digest-go/backend/internal/repos/domain"
	skillsdomain "dev-digest-go/backend/internal/skills/domain"
)

// AgentService is the part of the agents use cases the adapter reads.
type AgentService interface {
	// Get answers a not-found apperr for an unknown agent or one of another workspace.
	Get(ctx context.Context, workspaceID, id string) (agentsdomain.Agent, error)
	// SkillLinks returns the agent's links by order.
	SkillLinks(ctx context.Context, workspaceID, id string) ([]agentsdomain.SkillLink, error)
}

// SkillService is the part of the skills use cases the adapter reads.
type SkillService interface {
	// Get answers a not-found apperr for an unknown skill or one of another workspace.
	Get(ctx context.Context, workspaceID, id string) (skillsdomain.Skill, error)
}

// RepoService is the part of the repos use cases the adapter reads.
type RepoService interface {
	List(ctx context.Context, workspaceID string) ([]reposdomain.Repo, error)
}

// Agents implements app.AgentDirectory over the agents and skills use cases.
type Agents struct {
	agents AgentService
	skills SkillService
}

var _ app.AgentDirectory = (*Agents)(nil)

// NewAgents builds the directory.
func NewAgents(agents AgentService, skills SkillService) *Agents {
	return &Agents{agents: agents, skills: skills}
}

// Get returns the agent with its linked and enabled skills in link order, the set a live review
// of the agent carries. A link whose skill is gone is left out, and so is a skill of a disabled
// plugin. The agent's own plugin is passed on for the export gate.
func (d *Agents) Get(ctx context.Context, workspaceID, agentID string) (domain.ExportAgent, bool, error) {
	a, found, err := d.agent(ctx, workspaceID, agentID)
	if err != nil || !found {
		return domain.ExportAgent{}, false, err
	}
	links, err := d.agents.SkillLinks(ctx, workspaceID, agentID)
	if err != nil {
		return domain.ExportAgent{}, false, fmt.Errorf("list skill links of agent %s: %w", agentID, err)
	}
	skills := make([]domain.ExportSkill, 0, len(links))
	for _, l := range links {
		s, err := d.skills.Get(ctx, workspaceID, l.SkillID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return domain.ExportAgent{}, false, fmt.Errorf("get skill %s: %w", l.SkillID, err)
		}
		if s.Enabled && (s.Plugin == nil || s.Plugin.Enabled) {
			skills = append(skills, domain.ExportSkill{Name: s.Name, Body: s.Body})
		}
	}
	out := domain.ExportAgent{
		ID: a.ID, Name: a.Name, Provider: a.Provider, Model: a.Model, SystemPrompt: a.SystemPrompt,
		Strategy: a.Strategy, CIFailOn: a.CIFailOn, Version: a.Version, Skills: skills,
	}
	if a.Plugin != nil {
		out.Plugin = &domain.PluginRef{Name: a.Plugin.Name, Enabled: a.Plugin.Enabled}
	}
	return out, true, nil
}

// Exists reports whether the workspace has the agent.
func (d *Agents) Exists(ctx context.Context, workspaceID, agentID string) (bool, error) {
	_, found, err := d.agent(ctx, workspaceID, agentID)
	return found, err
}

func (d *Agents) agent(ctx context.Context, workspaceID, agentID string) (agentsdomain.Agent, bool, error) {
	a, err := d.agents.Get(ctx, workspaceID, agentID)
	if isNotFound(err) {
		return agentsdomain.Agent{}, false, nil
	}
	if err != nil {
		return agentsdomain.Agent{}, false, err
	}
	return a, true, nil
}

func isNotFound(err error) bool {
	ae, ok := apperr.As(err)
	return ok && ae.Kind == apperr.KindNotFound
}

// RepoPathStore stores a GitLab project's current path on its repository (spec A FR47). The repos
// context's store implements it; a path another repository of the workspace and host already holds
// is not stored and that repository's id comes back as skippedFor.
type RepoPathStore interface {
	UpdatePath(ctx context.Context, workspaceID, id string, p reposapp.ProjectPath) (skippedFor string, err error)
}

// Repos implements app.RepoDirectory over the repos use cases.
type Repos struct {
	repos   RepoService
	paths   RepoPathStore
	resolve glclient.ResolveFunc
	log     *slog.Logger
}

var _ app.RepoDirectory = (*Repos)(nil)

// NewRepos builds the directory. Without WithGitLab it cannot follow a GitLab path.
func NewRepos(repos RepoService) *Repos {
	return &Repos{repos: repos, log: slog.New(slog.DiscardHandler)}
}

// WithGitLab enables FollowGitLabPath: paths stores the path, resolve finds the connection whose
// base URL the stored web address is built from (never from a GitLab response). log receives the
// one warning of a skipped path; nil discards. It returns d.
func (d *Repos) WithGitLab(paths RepoPathStore, resolve glclient.ResolveFunc, log *slog.Logger) *Repos {
	d.paths, d.resolve = paths, resolve
	if log != nil {
		d.log = log
	}
	return d
}

// Find resolves q (spec C FR1). A non-empty ID wins: that repository of the workspace on any host,
// false when it is not in the workspace. Otherwise the exact FullName: a GitHub repository wins;
// else the one GitLab repository; GitLab repositories on several hosts are an
// *app.AmbiguousRepoError (their ids in list order); none is false. A GitLab repository is
// returned whether or not its host has a connection.
func (d *Repos) Find(ctx context.Context, workspaceID string, q app.RepoQuery) (app.ExportRepo, bool, error) {
	list, err := d.repos.List(ctx, workspaceID)
	if err != nil {
		return app.ExportRepo{}, false, fmt.Errorf("list repos: %w", err)
	}
	if q.ID != "" {
		for _, r := range list {
			if r.ID == q.ID {
				return exportRepo(r), true, nil
			}
		}
		return app.ExportRepo{}, false, nil
	}
	var gitlab []reposdomain.Repo
	for _, r := range list {
		if r.FullName != q.FullName {
			continue
		}
		if isGitHub(r) {
			return exportRepo(r), true, nil
		}
		if r.Provider == codehost.GitLab {
			gitlab = append(gitlab, r)
		}
	}
	switch len(gitlab) {
	case 0:
		return app.ExportRepo{}, false, nil
	case 1:
		return exportRepo(gitlab[0]), true, nil
	}
	ids := make([]string, len(gitlab))
	for i, r := range gitlab {
		ids[i] = r.ID
	}
	return app.ExportRepo{}, false, &app.AmbiguousRepoError{IDs: ids}
}

// FollowGitLabPath stores the project's current path_with_namespace and the web address built from
// the connection on the repository (spec A FR47) and returns the repository as stored afterwards. A
// path another repository of the workspace holds is not stored: one warning, the repository comes
// back unchanged. A path that is not a plain GitLab path (codehost.ProjectRef), a repository that
// is not a GitLab one and a missing connection are errors.
func (d *Repos) FollowGitLabPath(ctx context.Context, workspaceID string, repo app.ExportRepo, pathWithNamespace string) (app.ExportRepo, error) {
	if repo.Provider != app.ProviderGitLab {
		return app.ExportRepo{}, errors.New("not a gitlab repository")
	}
	owner, name, err := ownerName(pathWithNamespace)
	if err != nil {
		return app.ExportRepo{}, err
	}
	if repo.FullName == pathWithNamespace && repo.WebURL != "" {
		return repo, nil
	}
	if d.paths == nil || d.resolve == nil {
		return app.ExportRepo{}, errors.New("gitlab path following is not configured")
	}
	b, ok, err := d.resolve(ctx, repo.Host)
	if err != nil {
		return app.ExportRepo{}, fmt.Errorf("resolve gitlab connection: %w", err)
	}
	if !ok {
		return app.ExportRepo{}, errors.New("gitlab connection of the repository is gone")
	}
	p := reposapp.ProjectPath{
		FullName: pathWithNamespace, Owner: owner, Name: name, WebURL: codehost.WebURL(b.BaseURL, pathWithNamespace),
	}
	heldBy, err := d.paths.UpdatePath(ctx, workspaceID, repo.ID, p)
	if err != nil {
		return app.ExportRepo{}, fmt.Errorf("update path of repo %s: %w", repo.ID, err)
	}
	if heldBy != "" {
		d.log.WarnContext(ctx, "gitlab project path not stored: another repo of the workspace has it",
			"repo", repo.ID, "other", heldBy, "host", repo.Host)
		return repo, nil
	}
	repo.FullName, repo.Owner, repo.Name, repo.WebURL = p.FullName, p.Owner, p.Name, p.WebURL
	return repo, nil
}

// ownerName splits a path_with_namespace after the plain-path check of the add.
func ownerName(full string) (owner, name string, err error) {
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return "", "", codehost.ErrInvalidProjectPath
	}
	return codehost.ProjectRef(full, full[i+1:], "")
}

// exportRepo maps a repository of the repos context. A row that predates the provider columns is
// a GitHub one.
func exportRepo(r reposdomain.Repo) app.ExportRepo {
	out := app.ExportRepo{
		ID: r.ID, Provider: app.ProviderGitHub, Host: codehost.GitHubHost,
		Owner: r.Owner, Name: r.Name, FullName: r.FullName, DefaultBranch: r.DefaultBranch,
	}
	if r.ClonePath != nil {
		out.ClonePath = *r.ClonePath
	}
	if r.Provider == codehost.GitLab {
		out.Provider, out.Host, out.ProjectID, out.WebURL = app.ProviderGitLab, r.Host, r.ProjectID, r.WebURL
	}
	return out
}

// isGitHub reports whether the repository is on github.com; an empty provider is a GitHub row
// that predates the provider column.
func isGitHub(r reposdomain.Repo) bool {
	return (r.Provider == "" || r.Provider == codehost.GitHub) && (r.Host == "" || r.Host == codehost.GitHubHost)
}

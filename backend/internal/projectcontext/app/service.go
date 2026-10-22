// Package app holds the use cases of the project-context context: listing and previewing
// documents, the agent and skill attachments, and the store files kept in the database.
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/projectcontext/domain"
)

// Service implements the project-context use cases.
type Service struct {
	store store
	clone cloneDocs
	tx    txRunner
	globs *domain.GlobSet
	log   *slog.Logger
}

// NewService builds a Service. globs are the listable-document globs
// (config.Config.ProjectContextGlobs); an invalid glob is an error.
func NewService(st store, clone cloneDocs, tx txRunner, globs []string, log *slog.Logger) (*Service, error) {
	set, err := domain.NewGlobSet(globs)
	if err != nil {
		return nil, fmt.Errorf("project context globs: %w", err)
	}
	return &Service{store: st, clone: clone, tx: tx, globs: set, log: log}, nil
}

// Doc is one document of the list.
type Doc struct {
	Path      string
	Name      string
	DocType   string
	SizeBytes int64
	Tokens    int
	UpdatedAt time.Time
	UsedBy    int
	Source    string // SourceRepo or SourceStore
	Editable  bool
	// Version is set on store documents only.
	Version *int
}

// List is the result of ListDocs.
type List struct {
	CloneStatus string // CloneReady or CloneNotCloned
	Globs       []string
	Docs        []Doc
	TokensTotal int
	// Truncated is true when the clone documents were cut at the cap.
	Truncated bool
}

// UsedByAgent is an agent that uses a document.
type UsedByAgent struct {
	ID        string
	Name      string
	Via       string // ViaDirect or ViaSkill
	SkillName string // set when Via is ViaSkill
}

// Preview is one document with its content.
type Preview struct {
	Path         string
	Name         string
	DocType      string
	Content      string
	Tokens       int
	SizeBytes    int64
	UsedBy       int
	UsedByAgents []UsedByAgent
	Source       string
	Editable     bool
	// Version is set on store documents only.
	Version *int
}

// Attachments are the paths an agent or skill attaches in one repo, in order.
type Attachments struct {
	RepoID string
	Paths  []string
}

// ListDocs returns the store files of the repo first, then the clone documents the globs accept.
func (s *Service) ListDocs(ctx context.Context, workspaceID, repoID string) (List, error) {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return List{}, err
	}
	stored, err := s.store.ListFiles(ctx, repoID)
	if err != nil {
		return List{}, fmt.Errorf("list store files: %w", err)
	}
	usage, err := s.store.ListUsage(ctx, repoID)
	if err != nil {
		return List{}, fmt.Errorf("list usage: %w", err)
	}
	used := usedByPath(usage)

	var cloned ClonedList
	ready := false
	if repo.ClonePath != nil {
		cloned, ready, err = s.clone.List(ctx, *repo.ClonePath, s.globs, domain.MaxDocs)
		if err != nil {
			return List{}, fmt.Errorf("list clone documents: %w", err)
		}
	}

	docs := make([]Doc, 0, len(stored)+len(cloned.Docs))
	inStore := make(map[string]struct{}, len(stored))
	for _, f := range stored {
		inStore[f.Path] = struct{}{}
		v := f.Version
		docs = append(docs, Doc{
			Path: f.Path, Name: domain.DocName(f.Path), DocType: string(domain.DocTypeSpecs),
			SizeBytes: int64(f.SizeBytes), Tokens: domain.Tokens(f.Chars), UpdatedAt: f.UpdatedAt,
			UsedBy: len(used[f.Path]), Source: SourceStore, Editable: true, Version: &v,
		})
	}
	for _, d := range cloned.Docs {
		if _, shadowed := inStore[d.Path]; shadowed { // a store file wins over a clone file
			continue
		}
		docs = append(docs, Doc{
			Path: d.Path, Name: domain.DocName(d.Path), DocType: string(domain.DocTypeOf(d.Path)),
			SizeBytes: d.SizeBytes, Tokens: d.Tokens, UpdatedAt: d.UpdatedAt,
			UsedBy: len(used[d.Path]), Source: SourceRepo,
		})
	}
	total := 0
	for _, d := range docs {
		total += d.Tokens
	}
	out := List{CloneStatus: CloneNotCloned, Globs: s.globs.Globs(), Docs: docs, TokensTotal: total}
	if ready {
		out.CloneStatus = CloneReady
		out.Truncated = cloned.Truncated
	}
	return out, nil
}

// PreviewDoc returns one store file or listable clone document with the agents that use it.
func (s *Service) PreviewDoc(ctx context.Context, workspaceID, repoID, path string) (Preview, error) {
	repo, err := s.requireRepo(ctx, workspaceID, repoID)
	if err != nil {
		return Preview{}, err
	}
	rows, err := s.store.ListUsage(ctx, repoID)
	if err != nil {
		return Preview{}, fmt.Errorf("list usage: %w", err)
	}
	usedBy := usedByPath(rows)[path]

	if domain.CheckStorePath(path) {
		file, ok, err := s.store.FindFile(ctx, repoID, path)
		if err != nil {
			return Preview{}, fmt.Errorf("find store file: %w", err)
		}
		if ok {
			return storePreview(file, usedBy), nil
		}
	}
	// Shape, excluded directories and globs are checked before any file system access. A
	// store-shaped path passes too, so a deleted store file is a 404, not a 400.
	if !domain.IsListablePath(path, s.globs) && !domain.CheckStorePath(path) {
		return Preview{}, apperr.InvalidInput("Not a listable document path").WithCode("invalid_path")
	}
	if repo.ClonePath == nil {
		return Preview{}, errDocNotFound()
	}
	read := s.clone.Read(*repo.ClonePath, path)
	switch read.Status {
	case ClonedOK:
	case ClonedTooLarge:
		return Preview{}, apperr.PayloadTooLarge("Document is too large to preview").WithCode("doc_too_large")
	default:
		return Preview{}, errDocNotFound()
	}
	return Preview{
		Path: path, Name: domain.DocName(path), DocType: string(domain.DocTypeOf(path)),
		Content: read.Content, Tokens: read.Tokens, SizeBytes: read.SizeBytes,
		UsedBy: len(usedBy), UsedByAgents: usedBy, Source: SourceRepo,
	}, nil
}

// AgentContext returns the agent's attached paths in one repo.
func (s *Service) AgentContext(ctx context.Context, workspaceID, agentID, repoID string) (Attachments, error) {
	if err := s.requireAgent(ctx, workspaceID, agentID); err != nil {
		return Attachments{}, err
	}
	if _, err := s.requireRepo(ctx, workspaceID, repoID); err != nil {
		return Attachments{}, err
	}
	paths, err := s.store.AgentPaths(ctx, agentID, repoID)
	if err != nil {
		return Attachments{}, fmt.Errorf("agent paths: %w", err)
	}
	return Attachments{RepoID: repoID, Paths: nonNil(paths)}, nil
}

// SkillContext returns the skill's attached paths in one repo.
func (s *Service) SkillContext(ctx context.Context, workspaceID, skillID, repoID string) (Attachments, error) {
	if err := s.requireSkill(ctx, workspaceID, skillID); err != nil {
		return Attachments{}, err
	}
	if _, err := s.requireRepo(ctx, workspaceID, repoID); err != nil {
		return Attachments{}, err
	}
	paths, err := s.store.SkillPaths(ctx, skillID, repoID)
	if err != nil {
		return Attachments{}, fmt.Errorf("skill paths: %w", err)
	}
	return Attachments{RepoID: repoID, Paths: nonNil(paths)}, nil
}

// PutAgentContext replaces the agent's attached paths in one repo. The agent's version is not touched.
func (s *Service) PutAgentContext(ctx context.Context, workspaceID, agentID, repoID string, paths []string) (Attachments, error) {
	if err := s.requireAgent(ctx, workspaceID, agentID); err != nil {
		return Attachments{}, err
	}
	if _, err := s.requireRepo(ctx, workspaceID, repoID); err != nil {
		return Attachments{}, err
	}
	if err := s.validatePaths(paths); err != nil {
		return Attachments{}, err
	}
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		return s.store.ReplaceAgentPaths(ctx, agentID, repoID, paths)
	})
	if err != nil {
		return Attachments{}, ownerGone(err, "agent_not_found", "replace agent paths")
	}
	return Attachments{RepoID: repoID, Paths: slices.Clone(paths)}, nil
}

// PutSkillContext replaces the skill's attached paths in one repo. The skill's version is not touched.
func (s *Service) PutSkillContext(ctx context.Context, workspaceID, skillID, repoID string, paths []string) (Attachments, error) {
	if err := s.requireSkill(ctx, workspaceID, skillID); err != nil {
		return Attachments{}, err
	}
	if _, err := s.requireRepo(ctx, workspaceID, repoID); err != nil {
		return Attachments{}, err
	}
	if err := s.validatePaths(paths); err != nil {
		return Attachments{}, err
	}
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		return s.store.ReplaceSkillPaths(ctx, skillID, repoID, paths)
	})
	if err != nil {
		return Attachments{}, ownerGone(err, "skill_not_found", "replace skill paths")
	}
	return Attachments{RepoID: repoID, Paths: slices.Clone(paths)}, nil
}

func (s *Service) validatePaths(paths []string) error {
	switch err := domain.ValidateAttachmentPaths(paths, s.globs); {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrTooManyPaths):
		return apperr.Validation("Too many paths").WithCode("too_many_paths")
	case errors.Is(err, domain.ErrDuplicatePath):
		return apperr.Validation("A path is attached more than once").WithCode("duplicate_path")
	default:
		return apperr.Validation("A path is not a listable document path").WithCode("invalid_path")
	}
}

// ownerGone maps a store error to the 404 of an owner or repo deleted after the existence check.
func ownerGone(err error, code, what string) error {
	if errors.Is(err, ErrOwnerGone) {
		return apperr.NotFound("Owner or repository no longer exists").WithCode(code)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (s *Service) requireRepo(ctx context.Context, workspaceID, repoID string) (Repo, error) {
	repo, ok, err := s.store.FindRepo(ctx, workspaceID, repoID)
	if err != nil {
		return Repo{}, fmt.Errorf("find repo: %w", err)
	}
	if !ok {
		return Repo{}, errRepoNotFound()
	}
	return repo, nil
}

func (s *Service) requireAgent(ctx context.Context, workspaceID, agentID string) error {
	ok, err := s.store.AgentExists(ctx, workspaceID, agentID)
	if err != nil {
		return fmt.Errorf("find agent: %w", err)
	}
	if !ok {
		return apperr.NotFound("Agent not found").WithCode("agent_not_found")
	}
	return nil
}

func (s *Service) requireSkill(ctx context.Context, workspaceID, skillID string) error {
	ok, err := s.store.SkillExists(ctx, workspaceID, skillID)
	if err != nil {
		return fmt.Errorf("find skill: %w", err)
	}
	if !ok {
		return apperr.NotFound("Skill not found").WithCode("skill_not_found")
	}
	return nil
}

func errRepoNotFound() error {
	return apperr.NotFound("Repository not found").WithCode("repo_not_found")
}
func errDocNotFound() error { return apperr.NotFound("Document not found").WithCode("doc_not_found") }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// usedByPath groups the agents using each path: one entry per agent, a direct attachment
// beating a skill one, sorted by name and then id.
func usedByPath(rows []UsageRow) map[string][]UsedByAgent {
	byPath := make(map[string]map[string]UsedByAgent)
	for _, r := range rows {
		agents := byPath[r.Path]
		if agents == nil {
			agents = make(map[string]UsedByAgent)
			byPath[r.Path] = agents
		}
		if have, ok := agents[r.AgentID]; ok && (have.Via == ViaDirect || r.Via == ViaSkill) {
			continue
		}
		u := UsedByAgent{ID: r.AgentID, Name: r.AgentName, Via: r.Via}
		if r.Via == ViaSkill {
			u.SkillName = r.SkillName
		}
		agents[r.AgentID] = u
	}
	out := make(map[string][]UsedByAgent, len(byPath))
	for path, agents := range byPath {
		list := make([]UsedByAgent, 0, len(agents))
		for _, u := range agents {
			list = append(list, u)
		}
		slices.SortFunc(list, func(a, b UsedByAgent) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
		})
		out[path] = list
	}
	return out
}

// storePreview is the single-document view of a store file.
func storePreview(f File, usedBy []UsedByAgent) Preview {
	v := f.Version
	if usedBy == nil {
		usedBy = []UsedByAgent{}
	}
	return Preview{
		Path: f.Path, Name: domain.DocName(f.Path), DocType: string(domain.DocTypeSpecs),
		Content: f.Content, Tokens: domain.StoreTextTokens(f.Content), SizeBytes: int64(f.SizeBytes),
		UsedBy: len(usedBy), UsedByAgents: usedBy, Source: SourceStore, Editable: true, Version: &v,
	}
}

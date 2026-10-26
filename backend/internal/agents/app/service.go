// Package app holds the agent use cases: CRUD with config versioning, ordered skill links and
// model listing. It talks to persistence and the LLM layer only through the ports below.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Client-visible texts of the not-found errors.
const (
	msgAgentNotFound   = "Agent not found"
	msgVersionNotFound = "Agent version not found"
)

// CodeUnknownSkill is the wire code of a skill-link request naming skills outside the workspace.
const CodeUnknownSkill = "unknown_skill"

// store is the persistence the use cases need. Every method reaches the database through the
// context, so calls made inside txRunner.InTx share one transaction. The methods that take an
// agent id expect the caller to have checked that the agent belongs to the workspace.
type store interface {
	// List returns the workspace's agents by creation time, name (byte order), id.
	List(ctx context.Context, workspaceID string) ([]domain.Agent, error)
	// Get returns domain.ErrNotFound for an unknown id or an agent of another workspace.
	Get(ctx context.Context, workspaceID, id string) (domain.Agent, error)
	// GetForUpdate is Get with a row lock held until the transaction ends.
	GetForUpdate(ctx context.Context, workspaceID, id string) (domain.Agent, error)
	// Insert stores a new agent (created_by = createdBy when not empty) and returns it with its id.
	Insert(ctx context.Context, workspaceID, createdBy string, a domain.Agent) (domain.Agent, error)
	// Save writes every mutable field of the agent, including its version.
	Save(ctx context.Context, workspaceID string, a domain.Agent) error
	// Delete removes the agent; domain.ErrNotFound when it does not exist in the workspace.
	Delete(ctx context.Context, workspaceID, id string) error
	// DeleteEvalCases removes the workspace's eval cases owned by the agent.
	DeleteEvalCases(ctx context.Context, workspaceID, agentID string) error
	// AddVersion records a snapshot; an existing (agent, version) is left alone.
	AddVersion(ctx context.Context, agentID string, version int, cfg domain.VersionConfig) error
	// Versions returns the agent's snapshots, newest first.
	Versions(ctx context.Context, agentID string) ([]domain.Version, error)
	// Version returns one snapshot or domain.ErrVersionNotFound.
	Version(ctx context.Context, agentID string, version int) (domain.Version, error)
	// SkillLinks returns the agent's links by order.
	SkillLinks(ctx context.Context, agentID string) ([]domain.SkillLink, error)
	// KnownSkillIDs returns which of ids are skills of the workspace.
	KnownSkillIDs(ctx context.Context, workspaceID string, ids []string) (map[string]bool, error)
	// ReplaceSkills replaces the agent's links with ids, order = index.
	ReplaceSkills(ctx context.Context, agentID string, ids []string) error
}

// txRunner runs fn in a database transaction that joins an outer one carried by ctx.
type txRunner interface {
	InTx(ctx context.Context, fn func(context.Context) error) error
}

// modelLister lists a provider's models (llm.Client implements it).
type modelLister interface {
	ListModels(ctx context.Context, p llm.Provider) ([]llm.ModelInfo, error)
}

// customProviders is the workspace's catalog of custom model providers (the customproviders
// context implements it). Ids are public ids, `custom:<slug>`.
type customProviders interface {
	// Defined reports whether the workspace defines the provider.
	Defined(ctx context.Context, workspaceID, id string) (bool, error)
	// LockDefined is Defined that also locks the provider row FOR SHARE until the transaction
	// carried by ctx ends, so a concurrent delete of the provider waits for the agent write
	// that references it instead of orphaning it. It must run inside txRunner.InTx.
	LockDefined(ctx context.Context, workspaceID, id string) (bool, error)
	// Models returns the provider's live and manual models (spec FR10). A failing live listing is
	// not an error there; the manual list alone is returned.
	Models(ctx context.Context, workspaceID, id string) ([]llm.ModelInfo, error)
}

// UndefinedProviderError means an agent write named a well-formed custom provider id that the
// workspace does not define. The transport maps it to a 422 at the provider path.
type UndefinedProviderError struct{ ID string }

func (e *UndefinedProviderError) Error() string {
	return "Custom provider " + e.ID + " is not configured"
}

// Service implements the agent use cases.
type Service struct {
	store  store
	tx     txRunner
	models modelLister
	custom customProviders
	log    *slog.Logger
}

// Option configures optional collaborators of a Service.
type Option func(*Service)

// WithCustomProviders enables custom provider ids on agents. Without it every custom id counts
// as undefined: writes naming one are refused and its model list is empty.
func WithCustomProviders(cp customProviders) Option {
	return func(s *Service) { s.custom = cp }
}

// NewService builds a Service. log receives the warnings of failed model listings.
func NewService(store store, tx txRunner, models modelLister, log *slog.Logger, opts ...Option) *Service {
	s := &Service{store: store, tx: tx, models: models, log: log}
	for _, o := range opts {
		o(s)
	}
	return s
}

// List returns the workspace's agents, enabled or not.
func (s *Service) List(ctx context.Context, workspaceID string) ([]domain.Agent, error) {
	agents, err := s.store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return agents, nil
}

// Get returns the agent or a not-found error.
func (s *Service) Get(ctx context.Context, workspaceID, id string) (domain.Agent, error) {
	a, err := s.store.Get(ctx, workspaceID, id)
	return a, agentErr(err, "get agent")
}

// Create stores a new agent together with its version-1 snapshot, in one transaction.
func (s *Service) Create(ctx context.Context, workspaceID, userID string, in domain.CreateInput) (domain.Agent, error) {
	var created domain.Agent
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.requireDefined(ctx, workspaceID, in.Provider); err != nil {
			return err
		}
		a, err := s.store.Insert(ctx, workspaceID, userID, domain.New(in))
		if err != nil {
			return fmt.Errorf("insert agent: %w", err)
		}
		if err := s.store.AddVersion(ctx, a.ID, a.Version, a.Snapshot(nil)); err != nil {
			return fmt.Errorf("snapshot agent %s v%d: %w", a.ID, a.Version, err)
		}
		created = a
		return nil
	})
	if err != nil {
		return domain.Agent{}, err
	}
	return created, nil
}

// Update applies the patch under a row lock. A config change raises the version by one and
// records a snapshot with the current skill ids; any other patch keeps the version.
func (s *Service) Update(ctx context.Context, workspaceID, id string, p domain.Patch) (domain.Agent, error) {
	var updated domain.Agent
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		current, err := s.store.GetForUpdate(ctx, workspaceID, id)
		if err != nil {
			return agentErr(err, "lock agent")
		}
		// An unchanged provider is exempt: an agent may legally hold an undefined custom id
		// (it fails at run time) and must stay editable by a client that sends the full object.
		if p.Provider != nil && *p.Provider != current.Provider {
			if err := s.requireDefined(ctx, workspaceID, *p.Provider); err != nil {
				return err
			}
		}
		next, changed := p.Apply(current)
		if changed {
			next.Version = current.Version + 1
		}
		if err := s.store.Save(ctx, workspaceID, next); err != nil {
			return fmt.Errorf("save agent %s: %w", id, err)
		}
		if changed {
			if err := s.snapshot(ctx, next); err != nil {
				return err
			}
		}
		updated = next
		return nil
	})
	if err != nil {
		return domain.Agent{}, err
	}
	return updated, nil
}

// Delete removes the agent and the workspace's eval cases that belong to it, in one transaction.
func (s *Service) Delete(ctx context.Context, workspaceID, id string) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Delete(ctx, workspaceID, id); err != nil {
			return agentErr(err, "delete agent")
		}
		if err := s.store.DeleteEvalCases(ctx, workspaceID, id); err != nil {
			return fmt.Errorf("delete eval cases of agent %s: %w", id, err)
		}
		return nil
	})
}

// Versions returns the agent's snapshots, newest first.
func (s *Service) Versions(ctx context.Context, workspaceID, id string) ([]domain.Version, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	versions, err := s.store.Versions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list versions of agent %s: %w", id, err)
	}
	return versions, nil
}

// Version returns one snapshot. An unknown agent and an unknown version are the same error.
func (s *Service) Version(ctx context.Context, workspaceID, id string, version int) (domain.Version, error) {
	if _, err := s.store.Get(ctx, workspaceID, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Version{}, apperr.NotFound(msgVersionNotFound)
		}
		return domain.Version{}, fmt.Errorf("get agent %s: %w", id, err)
	}
	v, err := s.store.Version(ctx, id, version)
	if errors.Is(err, domain.ErrVersionNotFound) {
		return domain.Version{}, apperr.NotFound(msgVersionNotFound)
	}
	if err != nil {
		return domain.Version{}, fmt.Errorf("get version %d of agent %s: %w", version, id, err)
	}
	return v, nil
}

// SkillLinks returns the agent's skill links by order.
func (s *Service) SkillLinks(ctx context.Context, workspaceID, id string) ([]domain.SkillLink, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	return s.links(ctx, id)
}

// SetSkills replaces the agent's links with skillIDs, in that order (set mode).
func (s *Service) SetSkills(ctx context.Context, workspaceID, id string, skillIDs []string) ([]domain.SkillLink, error) {
	return s.changeLinks(ctx, workspaceID, id, func([]string) []string { return skillIDs })
}

// LinkSkill links one skill at index order (nil appends); a skill linked already moves there
// (link mode).
func (s *Service) LinkSkill(ctx context.Context, workspaceID, id, skillID string, order *int) ([]domain.SkillLink, error) {
	return s.changeLinks(ctx, workspaceID, id, func(current []string) []string {
		return domain.WithSkillAt(current, skillID, order)
	})
}

// AppendSkill links the skill at the end of the agent's list (link mode). It joins the
// transaction carried by ctx, so the skills merge can call it inside its own.
func (s *Service) AppendSkill(ctx context.Context, workspaceID, agentID, skillID string) error {
	_, err := s.LinkSkill(ctx, workspaceID, agentID, skillID, nil)
	return err
}

// AgentModels lists the models of the agent's provider; a listing problem gives an empty list.
func (s *Service) AgentModels(ctx context.Context, workspaceID, id string) ([]llm.ModelInfo, error) {
	a, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	return s.ProviderModels(ctx, workspaceID, a.Provider)
}

// ProviderModels lists a provider's models. For a built-in provider a missing key, a provider
// error and a timeout give an empty list; the cause is logged at warn (the llm errors carry no
// key). A custom id answers from the workspace's catalog: the union of the live and the manual
// models when defined, an empty list when not. Only a failed catalog lookup is an error.
func (s *Service) ProviderModels(ctx context.Context, workspaceID, provider string) ([]llm.ModelInfo, error) {
	if _, isCustom := custom.ParseID(provider); isCustom {
		return s.customModels(ctx, workspaceID, provider)
	}
	p, ok := llm.ParseProvider(provider)
	if !ok {
		return []llm.ModelInfo{}, nil
	}
	models, err := s.models.ListModels(ctx, p)
	if err != nil {
		s.log.WarnContext(ctx, "model listing failed", "provider", provider, "error", err.Error())
		return []llm.ModelInfo{}, nil
	}
	if models == nil {
		return []llm.ModelInfo{}, nil
	}
	return models, nil
}

func (s *Service) customModels(ctx context.Context, workspaceID, id string) ([]llm.ModelInfo, error) {
	if s.custom == nil {
		return []llm.ModelInfo{}, nil
	}
	defined, err := s.custom.Defined(ctx, workspaceID, id)
	if err != nil {
		return nil, fmt.Errorf("look up custom provider %s: %w", id, err)
	}
	if !defined {
		return []llm.ModelInfo{}, nil
	}
	models, err := s.custom.Models(ctx, workspaceID, id)
	if err != nil {
		return nil, fmt.Errorf("list models of custom provider %s: %w", id, err)
	}
	if models == nil {
		return []llm.ModelInfo{}, nil
	}
	return models, nil
}

// requireDefined refuses a custom provider id the workspace does not define. It locks the
// provider row FOR SHARE for the rest of the transaction (A8). Built-in ids pass untouched.
func (s *Service) requireDefined(ctx context.Context, workspaceID, provider string) error {
	if _, isCustom := custom.ParseID(provider); !isCustom {
		return nil
	}
	if s.custom == nil {
		return &UndefinedProviderError{ID: provider}
	}
	defined, err := s.custom.LockDefined(ctx, workspaceID, provider)
	if err != nil {
		return fmt.Errorf("lock custom provider %s: %w", provider, err)
	}
	if !defined {
		return &UndefinedProviderError{ID: provider}
	}
	return nil
}

// changeLinks is the shared body of the two link modes. Under a lock on the agent it computes
// the wanted list from the current one, checks every id against the workspace's skills, and,
// when the list changed, replaces the links, raises the version and records a snapshot.
func (s *Service) changeLinks(ctx context.Context, workspaceID, id string, next func(current []string) []string) ([]domain.SkillLink, error) {
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		agent, err := s.store.GetForUpdate(ctx, workspaceID, id)
		if err != nil {
			return agentErr(err, "lock agent")
		}
		links, err := s.links(ctx, id)
		if err != nil {
			return err
		}
		current := skillIDs(links)
		wanted := domain.Dedupe(next(current))
		known, err := s.store.KnownSkillIDs(ctx, workspaceID, wanted)
		if err != nil {
			return fmt.Errorf("look up skills of workspace %s: %w", workspaceID, err)
		}
		if unknown := domain.UnknownSkillIDs(wanted, known); len(unknown) > 0 {
			return apperr.Validation((&domain.UnknownSkillsError{IDs: unknown}).Error()).
				WithCode(CodeUnknownSkill).WithDetails(map[string]any{"skill_ids": unknown})
		}
		if slices.Equal(current, wanted) {
			return nil
		}
		if err := s.store.ReplaceSkills(ctx, id, wanted); err != nil {
			return fmt.Errorf("replace skills of agent %s: %w", id, err)
		}
		agent.Version++
		if err := s.store.Save(ctx, workspaceID, agent); err != nil {
			return fmt.Errorf("save agent %s: %w", id, err)
		}
		return s.snapshot(ctx, agent)
	})
	if err != nil {
		return nil, err
	}
	return s.links(ctx, id)
}

// snapshot records the agent's config at its current version, with the linked skill ids.
func (s *Service) snapshot(ctx context.Context, a domain.Agent) error {
	links, err := s.links(ctx, a.ID)
	if err != nil {
		return err
	}
	if err := s.store.AddVersion(ctx, a.ID, a.Version, a.Snapshot(skillIDs(links))); err != nil {
		return fmt.Errorf("snapshot agent %s v%d: %w", a.ID, a.Version, err)
	}
	return nil
}

func (s *Service) links(ctx context.Context, agentID string) ([]domain.SkillLink, error) {
	links, err := s.store.SkillLinks(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list skill links of agent %s: %w", agentID, err)
	}
	return links, nil
}

func skillIDs(links []domain.SkillLink) []string {
	ids := make([]string, len(links))
	for i, l := range links {
		ids[i] = l.SkillID
	}
	return ids
}

// agentErr turns domain.ErrNotFound into the client-visible not-found error and wraps any
// other error with what was being done.
func agentErr(err error, doing string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return apperr.NotFound(msgAgentNotFound)
	default:
		return fmt.Errorf("%s: %w", doing, err)
	}
}

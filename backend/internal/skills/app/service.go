// Package app holds the use cases of the skills context: skill CRUD with versions, the import
// preview, the statistics and the merge of accepted conventions into a skill.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/skills/domain"
)

// store is what the skill use cases need from persistence. Every method joins the transaction
// carried by ctx, if any.
type store interface {
	// List returns the workspace's skills ordered by name in byte order, each with its used_by.
	List(ctx context.Context, workspaceID string) ([]domain.Skill, error)
	// Get returns domain.ErrNotFound for an unknown id or a skill of another workspace.
	Get(ctx context.Context, workspaceID, id string) (domain.Skill, error)
	// Lock is Get with a row lock held until the transaction ends.
	Lock(ctx context.Context, workspaceID, id string) (domain.Skill, error)
	// Insert stores the skill as version 1; domain.ErrDuplicateName when the name is taken.
	Insert(ctx context.Context, in domain.NewSkill) (domain.Skill, error)
	// Update changes the non-nil fields, sets updated_at and returns the re-read skill;
	// domain.ErrDuplicateName when the new name is taken, domain.ErrNotFound for an unknown id.
	Update(ctx context.Context, workspaceID, id string, w domain.Write) (domain.Skill, error)
	InsertVersion(ctx context.Context, in domain.NewVersion) error
	// ListVersions returns the snapshots newest first.
	ListVersions(ctx context.Context, skillID string) ([]domain.Version, error)
	// GetVersion returns domain.ErrVersionNotFound when there is no such snapshot.
	GetVersion(ctx context.Context, skillID string, version int) (domain.Version, error)
	// Delete removes the skill; links, snapshots and run links go by cascade.
	// domain.ErrNotFound for an unknown id.
	Delete(ctx context.Context, workspaceID, id string) error
	// DeleteEvalCases removes the eval cases owned by the skill (they have no foreign key).
	DeleteEvalCases(ctx context.Context, workspaceID, skillID string) error
	// Agents returns the agents linking the skill, by name then id.
	Agents(ctx context.Context, skillID string) ([]domain.AgentRef, error)
	// Counters returns the statistics per skill since the given time; skillID "" means every
	// skill of the workspace. A skill without data is absent.
	Counters(ctx context.Context, workspaceID string, since time.Time, skillID string) (map[string]domain.Counters, error)
	Breakdown(ctx context.Context, workspaceID, skillID string, since time.Time) (domain.Breakdown, error)
}

// TxRunner runs a function in one database transaction; stores called with the context it
// passes join that transaction. A nested call joins the outer one. *postgres.Tx satisfies it.
type TxRunner interface {
	InTx(ctx context.Context, fn func(context.Context) error) error
}

// Service implements the skill use cases.
type Service struct {
	store store
	tx    TxRunner
	now   func() time.Time
}

// NewService builds a Service. now is the clock of the statistics window; nil means time.Now.
func NewService(store store, tx TxRunner, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, tx: tx, now: now}
}

// CreateInput is a validated POST /skills body.
type CreateInput struct {
	Name        string
	Description string
	Type        domain.SkillType
	Body        string
	// Enabled is nil when the client did not send it.
	Enabled *bool
	// Source is empty for manual.
	Source    domain.Source
	SourceRef *string
}

const (
	msgSkillNotFound   = "Skill not found"
	msgVersionNotFound = "Skill version not found"
)

func errSkillNotFound() *apperr.Error   { return apperr.NotFound(msgSkillNotFound) }
func errVersionNotFound() *apperr.Error { return apperr.NotFound(msgVersionNotFound) }

func errDuplicateName(name string) *apperr.Error {
	return apperr.Conflict(`A skill named "` + name + `" already exists in this workspace`).WithCode("conflict")
}

// List returns the workspace's skills by name.
func (s *Service) List(ctx context.Context, workspaceID string) ([]domain.Skill, error) {
	skills, err := s.store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	return skills, nil
}

// Get returns the skill, or 404 "Skill not found".
func (s *Service) Get(ctx context.Context, workspaceID, id string) (domain.Skill, error) {
	skill, err := s.store.Get(ctx, workspaceID, id)
	if err != nil {
		return domain.Skill{}, skillErr(err, "get skill")
	}
	return skill, nil
}

// skillErr maps the store's skill errors to application errors and wraps anything else.
func skillErr(err error, what string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return errSkillNotFound()
	case errors.Is(err, domain.ErrVersionNotFound):
		return errVersionNotFound()
	default:
		return fmt.Errorf("%s: %w", what, err)
	}
}

// Create stores a skill and its version-1 snapshot in one transaction. A skill that is not
// manual is stored disabled; a duplicate name is a 409.
func (s *Service) Create(ctx context.Context, workspaceID string, in CreateInput) (domain.Skill, error) {
	source := in.Source
	if source == "" {
		source = domain.SourceManual
	}
	var ref *string
	if source != domain.SourceManual {
		ref = in.SourceRef
	}
	return s.insertWithV1(ctx, domain.NewSkill{
		WorkspaceID: workspaceID,
		Name:        in.Name,
		Description: in.Description,
		Type:        in.Type,
		Source:      source,
		SourceRef:   ref,
		Body:        in.Body,
		Enabled:     domain.InitialEnabled(source, in.Enabled),
	}, domain.CreatedMessage(source, ref))
}

// createExtracted stores a skill merged from a repo's accepted conventions. Unlike an import it
// keeps the requested enabled flag: the text comes from the user's own repo and was reviewed
// before saving.
func (s *Service) createExtracted(ctx context.Context, workspaceID, repoFullName string, in CreateInput, enabled bool) (domain.Skill, error) {
	ref := domain.ExtractedSourceRef(repoFullName)
	return s.insertWithV1(ctx, domain.NewSkill{
		WorkspaceID: workspaceID,
		Name:        in.Name,
		Description: in.Description,
		Type:        in.Type,
		Source:      domain.SourceExtracted,
		SourceRef:   &ref,
		Body:        in.Body,
		Enabled:     enabled,
	}, domain.ExtractedMessage(repoFullName))
}

func (s *Service) insertWithV1(ctx context.Context, in domain.NewSkill, message string) (domain.Skill, error) {
	var out domain.Skill
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		skill, err := s.store.Insert(ctx, in)
		if err != nil {
			return err
		}
		if err := s.store.InsertVersion(ctx, domain.NewVersion{
			SkillID: skill.ID, Version: skill.Version, Body: in.Body, Description: in.Description, Message: message,
		}); err != nil {
			return err
		}
		out = skill
		return nil
	})
	if errors.Is(err, domain.ErrDuplicateName) {
		return domain.Skill{}, errDuplicateName(in.Name)
	}
	if err != nil {
		return domain.Skill{}, fmt.Errorf("create skill: %w", err)
	}
	return out, nil
}

// Update applies a partial update under a row lock. Checks, in order: unknown skill (404), stale
// base_version (409 stale_version), write, duplicate name (409). A changed body or description
// creates the next version and its snapshot; name, type and enabled change in place.
func (s *Service) Update(ctx context.Context, workspaceID, id string, patch domain.Patch) (domain.Skill, error) {
	var out domain.Skill
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.store.Lock(ctx, workspaceID, id)
		if err != nil {
			return err
		}
		if patch.BaseVersion != nil && *patch.BaseVersion != cur.Version {
			return apperr.Conflict(fmt.Sprintf("The skill changed since you opened it (you edited v%d, current is v%d)",
				*patch.BaseVersion, cur.Version)).
				WithCode("stale_version").WithDetails(map[string]int{"current_version": cur.Version})
		}
		message := patch.EditMessage(cur)
		w := domain.Write{
			Name: patch.Name, Description: patch.Description, Type: patch.Type, Body: patch.Body, Enabled: patch.Enabled,
		}
		if message != "" {
			next := cur.Version + 1
			w.Version = &next
		}
		saved, err := s.store.Update(ctx, workspaceID, id, w)
		if err != nil {
			return err
		}
		if message != "" {
			if err := s.store.InsertVersion(ctx, domain.NewVersion{
				SkillID: id, Version: saved.Version, Body: saved.Body, Description: saved.Description, Message: message,
			}); err != nil {
				return err
			}
		}
		out = saved
		return nil
	})
	if errors.Is(err, domain.ErrDuplicateName) && patch.Name != nil {
		return domain.Skill{}, errDuplicateName(*patch.Name)
	}
	if err != nil {
		return domain.Skill{}, skillErr(err, "update skill")
	}
	return out, nil
}

// Restore writes the text of a snapshot as the next version (history is never rewritten). The
// snapshot's description is used when it has one.
func (s *Service) Restore(ctx context.Context, workspaceID, id string, version int) (domain.Skill, error) {
	var out domain.Skill
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		cur, err := s.store.Lock(ctx, workspaceID, id)
		if err != nil {
			return err
		}
		snap, err := s.store.GetVersion(ctx, id, version)
		if err != nil {
			return err
		}
		next := cur.Version + 1
		description := cur.Description
		if snap.Description != nil {
			description = *snap.Description
		}
		saved, err := s.store.Update(ctx, workspaceID, id, domain.Write{Body: &snap.Body, Description: &description, Version: &next})
		if err != nil {
			return err
		}
		if err := s.store.InsertVersion(ctx, domain.NewVersion{
			SkillID: id, Version: next, Body: snap.Body, Description: description, Message: domain.RestoredMessage(version),
		}); err != nil {
			return err
		}
		out = saved
		return nil
	})
	if err != nil {
		return domain.Skill{}, skillErr(err, "restore skill")
	}
	return out, nil
}

// Delete removes the skill and the eval cases it owns in one transaction.
func (s *Service) Delete(ctx context.Context, workspaceID, id string) error {
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.store.Lock(ctx, workspaceID, id); err != nil {
			return err
		}
		if err := s.store.DeleteEvalCases(ctx, workspaceID, id); err != nil {
			return err
		}
		return s.store.Delete(ctx, workspaceID, id)
	})
	if err != nil {
		return skillErr(err, "delete skill")
	}
	return nil
}

// ListVersions returns the snapshots newest first; 404 for an unknown skill.
func (s *Service) ListVersions(ctx context.Context, workspaceID, id string) ([]domain.Version, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	versions, err := s.store.ListVersions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list skill versions: %w", err)
	}
	return versions, nil
}

// GetVersion returns one snapshot; 404 "Skill version not found" also for an unknown skill.
func (s *Service) GetVersion(ctx context.Context, workspaceID, id string, version int) (domain.Version, error) {
	if _, err := s.store.Get(ctx, workspaceID, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Version{}, errVersionNotFound()
		}
		return domain.Version{}, fmt.Errorf("get skill: %w", err)
	}
	v, err := s.store.GetVersion(ctx, id, version)
	if err != nil {
		return domain.Version{}, skillErr(err, "get skill version")
	}
	return v, nil
}

// Agents returns the agents linking the skill; 404 for an unknown skill.
func (s *Service) Agents(ctx context.Context, workspaceID, id string) ([]domain.AgentRef, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	agents, err := s.store.Agents(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list skill agents: %w", err)
	}
	return agents, nil
}

// Stats returns the usage of one skill over the last days days; 404 for an unknown skill.
func (s *Service) Stats(ctx context.Context, workspaceID, id string, days int) (domain.Stats, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return domain.Stats{}, err
	}
	since := domain.WindowStart(s.now(), days)
	counters, err := s.store.Counters(ctx, workspaceID, since, id)
	if err != nil {
		return domain.Stats{}, fmt.Errorf("skill counters: %w", err)
	}
	breakdown, err := s.store.Breakdown(ctx, workspaceID, id, since)
	if err != nil {
		return domain.Stats{}, fmt.Errorf("skill breakdown: %w", err)
	}
	usedBy, err := s.store.Agents(ctx, id)
	if err != nil {
		return domain.Stats{}, fmt.Errorf("list skill agents: %w", err)
	}
	c := counters[id]
	return domain.Stats{
		SkillID: id, WindowDays: days,
		RunsAttached: c.RunsAttached, RunsCited: c.RunsCited, PullRate: c.PullRate(),
		Findings: c.Findings, Accepted: c.Accepted, Dismissed: c.Dismissed, AcceptRate: c.AcceptRate(),
		ByCategory: nonNil(breakdown.ByCategory), BySeverity: nonNil(breakdown.BySeverity),
		UsedBy: nonNilAgents(usedBy),
	}, nil
}

// StatsSummary returns one entry per skill, in List order, for the list cards.
func (s *Service) StatsSummary(ctx context.Context, workspaceID string, days int) ([]domain.StatsSummary, error) {
	skills, err := s.store.List(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	counters, err := s.store.Counters(ctx, workspaceID, domain.WindowStart(s.now(), days), "")
	if err != nil {
		return nil, fmt.Errorf("skill counters: %w", err)
	}
	out := make([]domain.StatsSummary, len(skills))
	for i, sk := range skills {
		c := counters[sk.ID]
		out[i] = domain.StatsSummary{SkillID: sk.ID, PullRate: c.PullRate(), AcceptRate: c.AcceptRate(), Findings: c.Findings}
	}
	return out, nil
}

func nonNil(in []domain.CountBy) []domain.CountBy {
	if in == nil {
		return []domain.CountBy{}
	}
	return in
}

func nonNilAgents(in []domain.AgentRef) []domain.AgentRef {
	if in == nil {
		return []domain.AgentRef{}
	}
	return in
}

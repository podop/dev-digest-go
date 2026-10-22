package app_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

const (
	wsID    = "11111111-1111-4111-8111-111111111111"
	otherWS = "22222222-2222-4222-8222-222222222222"
)

// world is the whole in-memory state the fakes share, so one snapshot rolls back everything a
// transaction touched: skills, snapshots, agent links and convention marks.
type world struct {
	seq      int
	skills   []domain.Skill
	versions []domain.Version
	evalGone []string
	// links maps an agent id to the skill ids it links, in order.
	links map[string][]string
	// agents is every agent by id (workspace is wsID).
	agents map[string]domain.AgentRef
	// repos maps a repo id to its full name (workspace is wsID).
	repos map[string]string
	// accepted is the set of accepted convention ids; marked maps a convention to its skill.
	accepted map[string]bool
	marked   map[string]string

	counters  map[string]domain.Counters
	breakdown domain.Breakdown

	// Failure injection.
	failInsertVersion error
	failAppend        error
	failAttach        error
}

func newWorld() *world {
	return &world{
		links: map[string][]string{}, agents: map[string]domain.AgentRef{}, repos: map[string]string{},
		accepted: map[string]bool{}, marked: map[string]string{}, counters: map[string]domain.Counters{},
	}
}

// snapshot copies the state a transaction may change.
func (w *world) snapshot() world {
	c := *w
	c.skills = slices.Clone(w.skills)
	c.versions = slices.Clone(w.versions)
	c.evalGone = slices.Clone(w.evalGone)
	c.marked = maps.Clone(w.marked)
	c.links = make(map[string][]string, len(w.links))
	for k, v := range w.links {
		c.links[k] = slices.Clone(v)
	}
	return c
}

func (w *world) restore(s world) {
	w.seq, w.skills, w.versions, w.evalGone, w.links, w.marked = s.seq, s.skills, s.versions, s.evalGone, s.links, s.marked
}

// memTx is a transaction runner over a world: an error rolls the state back, a nested call joins.
type memTx struct {
	w     *world
	depth int
}

func (m *memTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	if m.depth > 0 {
		return fn(ctx)
	}
	snap := m.w.snapshot()
	m.depth++
	defer func() { m.depth-- }()
	if err := fn(ctx); err != nil {
		m.w.restore(snap)
		return err
	}
	return nil
}

// memStore implements the skill store on a world.
type memStore struct{ w *world }

func (s *memStore) index(ws, id string) int {
	return slices.IndexFunc(s.w.skills, func(k domain.Skill) bool { return k.WorkspaceID == ws && k.ID == id })
}

func (s *memStore) usedBy(id string) int {
	n := 0
	for _, ids := range s.w.links {
		if slices.Contains(ids, id) {
			n++
		}
	}
	return n
}

func (s *memStore) List(_ context.Context, ws string) ([]domain.Skill, error) {
	var out []domain.Skill
	for _, k := range s.w.skills {
		if k.WorkspaceID == ws {
			k.UsedBy = s.usedBy(k.ID)
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b domain.Skill) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *memStore) Get(_ context.Context, ws, id string) (domain.Skill, error) {
	i := s.index(ws, id)
	if i < 0 {
		return domain.Skill{}, domain.ErrNotFound
	}
	k := s.w.skills[i]
	k.UsedBy = s.usedBy(k.ID)
	return k, nil
}

func (s *memStore) Lock(ctx context.Context, ws, id string) (domain.Skill, error) {
	return s.Get(ctx, ws, id)
}

func (s *memStore) nameTaken(ws, name, exceptID string) bool {
	return slices.ContainsFunc(s.w.skills, func(k domain.Skill) bool { return k.WorkspaceID == ws && k.Name == name && k.ID != exceptID })
}

func (s *memStore) Insert(_ context.Context, in domain.NewSkill) (domain.Skill, error) {
	if s.nameTaken(in.WorkspaceID, in.Name, "") {
		return domain.Skill{}, domain.ErrDuplicateName
	}
	s.w.seq++
	now := time.Date(2026, 10, 22, 10, 0, s.w.seq, 0, time.UTC)
	k := domain.Skill{
		ID: fmt.Sprintf("skill-%d", s.w.seq), WorkspaceID: in.WorkspaceID, Name: in.Name, Description: in.Description,
		Type: in.Type, Source: in.Source, SourceRef: in.SourceRef, Body: in.Body, Enabled: in.Enabled, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	s.w.skills = append(s.w.skills, k)
	return k, nil
}

func (s *memStore) Update(ctx context.Context, ws, id string, w domain.Write) (domain.Skill, error) {
	i := s.index(ws, id)
	if i < 0 {
		return domain.Skill{}, domain.ErrNotFound
	}
	k := s.w.skills[i]
	if w.Name != nil {
		if s.nameTaken(ws, *w.Name, id) {
			return domain.Skill{}, domain.ErrDuplicateName
		}
		k.Name = *w.Name
	}
	if w.Description != nil {
		k.Description = *w.Description
	}
	if w.Type != nil {
		k.Type = *w.Type
	}
	if w.Body != nil {
		k.Body = *w.Body
	}
	if w.Enabled != nil {
		k.Enabled = *w.Enabled
	}
	if w.Version != nil {
		k.Version = *w.Version
	}
	k.UpdatedAt = k.UpdatedAt.Add(time.Second)
	s.w.skills[i] = k
	return s.Get(ctx, ws, id)
}

func (s *memStore) InsertVersion(_ context.Context, in domain.NewVersion) error {
	if s.w.failInsertVersion != nil {
		return s.w.failInsertVersion
	}
	d, m := in.Description, in.Message
	s.w.versions = append(s.w.versions, domain.Version{SkillID: in.SkillID, Version: in.Version, Body: in.Body, Description: &d, Message: &m})
	return nil
}

func (s *memStore) ListVersions(_ context.Context, skillID string) ([]domain.Version, error) {
	var out []domain.Version
	for _, v := range s.w.versions {
		if v.SkillID == skillID {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b domain.Version) int { return b.Version - a.Version })
	return out, nil
}

func (s *memStore) GetVersion(ctx context.Context, skillID string, version int) (domain.Version, error) {
	vs, _ := s.ListVersions(ctx, skillID)
	for _, v := range vs {
		if v.Version == version {
			return v, nil
		}
	}
	return domain.Version{}, domain.ErrVersionNotFound
}

func (s *memStore) Delete(_ context.Context, ws, id string) error {
	i := s.index(ws, id)
	if i < 0 {
		return domain.ErrNotFound
	}
	s.w.skills = slices.Delete(s.w.skills, i, i+1)
	for a, ids := range s.w.links {
		s.w.links[a] = slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == id })
	}
	return nil
}

func (s *memStore) DeleteEvalCases(_ context.Context, _, skillID string) error {
	s.w.evalGone = append(s.w.evalGone, skillID)
	return nil
}

func (s *memStore) Agents(_ context.Context, skillID string) ([]domain.AgentRef, error) {
	var out []domain.AgentRef
	for a, ids := range s.w.links {
		if slices.Contains(ids, skillID) {
			out = append(out, s.w.agents[a])
		}
	}
	slices.SortFunc(out, func(a, b domain.AgentRef) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *memStore) Counters(_ context.Context, _ string, _ time.Time, skillID string) (map[string]domain.Counters, error) {
	if skillID == "" {
		return maps.Clone(s.w.counters), nil
	}
	out := map[string]domain.Counters{}
	if c, ok := s.w.counters[skillID]; ok {
		out[skillID] = c
	}
	return out, nil
}

func (s *memStore) Breakdown(context.Context, string, string, time.Time) (domain.Breakdown, error) {
	return s.w.breakdown, nil
}

// FindRepo and FindAgents are the merge lookups.
func (s *memStore) FindRepo(_ context.Context, ws, repoID string) (app.RepoRef, error) {
	if name, ok := s.w.repos[repoID]; ok && ws == wsID {
		return app.RepoRef{ID: repoID, FullName: name}, nil
	}
	return app.RepoRef{}, domain.ErrRepoNotFound
}

func (s *memStore) FindAgents(_ context.Context, ws string, ids []string) ([]domain.AgentRef, error) {
	var out []domain.AgentRef
	for _, id := range ids {
		if a, ok := s.w.agents[id]; ok && ws == wsID {
			out = append(out, a)
		}
	}
	return out, nil
}

// fakeAgents is the agents context's AppendSkill on a world.
type fakeAgents struct{ w *world }

func (f fakeAgents) AppendSkill(_ context.Context, _, agentID, skillID string) error {
	if f.w.failAppend != nil {
		return f.w.failAppend
	}
	f.w.links[agentID] = append(f.w.links[agentID], skillID)
	return nil
}

// fakeConventions is the conventions context's gate on a world.
type fakeConventions struct{ w *world }

func (f fakeConventions) NotAccepted(_ context.Context, _, _ string, ids []string) ([]string, error) {
	var bad []string
	for _, id := range ids {
		if !f.w.accepted[id] {
			bad = append(bad, id)
		}
	}
	return bad, nil
}

func (f fakeConventions) AttachSkill(_ context.Context, _, _ string, ids []string, skillID string) error {
	if f.w.failAttach != nil {
		return f.w.failAttach
	}
	for _, id := range ids {
		f.w.marked[id] = skillID
	}
	return nil
}

var errBoom = errors.New("boom")

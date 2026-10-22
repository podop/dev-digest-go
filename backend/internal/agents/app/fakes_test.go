package app_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/llm"
)

// state is the data of fakeStore; fakeTx copies it to roll back.
type state struct {
	seq       int
	agents    map[string]domain.Agent // by id
	owner     map[string]string       // agent id -> workspace
	versions  map[string][]domain.Version
	links     map[string][]string // agent id -> ordered skill ids
	skills    map[string]string   // skill id -> workspace
	evalCases map[string][]string // agent id -> eval case ids
	createdBy map[string]string
}

func (s state) clone() state {
	c := s
	c.agents = maps.Clone(s.agents)
	c.owner = maps.Clone(s.owner)
	c.createdBy = maps.Clone(s.createdBy)
	c.skills = maps.Clone(s.skills)
	c.versions = make(map[string][]domain.Version, len(s.versions))
	for k, v := range s.versions {
		c.versions[k] = slices.Clone(v)
	}
	c.links = make(map[string][]string, len(s.links))
	for k, v := range s.links {
		c.links[k] = slices.Clone(v)
	}
	c.evalCases = make(map[string][]string, len(s.evalCases))
	for k, v := range s.evalCases {
		c.evalCases[k] = slices.Clone(v)
	}
	return c
}

// fakeStore is an in-memory store port.
type fakeStore struct {
	state
	failAddVersion error
	locked         []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{state: state{
		agents: map[string]domain.Agent{}, owner: map[string]string{}, versions: map[string][]domain.Version{},
		links: map[string][]string{}, skills: map[string]string{}, evalCases: map[string][]string{},
		createdBy: map[string]string{},
	}}
}

func (f *fakeStore) List(_ context.Context, ws string) ([]domain.Agent, error) {
	var out []domain.Agent
	for id, a := range f.agents {
		if f.owner[id] == ws {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, ws, id string) (domain.Agent, error) {
	a, ok := f.agents[id]
	if !ok || f.owner[id] != ws {
		return domain.Agent{}, domain.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) GetForUpdate(ctx context.Context, ws, id string) (domain.Agent, error) {
	a, err := f.Get(ctx, ws, id)
	if err == nil {
		f.locked = append(f.locked, id)
	}
	return a, err
}

func (f *fakeStore) Insert(_ context.Context, ws, createdBy string, a domain.Agent) (domain.Agent, error) {
	f.seq++
	a.ID = fmt.Sprintf("agent-%d", f.seq)
	f.agents[a.ID] = a
	f.owner[a.ID] = ws
	f.createdBy[a.ID] = createdBy
	return a, nil
}

func (f *fakeStore) Save(_ context.Context, ws string, a domain.Agent) error {
	if f.owner[a.ID] != ws {
		return domain.ErrNotFound
	}
	f.agents[a.ID] = a
	return nil
}

func (f *fakeStore) Delete(_ context.Context, ws, id string) error {
	if _, ok := f.agents[id]; !ok || f.owner[id] != ws {
		return domain.ErrNotFound
	}
	delete(f.agents, id)
	delete(f.versions, id)
	delete(f.links, id)
	return nil
}

func (f *fakeStore) DeleteEvalCases(_ context.Context, _, agentID string) error {
	delete(f.evalCases, agentID)
	return nil
}

func (f *fakeStore) AddVersion(_ context.Context, agentID string, version int, cfg domain.VersionConfig) error {
	if f.failAddVersion != nil {
		return f.failAddVersion
	}
	for _, v := range f.versions[agentID] {
		if v.Version == version {
			return nil
		}
	}
	f.versions[agentID] = append(f.versions[agentID], domain.Version{AgentID: agentID, Version: version, Config: cfg})
	return nil
}

func (f *fakeStore) Versions(_ context.Context, agentID string) ([]domain.Version, error) {
	out := slices.Clone(f.versions[agentID])
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (f *fakeStore) Version(_ context.Context, agentID string, version int) (domain.Version, error) {
	for _, v := range f.versions[agentID] {
		if v.Version == version {
			return v, nil
		}
	}
	return domain.Version{}, domain.ErrVersionNotFound
}

func (f *fakeStore) SkillLinks(_ context.Context, agentID string) ([]domain.SkillLink, error) {
	var out []domain.SkillLink
	for i, id := range f.links[agentID] {
		out = append(out, domain.SkillLink{AgentID: agentID, SkillID: id, Order: i})
	}
	return out, nil
}

func (f *fakeStore) KnownSkillIDs(_ context.Context, ws string, ids []string) (map[string]bool, error) {
	known := map[string]bool{}
	for _, id := range ids {
		if f.skills[id] == ws {
			known[id] = true
		}
	}
	return known, nil
}

func (f *fakeStore) ReplaceSkills(_ context.Context, agentID string, ids []string) error {
	f.links[agentID] = slices.Clone(ids)
	return nil
}

// fakeTx runs fn on the store and restores its data when fn fails.
type fakeTx struct {
	store *fakeStore
	depth int
}

func (t *fakeTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	if t.depth > 0 {
		return fn(ctx)
	}
	before := t.store.state.clone()
	t.depth++
	defer func() { t.depth-- }()
	if err := fn(ctx); err != nil {
		t.store.state = before
		return err
	}
	return nil
}

// fakeModels is the modelLister port.
type fakeModels struct {
	models map[llm.Provider][]llm.ModelInfo
	err    error
	asked  []llm.Provider
}

func (m *fakeModels) ListModels(_ context.Context, p llm.Provider) ([]llm.ModelInfo, error) {
	m.asked = append(m.asked, p)
	if m.err != nil {
		return nil, m.err
	}
	return m.models[p], nil
}

var errBoom = errors.New("boom")

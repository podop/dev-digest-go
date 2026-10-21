package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

// memStore is an in-memory implementation of the service and job stores.
type memStore struct {
	mu    sync.Mutex
	repos []domain.Repo
	seq   int
	// onGet runs after every GetByID, to simulate concurrent changes.
	onGet func()
}

func (m *memStore) Add(_ context.Context, in app.NewRepo) (domain.Repo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repos {
		if r.WorkspaceID == in.WorkspaceID && r.FullName == in.Ref.FullName() {
			return r, false, nil
		}
	}
	m.seq++
	by := in.CreatedBy
	r := domain.Repo{
		ID: fmt.Sprintf("id-%d", m.seq), WorkspaceID: in.WorkspaceID, Owner: in.Ref.Owner, Name: in.Ref.Name,
		FullName: in.Ref.FullName(), DefaultBranch: domain.DefaultBranch, CreatedBy: &by,
	}
	m.repos = append(m.repos, r)
	return r, true, nil
}

func (m *memStore) List(_ context.Context, ws string) ([]domain.Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Repo
	for _, r := range m.repos {
		if r.WorkspaceID == ws {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) Get(_ context.Context, ws, id string) (domain.Repo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.repos {
		if r.WorkspaceID == ws && r.ID == id {
			return r, nil
		}
	}
	return domain.Repo{}, domain.ErrNotFound
}

func (m *memStore) GetByID(_ context.Context, id string) (domain.Repo, error) {
	m.mu.Lock()
	var found *domain.Repo
	for i := range m.repos {
		if m.repos[i].ID == id {
			r := m.repos[i]
			found = &r
		}
	}
	hook := m.onGet
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	if found == nil {
		return domain.Repo{}, domain.ErrNotFound
	}
	return *found, nil
}

func (m *memStore) Delete(_ context.Context, ws, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.repos {
		if r.WorkspaceID == ws && r.ID == id {
			m.repos = append(m.repos[:i], m.repos[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *memStore) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.repos {
		if r.ID == id {
			m.repos = append(m.repos[:i], m.repos[i+1:]...)
			return
		}
	}
}

func (m *memStore) update(id string, fn func(*domain.Repo)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.repos {
		if m.repos[i].ID == id {
			fn(&m.repos[i])
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *memStore) SetCloned(_ context.Context, id, path string, at time.Time) error {
	return m.update(id, func(r *domain.Repo) { r.ClonePath, r.LastPolledAt = &path, &at })
}

func (m *memStore) SetPolled(_ context.Context, id string, at time.Time) error {
	return m.update(id, func(r *domain.Repo) { r.LastPolledAt = &at })
}

func (m *memStore) get(id string) domain.Repo {
	r, _ := m.GetByID(context.Background(), id)
	return r
}

type enqueued struct {
	WorkspaceID string
	Kind        string
	Payload     any
}

type fakeQueue struct {
	jobs []enqueued
	err  error
}

func (q *fakeQueue) Enqueue(_ context.Context, ws, kind string, payload any) (string, error) {
	if q.err != nil {
		return "", q.err
	}
	q.jobs = append(q.jobs, enqueued{ws, kind, payload})
	return fmt.Sprintf("job-%d", len(q.jobs)), nil
}

// fakeGit records the calls the jobs make.
type fakeGit struct {
	cloneURL, cloneOwner, cloneName string
	cloneDepth                      int
	cloneDir                        string
	cloneErr                        error
	onClone                         func()

	defaultBranch    string
	defaultBranchErr error
	syncBranch       string
	syncErr          error
	synced           bool
}

func (g *fakeGit) Clone(_ context.Context, owner, name, url string, opts gitcli.CloneOptions) (string, error) {
	g.cloneOwner, g.cloneName, g.cloneURL, g.cloneDepth = owner, name, url, opts.Depth
	if g.onClone != nil {
		g.onClone()
	}
	if g.cloneErr != nil {
		return "", g.cloneErr
	}
	return g.cloneDir, nil
}

func (g *fakeGit) DefaultBranch(context.Context, string, string) (string, error) {
	return g.defaultBranch, g.defaultBranchErr
}

func (g *fakeGit) Sync(_ context.Context, _, _, branch string) (string, error) {
	g.synced, g.syncBranch = true, branch
	return "sha", g.syncErr
}

var errBoom = errors.New("boom")

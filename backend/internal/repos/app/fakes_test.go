package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"dev-digest-go/backend/internal/platform/codehost"
	"dev-digest-go/backend/internal/platform/gitcli"
	"dev-digest-go/backend/internal/platform/gitlab"
	"dev-digest-go/backend/internal/repos/app"
	"dev-digest-go/backend/internal/repos/domain"
)

// memStore is an in-memory implementation of the service and job stores.
type memStore struct {
	mu    sync.Mutex
	repos []domain.Repo
	// adds records every NewRepo the store was asked to insert.
	adds []app.NewRepo
	seq  int
	// onGet runs after every GetByID, to simulate concurrent changes.
	onGet func()
}

func (m *memStore) Add(_ context.Context, in app.NewRepo) (domain.Repo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.adds = append(m.adds, in)
	for _, r := range m.repos {
		if r.WorkspaceID == in.WorkspaceID && r.Host == in.Host &&
			in.Provider == codehost.GitLab && r.ProjectID == in.ProjectID {
			return r, false, nil
		}
	}
	for _, r := range m.repos {
		if r.WorkspaceID == in.WorkspaceID && r.Host == in.Host && r.FullName == in.Ref.FullName() {
			return r, false, nil
		}
	}
	m.seq++
	by := in.CreatedBy
	branch := in.DefaultBranch
	if branch == "" {
		branch = domain.DefaultBranch
	}
	r := domain.Repo{
		ID: fmt.Sprintf("id-%d", m.seq), WorkspaceID: in.WorkspaceID, Owner: in.Ref.Owner, Name: in.Ref.Name,
		FullName: in.Ref.FullName(), DefaultBranch: branch, CreatedBy: &by,
		Provider: in.Provider, Host: in.Host, ProjectID: in.ProjectID, WebURL: in.WebURL,
	}
	m.repos = append(m.repos, r)
	return r, true, nil
}

func (m *memStore) UpdatePath(_ context.Context, ws, id string, p app.ProjectPath) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target := -1
	for i, r := range m.repos {
		if r.WorkspaceID == ws && r.ID == id {
			target = i
		}
	}
	if target < 0 {
		return "", domain.ErrNotFound
	}
	for _, r := range m.repos {
		if r.WorkspaceID == ws && r.Host == m.repos[target].Host && r.ID != id && r.FullName == p.FullName {
			return r.ID, nil
		}
	}
	m.repos[target].FullName, m.repos[target].Owner, m.repos[target].Name, m.repos[target].WebURL =
		p.FullName, p.Owner, p.Name, p.WebURL
	return "", nil
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

	remoteCalls []gitlabCall
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

// gitlabCall records one CloneRemote, DefaultBranchRemote or SyncRemote call.
type gitlabCall struct {
	Kind          string
	First, Second string
	Branch        string
	Remote        gitcli.Remote
}

func (g *fakeGit) CloneRemote(_ context.Context, first, second string, r gitcli.Remote, opts gitcli.CloneOptions) (string, error) {
	g.remoteCalls = append(g.remoteCalls, gitlabCall{Kind: "clone", First: first, Second: second, Remote: r})
	g.cloneDepth = opts.Depth
	return g.cloneDir, g.cloneErr
}

func (g *fakeGit) DefaultBranchRemote(_ context.Context, first, second string, r gitcli.Remote) (string, error) {
	g.remoteCalls = append(g.remoteCalls, gitlabCall{Kind: "default-branch", First: first, Second: second, Remote: r})
	return g.defaultBranch, g.defaultBranchErr
}

func (g *fakeGit) SyncRemote(_ context.Context, first, second, branch string, r gitcli.Remote) (string, error) {
	g.remoteCalls = append(g.remoteCalls, gitlabCall{Kind: "sync", First: first, Second: second, Branch: branch, Remote: r})
	return "sha", g.syncErr
}

// fakeConns is the connection port: conns by host key; resolveErr fails Resolve.
type fakeConns struct {
	conns      map[string]app.GitLabConn
	resolveErr error
	resolved   []string
}

func (f *fakeConns) Resolve(_ context.Context, hostKey string) (app.GitLabConn, bool, error) {
	f.resolved = append(f.resolved, hostKey)
	if f.resolveErr != nil {
		return app.GitLabConn{}, false, f.resolveErr
	}
	c, ok := f.conns[hostKey]
	return c, ok, nil
}

func (f *fakeConns) HostKeysByName(_ context.Context, name string) ([]string, error) {
	var keys []string
	for key := range f.conns {
		if n, _, _ := strings.Cut(key, ":"); n == name {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

// fakeProjects is the GitLab project port: it records every call and answers project or err.
type fakeProjects struct {
	project gitlab.Project
	err     error
	calls   []projectCall
}

type projectCall struct {
	Conn gitlab.Conn
	Path string
}

func (f *fakeProjects) ProjectByPath(_ context.Context, conn gitlab.Conn, path string) (gitlab.Project, error) {
	f.calls = append(f.calls, projectCall{conn, path})
	return f.project, f.err
}

var errBoom = errors.New("boom")

// fakeTrigger records the repo-intel triggers; err makes every one fail.
type fakeTrigger struct {
	calls []string
	err   error
}

func (f *fakeTrigger) TriggerIndex(_ context.Context, ws, repoID, owner, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("index %s %s %s/%s", ws, repoID, owner, name))
	return f.err
}

func (f *fakeTrigger) TriggerRefresh(_ context.Context, ws, repoID string) error {
	f.calls = append(f.calls, fmt.Sprintf("refresh %s %s", ws, repoID))
	return f.err
}

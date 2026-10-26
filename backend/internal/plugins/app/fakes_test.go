package app_test

import (
	"context"
	"encoding/json"
	"slices"

	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
)

// fakeReader returns canned export data and records the selection it was asked for.
type fakeReader struct {
	data    domain.ExportData
	missing []string
	err     error
	calls   []app.ExportSelection
}

func (f *fakeReader) ReadExport(_ context.Context, _ string, sel app.ExportSelection) (domain.ExportData, []string, error) {
	f.calls = append(f.calls, sel)
	return f.data, f.missing, f.err
}

// fakeNames reports canned conflicts and records the names it was asked about.
type fakeNames struct {
	found  domain.Conflicts
	err    error
	plugin []string
	skills [][]string
}

func (f *fakeNames) Conflicts(_ context.Context, _, pluginName string, skillNames []string) (domain.Conflicts, error) {
	f.plugin = append(f.plugin, pluginName)
	f.skills = append(f.skills, skillNames)
	return f.found, f.err
}

// fakeInstaller records the plans it installs; err (when set) is returned instead, e.g. the
// *domain.ConflictError of a lost race.
type fakeInstaller struct {
	plans  []domain.Install
	err    error
	nextID int
}

func (f *fakeInstaller) Install(_ context.Context, _ string, in domain.Install) (domain.Installed, error) {
	if f.err != nil {
		return domain.Installed{}, f.err
	}
	f.plans = append(f.plans, in)
	f.nextID++
	created := in.Created()
	return domain.Installed{
		Plugin: domain.Plugin{
			ID: "p" + string(rune('0'+f.nextID)), Name: in.Name,
			Counts: domain.PluginCounts{Agents: created.Agents, Skills: created.Skills, EvalCases: created.EvalCases, Learnings: created.Learnings},
		},
		Created: created,
	}, nil
}

// fakePlugins is an in-memory plugin list.
type fakePlugins struct {
	rows    []domain.Plugin
	listErr error
	removed domain.Removed
	removes int
}

func (f *fakePlugins) List(context.Context, string) ([]domain.Plugin, error) {
	return slices.Clone(f.rows), f.listErr
}

func (f *fakePlugins) SetEnabled(_ context.Context, _, id string, enabled bool) (domain.Plugin, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows[i].Enabled = enabled
			return f.rows[i], nil
		}
	}
	return domain.Plugin{}, domain.ErrNotFound
}

func (f *fakePlugins) Remove(_ context.Context, _, id string, check func(name string) error) (domain.Removed, error) {
	for i, p := range f.rows {
		if p.ID != id {
			continue
		}
		if err := check(p.Name); err != nil {
			return domain.Removed{}, err
		}
		f.rows = slices.Delete(f.rows, i, i+1)
		f.removes++
		return f.removed, nil
	}
	return domain.Removed{}, domain.ErrNotFound
}

// fakeLastExports keeps the last export in memory.
type fakeLastExports struct {
	raw    json.RawMessage
	found  bool
	getErr error
	setErr error
	sets   int
	users  []string
}

func (f *fakeLastExports) LastExport(context.Context, string) (json.RawMessage, bool, error) {
	return f.raw, f.found, f.getErr
}

func (f *fakeLastExports) SetLastExport(_ context.Context, _, userID string, raw json.RawMessage) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.raw, f.found = raw, true
	f.sets++
	f.users = append(f.users, userID)
	return nil
}

// fakeWorkspace is the workspace name port.
type fakeWorkspace struct {
	name  string
	found bool
	err   error
}

func (f fakeWorkspace) WorkspaceName(context.Context, string) (string, bool, error) {
	return f.name, f.found, f.err
}

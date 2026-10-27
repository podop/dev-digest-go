package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

const (
	hostA = "gitlab.example.com"
	hostB = "git.corp.example:8443"
)

// connect stores a GitLab connection for host (base URL https://<host>).
func (f fixture) connect(t *testing.T, host string) {
	t.Helper()
	f.scan(t, `INSERT INTO gitlab_connections (name, base_url, host) VALUES (?, ?, ?) RETURNING id`, "conn "+host, "https://"+host, host)
}

// glRepo stores a GitLab repository of the workspace (the bound repository of an installation).
func (f fixture) glRepo(t *testing.T, ws, host string, projectID int64, fullName string) {
	t.Helper()
	f.scan(t, `INSERT INTO repos (workspace_id, owner, name, full_name, provider, host, project_id, web_url)
		VALUES (?, 'o', 'n', ?, 'gitlab', ?, ?, ?) RETURNING id`, ws, fullName, host, projectID, "https://"+host+"/"+fullName)
}

func glInstall(agentID, host string, projectID int64, repo, slug string) app.NewGitLabInstallation {
	return app.NewGitLabInstallation{
		AgentID: agentID, Host: host, ProjectID: projectID, Repo: repo, Slug: slug,
		ManifestVersion: 2, Provider: domain.ProviderOpenRouter,
	}
}

// glInstallID upserts the installation and returns its id.
func (f fixture) glInstallID(t *testing.T, in app.NewGitLabInstallation) string {
	t.Helper()
	if err := f.store.UpsertGitLabInstallation(t.Context(), in); err != nil {
		t.Fatalf("UpsertGitLabInstallation(%+v) = %v, want nil", in, err)
	}
	return f.scan(t, `SELECT id FROM ci_installations WHERE agent_id = ? AND host = ? AND project_id = ?`, in.AgentID, in.Host, in.ProjectID)
}

func glRun(f fixture, inst domain.Installation, jobID int64, host string, at time.Time) domain.GitLabNewRun {
	return domain.GitLabNewRun{
		NewRun: domain.NewRun{
			WorkspaceID: f.ws, InstallationID: inst.ID, AgentID: inst.AgentID,
			GithubRunID: jobID, RunAttempt: domain.GitLabAttempt,
			HeadSHA: "abc123", GithubURL: fmt.Sprintf("https://%s/%s/-/jobs/%d", host, inst.Repo, jobID),
			RanAt: at, PRNumber: ptr(12), ManifestVersion: ptr(2),
			Status: domain.RunSucceeded, Model: ptr("m"), CostUSD: ptr(0.5), DurationMS: ptr(100),
			FindingsCount: 1, Blockers: 0,
		},
		Host: host, Source: domain.RunSourceGitLab,
	}
}

func (f fixture) glInstallation(t *testing.T, agentID, host string, projectID int64) domain.Installation {
	t.Helper()
	got, ok, err := f.store.FindGitLabInstallation(t.Context(), f.ws, agentID, host, projectID)
	if err != nil || !ok {
		t.Fatalf("FindGitLabInstallation(%s, %d) = _, %v, %v, want found", host, projectID, ok, err)
	}
	return got
}

func TestUpsertGitLabInstallation(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	id := f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/web", "alpha"))
	var first time.Time
	if err := f.db.Raw(`SELECT installed_at FROM ci_installations WHERE id = ?`, id).Scan(&first).Error; err != nil {
		t.Fatal(err)
	}

	// A re-export after a rename and a provider change updates the row in place.
	again := glInstall(f.agentA, hostA, 7, "grp/renamed", "alpha-2")
	again.ManifestVersion, again.Provider = 3, domain.ProviderAnthropic
	if err := f.store.UpsertGitLabInstallation(ctx, again); err != nil {
		t.Fatalf("UpsertGitLabInstallation(update) = %v, want nil", err)
	}
	got := f.glInstallation(t, f.agentA, hostA, 7)
	want := domain.Installation{
		ID: id, AgentID: f.agentA, Repo: "grp/renamed", TargetType: domain.TargetGitLabCI, InstalledAt: first.UTC(),
		Slug: ptr("alpha-2"), ManifestVersion: ptr(3), Provider: domain.ProviderAnthropic, Host: hostA, ProjectID: 7,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindGitLabInstallation() mismatch (-want +got):\n%s", diff)
	}
	if n := f.count(t, "ci_installations"); n != 1 {
		t.Errorf("ci_installations rows = %d, want 1", n)
	}

	owner, found, err := f.store.GitLabSlugOwner(ctx, hostA, 7, "alpha-2")
	if err != nil || !found || owner != f.agentA {
		t.Errorf("GitLabSlugOwner(alpha-2) = %q, %v, %v, want %q, true, nil", owner, found, err, f.agentA)
	}
	for name, probe := range map[string]struct {
		host string
		pid  int64
		slug string
	}{
		"old slug":      {hostA, 7, "alpha"},
		"other project": {hostA, 8, "alpha-2"},
		"other host":    {hostB, 7, "alpha-2"},
	} {
		if _, found, err := f.store.GitLabSlugOwner(ctx, probe.host, probe.pid, probe.slug); err != nil || found {
			t.Errorf("GitLabSlugOwner(%s) = _, %v, %v, want false, nil", name, found, err)
		}
	}
	if _, ok, err := f.store.FindGitLabInstallation(ctx, f.ws, f.agentB, hostA, 7); err != nil || ok {
		t.Errorf("FindGitLabInstallation(other agent) = _, %v, %v, want false, nil", ok, err)
	}
	if _, ok, err := f.store.FindGitLabInstallation(ctx, f.other, f.agentA, hostA, 7); err != nil || ok {
		t.Errorf("FindGitLabInstallation(other workspace) = _, %v, %v, want false, nil", ok, err)
	}
}

// The identity of a GitLab installation is (agent, host, project id): the same agent exports to
// several projects and hosts, and to a GitHub repository with the same path.
func TestUpsertGitLabInstallation_IdentityPerHostAndProject(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.install(t, f.agentA, "acme/web", "alpha", 1)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "acme/web", "alpha"))
	f.glInstallID(t, glInstall(f.agentA, hostA, 8, "acme/web", "alpha")) // same path text, other project
	f.glInstallID(t, glInstall(f.agentA, hostB, 7, "acme/web", "alpha")) // same project id, other host
	if n := f.count(t, "ci_installations"); n != 4 {
		t.Fatalf("ci_installations rows = %d, want 4", n)
	}

	// The GitHub upsert and lookups never see the GitLab rows with the same repo text and slug.
	if err := f.store.UpsertInstallation(ctx, domain.NewInstallation{AgentID: f.agentA, Repo: "acme/web", Slug: "alpha-2", ManifestVersion: 5, Provider: domain.ProviderOpenAI}); err != nil {
		t.Fatalf("UpsertInstallation(github, same repo text) = %v, want nil", err)
	}
	if n := f.count(t, "ci_installations"); n != 4 {
		t.Errorf("ci_installations rows after the GitHub re-export = %d, want 4", n)
	}
	if got := f.glInstallation(t, f.agentA, hostA, 7); *got.Slug != "alpha" || *got.ManifestVersion != 2 {
		t.Errorf("GitLab installation after the GitHub re-export = %+v, want slug alpha, version 2", got)
	}
	gh, ok, err := f.store.FindInstallation(ctx, f.ws, f.agentA, "acme/web")
	if err != nil || !ok || gh.Host != "github.com" || gh.TargetType != "gha" || *gh.Slug != "alpha-2" || gh.ProjectID != 0 {
		t.Errorf("FindInstallation(acme/web) = %+v, %v, %v, want the gha row on github.com", gh, ok, err)
	}
	if owner, found, err := f.store.SlugOwner(ctx, "acme/web", "alpha"); err != nil || found {
		t.Errorf("SlugOwner(acme/web, alpha) = %q, %v, %v, want none (only GitLab rows have it)", owner, found, err)
	}

	// A GitLab upsert leaves the GitHub row alone.
	again := glInstall(f.agentA, hostA, 7, "acme/web", "alpha-3")
	if err := f.store.UpsertGitLabInstallation(ctx, again); err != nil {
		t.Fatalf("UpsertGitLabInstallation(update) = %v, want nil", err)
	}
	if gh, _, _ := f.store.FindInstallation(ctx, f.ws, f.agentA, "acme/web"); *gh.Slug != "alpha-2" {
		t.Errorf("GitHub slug after a GitLab upsert = %q, want alpha-2", *gh.Slug)
	}
}

func TestUpsertGitLabInstallation_SlugTaken(t *testing.T) {
	f := newFixture(t)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/web", "shared"))
	in := glInstall(f.agentB, hostA, 7, "grp/web", "shared")

	if err := f.store.UpsertGitLabInstallation(t.Context(), in); !errors.Is(err, app.ErrSlugTaken) {
		t.Fatalf("UpsertGitLabInstallation(taken slug) = %v, want ErrSlugTaken", err)
	}
	if n := f.count(t, "ci_installations"); n != 1 {
		t.Errorf("ci_installations rows = %d, want 1", n)
	}

	// Inside a transaction the lost race does not abort it.
	err := platformpg.NewTx(f.db).InTx(t.Context(), func(ctx context.Context) error {
		if err := f.store.UpsertGitLabInstallation(ctx, in); !errors.Is(err, app.ErrSlugTaken) {
			t.Errorf("UpsertGitLabInstallation(taken slug in tx) = %v, want ErrSlugTaken", err)
		}
		in.Slug = "free"
		return f.store.UpsertGitLabInstallation(ctx, in)
	})
	if err != nil {
		t.Fatalf("transaction after ErrSlugTaken = %v, want nil", err)
	}

	// The same slug is free in another project, on another host and among GitHub repositories.
	for name, other := range map[string]app.NewGitLabInstallation{
		"other project": glInstall(f.agentB, hostA, 8, "grp/other", "shared"),
		"other host":    glInstall(f.agentB, hostB, 7, "grp/web", "shared"),
	} {
		if err := f.store.UpsertGitLabInstallation(t.Context(), other); err != nil {
			t.Errorf("UpsertGitLabInstallation(%s) = %v, want nil", name, err)
		}
	}
	f.install(t, f.agentB, "grp/web", "shared", 1)
}

func TestUpsertGitLabInstallation_NotStorable(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tests := []struct {
		name     string
		mutate   func(*app.NewGitLabInstallation)
		notStore bool
	}{
		{"NUL in repo", func(in *app.NewGitLabInstallation) { in.Repo = "grp/\x00web" }, true},
		{"unknown provider", func(in *app.NewGitLabInstallation) { in.Provider = "bogus" }, true},
		{"version overflow", func(in *app.NewGitLabInstallation) { in.ManifestVersion = 1 << 40 }, true},
		{"github host", func(in *app.NewGitLabInstallation) { in.Host = "github.com" }, false},
		{"empty host", func(in *app.NewGitLabInstallation) { in.Host = "" }, false},
		{"upper-case host", func(in *app.NewGitLabInstallation) { in.Host = "GitLab.example.com" }, false},
		{"zero project id", func(in *app.NewGitLabInstallation) { in.ProjectID = 0 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := glInstall(f.agentA, hostA, 7, "grp/web", "alpha")
			tc.mutate(&in)
			err := f.store.UpsertGitLabInstallation(ctx, in)
			if err == nil {
				t.Fatalf("UpsertGitLabInstallation(%s) = nil, want an error", tc.name)
			}
			if got := errors.Is(err, app.ErrNotStorable); got != tc.notStore {
				t.Errorf("UpsertGitLabInstallation(%s) = %v, ErrNotStorable = %v, want %v", tc.name, err, got, tc.notStore)
			}
		})
	}
	if n := f.count(t, "ci_installations"); n != 0 {
		t.Errorf("ci_installations rows = %d, want 0", n)
	}
}

// EC30: a GitLab job id equal to a stored GitHub run id (and to a job id of another instance) is a
// different run.
func TestRecordGitLabRun_SameIDOnTwoHosts(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	gh := f.install(t, f.agentA, "acme/api", "alpha", 3)
	if rec, err := f.store.RecordRun(ctx, newRun(f, gh, f.agentA, 555, 1, t0)); err != nil || !rec.Stored {
		t.Fatalf("RecordRun(github 555) = %+v, %v, want Stored", rec, err)
	}
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/web", "alpha"))
	f.glInstallID(t, glInstall(f.agentA, hostB, 7, "grp/web", "alpha"))
	instA := f.glInstallation(t, f.agentA, hostA, 7)
	instB := f.glInstallation(t, f.agentA, hostB, 7)

	for _, run := range []domain.GitLabNewRun{glRun(f, instA, 555, hostA, t0), glRun(f, instB, 555, hostB, t0)} {
		rec, err := f.store.RecordGitLabRun(ctx, run)
		if err != nil || !rec.Stored || rec.AgentRunID == "" || rec.Provider != domain.ProviderOpenRouter {
			t.Fatalf("RecordGitLabRun(%s 555) = %+v, %v, want Stored with the installation's provider", run.Host, rec, err)
		}
	}
	if n := f.count(t, "ci_runs"); n != 3 {
		t.Fatalf("ci_runs rows = %d, want 3 (one GitHub run and two GitLab jobs with the same id)", n)
	}
	if n := f.count(t, "agent_runs"); n != 3 {
		t.Errorf("agent_runs rows = %d, want 3", n)
	}

	// The same (host, job id, attempt) again stores nothing.
	rec, err := f.store.RecordGitLabRun(ctx, glRun(f, instA, 555, hostA, t0.Add(time.Hour)))
	if err != nil || rec.Stored || rec.AgentRunID != "" {
		t.Errorf("RecordGitLabRun(duplicate) = %+v, %v, want not Stored", rec, err)
	}
	if n, a := f.count(t, "ci_runs"), f.count(t, "agent_runs"); n != 3 || a != 3 {
		t.Errorf("rows after the duplicate ci_runs, agent_runs = %d, %d, want 3, 3", n, a)
	}

	var row struct {
		Host, Source, URL, Head string
		RunID                   int64
		Attempt                 int
		PR                      int
	}
	err = f.db.Raw(`SELECT host, source, github_url AS url, head_sha AS head, github_run_id AS run_id, run_attempt AS attempt, pr_number AS pr
		FROM ci_runs WHERE host = ?`, hostB).Scan(&row).Error
	if err != nil {
		t.Fatal(err)
	}
	want := struct {
		Host, Source, URL, Head string
		RunID                   int64
		Attempt                 int
		PR                      int
	}{hostB, "gitlab_ci", "https://" + hostB + "/grp/web/-/jobs/555", "abc123", 555, 1, 12}
	if row != want {
		t.Errorf("GitLab ci_runs row = %+v, want %+v", row, want)
	}

	keys := []domain.RunKey{{GithubRunID: 555, Attempt: 1}, {GithubRunID: 556, Attempt: 1}, {GithubRunID: 555, Attempt: 2}}
	onlyFirst := map[domain.RunKey]bool{keys[0]: true}
	for name, load := range map[string]func() (map[domain.RunKey]bool, error){
		"github": func() (map[domain.RunKey]bool, error) { return f.store.StoredRunKeys(ctx, keys) },
		"host a": func() (map[domain.RunKey]bool, error) { return f.store.StoredGitLabRunKeys(ctx, hostA, keys) },
		"host b": func() (map[domain.RunKey]bool, error) { return f.store.StoredGitLabRunKeys(ctx, hostB, keys) },
	} {
		got, err := load()
		if err != nil || !cmp.Equal(onlyFirst, got) {
			t.Errorf("stored keys (%s) = %v, %v, want %v", name, got, err, onlyFirst)
		}
	}
	if got, err := f.store.StoredGitLabRunKeys(ctx, "other.example.com", keys); err != nil || len(got) != 0 {
		t.Errorf("StoredGitLabRunKeys(other host) = %v, %v, want none", got, err)
	}
	if got, err := f.store.StoredGitLabRunKeys(ctx, hostA, nil); err != nil || len(got) != 0 {
		t.Errorf("StoredGitLabRunKeys(no keys) = %v, %v, want none", got, err)
	}
}

// A GitHub run id that is stored on the GitLab side only is not reported as a stored GitHub run.
func TestStoredRunKeys_IgnoresGitLabRuns(t *testing.T) {
	f := newFixture(t)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/web", "alpha"))
	inst := f.glInstallation(t, f.agentA, hostA, 7)
	if _, err := f.store.RecordGitLabRun(t.Context(), glRun(f, inst, 900, hostA, t0)); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.StoredRunKeys(t.Context(), []domain.RunKey{{GithubRunID: 900, Attempt: 1}})
	if err != nil || len(got) != 0 {
		t.Errorf("StoredRunKeys(gitlab job id) = %v, %v, want none", got, err)
	}
}

func TestRecordGitLabRun_JoinsCallerTransaction(t *testing.T) {
	f := newFixture(t)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/web", "alpha"))
	inst := f.glInstallation(t, f.agentA, hostA, 7)
	sentinel := errors.New("roll back")
	err := platformpg.NewTx(f.db).InTx(t.Context(), func(ctx context.Context) error {
		rec, err := f.store.RecordGitLabRun(ctx, glRun(f, inst, 1, hostA, t0))
		if err != nil || !rec.Stored {
			t.Errorf("RecordGitLabRun() = %+v, %v, want Stored", rec, err)
		}
		keys, err := f.store.StoredGitLabRunKeys(ctx, hostA, []domain.RunKey{{GithubRunID: 1, Attempt: 1}})
		if err != nil || len(keys) != 1 {
			t.Errorf("StoredGitLabRunKeys() in the caller tx = %v, %v, want the run", keys, err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx() = %v, want the sentinel", err)
	}
	if ci, ag := f.count(t, "ci_runs"), f.count(t, "agent_runs"); ci != 0 || ag != 0 {
		t.Errorf("rows after rollback ci_runs, agent_runs = %d, %d, want 0, 0", ci, ag)
	}
}

func TestRecordGitLabRun_UsesInstallationProviderAndFailedStatus(t *testing.T) {
	f := newFixture(t)
	in := glInstall(f.agentA, hostA, 7, "grp/web", "alpha")
	in.Provider = "custom:acme-llm"
	f.glInstallID(t, in)
	inst := f.glInstallation(t, f.agentA, hostA, 7)
	run := glRun(f, inst, 2, hostA, t0)
	run.Status, run.Error, run.CostUSD, run.Model, run.PRNumber = domain.RunFailed, ptr("job failed"), nil, nil, nil
	rec, err := f.store.RecordGitLabRun(t.Context(), run)
	if err != nil || !rec.Stored || rec.Provider != "custom:acme-llm" {
		t.Fatalf("RecordGitLabRun(failed) = %+v, %v, want Stored with provider custom:acme-llm", rec, err)
	}
	var agentRun struct{ Provider, Status, Source string }
	if err := f.db.Raw(`SELECT provider, status, source FROM agent_runs WHERE id = ?`, rec.AgentRunID).Scan(&agentRun).Error; err != nil {
		t.Fatal(err)
	}
	if want := (struct{ Provider, Status, Source string }{"custom:acme-llm", "failed", "ci"}); agentRun != want {
		t.Errorf("agent_runs row = %+v, want %+v", agentRun, want)
	}
}

func TestRecordGitLabRun_Refused(t *testing.T) {
	f := newFixture(t)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/web", "alpha"))
	inst := f.glInstallation(t, f.agentA, hostA, 7)
	tests := []struct {
		name     string
		mutate   func(*domain.GitLabNewRun)
		notStore bool
	}{
		{"github host", func(r *domain.GitLabNewRun) { r.Host = "github.com" }, false},
		{"empty host", func(r *domain.GitLabNewRun) { r.Host = "" }, false},
		{"wrong source", func(r *domain.GitLabNewRun) { r.Source = "gha" }, false},
		{"NUL in sha", func(r *domain.GitLabNewRun) { r.HeadSHA = "a\x00b" }, true},
		{"attempt overflow", func(r *domain.GitLabNewRun) { r.RunAttempt = 1 << 40 }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := glRun(f, inst, 3, hostA, t0)
			tc.mutate(&run)
			_, err := f.store.RecordGitLabRun(t.Context(), run)
			if err == nil {
				t.Fatalf("RecordGitLabRun(%s) = nil, want an error", tc.name)
			}
			if got := errors.Is(err, app.ErrNotStorable); got != tc.notStore {
				t.Errorf("RecordGitLabRun(%s) = %v, ErrNotStorable = %v, want %v", tc.name, err, got, tc.notStore)
			}
		})
	}
	if _, err := f.store.StoredGitLabRunKeys(t.Context(), "github.com", []domain.RunKey{{GithubRunID: 1, Attempt: 1}}); err == nil {
		t.Error("StoredGitLabRunKeys(github.com) = nil, want an error")
	}
	if n := f.count(t, "ci_runs") + f.count(t, "agent_runs"); n != 0 {
		t.Errorf("rows after the refused runs = %d, want 0", n)
	}
}

func TestListGitLabInstallations(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.install(t, f.agentA, "acme/web", "alpha", 1)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/old-name", "alpha"))
	f.glInstallID(t, glInstall(f.agentB, hostB, 9, "ops/api", "beta"))
	// The workspace's repository was renamed after the export: the ingest still gets the stored path.
	f.glRepo(t, f.ws, hostA, 7, "grp/new-name")
	// Another workspace's agent never shows.
	other := glInstall(f.agentO, hostA, 70, "x/y", "outsider")
	f.glInstallID(t, other)

	inst := f.glInstallation(t, f.agentA, hostA, 7)
	if _, err := f.store.RecordGitLabRun(ctx, glRun(f, inst, 41, hostA, t0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecordGitLabRun(ctx, glRun(f, inst, 42, hostA, t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	got, err := f.store.ListGitLabInstallations(ctx, f.ws, app.InstallationFilter{})
	if err != nil {
		t.Fatalf("ListGitLabInstallations() = _, %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListGitLabInstallations() = %d installations, want 2 (no GitHub, no foreign)", len(got))
	}
	byAgent := map[string]domain.Installation{}
	for _, g := range got {
		byAgent[g.AgentID] = g
		if g.TargetType != domain.TargetGitLabCI || g.ProjectID == 0 || g.Host == "" {
			t.Errorf("ListGitLabInstallations() row = %+v, want gitlab_ci with host and project id", g)
		}
	}
	a := byAgent[f.agentA]
	if a.Repo != "grp/old-name" || a.Host != hostA || a.ProjectID != 7 || a.LastRun == nil || *a.LastRun.GithubURL != "https://"+hostA+"/grp/old-name/-/jobs/42" {
		t.Errorf("agent A installation = %+v (last run %+v), want the stored path, host, project 7 and job 42 as the newest run", a, a.LastRun)
	}
	if b := byAgent[f.agentB]; b.Host != hostB || b.ProjectID != 9 || b.LastRun != nil {
		t.Errorf("agent B installation = %+v, want host %s, project 9, no run", b, hostB)
	}

	filtered, err := f.store.ListGitLabInstallations(ctx, f.ws, app.InstallationFilter{AgentID: f.agentB})
	if err != nil || len(filtered) != 1 || filtered[0].AgentID != f.agentB {
		t.Errorf("ListGitLabInstallations(agent B) = %v, %v, want agent B's only", filtered, err)
	}
}

// AC35 (FR37): the lists carry host, project, the bound repository's current path and pr_url.
func TestLists_GitLabFields(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.connect(t, hostA) // hostB has no connection
	ghInst := f.install(t, f.agentA, "acme/web", "alpha", 1)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "grp/stored-path", "alpha"))
	f.glInstallID(t, glInstall(f.agentA, hostB, 9, "ops/web", "alpha"))
	f.glInstallID(t, glInstall(f.agentB, hostA, 8, "grp/unbound", "beta"))
	f.glRepo(t, f.ws, hostA, 7, "grp/current-path/")
	f.glRepo(t, f.ws, hostB, 9, "ops/web")
	// glRepo wrote web_url https://gitlab.example.com/grp/current-path/ (a stored trailing slash).
	instBound := f.glInstallation(t, f.agentA, hostA, 7)
	instNoConn := f.glInstallation(t, f.agentA, hostB, 9)
	instUnbound := f.glInstallation(t, f.agentB, hostA, 8)

	mustRecord := func(r domain.GitLabNewRun) {
		t.Helper()
		if rec, err := f.store.RecordGitLabRun(ctx, r); err != nil || !rec.Stored {
			t.Fatalf("RecordGitLabRun(%d) = %+v, %v, want Stored", r.GithubRunID, rec, err)
		}
	}
	mustRecord(glRun(f, instBound, 1, hostA, t0.Add(3*time.Hour)))
	mustRecord(glRun(f, instNoConn, 2, hostB, t0.Add(2*time.Hour)))
	mustRecord(glRun(f, instUnbound, 3, hostA, t0.Add(time.Hour)))
	noMR := glRun(f, instBound, 4, hostA, t0.Add(30*time.Minute))
	noMR.PRNumber = nil
	mustRecord(noMR)
	if rec, err := f.store.RecordRun(ctx, newRun(f, ghInst, f.agentA, 1, 1, t0)); err != nil || !rec.Stored {
		t.Fatalf("RecordRun(github) = %+v, %v", rec, err)
	}

	runs, err := f.store.ListRuns(ctx, f.ws, app.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns() = _, %v", err)
	}
	type view struct {
		Host, Repo string
		PRURL      string
	}
	got := map[string]view{}
	for _, r := range runs {
		v := view{Host: r.Host, Repo: *r.Repo}
		if r.PRURL != nil {
			v.PRURL = *r.PRURL
		}
		got[*r.GithubURL] = v
	}
	want := map[string]view{
		// Bound repository: its current path and web URL (trailing slash trimmed).
		"https://" + hostA + "/grp/stored-path/-/jobs/1": {hostA, "grp/current-path/", "https://" + hostA + "/grp/current-path/-/merge_requests/12"},
		// Connected host, no bound repository row: no MR link (the stored web URL is the source).
		"https://" + hostA + "/grp/unbound/-/jobs/3": {hostA, "grp/unbound", ""},
		// Host without a connection: no MR link (EC34), the run still lists. Its repository row
		// exists (a deleted connection keeps its repositories) and still gives none.
		"https://" + hostB + "/ops/web/-/jobs/2": {hostB, "ops/web", ""},
		// No MR number: no MR link.
		"https://" + hostA + "/grp/stored-path/-/jobs/4": {hostA, "grp/current-path/", ""},
		// GitHub: github.com, never a pr_url.
		"https://github.com/acme/api/actions/runs/1": {"github.com", "acme/web", ""},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListRuns() views mismatch (-want +got):\n%s", diff)
	}

	list, err := f.store.ListInstallations(ctx, f.ws, app.InstallationFilter{})
	if err != nil {
		t.Fatalf("ListInstallations() = _, %v", err)
	}
	type inst struct {
		Agent, Host, Repo, Target string
		Project                   int64
		LastURL                   string
	}
	var gotInst []inst
	for _, i := range list {
		v := inst{Agent: i.AgentID, Host: i.Host, Repo: i.Repo, Target: i.TargetType, Project: i.ProjectID}
		if i.LastRun != nil {
			v.LastURL = *i.LastRun.GithubURL
		}
		gotInst = append(gotInst, v)
	}
	wantInst := []inst{
		{f.agentA, "github.com", "acme/web", "gha", 0, "https://github.com/acme/api/actions/runs/1"},
		{f.agentA, hostA, "grp/current-path/", "gitlab_ci", 7, "https://" + hostA + "/grp/stored-path/-/jobs/1"},
		{f.agentA, hostB, "ops/web", "gitlab_ci", 9, "https://" + hostB + "/ops/web/-/jobs/2"},
		{f.agentB, hostA, "grp/unbound", "gitlab_ci", 8, "https://" + hostA + "/grp/unbound/-/jobs/3"},
	}
	// Same installed_at order is not part of the contract inside one transaction-time tie: compare sorted.
	if diff := cmp.Diff(wantInst, gotInst, cmpopts.SortSlices(func(a, b inst) bool {
		return a.Agent+a.Host < b.Agent+b.Host
	})); diff != "" {
		t.Errorf("ListInstallations() mismatch (-want +got):\n%s", diff)
	}
	onlyB, err := f.store.ListInstallations(ctx, f.ws, app.InstallationFilter{AgentID: f.agentB})
	if err != nil || len(onlyB) != 1 || onlyB[0].Host != hostA {
		t.Errorf("ListInstallations(agent B) = %+v, %v, want the one GitLab installation", onlyB, err)
	}

	// FR45: the repository listing's newest run carries the merge request number and link (same rule
	// as the runs); GitHub keeps its pull request number and never has a link.
	repoList, err := f.store.ListRepoInstallations(ctx, f.ws)
	if err != nil {
		t.Fatalf("ListRepoInstallations() = _, %v", err)
	}
	type prView struct {
		Number int
		URL    string
	}
	gotPR := map[string]prView{}
	for _, g := range domain.GroupByRepo(repoList) {
		if g.LastRun == nil || g.LastRun.PRNumber == nil {
			t.Fatalf("GroupByRepo()[%s %s] last run = %+v, want one with a PR number", g.Host, g.Repo, g.LastRun)
		}
		v := prView{Number: *g.LastRun.PRNumber}
		if g.LastRun.PRURL != nil {
			v.URL = *g.LastRun.PRURL
		}
		gotPR[g.Host+"|"+g.Repo] = v
	}
	wantPR := map[string]prView{
		hostA + "|grp/current-path/": {12, "https://" + hostA + "/grp/current-path/-/merge_requests/12"},
		hostB + "|ops/web":           {12, ""}, // no connection: no link (EC34)
		hostA + "|grp/unbound":       {12, ""}, // connected host, no repository row
		"github.com|acme/web":        {7, ""},
	}
	if diff := cmp.Diff(wantPR, gotPR); diff != "" {
		t.Errorf("ListRepoInstallations() last-run PR mismatch (-want +got):\n%s", diff)
	}
}

// AC35 (FR46): the per-repository list has GitLab entries, grouped by repository identity and
// ordered by repo then host.
func TestListRepoInstallations_GitLab(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.install(t, f.agentA, "acme/web", "alpha", 1)
	f.install(t, f.agentB, "acme/web", "beta", 1)
	f.glInstallID(t, glInstall(f.agentA, hostA, 7, "acme/web", "alpha")) // same path text as the GitHub repo
	f.glInstallID(t, glInstall(f.agentB, hostA, 7, "acme/web", "beta"))  // same project: one group
	f.glInstallID(t, glInstall(f.agentA, hostB, 7, "acme/web", "alpha")) // same project id, other host
	f.glInstallID(t, glInstall(f.agentA, hostA, 8, "grp/old", "alpha"))
	f.glRepo(t, f.ws, hostA, 8, "grp/zeta") // renamed since the export
	instB := f.glInstallation(t, f.agentA, hostB, 7)
	if _, err := f.store.RecordGitLabRun(ctx, glRun(f, instB, 5, hostB, t0)); err != nil {
		t.Fatal(err)
	}

	list, err := f.store.ListRepoInstallations(ctx, f.ws)
	if err != nil {
		t.Fatalf("ListRepoInstallations() = _, %v", err)
	}
	if len(list) != 6 {
		t.Fatalf("ListRepoInstallations() = %d installations, want 6", len(list))
	}
	type group struct {
		Repo, Host, Target string
		Project            int64
		Agents             []string
		HasRun             bool
	}
	var got []group
	for _, g := range domain.GroupByRepo(list) {
		v := group{Repo: g.Repo, Host: g.Host, Target: g.TargetType, Project: g.ProjectID, HasRun: g.LastRun != nil}
		for _, i := range g.Installations {
			v.Agents = append(v.Agents, i.AgentName)
		}
		got = append(got, v)
	}
	want := []group{
		{"acme/web", hostB, "gitlab_ci", 7, []string{"alpha"}, true},
		{"acme/web", "github.com", "gha", 0, []string{"alpha", "beta"}, false},
		{"acme/web", hostA, "gitlab_ci", 7, []string{"alpha", "beta"}, false},
		{"grp/zeta", hostA, "gitlab_ci", 8, []string{"alpha"}, false},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GroupByRepo(ListRepoInstallations()) mismatch (-want +got):\n%s", diff)
	}
}

// FR39: a GitLab row that names no host cannot be written (the default would be github.com).
func TestGitLabRowsNeedAGitLabHost(t *testing.T) {
	f := newFixture(t)
	err := f.db.Exec(`INSERT INTO ci_installations (agent_id, repo, target_type, slug, project_id, provider)
		VALUES (?, 'grp/web', 'gitlab_ci', 'a', 7, 'openrouter')`, f.agentA).Error
	if err == nil {
		t.Error("inserting a gitlab_ci installation without a host = nil, want the target CHECK to refuse it")
	}
}

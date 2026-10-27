package migrate_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// TestMigration0012_GitLabCI: Up adds `gitlab_ci` to the installation target, ci_installations.host
// (existing rows github.com) and project_id (GitLab only), replaces the (agent, repo, target) and
// (repo, slug) uniques by partial uniques per target kind (host + repo for the GitHub-style targets,
// host + project id for gitlab_ci), CHECKs that tie the target to host and project id, and
// ci_runs.host (existing rows github.com) with the unique (host, run id, attempt), so a GitLab job id
// equal to a GitHub run id is stored (FR39, EC30, AC36). Down REFUSES while a gitlab_ci installation
// or a GitLab CI run exists and names the data (A12, EC33); with only GitHub data it restores
// version 11 exactly and Up again restores version 12, with every row untouched.
func TestMigration0012_GitLabCI(t *testing.T) {
	upTo11Names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes", "0005_cost_ledger", "0006_auto_review_records", "0007_pr_head_repo", "0008_plugin_ownership", "0009_custom_providers", "0010_gitlab", "0011_gitlab_diff_positions"}
	upTo12Names := append(append([]string{}, upTo11Names...), "0012_gitlab_ci")

	const ws = "00000000-0000-0000-0000-0000000000a1"
	const agentA, agentB = "00000000-0000-0000-0000-0000000000d1", "00000000-0000-0000-0000-0000000000d2"
	const instGH, instCLI = "00000000-0000-0000-0000-0000000000e1", "00000000-0000-0000-0000-0000000000e2"
	const glHost, glHost2 = "git.corp.example", "gitlab.com"

	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := newMigrator(t, db, embeddedFS(t, upTo11Names...)).Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-11 error = %v", err)
	}
	exec := func(q string) error {
		_, err := db.ExecContext(t.Context(), q)
		return err
	}
	// Version 11 data: two agents, a GitHub installation with a slug, a CLI installation, and two
	// GitHub runs, inserted the way the pre-GitLab code did, without naming the new columns.
	if err := exec(`
INSERT INTO workspaces (id, name) VALUES ('` + ws + `', 'w1');
INSERT INTO agents (id, workspace_id, name, provider, model, system_prompt) VALUES
  ('` + agentA + `', '` + ws + `', 'a1', 'openai', 'm', 's'),
  ('` + agentB + `', '` + ws + `', 'a2', 'openai', 'm', 's');
INSERT INTO ci_installations (id, agent_id, repo, target_type, slug, manifest_version, provider) VALUES
  ('` + instGH + `', '` + agentA + `', 'acme/web', 'gha', 'sl1', 3, 'openrouter'),
  ('` + instCLI + `', '` + agentA + `', 'acme/web', 'cli', NULL, NULL, 'openrouter');
INSERT INTO ci_runs (ci_installation_id, github_run_id, run_attempt, status, source) VALUES
  ('` + instGH + `', 111, 1, 'done', 'ci'),
  ('` + instGH + `', 222, 1, 'failed', 'ci');`); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	before := catalog(t, db)
	const rowsQ = `SELECT (SELECT string_agg(i.id::text || ' ' || i.agent_id::text || ' ' || i.repo || ' ' || i.target_type || ' ' || coalesce(i.slug, '-') || ' ' || coalesce(i.manifest_version::text, '-') || ' ' || i.provider, ',' ORDER BY i.id) FROM ci_installations i)
	  || ' | ' || (SELECT string_agg(r.ci_installation_id::text || ' ' || r.github_run_id::text || ' ' || r.run_attempt::text || ' ' || r.status, ',' ORDER BY r.github_run_id, r.run_attempt) FROM ci_runs r)`
	rowsBefore := queryStrings(t, db, rowsQ)

	upTo12 := embeddedFS(t, upTo12Names...)
	all, err := migrate.Load(upTo12)
	if err != nil {
		t.Fatalf("Load(versions 1-12) error = %v", err)
	}
	if len(all) != 12 || all[11].Version != 12 || all[11].Name != "gitlab_ci" {
		t.Fatalf("Load(versions 1-12) = %d migrations, last %+v, want 12 ending in version 12 gitlab_ci", len(all), all[len(all)-1])
	}
	m := newMigrator(t, db, upTo12)
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 12 only)", n, err)
	}
	afterUp := catalog(t, db)

	wantRemoved := []string{
		"constraint ci_installations ci_installations_agent_repo_target_uq u UNIQUE (agent_id, repo, target_type)",
		"constraint ci_installations ci_installations_repo_slug_uq u UNIQUE (repo, slug)",
		"constraint ci_installations ci_installations_target_type_chk c CHECK ((target_type = ANY (ARRAY['gha'::text, 'circle'::text, 'jenkins'::text, 'cli'::text])))",
		"constraint ci_runs ci_runs_github_run_attempt_uq u UNIQUE (github_run_id, run_attempt)",
		"index ci_installations ci_installations_agent_repo_target_uq CREATE UNIQUE INDEX ci_installations_agent_repo_target_uq ON public.ci_installations USING btree (agent_id, repo, target_type)",
		"index ci_installations ci_installations_repo_slug_uq CREATE UNIQUE INDEX ci_installations_repo_slug_uq ON public.ci_installations USING btree (repo, slug)",
		"index ci_runs ci_runs_github_run_attempt_uq CREATE UNIQUE INDEX ci_runs_github_run_attempt_uq ON public.ci_runs USING btree (github_run_id, run_attempt)",
	}
	if diff := cmp.Diff(wantRemoved, onlyIn(before, afterUp)); diff != "" {
		t.Errorf("objects removed by 0012 mismatch (-want +got):\n%s", diff)
	}
	wantAdded := []string{
		"column ci_installations.host #9 text NOT NULL default='github.com'::text generated= identity=",
		"column ci_installations.project_id #10 bigint default=<none> generated= identity=",
		"column ci_runs.host #15 text NOT NULL default='github.com'::text generated= identity=",
		"constraint ci_installations ci_installations_host_chk c CHECK (((host <> ''::text) AND (host = lower(host)) AND (host !~ '[[:space:]]'::text)))",
		"constraint ci_installations ci_installations_project_id_chk c CHECK (((project_id IS NULL) OR (project_id > 0)))",
		"constraint ci_installations ci_installations_target_cols_chk c CHECK ((((target_type = 'gitlab_ci'::text) AND (project_id IS NOT NULL) AND (host <> ALL (ARRAY['github.com'::text, 'www.github.com'::text, 'api.github.com'::text]))) OR ((target_type <> 'gitlab_ci'::text) AND (project_id IS NULL) AND (host = 'github.com'::text))))",
		"constraint ci_installations ci_installations_target_type_chk c CHECK ((target_type = ANY (ARRAY['gha'::text, 'circle'::text, 'jenkins'::text, 'cli'::text, 'gitlab_ci'::text])))",
		"constraint ci_runs ci_runs_host_chk c CHECK (((host <> ''::text) AND (host = lower(host)) AND (host !~ '[[:space:]]'::text)))",
		"constraint ci_runs ci_runs_host_run_attempt_uq u UNIQUE (host, github_run_id, run_attempt)",
		"index ci_installations ci_installations_agent_host_project_target_uq CREATE UNIQUE INDEX ci_installations_agent_host_project_target_uq ON public.ci_installations USING btree (agent_id, host, project_id, target_type) WHERE (target_type = 'gitlab_ci'::text)",
		"index ci_installations ci_installations_agent_host_repo_target_uq CREATE UNIQUE INDEX ci_installations_agent_host_repo_target_uq ON public.ci_installations USING btree (agent_id, host, repo, target_type) WHERE (target_type <> 'gitlab_ci'::text)",
		"index ci_installations ci_installations_host_project_slug_uq CREATE UNIQUE INDEX ci_installations_host_project_slug_uq ON public.ci_installations USING btree (host, project_id, slug) WHERE (target_type = 'gitlab_ci'::text)",
		"index ci_installations ci_installations_host_repo_slug_uq CREATE UNIQUE INDEX ci_installations_host_repo_slug_uq ON public.ci_installations USING btree (host, repo, slug) WHERE (target_type <> 'gitlab_ci'::text)",
		"index ci_runs ci_runs_host_run_attempt_uq CREATE UNIQUE INDEX ci_runs_host_run_attempt_uq ON public.ci_runs USING btree (host, github_run_id, run_attempt)",
	}
	if diff := cmp.Diff(wantAdded, onlyIn(afterUp, before)); diff != "" {
		t.Errorf("objects added by 0012 mismatch (-want +got):\n%s", diff)
	}

	// Existing rows: unchanged, host github.com, no project id (AC36).
	if diff := cmp.Diff(rowsBefore, queryStrings(t, db, rowsQ)); diff != "" {
		t.Errorf("installations and runs after Up mismatch (-before +got):\n%s", diff)
	}
	const hostsQ = `SELECT (SELECT count(*) FROM ci_installations WHERE host = 'github.com' AND project_id IS NULL)::text || ' ' || (SELECT count(*) FROM ci_runs WHERE host = 'github.com')::text`
	if got := queryStrings(t, db, hostsQ); !cmp.Equal(got, []string{"2 2"}) {
		t.Errorf("rows on github.com without project id after Up = %v, want 2 installations and 2 runs", got)
	}

	// violates runs a statement that must fail on the named constraint.
	violates := func(what, constraint, q string) {
		t.Helper()
		if err := exec(q); err == nil || !strings.Contains(err.Error(), constraint) {
			t.Errorf("%s error = %v, want a violation of %s", what, err, constraint)
		}
	}
	gl := func(agent, repo, host, project, slug string) string {
		return `INSERT INTO ci_installations (agent_id, repo, target_type, host, project_id, slug, provider) VALUES ('` + agent + `', '` + repo + `', 'gitlab_ci', '` + host + `', ` + project + `, ` + slug + `, 'openrouter');`
	}

	// A GitHub insert that names neither new column still works (the GitHub path is unchanged).
	if err := exec(`INSERT INTO ci_installations (agent_id, repo, target_type, slug, provider) VALUES ('` + agentB + `', 'acme/api', 'gha', 'sl2', 'openrouter');
INSERT INTO ci_runs (github_run_id, run_attempt, status) VALUES (333, 1, 'done');`); err != nil {
		t.Errorf("insert a GitHub installation and run without the new columns: %v", err)
	}

	// A GitLab `acme/web` with the GitHub row's slug coexists (EC30); so does the same project id on
	// another host or under another agent.
	if err := exec(gl(agentA, "acme/web", glHost, "42", "'sl1'") + gl(agentA, "acme/web", glHost2, "42", "'sl1'") + gl(agentB, "acme/web", glHost, "42", "'sl9'")); err != nil {
		t.Fatalf("insert GitLab installations next to the GitHub one: %v", err)
	}
	// Identity of a GitLab installation is (agent, host, project id): a renamed project is the same one.
	violates("a second installation of the same GitLab project under a new path", "ci_installations_agent_host_project_target_uq",
		gl(agentA, "acme/renamed", glHost, "42", "NULL"))
	violates("the slug of another agent's installation of the same GitLab project", "ci_installations_host_project_slug_uq",
		`UPDATE ci_installations SET slug = 'sl1' WHERE agent_id = '`+agentB+`' AND host = '`+glHost+`' AND project_id = 42`)
	// GitHub-style identity is (agent, host, repo, target) and (host, repo, slug).
	violates("a second GitHub installation of the same repo", "ci_installations_agent_host_repo_target_uq",
		`INSERT INTO ci_installations (agent_id, repo, target_type, provider) VALUES ('`+agentA+`', 'acme/web', 'gha', 'openrouter')`)
	violates("a taken GitHub slug", "ci_installations_host_repo_slug_uq",
		`INSERT INTO ci_installations (agent_id, repo, target_type, slug, provider) VALUES ('`+agentB+`', 'acme/web', 'gha', 'sl1', 'openrouter')`)

	// The upsert shapes the store uses infer the partial indexes.
	for name, q := range map[string]string{
		"GitHub, gha predicate": `INSERT INTO ci_installations (agent_id, repo, target_type, slug, provider) VALUES ('` + agentA + `', 'acme/web', 'gha', 'sl1', 'openrouter')
		  ON CONFLICT (agent_id, host, repo, target_type) WHERE target_type = 'gha' DO UPDATE SET manifest_version = 4`,
		"GitHub, index predicate": `INSERT INTO ci_installations (agent_id, repo, target_type, slug, provider) VALUES ('` + agentA + `', 'acme/web', 'gha', 'sl1', 'openrouter')
		  ON CONFLICT (agent_id, host, repo, target_type) WHERE target_type <> 'gitlab_ci' DO UPDATE SET manifest_version = 4`,
		"GitLab": `INSERT INTO ci_installations (agent_id, repo, target_type, host, project_id, slug, provider) VALUES ('` + agentA + `', 'acme/renamed', 'gitlab_ci', '` + glHost + `', 42, 'sl1', 'openrouter')
		  ON CONFLICT (agent_id, host, project_id, target_type) WHERE target_type = 'gitlab_ci' DO UPDATE SET repo = EXCLUDED.repo, manifest_version = 5`,
	} {
		if err := exec(q); err != nil {
			t.Errorf("upsert (%s) error = %v, want it to infer its partial unique index", name, err)
		}
	}
	if got := queryStrings(t, db, `SELECT string_agg(host || ' ' || repo || ' ' || manifest_version::text, ',' ORDER BY host, repo) FROM ci_installations WHERE slug = 'sl1' AND manifest_version >= 4`); !cmp.Equal(got, []string{"git.corp.example acme/renamed 5,github.com acme/web 4"}) {
		t.Errorf("installations after the upserts = %v, want the GitHub row updated and the GitLab row renamed", got)
	}

	// CHECKs: target <-> host/project id (FR39).
	for _, tc := range []struct{ name, constraint, q string }{
		{"a gitlab_ci installation without a project id", "ci_installations_target_cols_chk", `INSERT INTO ci_installations (agent_id, repo, target_type, host, provider) VALUES ('` + agentA + `', 'x/y', 'gitlab_ci', '` + glHost + `', 'openrouter')`},
		{"a gitlab_ci installation on github.com", "ci_installations_target_cols_chk", `INSERT INTO ci_installations (agent_id, repo, target_type, project_id, provider) VALUES ('` + agentA + `', 'x/y', 'gitlab_ci', 5, 'openrouter')`},
		{"a gitlab_ci installation on api.github.com", "ci_installations_target_cols_chk", gl(agentA, "x/y", "api.github.com", "5", "NULL")},
		{"a gha installation with a project id", "ci_installations_target_cols_chk", `INSERT INTO ci_installations (agent_id, repo, target_type, project_id, provider) VALUES ('` + agentA + `', 'x/y', 'gha', 5, 'openrouter')`},
		{"a gha installation on a GitLab host", "ci_installations_target_cols_chk", `INSERT INTO ci_installations (agent_id, repo, target_type, host, provider) VALUES ('` + agentA + `', 'x/y', 'gha', '` + glHost + `', 'openrouter')`},
		{"an upper-case host", "ci_installations_host_chk", gl(agentA, "x/y", "GitLab.com", "5", "NULL")},
		{"an empty host", "ci_installations_host_chk", gl(agentA, "x/y", "", "5", "NULL")},
		{"a host with a space", "ci_installations_host_chk", gl(agentA, "x/y", "git lab", "5", "NULL")},
		{"project id 0", "ci_installations_project_id_chk", gl(agentA, "x/y", glHost, "0", "NULL")},
		{"an unknown target", "ci_installations_target_type_chk", `INSERT INTO ci_installations (agent_id, repo, target_type, provider) VALUES ('` + agentA + `', 'x/y', 'bitbucket', 'openrouter')`},
		{"a run with an upper-case host", "ci_runs_host_chk", `INSERT INTO ci_runs (host, github_run_id, run_attempt) VALUES ('GitLab.com', 1, 1)`},
		{"a run with an empty host", "ci_runs_host_chk", `INSERT INTO ci_runs (host, github_run_id, run_attempt) VALUES ('', 1, 1)`},
	} {
		violates(tc.name, tc.constraint, tc.q)
	}

	// ci_runs: a GitLab job id equal to a GitHub run id is stored (EC30); the same (host, id, attempt)
	// is refused for GitLab and GitHub alike (AC36).
	glRun := func(host, id, attempt string) string {
		return `INSERT INTO ci_runs (host, github_run_id, run_attempt, status) VALUES ('` + host + `', ` + id + `, ` + attempt + `, 'done');`
	}
	if err := exec(glRun(glHost, "111", "1") + glRun(glHost, "111", "2") + glRun(glHost2, "111", "1")); err != nil {
		t.Fatalf("insert GitLab runs whose job id equals a GitHub run id: %v", err)
	}
	violates("a duplicate GitLab (host, job id, attempt)", "ci_runs_host_run_attempt_uq", glRun(glHost, "111", "1"))
	violates("a duplicate GitHub (run id, attempt) on the default host", "ci_runs_host_run_attempt_uq",
		`INSERT INTO ci_runs (github_run_id, run_attempt) VALUES (111, 1)`)
	if err := exec(`INSERT INTO ci_runs (host, github_run_id, run_attempt) VALUES ('` + glHost + `', 111, 1) ON CONFLICT (host, github_run_id, run_attempt) DO NOTHING`); err != nil {
		t.Errorf("ON CONFLICT (host, github_run_id, run_attempt) error = %v, want it to infer the unique", err)
	}
	if got := queryStrings(t, db, `SELECT string_agg(host || ' ' || github_run_id::text || '/' || run_attempt::text, ',' ORDER BY host, github_run_id, run_attempt) FROM ci_runs WHERE github_run_id = 111`); !cmp.Equal(got, []string{"git.corp.example 111/1,git.corp.example 111/2,github.com 111/1,gitlab.com 111/1"}) {
		t.Errorf("runs with id 111 = %v, want one per (host, attempt)", got)
	}

	// Down refuses while GitLab CI data exists (A12, EC33): the error names it, the transaction
	// rolled back, the schema, the rows and the version are unchanged.
	const dataQ = `SELECT (SELECT string_agg(id::text || ' ' || host || ' ' || repo || ' ' || target_type, ',' ORDER BY id) FROM ci_installations)
	  || ' | ' || (SELECT string_agg(host || ' ' || github_run_id::text || '/' || run_attempt::text, ',' ORDER BY host, github_run_id, run_attempt) FROM ci_runs)`
	refused := func(what string, want ...string) {
		t.Helper()
		wantRows := queryStrings(t, db, dataQ)
		_, _, err := m.Down(t.Context())
		if err == nil {
			t.Fatalf("Down() %s error = nil, want the refusal", what)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("Down() %s error = %v, want it to contain %q", what, err, w)
			}
		}
		if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
			t.Errorf("schema after a refused Down (%s) differs from version 12 (-v12 +got):\n%s", what, diff)
		}
		if got := queryStrings(t, db, dataQ); !cmp.Equal(got, wantRows) {
			t.Errorf("CI rows after a refused Down (%s) = %v, want %v", what, got, wantRows)
		}
		if st, err := m.Status(t.Context()); err != nil || len(st) != 12 || !st[11].Applied {
			t.Errorf("Status() after a refused Down (%s) = %+v, %v, want version 12 still applied", what, st, err)
		}
	}
	refused("with GitLab installations and runs",
		"cannot revert 0012_gitlab_ci: GitLab CI data exists: 3 gitlab_ci installation(s), 3 GitLab CI run(s)",
		"git.corp.example acme/renamed", "git.corp.example acme/web", "gitlab.com acme/web")
	if err := exec(`DELETE FROM ci_runs WHERE host <> 'github.com'`); err != nil {
		t.Fatal(err)
	}
	refused("with only GitLab installations", "cannot revert 0012_gitlab_ci: GitLab CI data exists: 3 gitlab_ci installation(s), 0 GitLab CI run(s)")
	if err := exec(`DELETE FROM ci_installations WHERE target_type = 'gitlab_ci'; ` + glRun(glHost, "7", "1")); err != nil {
		t.Fatal(err)
	}
	refused("with only a GitLab run", "cannot revert 0012_gitlab_ci: GitLab CI data exists: 0 gitlab_ci installation(s), 1 GitLab CI run(s) (up to 20 installations listed: none)")
	if err := exec(`DELETE FROM ci_runs WHERE host <> 'github.com'`); err != nil {
		t.Fatal(err)
	}

	// With only GitHub data, Down reverses everything: exactly version 11, rows untouched; the old
	// uniqueness is back, and Up again restores version 12.
	wantRows := queryStrings(t, db, dataQ)
	v, ok, err := m.Down(t.Context())
	if err != nil || !ok || v != 12 {
		t.Fatalf("Down() = %d, %v, %v, want 12, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0012 differs from version 11 (-v11 +got):\n%s", diff)
	}
	if got := queryStrings(t, db, `SELECT string_agg(repo || ' ' || target_type, ',' ORDER BY repo, target_type) FROM ci_installations`); !cmp.Equal(got, []string{"acme/api gha,acme/web cli,acme/web gha"}) {
		t.Errorf("installations after Down = %v, want the GitHub ones", got)
	}
	if err := exec(`INSERT INTO ci_runs (github_run_id, run_attempt) VALUES (111, 1)`); err == nil || !strings.Contains(err.Error(), "ci_runs_github_run_attempt_uq") {
		t.Errorf("duplicate (run id, attempt) after Down error = %v, want a violation of ci_runs_github_run_attempt_uq", err)
	}
	if err := exec(`INSERT INTO ci_installations (agent_id, repo, target_type, provider) VALUES ('` + agentA + `', 'acme/web', 'gha', 'openrouter')`); err == nil || !strings.Contains(err.Error(), "ci_installations_agent_repo_target_uq") {
		t.Errorf("duplicate (agent, repo, target) after Down error = %v, want a violation of ci_installations_agent_repo_target_uq", err)
	}
	if err := exec(`INSERT INTO ci_installations (agent_id, repo, target_type, provider) VALUES ('` + agentA + `', 'x/y', 'gitlab_ci', 'openrouter')`); err == nil || !strings.Contains(err.Error(), "ci_installations_target_type_chk") {
		t.Errorf("gitlab_ci installation after Down error = %v, want a violation of ci_installations_target_type_chk", err)
	}

	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
	if got := queryStrings(t, db, dataQ); !cmp.Equal(got, wantRows) {
		t.Errorf("CI rows after Down->Up = %v, want %v", got, wantRows)
	}
	if got := queryStrings(t, db, hostsQ); !cmp.Equal(got, []string{"3 3"}) {
		t.Errorf("rows on github.com without project id after Down->Up = %v, want 3 installations and 3 runs", got)
	}
}

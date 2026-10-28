package migrate_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// TestMigration0013_FilePackages: Up adds file_packages (primary key (repo, file), index on
// (repo, package key), rows deleted with the repo); Down drops it and Up again restores it (FR18, AC19).
func TestMigration0013_FilePackages(t *testing.T) {
	names := []string{"0001_baseline", "0002_pr_merge_state", "0003_spend_query_indexes", "0004_reset_history_indexes", "0005_cost_ledger", "0006_auto_review_records", "0007_pr_head_repo", "0008_plugin_ownership", "0009_custom_providers", "0010_gitlab", "0011_gitlab_diff_positions", "0012_gitlab_ci"}
	d := pgtest.Empty(t)
	db := sqlDBOf(t, d)
	if _, err := newMigrator(t, db, embeddedFS(t, names...)).Up(t.Context()); err != nil {
		t.Fatalf("Up() versions 1-12 error = %v", err)
	}
	before := catalog(t, db)

	upTo13 := embeddedFS(t, append(names, "0013_file_packages")...)
	m := newMigrator(t, db, upTo13)
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() = %d, %v, want 1, nil (version 13 only)", n, err)
	}
	afterUp := catalog(t, db)
	wantAdded := []string{
		"column file_packages.file_path #2 text NOT NULL default=<none> generated= identity=",
		"column file_packages.package_key #3 text NOT NULL default=<none> generated= identity=",
		"column file_packages.repo_id #1 uuid NOT NULL default=<none> generated= identity=",
	}
	got := onlyIn(afterUp, before)
	for _, w := range wantAdded {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("objects added by 0013 = %q, want it to contain %q", got, w)
		}
	}
	const objQ = `SELECT string_agg(x, ',' ORDER BY x) FROM (
	  SELECT 'idx ' || indexname || ' ' || indexdef AS x FROM pg_indexes WHERE tablename = 'file_packages'
	  UNION ALL SELECT 'con ' || conname || ' ' || pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'public.file_packages'::regclass) o`
	wantObjs := "con file_packages_repo_id_file_path_pk PRIMARY KEY (repo_id, file_path)," +
		"con file_packages_repo_id_repos_id_fk FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE," +
		"idx file_packages_repo_id_file_path_pk CREATE UNIQUE INDEX file_packages_repo_id_file_path_pk ON public.file_packages USING btree (repo_id, file_path)," +
		"idx file_packages_repo_key_idx CREATE INDEX file_packages_repo_key_idx ON public.file_packages USING btree (repo_id, package_key)"
	if diff := cmp.Diff([]string{wantObjs}, queryStrings(t, db, objQ)); diff != "" {
		t.Errorf("file_packages constraints and indexes mismatch (-want +got):\n%s", diff)
	}

	// Rows go with the repo.
	const ws, repo = "00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000b1"
	if _, err := db.ExecContext(t.Context(), `
INSERT INTO workspaces (id, name) VALUES ('`+ws+`', 'w1');
INSERT INTO repos (id, workspace_id, owner, name, full_name, clone_path) VALUES ('`+repo+`', '`+ws+`', 'o', 'r', 'o/r', '/c/o/r');
INSERT INTO file_packages (repo_id, file_path, package_key) VALUES ('`+repo+`', 'a/x.go', 'go:a');`); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM repos WHERE id = '`+repo+`'`); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, `SELECT count(*)::text FROM file_packages`); !cmp.Equal(got, []string{"0"}) {
		t.Errorf("file_packages rows after deleting the repo = %v, want 0", got)
	}

	if v, ok, err := m.Down(t.Context()); err != nil || !ok || v != 13 {
		t.Fatalf("Down() = %d, %v, %v, want 13, true, nil", v, ok, err)
	}
	if diff := cmp.Diff(before, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down of 0013 differs from version 12 (-v12 +got):\n%s", diff)
	}
	if n, err := m.Up(t.Context()); err != nil || n != 1 {
		t.Fatalf("Up() after Down = %d, %v, want 1, nil", n, err)
	}
	if diff := cmp.Diff(afterUp, catalog(t, db)); diff != "" {
		t.Errorf("schema after Down->Up differs from first Up (-first +got):\n%s", diff)
	}
}

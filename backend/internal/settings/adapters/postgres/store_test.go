package postgres_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	settingspg "dev-digest-go/backend/internal/settings/adapters/postgres"
	"dev-digest-go/backend/internal/settings/app"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func seeded(t *testing.T) (*settingspg.Store, seed.Result, *pgtest.Database) {
	t.Helper()
	d := pgtest.Migrated(t)
	res, err := seed.New(seedpg.NewStore(d.DB), slog.New(slog.DiscardHandler)).Run(t.Context())
	if err != nil {
		t.Fatalf("seed error = %v", err)
	}
	return settingspg.NewStore(d.DB), res, d
}

func decoded(t *testing.T, bag app.Bag) map[string]any {
	t.Helper()
	out := map[string]any{}
	for k, v := range bag {
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			t.Fatalf("value of %q = %s is not JSON: %v", k, v, err)
		}
		out[k] = x
	}
	return out
}

func TestList_seededSettings(t *testing.T) {
	s, res, _ := seeded(t)
	got, err := s.List(t.Context(), res.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := map[string]any{"polling_interval_min": 5.0, "theme": "dark", "density": "regular", "sync_to_folder": true}
	if diff := cmp.Diff(want, decoded(t, got)); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
}

func TestList_emptyAndOtherWorkspace(t *testing.T) {
	s, _, d := seeded(t)
	var otherWS string
	if err := d.DB.Raw(`INSERT INTO workspaces (name) VALUES ('other') RETURNING id`).Scan(&otherWS).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.List(t.Context(), otherWS)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List(other workspace) = %v, want empty (workspace scoping)", got)
	}
}

func TestUpsert_insertsAndOverwrites(t *testing.T) {
	s, res, _ := seeded(t)
	ctx := t.Context()
	entries := app.Bag{
		"theme":          json.RawMessage(`"light"`),
		"x_custom":       json.RawMessage(`null`),
		"feature_models": json.RawMessage(`{"risk_brief":{"provider":"openai","model":"gpt-4.1"}}`),
	}
	if err := s.Upsert(ctx, res.WorkspaceID, res.UserID, entries); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	// Writing the same keys again overwrites, never duplicates.
	if err := s.Upsert(ctx, res.WorkspaceID, res.UserID, app.Bag{"theme": json.RawMessage(`"dark"`)}); err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}

	got, err := s.List(ctx, res.WorkspaceID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := map[string]any{
		"polling_interval_min": 5.0, "theme": "dark", "density": "regular", "sync_to_folder": true,
		"x_custom":       nil,
		"feature_models": map[string]any{"risk_brief": map[string]any{"provider": "openai", "model": "gpt-4.1"}},
	}
	if diff := cmp.Diff(want, decoded(t, got)); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
}

func TestUpsert_isAtomic(t *testing.T) {
	s, res, _ := seeded(t)
	// An unknown user violates the FK on the second row: nothing may be written.
	bad := app.Bag{"theme": json.RawMessage(`"light"`), "a_new_key": json.RawMessage(`1`)}
	if err := s.Upsert(t.Context(), res.WorkspaceID, "00000000-0000-0000-0000-000000000000", bad); err == nil {
		t.Fatal("Upsert() error = nil, want a foreign-key error")
	}
	got, err := s.List(t.Context(), res.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["theme"]) != `"dark"` {
		t.Errorf("theme after failed upsert = %s, want \"dark\"", got["theme"])
	}
	if _, ok := got["a_new_key"]; ok {
		t.Error("a_new_key was written by a failed upsert")
	}
}

func TestUpsert_readsSQLNullAsJSONNull(t *testing.T) {
	s, res, d := seeded(t)
	if err := d.DB.Exec(`INSERT INTO settings (workspace_id, user_id, key, value) VALUES (?, ?, 'sql_null', NULL)`, res.WorkspaceID, res.UserID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.List(t.Context(), res.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["sql_null"]) != "null" {
		t.Errorf("List()[sql_null] = %v (len %d), want null", []byte(got["sql_null"]), len(got["sql_null"]))
	}
}

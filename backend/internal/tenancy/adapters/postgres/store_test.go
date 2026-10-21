package postgres_test

import (
	"errors"
	"log/slog"
	"os"
	"testing"

	"dev-digest-go/backend/internal/platform/postgres/pgtest"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	"dev-digest-go/backend/internal/tenancy"
	tenancypg "dev-digest-go/backend/internal/tenancy/adapters/postgres"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func TestResolve_SeededDatabase(t *testing.T) {
	d := pgtest.Migrated(t)
	res, err := seed.New(seedpg.NewStore(d.DB), slog.New(slog.DiscardHandler)).Run(t.Context())
	if err != nil {
		t.Fatalf("seed error = %v", err)
	}

	got, err := tenancy.NewResolver(tenancypg.NewStore(d.DB)).Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := (tenancy.Identity{WorkspaceID: res.WorkspaceID, UserID: res.UserID}); got != want {
		t.Errorf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_IgnoresOtherWorkspacesAndUsers(t *testing.T) {
	d := pgtest.Migrated(t)
	res, err := seed.New(seedpg.NewStore(d.DB), slog.New(slog.DiscardHandler)).Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DB.Exec(`INSERT INTO workspaces (name) VALUES ('other'); INSERT INTO users (email, name) VALUES ('someone@else', 'S')`).Error; err != nil {
		t.Fatal(err)
	}

	got, err := tenancy.NewResolver(tenancypg.NewStore(d.DB)).Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.WorkspaceID != res.WorkspaceID || got.UserID != res.UserID {
		t.Errorf("Resolve() = %+v, want the default workspace and system user", got)
	}
}

func TestResolve_NotProvisioned(t *testing.T) {
	tests := []struct {
		name  string
		setup string
	}{
		{name: "empty database"},
		{name: "workspace without user", setup: `INSERT INTO workspaces (name) VALUES ('default')`},
		{name: "user without workspace", setup: `INSERT INTO users (email, name) VALUES ('you@local', 'You')`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := pgtest.Migrated(t)
			if tc.setup != "" {
				if err := d.DB.Exec(tc.setup).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err := tenancy.NewResolver(tenancypg.NewStore(d.DB)).Resolve(t.Context())
			if !errors.Is(err, tenancy.ErrNotProvisioned) {
				t.Errorf("Resolve() error = %v, want errors.Is ErrNotProvisioned", err)
			}
		})
	}
}

func TestResolve_DatabaseError(t *testing.T) {
	d := pgtest.Migrated(t)
	d.Drop(t)
	_, err := tenancy.NewResolver(tenancypg.NewStore(d.DB)).Resolve(t.Context())
	if err == nil || errors.Is(err, tenancy.ErrNotProvisioned) {
		t.Errorf("Resolve() on a dropped database error = %v, want a non-ErrNotProvisioned error", err)
	}
}

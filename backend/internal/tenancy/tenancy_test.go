package tenancy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/tenancy"
)

type fakeStore struct {
	id        tenancy.Identity
	err       error
	gotWS     string
	gotUser   string
	callCount int
}

func (f *fakeStore) FindIdentity(_ context.Context, ws, user string) (tenancy.Identity, error) {
	f.callCount++
	f.gotWS, f.gotUser = ws, user
	return f.id, f.err
}

func TestResolver_Resolve(t *testing.T) {
	t.Run("looks up the default workspace and the system user", func(t *testing.T) {
		st := &fakeStore{id: tenancy.Identity{WorkspaceID: "w", UserID: "u"}}
		got, err := tenancy.NewResolver(st).Resolve(t.Context())
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if diff := cmp.Diff(st.id, got); diff != "" {
			t.Errorf("Resolve() mismatch (-want +got):\n%s", diff)
		}
		if st.gotWS != "default" || st.gotUser != "you@local" {
			t.Errorf("Resolve() looked up (%q, %q), want (default, you@local)", st.gotWS, st.gotUser)
		}
	})
	t.Run("missing rows give a plain error that keeps the cause", func(t *testing.T) {
		st := &fakeStore{err: tenancy.ErrNotProvisioned}
		got, err := tenancy.NewResolver(st).Resolve(t.Context())
		if !errors.Is(err, tenancy.ErrNotProvisioned) {
			t.Fatalf("Resolve() error = %v, want errors.Is ErrNotProvisioned", err)
		}
		if got != (tenancy.Identity{}) {
			t.Errorf("Resolve() identity = %+v, want zero value on error", got)
		}
	})
	t.Run("reads the store on every call", func(t *testing.T) {
		st := &fakeStore{err: tenancy.ErrNotProvisioned}
		r := tenancy.NewResolver(st)
		_, _ = r.Resolve(t.Context())
		st.err, st.id = nil, tenancy.Identity{WorkspaceID: "w", UserID: "u"}
		if _, err := r.Resolve(t.Context()); err != nil {
			t.Errorf("Resolve() after the rows appeared error = %v, want nil", err)
		}
		if st.callCount != 2 {
			t.Errorf("store calls = %d, want 2", st.callCount)
		}
	})
}

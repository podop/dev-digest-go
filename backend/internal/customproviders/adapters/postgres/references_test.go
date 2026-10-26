package postgres_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/customproviders/domain"
	pg "dev-digest-go/backend/internal/platform/postgres"
)

func (f fixture) agent(t *testing.T, ws, name, provider string, enabled bool) string {
	t.Helper()
	return f.scan(t, `INSERT INTO agents (workspace_id, name, provider, model, system_prompt, enabled)
		VALUES (?, ?, ?, 'm', 'p', ?) RETURNING id::text`, ws, name, provider, enabled)
}

func (f fixture) setting(t *testing.T, ws, key, value string) {
	t.Helper()
	f.exec(t, `INSERT INTO settings (workspace_id, key, value) VALUES (?, ?, ?::jsonb)`, ws, key, value)
}

func TestReferencesFind(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	const id = "custom:gw"
	enabled := f.agent(t, f.ws, "Zed reviewer", id, true)
	disabled := f.agent(t, f.ws, "Alpha reviewer", id, false)
	f.agent(t, f.ws, "Other provider", "openai", true)
	f.agent(t, f.ws, "Other custom", "custom:other", true)
	f.agent(t, f.other, "Another workspace", id, true)
	f.setting(t, f.ws, "feature_models", `{"risk_brief":{"provider":"custom:gw","model":"m"},
		"conformance":{"provider":"openai","model":"gpt-4.1"},"onboarding":{"provider":"custom:gw","model":"x"},
		"conventions":{"provider":"custom:gw-two","model":"x"}}`)
	f.setting(t, f.ws, "default_model", `{"provider":"custom:gw","model":"big"}`)
	f.setting(t, f.other, "default_model", `{"provider":"custom:gw","model":"theirs"}`)

	got, err := f.refs.Find(ctx, f.ws, id)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	want := domain.Usage{
		Agents:        []domain.AgentRef{{ID: disabled, Name: "Alpha reviewer"}, {ID: enabled, Name: "Zed reviewer"}},
		FeatureModels: []string{"onboarding", "risk_brief"},
		DefaultModel:  true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Find(%q) mismatch (-want +got):\n%s", id, diff)
	}
	if got.Empty() {
		t.Error("Find().Empty() = true, want false")
	}

	other, err := f.refs.Find(ctx, f.ws, "custom:unused")
	if err != nil || !other.Empty() {
		t.Errorf("Find(unused) = %+v, %v, want empty", other, err)
	}
	if u, err := f.refs.Find(ctx, f.ws, "openai"); err != nil || !u.Empty() {
		t.Errorf("Find(built-in id) = %+v, %v, want empty", u, err)
	}
}

func TestReferencesFindToleratesBadSettings(t *testing.T) {
	f := newFixture(t)
	f.setting(t, f.ws, "feature_models", `"not an object"`)
	f.setting(t, f.ws, "default_model", `null`)
	u, err := f.refs.Find(t.Context(), f.ws, "custom:gw")
	if err != nil || !u.Empty() {
		t.Errorf("Find() with unusable settings = %+v, %v, want empty and no error", u, err)
	}
	if model, ok, err := f.refs.DefaultModel(t.Context(), f.ws, "custom:gw"); ok || err != nil {
		t.Errorf("DefaultModel() = %q, %v, %v, want not found", model, ok, err)
	}
}

func TestReferencesDefaultModel(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if model, ok, err := f.refs.DefaultModel(ctx, f.ws, "custom:gw"); ok || err != nil {
		t.Fatalf("DefaultModel(no setting) = %q, %v, %v, want not found", model, ok, err)
	}
	f.setting(t, f.ws, "default_model", `{"provider":"custom:gw","model":"big"}`)
	f.setting(t, f.other, "default_model", `{"provider":"custom:gw","model":"theirs"}`)
	if model, ok, err := f.refs.DefaultModel(ctx, f.ws, "custom:gw"); model != "big" || !ok || err != nil {
		t.Errorf("DefaultModel(gw) = %q, %v, %v, want big, true, nil", model, ok, err)
	}
	if model, ok, err := f.refs.DefaultModel(ctx, f.ws, "custom:else"); ok || err != nil {
		t.Errorf("DefaultModel(else) = %q, %v, %v, want not found", model, ok, err)
	}
}

// Find reads inside the transaction its ctx carries: a row written in it is seen before commit,
// and not by a reader outside it.
func TestReferencesFindJoinsTheTransaction(t *testing.T) {
	f := newFixture(t)
	err := f.store.InTx(t.Context(), func(ctx context.Context) error {
		err := pg.Conn(ctx, f.db).Exec(`INSERT INTO agents (workspace_id, name, provider, model, system_prompt)
			VALUES (?, 'inside', 'custom:tx', 'm', 'p')`, f.ws).Error
		if err != nil {
			return err
		}
		u, err := f.refs.Find(ctx, f.ws, "custom:tx")
		if err != nil {
			return err
		}
		if len(u.Agents) != 1 || u.Agents[0].Name != "inside" {
			t.Errorf("Find(tx ctx) = %+v, want the agent written in the transaction", u)
		}
		outside, err := f.refs.Find(t.Context(), f.ws, "custom:tx")
		if err != nil {
			return err
		}
		if !outside.Empty() {
			t.Errorf("Find(outside ctx) = %+v, want empty before the commit", outside)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

package app_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/agents/app"
	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/llm"
)

type customEnv struct {
	svc    *app.Service
	store  *fakeStore
	custom *fakeCustom
}

// newCustomEnv wires a service with a custom catalog that defines custom:synapse.
func newCustomEnv() customEnv {
	store := newFakeStore()
	tx := &fakeTx{store: store}
	cp := newFakeCustom(tx, "custom:synapse")
	svc := app.NewService(store, tx, &fakeModels{models: map[llm.Provider][]llm.ModelInfo{}},
		slog.New(slog.DiscardHandler), app.WithCustomProviders(cp))
	return customEnv{svc: svc, store: store, custom: cp}
}

func customInput(provider string) domain.CreateInput {
	return domain.CreateInput{Name: "A", Provider: provider, Model: "m", SystemPrompt: "p"}
}

func wantUndefined(t *testing.T, err error, id string) {
	t.Helper()
	var undefined *app.UndefinedProviderError
	if !errors.As(err, &undefined) || undefined.ID != id {
		t.Fatalf("error = %v, want UndefinedProviderError for %s", err, id)
	}
	if want := "Custom provider " + id + " is not configured"; err.Error() != want {
		t.Errorf("error text = %q, want %q", err.Error(), want)
	}
}

func TestCreate_CustomProvider(t *testing.T) {
	e := newCustomEnv()
	a, err := e.svc.Create(t.Context(), ws, "u", customInput("custom:synapse"))
	if err != nil {
		t.Fatalf("Create(custom:synapse) error = %v", err)
	}
	if a.Provider != "custom:synapse" {
		t.Errorf("Create(custom:synapse).Provider = %q, want it unchanged", a.Provider)
	}
	if diff := cmp.Diff([]string{"custom:synapse"}, e.custom.locked); diff != "" {
		t.Errorf("LockDefined calls mismatch (-want +got):\n%s", diff)
	}
	if len(e.custom.lockedOut) != 0 {
		t.Errorf("LockDefined ran outside the transaction for %v, want inside (FOR SHARE needs the tx)", e.custom.lockedOut)
	}
}

func TestCreate_UndefinedCustomProvider(t *testing.T) {
	e := newCustomEnv()
	_, err := e.svc.Create(t.Context(), ws, "u", customInput("custom:ghost"))
	wantUndefined(t, err, "custom:ghost")
	if n := len(e.store.agents); n != 0 {
		t.Errorf("agents stored after a refused create = %d, want 0", n)
	}
}

func TestCreate_BuiltInProviderSkipsCatalog(t *testing.T) {
	e := newCustomEnv()
	if _, err := e.svc.Create(t.Context(), ws, "u", customInput("openai")); err != nil {
		t.Fatalf("Create(openai) error = %v", err)
	}
	if len(e.custom.locked)+len(e.custom.askedDefined) != 0 {
		t.Errorf("catalog asked for a built-in provider: locked %v, defined %v", e.custom.locked, e.custom.askedDefined)
	}
}

func TestCreate_CustomWithoutCatalogIsUndefined(t *testing.T) {
	env := newEnv()
	_, err := env.svc.Create(t.Context(), ws, "u", customInput("custom:synapse"))
	wantUndefined(t, err, "custom:synapse")
}

func TestCreate_CatalogErrorPropagates(t *testing.T) {
	e := newCustomEnv()
	e.custom.err = errBoom
	_, err := e.svc.Create(t.Context(), ws, "u", customInput("custom:synapse"))
	if !errors.Is(err, errBoom) {
		t.Fatalf("Create() error = %v, want the catalog error", err)
	}
	var undefined *app.UndefinedProviderError
	if errors.As(err, &undefined) {
		t.Errorf("a failed lookup was reported as an undefined provider: %v", err)
	}
}

func TestUpdate_CustomProvider(t *testing.T) {
	e := newCustomEnv()
	a, err := e.svc.Create(t.Context(), ws, "u", customInput("openai"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Update(t.Context(), ws, a.ID, domain.Patch{Provider: ptr("custom:synapse")})
	if err != nil {
		t.Fatalf("Update(custom:synapse) error = %v", err)
	}
	if got.Provider != "custom:synapse" || got.Version != 2 {
		t.Errorf("Update(custom:synapse) = provider %q v%d, want custom:synapse v2", got.Provider, got.Version)
	}
	if len(e.custom.lockedOut) != 0 || len(e.custom.locked) != 1 {
		t.Errorf("LockDefined calls = %v (outside tx %v), want one inside the transaction", e.custom.locked, e.custom.lockedOut)
	}

	_, err = e.svc.Update(t.Context(), ws, a.ID, domain.Patch{Provider: ptr("custom:ghost")})
	wantUndefined(t, err, "custom:ghost")
	if again, _ := e.svc.Get(t.Context(), ws, a.ID); again.Provider != "custom:synapse" || again.Version != 2 {
		t.Errorf("agent after a refused update = provider %q v%d, want it untouched", again.Provider, again.Version)
	}
}

func TestUpdate_WithoutProviderSkipsCatalog(t *testing.T) {
	e := newCustomEnv()
	a, err := e.svc.Create(t.Context(), ws, "u", customInput("custom:synapse"))
	if err != nil {
		t.Fatal(err)
	}
	e.custom.locked = nil
	if _, err := e.svc.Update(t.Context(), ws, a.ID, domain.Patch{Name: ptr("Renamed")}); err != nil {
		t.Fatalf("Update(name) error = %v", err)
	}
	if len(e.custom.locked) != 0 {
		t.Errorf("a patch without provider locked %v, want no lookup", e.custom.locked)
	}
}

func TestProviderModels_Custom(t *testing.T) {
	e := newCustomEnv()
	want := []llm.ModelInfo{{ID: "a", Provider: "custom:synapse"}, {ID: "b", Provider: "custom:synapse"}}
	e.custom.models["custom:synapse"] = want

	got, err := e.svc.ProviderModels(t.Context(), ws, "custom:synapse")
	if err != nil {
		t.Fatalf("ProviderModels(custom:synapse) error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ProviderModels(custom:synapse) mismatch (-want +got):\n%s", diff)
	}

	got, err = e.svc.ProviderModels(t.Context(), ws, "custom:ghost")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ProviderModels(custom:ghost) = %#v, %v, want an empty non-nil list", got, err)
	}
	if diff := cmp.Diff([]string{"custom:synapse"}, e.custom.listed); diff != "" {
		t.Errorf("Models calls mismatch (-want +got):\n%s", diff)
	}
}

func TestProviderModels_CustomWithoutCatalogIsEmpty(t *testing.T) {
	got, err := newEnv().svc.ProviderModels(t.Context(), ws, "custom:synapse")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ProviderModels(custom:synapse) without a catalog = %#v, %v, want an empty non-nil list", got, err)
	}
}

func TestProviderModels_CatalogErrors(t *testing.T) {
	e := newCustomEnv()
	e.custom.modelErr = errBoom
	if _, err := e.svc.ProviderModels(t.Context(), ws, "custom:synapse"); !errors.Is(err, errBoom) {
		t.Errorf("ProviderModels() with a failing catalog = %v, want the error", err)
	}
	e.custom.err = errBoom
	if _, err := e.svc.ProviderModels(t.Context(), ws, "custom:synapse"); !errors.Is(err, errBoom) {
		t.Errorf("ProviderModels() with a failing lookup = %v, want the error", err)
	}
}

func TestAgentModels_CustomAgent(t *testing.T) {
	e := newCustomEnv()
	want := []llm.ModelInfo{{ID: "a", Provider: "custom:synapse"}}
	e.custom.models["custom:synapse"] = want
	a, err := e.svc.Create(t.Context(), ws, "u", customInput("custom:synapse"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.AgentModels(t.Context(), ws, a.ID)
	if err != nil {
		t.Fatalf("AgentModels() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("AgentModels() mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdate_UnchangedUndefinedProviderIsExempt(t *testing.T) {
	e := newCustomEnv()
	stored, err := e.store.Insert(t.Context(), ws, "u", domain.New(customInput("custom:ghost")))
	if err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Update(t.Context(), ws, stored.ID, domain.Patch{Provider: ptr("custom:ghost"), Name: ptr("Renamed")})
	if err != nil {
		t.Fatalf("Update(same undefined provider, new name) error = %v, want success", err)
	}
	if got.Name != "Renamed" || got.Provider != "custom:ghost" {
		t.Errorf("Update() = name %q provider %q, want Renamed custom:ghost", got.Name, got.Provider)
	}
	if len(e.custom.locked) != 0 {
		t.Errorf("an unchanged provider locked %v, want no lookup", e.custom.locked)
	}

	_, err = e.svc.Update(t.Context(), ws, stored.ID, domain.Patch{Provider: ptr("custom:other")})
	wantUndefined(t, err, "custom:other")
}

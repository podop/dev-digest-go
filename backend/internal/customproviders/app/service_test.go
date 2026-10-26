package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/egress"
	"dev-digest-go/backend/internal/platform/secrets"
)

const (
	ws        = "ws1"
	synapse   = "custom:synapse"
	keyValue  = "sk-SYNAPSE-SECRET-1"
	headValue = "TENANT-SECRET-9"
)

type rig struct {
	svc      *app.Service
	store    *fakeStore
	secrets  *fakeSecrets
	refs     *fakeReferences
	adapters *fakeAdapters
	ad       *fakeAdapter
	logs     *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{store: newFakeStore(), secrets: newFakeSecrets(), ad: &fakeAdapter{}, logs: &bytes.Buffer{}}
	r.refs = &fakeReferences{usage: map[string]domain.Usage{}, defaults: map[string]string{}, store: r.store}
	r.adapters = &fakeAdapters{adapter: r.ad}
	var (
		mu   sync.Mutex
		tick int
	)
	base := time.Date(2026, 10, 26, 12, 0, 0, 123456789, time.UTC)
	r.svc = app.NewService(app.Deps{
		Store: r.store, Secrets: r.secrets, References: r.refs, Adapters: r.adapters,
		Clock: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			tick++
			return base.Add(time.Duration(tick) * time.Second)
		},
		Log: slog.New(slog.NewTextHandler(&syncWriter{w: r.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return r
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func sp(s string) *string { return &s }

// input is a complete valid request: key, one extra header and one priced manual model.
func input() domain.Input {
	return domain.Input{
		ID: synapse, Name: "Synapse", Protocol: custom.ProtocolOpenAI, BaseURL: "https://synapse.test/v1/",
		AuthMode: custom.AuthHeader, AuthHeader: sp("api-key"),
		APIKey:       domain.KeyChange{Op: domain.KeyReplace, Value: keyValue},
		ExtraHeaders: []domain.HeaderInput{{Name: "X-Tenant", Value: sp(headValue)}},
		Models:       []domain.Model{{ID: "m1", Label: sp("M one"), Pricing: &domain.Pricing{PromptPerM: 2, CompletionPerM: 8}}},
	}
}

func (r *rig) create(t *testing.T, in domain.Input) app.View {
	t.Helper()
	v, err := r.svc.Create(t.Context(), ws, in)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", in.ID, err)
	}
	return v
}

func wantAppErr(t *testing.T, err error, kind apperr.Kind, code string) *apperr.Error {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("error = %v (%T), want *apperr.Error", err, err)
	}
	if ae.Kind != kind || ae.Code != code {
		t.Fatalf("error = {%s %s %q}, want {%s %s}", ae.Kind, ae.Code, ae.Message, kind, code)
	}
	return ae
}

// issuePaths lists the paths of the 422 details of err as "a.b.0" strings.
func issuePaths(t *testing.T, err error) []string {
	t.Helper()
	ae := wantAppErr(t, err, apperr.KindValidation, "validation_error")
	raw, mErr := json.Marshal(ae.Details)
	if mErr != nil {
		t.Fatal(mErr)
	}
	var details []struct {
		Params struct {
			Issue struct{ Path []any }
		}
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range details {
		parts := make([]string, len(d.Params.Issue.Path))
		for i, p := range d.Params.Issue.Path {
			if f, ok := p.(float64); ok {
				parts[i] = fmt.Sprint(int(f))
			} else {
				parts[i] = fmt.Sprint(p)
			}
		}
		out = append(out, strings.Join(parts, "."))
	}
	return out
}

func (r *rig) noSecretInLogs(t *testing.T) {
	t.Helper()
	for _, secret := range []string{keyValue, headValue, "NEW-SECRET", "OTHER-SECRET"} {
		if strings.Contains(r.logs.String(), secret) {
			t.Errorf("a log line contains the secret %q:\n%s", secret, r.logs.String())
		}
	}
}

// ---- Create ----

func TestCreate_StoresProviderAndSecrets(t *testing.T) { // AC1, FR1, FR5
	r := newRig(t)
	v := r.create(t, input())

	p := v.Provider
	if p.ID != synapse || p.BaseURL != "https://synapse.test/v1" || p.AuthMode != custom.AuthHeader || p.AuthHeader != "api-key" {
		t.Errorf("Create() provider = %+v", p)
	}
	if !v.KeyConfigured || v.PlainHTTP {
		t.Errorf("Create() KeyConfigured = %v PlainHTTP = %v, want true false", v.KeyConfigured, v.PlainHTTP)
	}
	if diff := cmp.Diff([]app.HeaderView{{Name: "X-Tenant", ValueConfigured: true}}, v.ExtraHeaders); diff != "" {
		t.Errorf("Create() ExtraHeaders mismatch (-want +got):\n%s", diff)
	}
	if want := time.Date(2026, 10, 26, 12, 0, 1, 123000000, time.UTC); !p.CreatedAt.Equal(want) || !p.UpdatedAt.Equal(want) {
		t.Errorf("Create() times = %v, %v; want %v (UTC, milliseconds)", p.CreatedAt, p.UpdatedAt, want)
	}
	want := secrets.CustomSecret{Key: keyValue, Headers: []secrets.CustomHeader{{Name: "X-Tenant", Value: headValue}}}
	if diff := cmp.Diff(want, r.secrets.Custom(ws, synapse)); diff != "" {
		t.Errorf("stored secrets mismatch (-want +got):\n%s", diff)
	}
	got, err := r.svc.Get(t.Context(), ws, synapse)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if diff := cmp.Diff(v, got); diff != "" {
		t.Errorf("Get() differs from the Create() view (-create +get):\n%s", diff)
	}
	r.noSecretInLogs(t)
}

func TestCreate_WithoutKeyStoresNoSecret(t *testing.T) {
	r := newRig(t)
	in := input()
	in.APIKey, in.ExtraHeaders = domain.KeyChange{}, nil
	v := r.create(t, in)
	if v.KeyConfigured {
		t.Errorf("KeyConfigured = true, want false")
	}
	if !r.secrets.Custom(ws, synapse).IsZero() {
		t.Errorf("secrets = %+v, want none", r.secrets.Custom(ws, synapse))
	}
}

func TestCreate_PlainHTTP(t *testing.T) { // EC24
	r := newRig(t)
	in := input()
	in.BaseURL = "http://gateway.corp/v1"
	if v := r.create(t, in); !v.PlainHTTP {
		t.Errorf("PlainHTTP = false, want true")
	}
}

func TestCreate_Invalid(t *testing.T) { // AC2, EC2, EC3, EC11, EC13
	tests := []struct {
		name      string
		edit      func(*domain.Input)
		wantPaths []string
	}{
		{"bad id", func(in *domain.Input) { in.ID = "custom:Syn" }, []string{"id"}},
		{"bad base url", func(in *domain.Input) { in.BaseURL = "http://169.254.169.254" }, []string{"base_url"}},
		{"reserved header", func(in *domain.Input) { in.ExtraHeaders = []domain.HeaderInput{{Name: "content-type", Value: sp("x")}} }, []string{"extra_headers.0.name"}},
		{"header without a value", func(in *domain.Input) { in.ExtraHeaders = []domain.HeaderInput{{Name: "X-New"}} }, []string{"extra_headers.0.value"}},
		{"json object with anthropic", func(in *domain.Input) {
			in.Protocol, in.StructuredOutput = custom.ProtocolAnthropic, custom.ModeJSONObject
		}, []string{"structured_output"}},
		{"201 models", func(in *domain.Input) {
			in.Models = nil
			for i := range 201 {
				in.Models = append(in.Models, domain.Model{ID: fmt.Sprint("m", i)})
			}
		}, []string{"models"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			in := input()
			tc.edit(&in)
			_, err := r.svc.Create(t.Context(), ws, in)
			if diff := cmp.Diff(tc.wantPaths, issuePaths(t, err)); diff != "" {
				t.Errorf("Create() issue paths mismatch (-want +got):\n%s", diff)
			}
			if r.store.count(ws) != 0 || len(r.secrets.callLog()) != 0 {
				t.Errorf("a refused create stored something: providers %d, secret calls %v", r.store.count(ws), r.secrets.callLog())
			}
		})
	}
}

func TestCreate_ValidationDetailsShape(t *testing.T) {
	r := newRig(t)
	in := input()
	in.Name = ""
	_, err := r.svc.Create(t.Context(), ws, in)
	ae := wantAppErr(t, err, apperr.KindValidation, "validation_error")
	raw, _ := json.Marshal(ae.Details)
	want := `[{"keyword":"too_small","instancePath":"/name","schemaPath":"#/name/too_small",` +
		`"message":"String must contain at least 1 character(s)",` +
		`"params":{"issue":{"code":"too_small","path":["name"],"message":"String must contain at least 1 character(s)"}}}]`
	if string(raw) != want {
		t.Errorf("details = %s\nwant      %s", raw, want)
	}
	if ae.Message != "Request validation failed" {
		t.Errorf("message = %q", ae.Message)
	}
}

func TestCreate_Duplicate(t *testing.T) { // EC1: the existing provider and its secrets stay
	r := newRig(t)
	r.create(t, input())
	in := input()
	in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: "OTHER-SECRET-77"}
	_, err := r.svc.Create(t.Context(), ws, in)
	ae := wantAppErr(t, err, apperr.KindConflict, "custom_provider_exists")
	if ae.Message != "Custom provider custom:synapse already exists" {
		t.Errorf("message = %q", ae.Message)
	}
	if got := r.secrets.Custom(ws, synapse).Key; got != keyValue {
		t.Errorf("key after a refused duplicate = %q, want the original", got)
	}
}

func TestCreate_Limit(t *testing.T) { // EC13
	r := newRig(t)
	for i := range domain.MaxProviders {
		in := input()
		in.ID = fmt.Sprintf("custom:p%d", i)
		in.APIKey, in.ExtraHeaders = domain.KeyChange{}, nil
		r.create(t, in)
	}
	in := input()
	in.ID = "custom:one-more"
	_, err := r.svc.Create(t.Context(), ws, in)
	ae := wantAppErr(t, err, apperr.KindValidation, "custom_provider_limit")
	if ae.Message != "At most 20 custom providers" {
		t.Errorf("message = %q", ae.Message)
	}
	if r.store.count(ws) != domain.MaxProviders || !r.secrets.Custom(ws, "custom:one-more").IsZero() {
		t.Errorf("the refused create left data behind")
	}
}

func TestCreate_ConcurrentSameID(t *testing.T) { // EC1
	r := newRig(t)
	const callers = 8
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			_, err := r.svc.Create(context.Background(), ws, input())
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var created, conflicts int
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, domain.ErrExists):
			conflicts++
		default:
			if ae, ok := apperr.As(err); ok && ae.Code == "custom_provider_exists" {
				conflicts++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}
	}
	if created != 1 || conflicts != callers-1 {
		t.Errorf("created %d, conflicts %d; want 1 and %d", created, conflicts, callers-1)
	}
}

func TestCreate_FailedCommitRestoresSecrets(t *testing.T) { // A7
	r := newRig(t)
	stale := secrets.CustomSecret{Key: "STALE-KEY-0000"}
	r.secrets.put(ws, synapse, stale)
	r.store.commitErr = errors.New("commit failed")

	_, err := r.svc.Create(t.Context(), ws, input())
	if err == nil || !strings.Contains(err.Error(), "commit failed") {
		t.Fatalf("Create() error = %v, want the commit failure", err)
	}
	if r.store.has(ws, synapse) {
		t.Errorf("the provider exists after a failed commit")
	}
	if diff := cmp.Diff(stale, r.secrets.Custom(ws, synapse)); diff != "" {
		t.Errorf("secrets after a failed commit (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"set custom:synapse", "restore custom:synapse"}, r.secrets.callLog()); diff != "" {
		t.Errorf("secret calls (-want +got):\n%s", diff)
	}
}

func TestCreate_SecretsWriteFailureRollsBack(t *testing.T) {
	r := newRig(t)
	r.secrets.setErr = errors.New("disk full")
	_, err := r.svc.Create(t.Context(), ws, input())
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Create() error = %v, want the write failure", err)
	}
	if r.store.has(ws, synapse) {
		t.Errorf("the provider exists although its secrets could not be written")
	}
}

func TestCreate_FailedRestoreIsLoggedWithoutValues(t *testing.T) {
	r := newRig(t)
	r.store.commitErr = errors.New("commit failed")
	r.secrets.restoreErr = errors.New("restore failed")
	_, _ = r.svc.Create(t.Context(), ws, input())
	if !strings.Contains(r.logs.String(), "level=ERROR") || !strings.Contains(r.logs.String(), "could not be restored") {
		t.Errorf("no ERROR line for the failed restore:\n%s", r.logs.String())
	}
	r.noSecretInLogs(t)
}

// ---- Update ----

func TestUpdate_KeyAndHeaders(t *testing.T) { // AC6, EC12, FR5
	type want struct {
		key     string
		headers []secrets.CustomHeader
	}
	tests := []struct {
		name string
		edit func(*domain.Input)
		want want
	}{
		{"api_key omitted keeps key, header kept by name in another case",
			func(in *domain.Input) {
				in.APIKey = domain.KeyChange{}
				in.ExtraHeaders = []domain.HeaderInput{{Name: "x-tenant"}}
			},
			want{keyValue, []secrets.CustomHeader{{Name: "x-tenant", Value: headValue}}}},
		{"api_key null removes the key",
			func(in *domain.Input) {
				in.APIKey = domain.KeyChange{Op: domain.KeyRemove}
				in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant"}}
			},
			want{"", []secrets.CustomHeader{{Name: "X-Tenant", Value: headValue}}}},
		{"api_key string replaces the key, header value replaced",
			func(in *domain.Input) {
				in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: "NEW-SECRET-key1"}
				in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant", Value: sp("NEW-SECRET-hdr")}}
			},
			want{"NEW-SECRET-key1", []secrets.CustomHeader{{Name: "X-Tenant", Value: "NEW-SECRET-hdr"}}}},
		{"headers not listed are removed with their values",
			func(in *domain.Input) {
				in.APIKey = domain.KeyChange{}
				in.ExtraHeaders = nil
			},
			want{keyValue, nil}},
		{"new header with a value, old one dropped",
			func(in *domain.Input) {
				in.APIKey = domain.KeyChange{}
				in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Other", Value: sp("OTHER-SECRET-3")}}
			},
			want{keyValue, []secrets.CustomHeader{{Name: "X-Other", Value: "OTHER-SECRET-3"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.create(t, input())
			in := input()
			tc.edit(&in)
			v, err := r.svc.Update(t.Context(), ws, synapse, in)
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			got := r.secrets.Custom(ws, synapse)
			if diff := cmp.Diff(tc.want.key, got.Key); diff != "" {
				t.Errorf("key mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.headers, got.Headers); diff != "" {
				t.Errorf("headers mismatch (-want +got):\n%s", diff)
			}
			again, err := r.svc.Get(t.Context(), ws, synapse)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if again.KeyConfigured != (tc.want.key != "") || len(again.ExtraHeaders) != len(tc.want.headers) {
				t.Errorf("Get() = key %v, %d headers; want key %v, %d headers", again.KeyConfigured, len(again.ExtraHeaders), tc.want.key != "", len(tc.want.headers))
			}
			if diff := cmp.Diff(v, again); diff != "" {
				t.Errorf("Update() view differs from Get() (-update +get):\n%s", diff)
			}
		})
	}
}

func TestUpdate_NewHeaderWithoutValueChangesNothing(t *testing.T) { // EC12
	r := newRig(t)
	created := r.create(t, input())
	in := input()
	in.APIKey = domain.KeyChange{Op: domain.KeyRemove}
	in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant"}, {Name: "X-Fresh"}}
	_, err := r.svc.Update(t.Context(), ws, synapse, in)
	if diff := cmp.Diff([]string{"extra_headers.1.value"}, issuePaths(t, err)); diff != "" {
		t.Errorf("Update() issue paths mismatch (-want +got):\n%s", diff)
	}
	after, _ := r.svc.Get(t.Context(), ws, synapse)
	if diff := cmp.Diff(created, after); diff != "" {
		t.Errorf("provider changed by a refused update (-before +after):\n%s", diff)
	}
	if got := r.secrets.Custom(ws, synapse).Key; got != keyValue {
		t.Errorf("key = %q after a refused update, want kept", got)
	}
}

func TestUpdate_ReplacesFieldsButNotIDOrCreatedAt(t *testing.T) { // FR2
	r := newRig(t)
	created := r.create(t, input())
	in := input()
	in.ID = "custom:ignored-in-body"
	in.Name = "Renamed"
	in.BaseURL = "http://gateway.corp/v1/"
	in.Models = []domain.Model{{ID: "z"}, {ID: "a"}}
	in.APIKey = domain.KeyChange{}
	in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant"}}
	v, err := r.svc.Update(t.Context(), ws, synapse, in)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	p := v.Provider
	if p.ID != synapse || p.Name != "Renamed" || p.BaseURL != "http://gateway.corp/v1" || !v.PlainHTTP {
		t.Errorf("Update() provider = %+v plain=%v", p, v.PlainHTTP)
	}
	if !p.CreatedAt.Equal(created.Provider.CreatedAt) || !p.UpdatedAt.After(created.Provider.UpdatedAt) {
		t.Errorf("times created %v updated %v; want created kept, updated later than %v", p.CreatedAt, p.UpdatedAt, created.Provider.UpdatedAt)
	}
	if got := []string{p.Models[0].ID, p.Models[1].ID}; got[0] != "z" || got[1] != "a" {
		t.Errorf("model order = %v, want the given order", got)
	}
	if r.store.has(ws, "custom:ignored-in-body") {
		t.Errorf("the id in the body created a provider")
	}
}

func TestUpdate_NotFoundAndInvalid(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	for _, id := range []string{"custom:ghost", "custom:Bad", "synapse", ""} {
		_, err := r.svc.Update(t.Context(), ws, id, input())
		wantAppErr(t, err, apperr.KindNotFound, "custom_provider_not_found")
	}
	in := input()
	in.BaseURL = "ftp://x"
	_, err := r.svc.Update(t.Context(), ws, synapse, in)
	if diff := cmp.Diff([]string{"base_url"}, issuePaths(t, err)); diff != "" {
		t.Errorf("Update() issue paths mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdate_FailedCommitRestoresSecretsAndProvider(t *testing.T) { // A7
	r := newRig(t)
	before := r.create(t, input())
	r.store.commitErr = errors.New("commit failed")
	in := input()
	in.Name = "Changed"
	in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: "NEW-SECRET-key1"}
	in.ExtraHeaders = nil
	if _, err := r.svc.Update(t.Context(), ws, synapse, in); err == nil {
		t.Fatal("Update() error = nil, want the commit failure")
	}
	r.store.commitErr = nil
	after, _ := r.svc.Get(t.Context(), ws, synapse)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("provider after a failed commit (-before +after):\n%s", diff)
	}
}

func TestUpdate_UnchangedSecretsAreNotRewritten(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	calls := len(r.secrets.callLog())
	in := input()
	in.APIKey = domain.KeyChange{}
	in.ExtraHeaders = []domain.HeaderInput{{Name: "X-Tenant"}}
	in.Name = "Only the name"
	if _, err := r.svc.Update(t.Context(), ws, synapse, in); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got := len(r.secrets.callLog()); got != calls {
		t.Errorf("secret writes = %d, want %d (nothing changed)", got, calls)
	}
}

// ---- Delete ----

func TestDelete_RemovesProviderAndSecrets(t *testing.T) { // AC1
	r := newRig(t)
	r.create(t, input())
	got, err := r.svc.Delete(t.Context(), ws, synapse)
	if err != nil || got != synapse {
		t.Fatalf("Delete() = %q, %v; want %q, nil", got, err, synapse)
	}
	if r.store.has(ws, synapse) || !r.secrets.Custom(ws, synapse).IsZero() {
		t.Errorf("provider or secrets remain after Delete")
	}
	if _, err := r.svc.Get(t.Context(), ws, synapse); err == nil {
		t.Errorf("Get() after Delete: error = nil")
	}
	r.noSecretInLogs(t)
}

func TestDelete_LocksThenChecksReferences(t *testing.T) { // A8
	r := newRig(t)
	r.create(t, input())
	if _, err := r.svc.Delete(t.Context(), ws, synapse); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	want := []string{"insert custom:synapse", "get_for_update custom:synapse", "references custom:synapse", "delete custom:synapse"}
	if diff := cmp.Diff(want, r.store.eventLog()); diff != "" {
		t.Errorf("store event order (-want +got):\n%s", diff)
	}
}

func TestDelete_InUse(t *testing.T) { // AC7, EC7, FR6
	r := newRig(t)
	r.create(t, input())
	r.refs.usage[synapse] = domain.Usage{
		Agents:        []domain.AgentRef{{ID: "a-1", Name: "Disabled reviewer"}},
		FeatureModels: []string{"conventions"},
		DefaultModel:  true,
	}
	_, err := r.svc.Delete(t.Context(), ws, synapse)
	ae := wantAppErr(t, err, apperr.KindConflict, "provider_in_use")
	raw, _ := json.Marshal(ae.Details)
	want := `{"agents":[{"id":"a-1","name":"Disabled reviewer"}],"feature_models":["conventions"],"default_model":true}`
	if string(raw) != want {
		t.Errorf("details = %s, want %s", raw, want)
	}
	if !r.store.has(ws, synapse) {
		t.Errorf("the provider was deleted although it is in use")
	}
	if got := r.secrets.Custom(ws, synapse); got.Key != keyValue || len(got.Headers) != 1 {
		t.Errorf("secrets = %+v, want kept", got)
	}
}

func TestDelete_InUseDetailsAreNeverNull(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.refs.usage[synapse] = domain.Usage{DefaultModel: true}
	_, err := r.svc.Delete(t.Context(), ws, synapse)
	ae := wantAppErr(t, err, apperr.KindConflict, "provider_in_use")
	raw, _ := json.Marshal(ae.Details)
	if want := `{"agents":[],"feature_models":[],"default_model":true}`; string(raw) != want {
		t.Errorf("details = %s, want %s", raw, want)
	}
}

func TestDelete_NotFound(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"custom:ghost", "custom:Bad", "openai"} {
		_, err := r.svc.Delete(t.Context(), ws, id)
		wantAppErr(t, err, apperr.KindNotFound, "custom_provider_not_found")
	}
	if r.refs.findCalls != 0 {
		t.Errorf("References asked %d times for providers that do not exist", r.refs.findCalls)
	}
}

func TestDelete_ReferencesFailureDeletesNothing(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.refs.err = errors.New("agents unreadable")
	if _, err := r.svc.Delete(t.Context(), ws, synapse); err == nil {
		t.Fatal("Delete() error = nil, want the references failure")
	}
	if !r.store.has(ws, synapse) || r.secrets.Custom(ws, synapse).IsZero() {
		t.Errorf("a failed reference check deleted data")
	}
}

func TestDelete_FailedCommitRestoresSecrets(t *testing.T) { // A7
	r := newRig(t)
	r.create(t, input())
	r.store.commitErr = errors.New("commit failed")
	if _, err := r.svc.Delete(t.Context(), ws, synapse); err == nil {
		t.Fatal("Delete() error = nil, want the commit failure")
	}
	r.store.commitErr = nil
	if !r.store.has(ws, synapse) {
		t.Errorf("provider gone after a failed commit")
	}
	if got := r.secrets.Custom(ws, synapse); got.Key != keyValue || len(got.Headers) != 1 {
		t.Errorf("secrets = %+v after a failed commit, want restored", got)
	}
}

// ---- List / Get ----

func TestList_SortedByNameThenIDInByteOrder(t *testing.T) { // FR2
	r := newRig(t)
	mk := func(id, name string) domain.Provider {
		return domain.Provider{ID: id, Name: name, Protocol: custom.ProtocolOpenAI, BaseURL: "https://x.test"}
	}
	r.store.seed(ws, mk("custom:c", "beta"), mk("custom:b", "beta"), mk("custom:z", "Zed"), mk("custom:a", "alpha"))
	list, err := r.svc.List(t.Context(), ws)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var got []string
	for _, v := range list {
		got = append(got, v.Provider.ID)
	}
	if diff := cmp.Diff([]string{"custom:z", "custom:a", "custom:b", "custom:c"}, got); diff != "" {
		t.Errorf("List() order (-want +got):\n%s", diff)
	}
}

func TestList_EmptyIsNotNil(t *testing.T) {
	list, err := newRig(t).svc.List(t.Context(), ws)
	if err != nil || list == nil || len(list) != 0 {
		t.Errorf("List() = %v, %v; want an empty non-nil slice", list, err)
	}
}

func TestGet_NotFound(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"custom:ghost", "custom:Bad!", "openai"} {
		_, err := r.svc.Get(t.Context(), ws, id)
		wantAppErr(t, err, apperr.KindNotFound, "custom_provider_not_found")
	}
}

func TestGet_OtherWorkspaceDoesNotSee(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	if _, err := r.svc.Get(t.Context(), "ws2", synapse); err == nil {
		t.Errorf("Get() from another workspace: error = nil")
	}
}

func TestGet_StoreFailureIsNotA404(t *testing.T) {
	r := newRig(t)
	r.store.getErr = errors.New("db down")
	_, err := r.svc.Get(t.Context(), ws, synapse)
	if err == nil {
		t.Fatal("Get() error = nil")
	}
	if ae, ok := apperr.As(err); ok && ae.Kind == apperr.KindNotFound {
		t.Errorf("a store failure was reported as not found")
	}
}

// ---- Defined ----

func TestDefined(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	tests := []struct {
		id   string
		want bool
	}{{synapse, true}, {"custom:ghost", false}, {"custom:Bad", false}, {"openai", false}, {"", false}}
	for _, tc := range tests {
		if got := r.svc.Defined(t.Context(), ws, tc.id); got != tc.want {
			t.Errorf("Defined(%q) = %v, want %v", tc.id, got, tc.want)
		}
		got, err := r.svc.IsDefined(t.Context(), ws, tc.id)
		if err != nil || got != tc.want {
			t.Errorf("IsDefined(%q) = %v, %v; want %v, nil", tc.id, got, err, tc.want)
		}
	}
}

func TestDefined_StoreFailure(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.store.getErr = errors.New("db down")
	if r.svc.Defined(t.Context(), ws, synapse) {
		t.Errorf("Defined() = true on a store failure, want false")
	}
	if _, err := r.svc.IsDefined(t.Context(), ws, synapse); err == nil {
		t.Errorf("IsDefined() error = nil on a store failure")
	}
}

func TestLockDefined(t *testing.T) { // A8
	r := newRig(t)
	r.create(t, input())
	ok, err := r.svc.LockDefined(t.Context(), ws, synapse)
	if err != nil || !ok {
		t.Fatalf("LockDefined() = %v, %v; want true, nil", ok, err)
	}
	if ok, _ := r.svc.LockDefined(t.Context(), ws, "custom:ghost"); ok {
		t.Errorf("LockDefined(ghost) = true")
	}
	if ok, _ := r.svc.LockDefined(t.Context(), ws, "custom:Bad"); ok {
		t.Errorf("LockDefined(malformed) = true")
	}
	events := r.store.eventLog()
	if got := events[len(events)-2:]; got[0] != "lock_defined custom:synapse" || got[1] != "lock_defined custom:ghost" {
		t.Errorf("store events = %v; a malformed id must not reach the store", events)
	}
}

// ---- TestConnection ----

func listed(ids ...string) []llm.ModelInfo {
	out := make([]llm.ModelInfo, len(ids))
	for i, id := range ids {
		out[i] = llm.ModelInfo{ID: id, Provider: synapse}
	}
	return out
}

func TestTestConnection_OK(t *testing.T) { // AC11, FR11
	tests := []struct {
		name          string
		listed        []string
		defaultModel  string
		manual        bool
		wantModel     string
		wantReachable bool
	}{
		{name: "first manual model", listed: []string{"b", "m1"}, manual: true, wantModel: "m1", wantReachable: true},
		{name: "manual model not listed", listed: []string{"b"}, manual: true, wantModel: "m1", wantReachable: false},
		{name: "default model wins", listed: []string{"b", "m1", "d"}, defaultModel: "d", manual: true, wantModel: "d", wantReachable: true},
		{name: "default model unlisted", listed: []string{"b"}, defaultModel: "gone", manual: true, wantModel: "gone", wantReachable: false},
		{name: "first listed model", listed: []string{"b", "c"}, wantModel: "b", wantReachable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			in := input()
			if !tc.manual {
				in.Models = nil
			}
			r.create(t, in)
			r.ad.list = listed(tc.listed...)
			if tc.defaultModel != "" {
				r.refs.defaults[synapse] = tc.defaultModel
			}
			res, err := r.svc.TestConnection(t.Context(), ws, synapse, "")
			if err != nil {
				t.Fatalf("TestConnection() error = %v", err)
			}
			if !res.OK || res.Message != fmt.Sprintf("OK — %d models available", len(tc.listed)) || res.Warning != nil {
				t.Errorf("TestConnection() = %+v", res)
			}
			if res.Model == nil || *res.Model != tc.wantModel || res.ModelReachable == nil || *res.ModelReachable != tc.wantReachable {
				t.Errorf("model = %v reachable = %v; want %q %v", deref(res.Model), res.ModelReachable, tc.wantModel, tc.wantReachable)
			}
			if res.LatencyMS == nil || *res.LatencyMS < 0 {
				t.Errorf("LatencyMS = %v", res.LatencyMS)
			}
			if r.ad.listCalls != 1 || r.ad.completeCalls != 0 {
				t.Errorf("list calls %d, completions %d; want exactly one list and no completion", r.ad.listCalls, r.ad.completeCalls)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestTestConnection_NoModelAtAll(t *testing.T) {
	r := newRig(t)
	in := input()
	in.Models = nil
	r.create(t, in)
	res, err := r.svc.TestConnection(t.Context(), ws, synapse, "")
	if err != nil || !res.OK || res.Model != nil || res.ModelReachable != nil {
		t.Errorf("TestConnection() = %+v, %v; want ok with no model", res, err)
	}
}

func TestTestConnection_DefaultModelErrorFallsBack(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.refs.defaultErr = errors.New("settings unreadable")
	r.ad.list = listed("m1")
	res, err := r.svc.TestConnection(t.Context(), ws, synapse, "")
	if err != nil || !res.OK || deref(res.Model) != "m1" {
		t.Errorf("TestConnection() = %+v, %v; want ok with the first manual model", res, err)
	}
}

func TestTestConnection_NoModelListIsAWarning(t *testing.T) { // EC5
	for _, code := range []int{404, 405} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			r := newRig(t)
			r.create(t, input())
			r.ad.listErr = &statusErr{code, "not found"}
			res, err := r.svc.TestConnection(t.Context(), ws, synapse, "")
			if err != nil {
				t.Fatalf("TestConnection() error = %v", err)
			}
			if !res.OK || res.Warning == nil || *res.Warning != "Model list not available; key not verified" || res.ModelReachable != nil {
				t.Errorf("TestConnection() = %+v, want ok with the warning and no model_reachable", res)
			}
			if deref(res.Model) != "m1" {
				t.Errorf("Model = %q, want the first manual model", deref(res.Model))
			}
		})
	}
}

func TestTestConnection_NoModelListWithoutAnyModel(t *testing.T) {
	r := newRig(t)
	in := input()
	in.Models = nil
	r.create(t, in)
	r.ad.listErr = &statusErr{404, "not found"}
	res, _ := r.svc.TestConnection(t.Context(), ws, synapse, "")
	if !res.OK || res.Model != nil {
		t.Errorf("TestConnection() = %+v, want ok with a null model", res)
	}
}

func TestTestConnection_Failures(t *testing.T) { // EC3, EC6, NFR1
	masked := `Incorrect API key provided: sk-SYN****-1 (tenant TENANT-SECRET-9 / ` + keyValue + `)`
	tests := []struct {
		name    string
		err     error
		wantMsg string
		notIn   []string
	}{
		{name: "401 is a fixed message", err: &statusErr{401, masked}, wantMsg: "API key rejected by the provider", notIn: []string{"SYN", keyValue, headValue}},
		{name: "403 is a fixed message", err: &statusErr{403, masked}, wantMsg: "API key rejected by the provider"},
		{name: "forbidden address", err: &url.Error{Op: "Get", URL: "https://synapse.test/v1/models?x=1", Err: egress.ErrForbiddenAddr}, wantMsg: egress.ForbiddenMessage, notIn: []string{"synapse.test"}},
		{name: "500 echoing the key and a header value", err: &statusErr{500, "upstream failed for " + keyValue + " and " + headValue}, notIn: []string{keyValue, headValue}},
		{name: "masked key echo", err: &statusErr{500, "bad key sk-SYN****-1 given"}, notIn: []string{"sk-SYN"}},
		{name: "url error is unwrapped", err: &url.Error{Op: "Get", URL: "https://synapse.test/v1/models?token=abc", Err: errors.New("connection refused")}, wantMsg: "connection refused", notIn: []string{"synapse.test", "token=abc"}},
		{name: "html page", err: errors.New("/models returned an unreadable response"), wantMsg: "/models returned an unreadable response"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.create(t, input())
			r.ad.listErr = tc.err
			res, err := r.svc.TestConnection(t.Context(), ws, synapse, "")
			if err != nil {
				t.Fatalf("TestConnection() error = %v, want a result", err)
			}
			if res.OK || res.LatencyMS != nil || res.Model != nil || res.ModelReachable != nil || res.Warning != nil {
				t.Errorf("TestConnection() = %+v, want ok=false with null extras", res)
			}
			if tc.wantMsg != "" && res.Message != tc.wantMsg {
				t.Errorf("Message = %q, want %q", res.Message, tc.wantMsg)
			}
			for _, s := range tc.notIn {
				if strings.Contains(res.Message, s) {
					t.Errorf("Message %q contains %q", res.Message, s)
				}
			}
			if res.Message == "" {
				t.Errorf("empty message")
			}
			r.noSecretInLogs(t)
		})
	}
}

func TestTestConnection_FailureWithoutKeyNamesTheStatus(t *testing.T) {
	r := newRig(t)
	in := input()
	in.APIKey = domain.KeyChange{}
	r.create(t, in)
	r.ad.listErr = &statusErr{401, "unauthorized"}
	res, _ := r.svc.TestConnection(t.Context(), ws, synapse, "")
	if res.OK || !strings.Contains(res.Message, "HTTP 401") {
		t.Errorf("TestConnection() = %+v, want a message naming HTTP 401", res)
	}
}

func TestTestConnection_LongMessageIsCut(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.listErr = errors.New(strings.Repeat("x", 5000))
	res, _ := r.svc.TestConnection(t.Context(), ws, synapse, "")
	if len(res.Message) > 300 {
		t.Errorf("len(Message) = %d, want <= 300", len(res.Message))
	}
}

func TestTestConnection_SavesTheKeyInEveryCase(t *testing.T) { // AC11
	for _, listErr := range []error{nil, &statusErr{404, "x"}, &statusErr{401, "x"}, errors.New("boom")} {
		t.Run(fmt.Sprint(listErr), func(t *testing.T) {
			r := newRig(t)
			r.create(t, input())
			r.ad.listErr = listErr
			if _, err := r.svc.TestConnection(t.Context(), ws, synapse, "NEW-SECRET-key9"); err != nil {
				t.Fatalf("TestConnection() error = %v", err)
			}
			got := r.secrets.Custom(ws, synapse)
			if got.Key != "NEW-SECRET-key9" || len(got.Headers) != 1 || got.Headers[0].Value != headValue {
				t.Errorf("secrets = %+v, want the new key and the kept header", got)
			}
			if r.ad.keys[0] != "NEW-SECRET-key9" {
				t.Errorf("the list used key %q, want the new one", r.ad.keys[0])
			}
			r.noSecretInLogs(t)
		})
	}
}

func TestTestConnection_ScrubsTheNewKey(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.listErr = &statusErr{500, "oops NEW-SECRET-key9 oops"}
	res, _ := r.svc.TestConnection(t.Context(), ws, synapse, "NEW-SECRET-key9")
	if strings.Contains(res.Message, "NEW-SECRET") {
		t.Errorf("Message = %q contains the new key", res.Message)
	}
}

func TestTestConnection_Undefined(t *testing.T) { // AC11: 404, nothing saved
	r := newRig(t)
	for _, id := range []string{"custom:ghost", "custom:Bad", "openai", ""} {
		_, err := r.svc.TestConnection(t.Context(), ws, id, "SOME-KEY-1234")
		wantAppErr(t, err, apperr.KindNotFound, "custom_provider_not_found")
	}
	if len(r.secrets.callLog()) != 0 || r.ad.listCalls != 0 || len(r.adapters.built) != 0 {
		t.Errorf("an undefined provider saved a key or built a request: %v, %d, %d", r.secrets.callLog(), r.ad.listCalls, len(r.adapters.built))
	}
}

func TestTestConnection_InvalidKey(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	_, err := r.svc.TestConnection(t.Context(), ws, synapse, "bad\nkey")
	if diff := cmp.Diff([]string{"key"}, issuePaths(t, err)); diff != "" {
		t.Errorf("issue paths (-want +got):\n%s", diff)
	}
	if r.ad.listCalls != 0 {
		t.Errorf("a request was made with an invalid key")
	}
}

func TestTestConnection_Timeout(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.block = true
	r.ad.policy = llm.Policy{ListTimeout: 20 * time.Millisecond}
	res, err := r.svc.TestConnection(t.Context(), ws, synapse, "")
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}
	if res.OK || !strings.Contains(res.Message, "timed out") {
		t.Errorf("TestConnection() = %+v, want a timeout failure", res)
	}
}

func TestTestConnection_CancelledCallerIsAnError(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.block = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.svc.TestConnection(ctx, ws, synapse, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("TestConnection() error = %v, want context.Canceled", err)
	}
}

func TestTestConnection_AdapterBuiltFromStoredSecrets(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.list = listed("m1")
	if _, err := r.svc.TestConnection(t.Context(), ws, synapse, ""); err != nil {
		t.Fatal(err)
	}
	b := r.adapters.built[len(r.adapters.built)-1]
	if b.Provider.ID != synapse || b.Secret.Key != keyValue || len(b.Secret.Headers) != 1 {
		t.Errorf("built with %+v", b)
	}
}

// ---- Models ----

func TestModels_MergesLiveAndManual(t *testing.T) { // AC10, FR10
	r := newRig(t)
	in := input()
	in.Models = []domain.Model{
		{ID: "a", Label: sp("A"), Pricing: &domain.Pricing{PromptPerM: 1, CompletionPerM: 2}},
		{ID: "c"},
	}
	r.create(t, in)
	created := int64(7)
	price := &llm.ModelPricing{PromptPerM: 99, CompletionPerM: 99}
	r.ad.list = []llm.ModelInfo{
		{ID: "b", Provider: "openai", Created: &created, Pricing: price},
		{ID: "a", Provider: "openai", Pricing: price},
		{ID: "b", Provider: "openai"},
		{ID: ""},
	}
	got, err := r.svc.Models(t.Context(), ws, synapse)
	if err != nil {
		t.Fatalf("Models() error = %v", err)
	}
	want := []llm.ModelInfo{
		{ID: "a", Provider: synapse, Label: sp("A"), Pricing: &llm.ModelPricing{PromptPerM: 1, CompletionPerM: 2}},
		{ID: "b", Provider: synapse, Created: &created},
		{ID: "c", Provider: synapse},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Models() mismatch (-want +got):\n%s", diff)
	}
	if r.ad.listCalls != 1 {
		t.Errorf("list calls = %d, want 1", r.ad.listCalls)
	}
}

func TestModels_ListingFailureGivesTheManualList(t *testing.T) { // AC10, EC5, EC6
	failures := map[string]error{
		"500":       &statusErr{500, "boom"},
		"404":       &statusErr{404, "no list"},
		"html":      errors.New("/models returned an unreadable response"),
		"forbidden": &url.Error{Op: "Get", URL: "http://x", Err: egress.ErrForbiddenAddr},
	}
	for name, listErr := range failures {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			in := input()
			in.Models = []domain.Model{{ID: "c"}, {ID: "a"}}
			r.create(t, in)
			r.ad.listErr = listErr
			got, err := r.svc.Models(t.Context(), ws, synapse)
			if err != nil {
				t.Fatalf("Models() error = %v, want the manual list", err)
			}
			var ids []string
			for _, m := range got {
				ids = append(ids, m.ID)
			}
			if diff := cmp.Diff([]string{"a", "c"}, ids); diff != "" {
				t.Errorf("Models() ids (-want +got):\n%s", diff)
			}
			r.noSecretInLogs(t)
		})
	}
}

func TestModels_TimeoutAndBuildFailureGiveTheManualList(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.block = true
	r.ad.policy = llm.Policy{ListTimeout: 20 * time.Millisecond}
	got, err := r.svc.Models(t.Context(), ws, synapse)
	if err != nil || len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("Models() on timeout = %v, %v; want the manual list", got, err)
	}
	r.adapters.buildErr = errors.New("cannot build")
	got, err = r.svc.Models(t.Context(), ws, synapse)
	if err != nil || len(got) != 1 {
		t.Errorf("Models() on build failure = %v, %v; want the manual list", got, err)
	}
}

func TestModels_UndefinedIsEmpty(t *testing.T) { // AC10: custom:ghost -> []
	r := newRig(t)
	for _, id := range []string{"custom:ghost", "custom:Bad", "openai"} {
		got, err := r.svc.Models(t.Context(), ws, id)
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("Models(%q) = %v, %v; want an empty non-nil list", id, got, err)
		}
	}
	if len(r.adapters.built) != 0 {
		t.Errorf("an adapter was built for an undefined provider")
	}
}

func TestModels_StoreFailureAndCancellation(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.ad.block = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.svc.Models(ctx, ws, synapse); !errors.Is(err, context.Canceled) {
		t.Errorf("Models() with a cancelled ctx: error = %v, want context.Canceled", err)
	}
	r.store.getErr = errors.New("db down")
	if _, err := r.svc.Models(t.Context(), ws, synapse); err == nil {
		t.Errorf("Models() with a failing store: error = nil")
	}
}

// ---- Resolve ----

func TestResolve(t *testing.T) { // P1, FR8, FR12, FR33
	r := newRig(t)
	in := input()
	in.StructuredOutput = custom.ModePromptOnly
	r.create(t, in)
	call, err := r.svc.Resolve(t.Context(), ws, synapse)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if call.Adapter != llm.Adapter(r.ad) {
		t.Errorf("Adapter is not the one the factory built")
	}
	if call.Name != "Synapse" || call.Mode != custom.ModePromptOnly || call.Protocol != custom.ProtocolOpenAI || call.Host != "synapse.test" {
		t.Errorf("Name = %q Mode = %q Protocol = %q Host = %q", call.Name, call.Mode, call.Protocol, call.Host)
	}
	if diff := cmp.Diff([]string{keyValue, headValue}, call.Secrets); diff != "" {
		t.Errorf("Secrets mismatch (-want +got):\n%s", diff)
	}
	if got := call.Price.Estimate("m1", 1_000_000, 500_000); got == nil || *got != 6 {
		t.Errorf("Price.Estimate(m1) = %v, want 6", got)
	}
	if got := call.Price.Estimate("gpt-4.1", 1000, 1000); got != nil {
		t.Errorf("Price.Estimate(gpt-4.1) = %v, want nil: the built-in price books are not consulted", *got)
	}
	b := r.adapters.built[0]
	if b.Provider.ID != synapse || b.Secret.Key != keyValue {
		t.Errorf("factory called with %+v", b)
	}
}

func TestResolve_ReadsDefinitionAndSecretsAtCallTime(t *testing.T) { // FR12, EC10
	r := newRig(t)
	r.create(t, input())
	in := input()
	in.APIKey = domain.KeyChange{Op: domain.KeyReplace, Value: "NEW-SECRET-key9"}
	in.ExtraHeaders = nil
	in.Models = []domain.Model{{ID: "m1", Pricing: &domain.Pricing{PromptPerM: 10, CompletionPerM: 10}}}
	if _, err := r.svc.Update(t.Context(), ws, synapse, in); err != nil {
		t.Fatal(err)
	}
	call, err := r.svc.Resolve(t.Context(), ws, synapse)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"NEW-SECRET-key9"}, call.Secrets); diff != "" {
		t.Errorf("Secrets mismatch (-want +got):\n%s", diff)
	}
	if got := call.Price.Estimate("m1", 1_000_000, 0); got == nil || *got != 10 {
		t.Errorf("Estimate after the price change = %v, want 10", got)
	}
}

func TestResolve_WithoutSecrets(t *testing.T) {
	r := newRig(t)
	in := input()
	in.APIKey, in.ExtraHeaders = domain.KeyChange{}, nil
	r.create(t, in)
	call, err := r.svc.Resolve(t.Context(), ws, synapse)
	if err != nil || len(call.Secrets) != 0 || call.Mode != custom.ModeStrict {
		t.Errorf("Resolve() = %+v, %v; want no secrets and strict", call, err)
	}
}

func TestResolve_Undefined(t *testing.T) { // FR9
	r := newRig(t)
	for _, id := range []llm.Provider{"custom:ghost", "custom:Bad", "openai", ""} {
		_, err := r.svc.Resolve(t.Context(), ws, id)
		if !errors.Is(err, llm.ErrCustomNotConfigured) {
			t.Errorf("Resolve(%q) error = %v, want llm.ErrCustomNotConfigured", id, err)
		}
	}
	if len(r.adapters.built) != 0 {
		t.Errorf("an adapter was built for an undefined provider")
	}
	r.create(t, input())
	if _, err := r.svc.Resolve(t.Context(), "ws2", synapse); !errors.Is(err, llm.ErrCustomNotConfigured) {
		t.Errorf("Resolve() from another workspace: error = %v", err)
	}
}

func TestResolve_FailuresAreNotNotConfigured(t *testing.T) {
	r := newRig(t)
	r.create(t, input())
	r.adapters.buildErr = errors.New("cannot build")
	if _, err := r.svc.Resolve(t.Context(), ws, synapse); err == nil || errors.Is(err, llm.ErrCustomNotConfigured) {
		t.Errorf("Resolve() on build failure: error = %v", err)
	}
	r.adapters.buildErr = nil
	r.store.getErr = errors.New("db down")
	if _, err := r.svc.Resolve(t.Context(), ws, synapse); err == nil || errors.Is(err, llm.ErrCustomNotConfigured) {
		t.Errorf("Resolve() on store failure: error = %v", err)
	}
}

func TestNewService_PanicsWithoutPorts(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("NewService(Deps{}) did not panic")
		}
	}()
	app.NewService(app.Deps{})
}

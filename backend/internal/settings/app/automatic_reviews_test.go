package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/settings/app"
)

var defaultAutoJSON = json.RawMessage(`{"enabled":false,"agent_ids":[],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`)

const (
	agentA = "6f1c0d2e-0000-4000-8000-00000000000a"
	agentB = "6f1c0d2e-0000-4000-8000-00000000000b"
)

// autoRig is a settings service on a clock the test moves.
type autoRig struct {
	svc   *app.Service
	store *fakeStore
	now   time.Time
}

func newAutoRig(stored json.RawMessage) *autoRig {
	r := &autoRig{store: &fakeStore{bag: app.Bag{}}, now: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)}
	if stored != nil {
		r.store.bag[app.AutomaticReviewsKey] = stored
	}
	r.svc = app.NewService(app.Deps{
		Store: r.store, Secrets: &fakeSecrets{}, GitHub: fakeGitHub{}, Providers: &fakeProviders{},
		Clock: func() time.Time { return r.now }, Logger: slog.New(slog.DiscardHandler),
	})
	return r
}

// put sends an automatic_reviews input like the transport layer does (validated patch) and
// returns the stored object as JSON text.
func (r *autoRig) put(t *testing.T, input string) string {
	t.Helper()
	bag, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{app.AutomaticReviewsKey: json.RawMessage(input)})
	if err != nil {
		t.Fatalf("Update(automatic_reviews=%s) error = %v", input, err)
	}
	if got, want := string(r.store.bag[app.AutomaticReviewsKey]), string(bag[app.AutomaticReviewsKey]); got != want {
		t.Errorf("Update(%s) returned %s, but stored %s", input, want, got)
	}
	return string(bag[app.AutomaticReviewsKey])
}

func TestGet_automaticReviewsReadsEveryStoredForm(t *testing.T) { // AC1, FR1, EC16
	tests := []struct {
		name   string
		stored json.RawMessage // nil = no row
		want   string
	}{
		{name: "no stored value", want: string(defaultAutoJSON)},
		{name: "null", stored: json.RawMessage(`null`), want: string(defaultAutoJSON)},
		{name: "stored false", stored: json.RawMessage(`false`), want: string(defaultAutoJSON)},
		{name: "stored true is enabled with no agents", stored: json.RawMessage(`true`),
			want: `{"enabled":true,"agent_ids":[],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
		{name: "unreadable shapes read as the default", stored: json.RawMessage(`"on"`), want: string(defaultAutoJSON)},
		{name: "partial object takes defaults", stored: json.RawMessage(`{"agent_ids":["` + agentA + `"]}`),
			want: `{"enabled":false,"agent_ids":["` + agentA + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
		{name: "ill-typed fields take defaults", stored: json.RawMessage(`{"enabled":1,"agent_ids":"a","on_new_pr":"x","enabled_at":5}`), want: string(defaultAutoJSON)},
		{name: "armed object keeps enabled_at",
			stored: json.RawMessage(`{"enabled":true,"agent_ids":["` + agentA + `"],"on_new_pr":false,"on_new_commits":true,"enabled_at":"2026-10-25T09:00:00.000Z"}`),
			want:   `{"enabled":true,"agent_ids":["` + agentA + `"],"on_new_pr":false,"on_new_commits":true,"enabled_at":"2026-10-25T09:00:00.000Z"}`},
		{name: "enabled_at of a not armed value reads null",
			stored: json.RawMessage(`{"enabled":true,"agent_ids":[],"enabled_at":"2026-10-25T09:00:00.000Z"}`),
			want:   `{"enabled":true,"agent_ids":[],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
		{name: "enabled_at is normalised to UTC milliseconds",
			stored: json.RawMessage(`{"enabled":true,"agent_ids":["` + agentA + `"],"enabled_at":"2026-10-25T11:00:00+02:00"}`),
			want:   `{"enabled":true,"agent_ids":["` + agentA + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":"2026-10-25T09:00:00.000Z"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutoRig(tc.stored)
			got, err := r.svc.Get(t.Context(), "ws")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if diff := cmp.Diff(tc.want, string(got[app.AutomaticReviewsKey])); diff != "" {
				t.Errorf("Get() automatic_reviews mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGet_automaticReviewsLeavesOtherKeysAlone(t *testing.T) {
	r := newAutoRig(nil)
	r.store.bag["theme"] = json.RawMessage(`"dark"`)
	got, err := r.svc.Get(t.Context(), "ws")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	want := app.Bag{"theme": json.RawMessage(`"dark"`), app.AutomaticReviewsKey: defaultAutoJSON}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Get() mismatch (-want +got):\n%s", diff)
	}
}

func TestUpdate_automaticReviewsLifecycle(t *testing.T) { // AC2, FR2, EC15, EC16
	r := newAutoRig(json.RawMessage(`true`)) // legacy: enabled, no agents
	const at1 = "2026-10-25T10:00:00.000Z"
	const prefix = `{"enabled":true,"agent_ids":["` + agentA + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":`

	// Picking agents arms the legacy true (EC16) and stamps enabled_at with the clock.
	if got, want := r.put(t, `{"agent_ids":["`+agentA+`"]}`), prefix+`"`+at1+`"}`; got != want {
		t.Errorf("arm by picking an agent = %s, want %s", got, want)
	}

	// An edit that stays armed keeps enabled_at, also on a later clock; enabled_at in the input is ignored.
	r.now = r.now.Add(time.Hour)
	if got, want := r.put(t, `{"on_new_commits":false}`), `{"enabled":true,"agent_ids":["`+agentA+`"],"on_new_pr":true,"on_new_commits":false,"enabled_at":"`+at1+`"}`; got != want {
		t.Errorf("stays armed = %s, want %s", got, want)
	}

	// Pausing keeps every other field and nulls enabled_at.
	if got, want := r.put(t, `false`), `{"enabled":false,"agent_ids":["`+agentA+`"],"on_new_pr":true,"on_new_commits":false,"enabled_at":null}`; got != want {
		t.Errorf("pause = %s, want %s", got, want)
	}

	// Re-arming is a new arming: enabled_at is the new time (EC15).
	r.now = r.now.Add(time.Hour)
	if got, want := r.put(t, `{"enabled":true}`), `{"enabled":true,"agent_ids":["`+agentA+`"],"on_new_pr":true,"on_new_commits":false,"enabled_at":"2026-10-25T12:00:00.000Z"}`; got != want {
		t.Errorf("re-arm = %s, want %s", got, want)
	}

	// Both triggers off is not armed.
	if got, want := r.put(t, `{"on_new_pr":false}`), `{"enabled":true,"agent_ids":["`+agentA+`"],"on_new_pr":false,"on_new_commits":false,"enabled_at":null}`; got != want {
		t.Errorf("no triggers = %s, want %s", got, want)
	}

	// Removing the last agent while enabled is not armed either.
	r.now = r.now.Add(time.Hour)
	r.put(t, `{"on_new_pr":true}`)
	if got, want := r.put(t, `{"agent_ids":[]}`), `{"enabled":true,"agent_ids":[],"on_new_pr":true,"on_new_commits":false,"enabled_at":null}`; got != want {
		t.Errorf("no agents = %s, want %s", got, want)
	}
}

func TestUpdate_automaticReviewsFromNothing(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "false", input: `false`, want: string(defaultAutoJSON)},
		{name: "true has no agents, so not armed", input: `true`,
			want: `{"enabled":true,"agent_ids":[],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
		{name: "empty patch stores the full default object", input: `{}`, want: string(defaultAutoJSON)},
		{name: "agents without enabled do not arm", input: `{"agent_ids":["` + agentA + `"]}`,
			want: `{"enabled":false,"agent_ids":["` + agentA + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
		{name: "enabled with agents arms at once", input: `{"enabled":true,"agent_ids":["` + agentA + `","` + agentB + `"],"on_new_pr":false}`,
			want: `{"enabled":true,"agent_ids":["` + agentA + `","` + agentB + `"],"on_new_pr":false,"on_new_commits":true,"enabled_at":"2026-10-25T10:00:00.000Z"}`},
		{name: "enabled_at and unknown fields of the input are dropped", input: `{"enabled":true,"enabled_at":"x","mode":"all"}`,
			want: `{"enabled":true,"agent_ids":[],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
		{name: "duplicates are removed keeping the first", input: `{"agent_ids":["` + agentB + `","` + agentA + `","` + agentB + `"]}`,
			want: `{"enabled":false,"agent_ids":["` + agentB + `","` + agentA + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":null}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutoRig(nil)
			if diff := cmp.Diff(tc.want, r.put(t, tc.input)); diff != "" {
				t.Errorf("Update(%s) mismatch (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

func TestUpdate_automaticReviewsArmedRowWithoutEnabledAtIsArmedFromNow(t *testing.T) {
	// A hand-edited armed row has no usable enabled_at: the next edit arms it from now, so it
	// can never reach back to PRs that existed before.
	r := newAutoRig(json.RawMessage(`{"enabled":true,"agent_ids":["` + agentA + `"]}`))
	want := `{"enabled":true,"agent_ids":["` + agentA + `"],"on_new_pr":false,"on_new_commits":true,"enabled_at":"2026-10-25T10:00:00.000Z"}`
	if got := r.put(t, `{"on_new_pr":false}`); got != want {
		t.Errorf("edit of an armed row without enabled_at = %s, want %s", got, want)
	}
}

func TestUpdate_automaticReviewsLastWriteWins(t *testing.T) { // A10
	r := newAutoRig(nil)
	r.put(t, `{"enabled":true,"agent_ids":["`+agentA+`"]}`)
	// Two edits computed from the same stored value: the second overwrites the first's fields only
	// where it sends them, and nothing is rejected.
	r.put(t, `{"agent_ids":["`+agentB+`"]}`)
	got := r.put(t, `{"agent_ids":["`+agentA+`"]}`)
	want := `{"enabled":true,"agent_ids":["` + agentA + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":"2026-10-25T10:00:00.000Z"}`
	if got != want {
		t.Errorf("last write = %s, want %s", got, want)
	}
}

func TestUpdate_automaticReviewsDoesNotCheckAgents(t *testing.T) { // A7: the use case reads the ids, not the agents
	// Whether an agent exists or is disabled is the trigger's and the UI's business: deselecting is
	// always possible (the id leaves the list) and the settings layer never rejects an id.
	r := newAutoRig(nil)
	r.put(t, `{"enabled":true,"agent_ids":["`+agentA+`","`+agentB+`"]}`)
	want := `{"enabled":true,"agent_ids":["` + agentB + `"],"on_new_pr":true,"on_new_commits":true,"enabled_at":"2026-10-25T10:00:00.000Z"}`
	if got := r.put(t, `{"agent_ids":["`+agentB+`"]}`); got != want {
		t.Errorf("deselect = %s, want %s", got, want)
	}
}

func TestUpdate_automaticReviewsErrors(t *testing.T) {
	t.Run("an input the transport should have rejected is a validation error and writes nothing", func(t *testing.T) {
		for _, input := range []string{`"on"`, `null`, `[]`, `{"enabled":1}`, `{"agent_ids":"a"}`, `{"agent_ids":[1]}`, `{`} {
			r := newAutoRig(nil)
			_, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{app.AutomaticReviewsKey: json.RawMessage(input)})
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Kind != apperr.KindValidation {
				t.Errorf("Update(automatic_reviews=%s) error = %v, want a validation error", input, err)
			}
			if r.store.upserts != 0 {
				t.Errorf("Update(automatic_reviews=%s) wrote %d times, want 0", input, r.store.upserts)
			}
		}
	})
	t.Run("more than 20 ids", func(t *testing.T) {
		r := newAutoRig(nil)
		ids := make([]string, 21)
		for i := range ids {
			ids[i] = fmt.Sprintf(`"6f1c0d2e-0000-4000-8000-%012x"`, i+1)
		}
		in := `{"agent_ids":[` + strings.Join(ids, ",") + `]}`
		if _, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{app.AutomaticReviewsKey: json.RawMessage(in)}); err == nil {
			t.Errorf("Update(21 ids) error = nil, want a validation error")
		}
	})
	t.Run("a failing read of the stored value is wrapped", func(t *testing.T) {
		boom := errors.New("boom")
		r := newAutoRig(nil)
		r.store.listErr = boom
		_, err := r.svc.Update(t.Context(), "ws", "user", app.Bag{app.AutomaticReviewsKey: json.RawMessage(`true`)})
		if !errors.Is(err, boom) {
			t.Errorf("Update() error = %v, want it to wrap %v", err, boom)
		}
	})
	t.Run("the caller's entries are not modified", func(t *testing.T) {
		r := newAutoRig(nil)
		entries := app.Bag{app.AutomaticReviewsKey: json.RawMessage(`true`)}
		if _, err := r.svc.Update(context.Background(), "ws", "user", entries); err != nil {
			t.Fatal(err)
		}
		if got := string(entries[app.AutomaticReviewsKey]); got != "true" {
			t.Errorf("entries[automatic_reviews] = %s after Update, want the input unchanged", got)
		}
	})
}

package postgres_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/postgres"
	"dev-digest-go/backend/internal/reviews/domain"
)

func usedIntent() *domain.IntentTrace {
	detail := "cut at 4,000"
	return &domain.IntentTrace{
		Status: domain.IntentStatusUsed, CacheHit: true, Confidence: strp("medium"), DerivedFrom: strp("inferred"),
		ChangeType: strp("bugfix"), Provider: "openrouter", Model: "m", TokensIn: 1200, TokensOut: 180, CostUSD: floatp(0.00042),
		Sources: []reviewer.IntentSource{
			{Kind: "title", Ref: "title", Status: "used"},
			{Kind: "body", Ref: "body", Status: "truncated", Detail: &detail},
		},
	}
}

func TestTraceDoc_IntentRoundTrip(t *testing.T) {
	completed, ended := completedTrace(), endedTrace()
	tests := []struct {
		name  string
		trace domain.Trace
	}{
		{"completed used", withIntent(completed, usedIntent(), "Add X.\nConfidence: medium")},
		{"completed used with unknowns", withIntent(completed, &domain.IntentTrace{Status: domain.IntentStatusUsed, Provider: "p", Model: "m"}, "")},
		{"completed unavailable", withIntent(completed, domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured"), "")},
		{"ended used", withIntent(ended, usedIntent(), "")},
		{"ended unavailable", withIntent(ended, domain.UnavailableIntentTrace("boom"), "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := postgres.EncodeTrace(tc.trace)
			if err != nil {
				t.Fatalf("EncodeTrace() error = %v", err)
			}
			got, err := postgres.DecodeTrace(raw)
			if err != nil {
				t.Fatalf("DecodeTrace(EncodeTrace()) error = %v", err)
			}
			if diff := cmp.Diff(tc.trace, got); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// withIntent returns t with an intent trace and, when rendered is set, the assembly's intent text
// (completed traces only: an ended trace's assembly has none).
func withIntent(t domain.Trace, it *domain.IntentTrace, rendered string) domain.Trace {
	t.Intent = it
	if rendered != "" && !t.Ended {
		t.PromptAssembly.Intent = &rendered
	}
	return t
}

func TestEncodeTrace_IntentShape(t *testing.T) {
	object := func(t *testing.T, tr domain.Trace) map[string]json.RawMessage {
		t.Helper()
		raw, err := postgres.EncodeTrace(tr)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		var intent map[string]json.RawMessage
		if err := json.Unmarshal(doc["intent"], &intent); err != nil {
			t.Fatalf("intent = %s, want an object: %v", doc["intent"], err)
		}
		return intent
	}
	keys := func(m map[string]json.RawMessage) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		return out
	}

	t.Run("used has every key but warning, null when unknown", func(t *testing.T) {
		got := object(t, withIntent(completedTrace(), &domain.IntentTrace{Status: domain.IntentStatusUsed, Provider: "p", Model: "m"}, ""))
		want := map[string]string{
			"status": `"used"`, "cache_hit": "false", "confidence": "null", "derived_from": "null", "change_type": "null",
			"provider": `"p"`, "model": `"m"`, "tokens_in": "0", "tokens_out": "0", "cost_usd": "null", "sources": "null",
		}
		if len(got) != len(want) {
			t.Errorf("used intent keys = %v, want %d keys", keys(got), len(want))
		}
		for k, v := range want {
			if string(got[k]) != v {
				t.Errorf("used intent %s = %s, want %s", k, got[k], v)
			}
		}
	})
	t.Run("unavailable is status and warning only", func(t *testing.T) {
		got := object(t, withIntent(completedTrace(), domain.UnavailableIntentTrace("w"), ""))
		if len(got) != 2 || string(got["status"]) != `"unavailable"` || string(got["warning"]) != `"w"` {
			t.Errorf("unavailable intent = %v, want only status and warning", got)
		}
	})
	t.Run("the source detail is always present", func(t *testing.T) {
		got := object(t, withIntent(completedTrace(), usedIntent(), ""))
		if !strings.Contains(string(got["sources"]), `"detail":null`) {
			t.Errorf("sources = %s, want a null detail for a source without one", got["sources"])
		}
	})
	t.Run("the assembly carries the rendered intent", func(t *testing.T) {
		raw, err := postgres.EncodeTrace(withIntent(completedTrace(), usedIntent(), "Add X."))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"intent":"Add X."`) {
			t.Errorf("document lacks the assembly intent: %s", raw)
		}
	})
	t.Run("text is made storable", func(t *testing.T) {
		it := domain.UnavailableIntentTrace("bad\x00warning")
		if got := object(t, withIntent(completedTrace(), it, "")); string(got["warning"]) != `"badwarning"` {
			t.Errorf("warning = %s, want the NUL dropped", got["warning"])
		}
	})
}

func TestDecodeTrace_IntentAcceptsTheContract(t *testing.T) {
	doc := strings.Replace(minimalDoc, `"log":[]`, `"log":[],"intent":{"status":"used","confidence":"low"}`, 1)
	got, err := postgres.DecodeTrace([]byte(doc))
	if err != nil {
		t.Fatalf("DecodeTrace(used intent with only some keys) error = %v", err)
	}
	want := &domain.IntentTrace{Status: domain.IntentStatusUsed, Confidence: strp("low")}
	if diff := cmp.Diff(want, got.Intent); diff != "" {
		t.Errorf("DecodeTrace() intent mismatch (-want +got):\n%s", diff)
	}
	doc = strings.Replace(minimalDoc, `"log":[]`, `"log":[],"intent":null`, 1)
	if got, err := postgres.DecodeTrace([]byte(doc)); err != nil || got.Intent != nil {
		t.Errorf("DecodeTrace(intent null) = %v, %v, want nil, nil", got.Intent, err)
	}
}

func TestDecodeTrace_IntentRejectsContractViolations(t *testing.T) {
	tests := []struct {
		name, intent, wantPath string
	}{
		{"unknown status", `{"status":"maybe"}`, "intent.status"},
		{"unknown confidence", `{"status":"used","confidence":"certain"}`, "intent.confidence"},
		{"unknown derived_from", `{"status":"used","derived_from":"guess"}`, "intent.derived_from"},
		{"unknown change type", `{"status":"used","change_type":"rewrite"}`, "intent.change_type"},
		{"tokens not an integer", `{"status":"used","tokens_in":1.5}`, "intent.tokens_in"},
		{"source kind unknown", `{"status":"used","sources":[{"kind":"memo","ref":"r","status":"used"}]}`, "intent.sources.kind"},
		{"source status unknown", `{"status":"used","sources":[{"kind":"title","ref":"r","status":"ok"}]}`, "intent.sources.status"},
		{"source without ref", `{"status":"used","sources":[{"kind":"title","status":"used"}]}`, "intent.sources.ref"},
		{"source null", `{"status":"used","sources":[null]}`, "intent.sources[0]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := strings.Replace(minimalDoc, `"log":[]`, `"log":[],"intent":`+tc.intent, 1)
			_, err := postgres.DecodeTrace([]byte(doc))
			if !errors.Is(err, domain.ErrTraceInvalid) {
				t.Fatalf("DecodeTrace() error = %v, want ErrTraceInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Errorf("error %q does not name %q", err, tc.wantPath)
			}
		})
	}
}

func TestSaveTrace_IntentSurvivesTheDatabase(t *testing.T) {
	f := newFixture(t)
	agent := f.agent(t, f.ws, "Alpha", true)
	run := f.run(t, agent, nil)
	want := withIntent(completedTrace(), usedIntent(), "Add X.")
	if err := f.store.SaveTrace(t.Context(), run, want); err != nil {
		t.Fatalf("SaveTrace() error = %v", err)
	}
	got, err := f.store.FindTrace(t.Context(), f.ws, run)
	if err != nil {
		t.Fatalf("FindTrace() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("FindTrace() mismatch (-want +got):\n%s", diff)
	}
}

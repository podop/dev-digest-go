package mock_test

import (
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
)

// intentSchema is the IntentClassification the intent layer asks for, as Zod emits it.
const intentSchema = `{"type":"object","required":["intent","in_scope","out_of_scope","change_type"],"properties":{
  "intent":{"type":"string"},
  "in_scope":{"type":"array","items":{"type":"string"}},
  "out_of_scope":{"type":"array","items":{"type":"string"}},
  "change_type":{"type":"string","enum":["feature","bugfix","refactor","chore","docs","test","other"]}}}`

// nodeIntent is dev-digest MOCK_INTENT_CLASSIFICATION through JSON.stringify (printed by tsx).
const nodeIntent = `{"intent":"[mock LLM] Add rate limiting to public API endpoints to prevent abuse from unauthenticated clients.","in_scope":["Add a rate-limiting middleware","Apply it to public, unauthenticated API endpoints"],"out_of_scope":["Authentication or session changes","Internal or admin-only endpoints"],"change_type":"feature"}`

func intentRequest() llm.Request {
	r := request("any prompt")
	r.SchemaName = mock.IntentClassification
	r.Schema = json.RawMessage(intentSchema)
	return r
}

func TestStructured_intentFixtureIsByteEqualToNode(t *testing.T) {
	t.Parallel()
	res, err := newClient(t, 0).Structured(t.Context(), intentRequest())
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.Raw != nodeIntent || string(res.Data) != nodeIntent {
		t.Errorf("Structured() raw = %s, want %s", res.Raw, nodeIntent)
	}
	if res.TokensIn != mock.TokensIn || res.TokensOut != mock.TokensOut || res.Attempts != 1 ||
		res.CostUSD == nil || *res.CostUSD != mock.CostUSD {
		t.Errorf("Structured() usage = %d/%d cost %v attempts %d, want 1200/180 cost 0.00042 in 1 attempt",
			res.TokensIn, res.TokensOut, res.CostUSD, res.Attempts)
	}
}

func TestStructured_intentWaitsTheConfiguredDelay(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if _, err := newClient(t, 3*time.Second).Structured(t.Context(), intentRequest()); err != nil || time.Since(start) != 3*time.Second {
			t.Errorf("Structured() = %v after %v, want nil after 3s", err, time.Since(start))
		}
	})
}

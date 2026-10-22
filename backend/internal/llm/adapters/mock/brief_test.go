package mock_test

import (
	"encoding/json"
	"testing"

	"dev-digest-go/backend/internal/llm/adapters/mock"
)

// briefSchema is the PrBriefLlm the brief layer asks for, as Zod emits it.
const briefSchema = `{"type":"object","required":["summary","risks","review_focus"],"properties":{
  "summary":{"type":"string"},
  "risks":{"type":"array","items":{"type":"object","required":["kind","title","explanation","severity","file_refs"],"properties":{
    "kind":{"type":"string"},"title":{"type":"string"},"explanation":{"type":"string"},
    "severity":{"type":"string","enum":["high","medium","low"]},
    "file_refs":{"type":"array","items":{"type":"string"}}}}},
  "review_focus":{"type":"array","items":{"type":"object","required":["file","line","reason"],"properties":{
    "file":{"type":"string"},"line":{"type":"integer"},"reason":{"type":"string"}}}}}}`

// nodeBrief is dev-digest MOCK_PR_BRIEF through JSON.stringify (printed by tsx).
const nodeBrief = `{"summary":"[mock LLM] Adds a token-bucket rate limiter in front of the public webhook endpoints and moves its limits into the config.","risks":[{"kind":"security","title":"Rate-limit key trusts a client-controlled header","explanation":"The bucket key comes from ` + "`X-Forwarded-For`" + `, so a client can dodge the limiter by sending a new value on every request.","severity":"high","file_refs":["src/middleware/ratelimit.ts:6-9"]},{"kind":"perf","title":"Per-user query inside a loop","explanation":"Each user triggers its own ` + "`orgs`" + ` query, so the list endpoint now issues one query per row.","severity":"medium","file_refs":["src/api/users.ts:45-50"]},{"kind":"data","title":"Live-looking key committed in the config","explanation":"A ` + "`stripeKey`" + ` literal is added to ` + "`config.ts`" + `; check that it is a placeholder and not a real secret.","severity":"low","file_refs":["src/config.ts:12"]}],"review_focus":[{"file":"src/middleware/ratelimit.ts","line":7,"reason":"The bucket key is taken from a spoofable header."},{"file":"src/api/public/webhooks.ts","line":4,"reason":"Where the limiter is attached to the public routes."},{"file":"src/api/users.ts","line":46,"reason":"New per-user query in the loop."},{"file":"src/config.ts","line":12,"reason":"A key-looking literal enters the config."}]}`

func TestStructured_briefFixtureIsByteEqualToNode(t *testing.T) {
	t.Parallel()
	req := request("any prompt")
	req.SchemaName = mock.PrBriefLlm
	req.Schema = json.RawMessage(briefSchema)
	res, err := newClient(t, 0).Structured(t.Context(), req)
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.Raw != nodeBrief || string(res.Data) != nodeBrief {
		t.Errorf("Structured() raw = %s, want %s", res.Raw, nodeBrief)
	}
	if res.TokensIn != mock.TokensIn || res.TokensOut != mock.TokensOut || res.Attempts != 1 ||
		res.CostUSD == nil || *res.CostUSD != mock.CostUSD {
		t.Errorf("Structured() usage = %d/%d cost %v attempts %d, want 1200/180 cost 0.00042 in 1 attempt",
			res.TokensIn, res.TokensOut, res.CostUSD, res.Attempts)
	}
}

func TestStructured_onboardingTourHasNoFixture(t *testing.T) {
	t.Parallel()
	req := request("any prompt")
	req.SchemaName = "OnboardingTour"
	_, err := newClient(t, 0).Structured(t.Context(), req)
	const want = "Mock LLM provider has no fixture for structured output 'OnboardingTour'"
	if err == nil || err.Error() != want {
		t.Errorf("Structured(OnboardingTour) error = %v, want %q", err, want)
	}
}

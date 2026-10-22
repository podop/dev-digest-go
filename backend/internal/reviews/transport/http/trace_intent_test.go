package reviewshttp_test

import (
	"testing"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

func intentTrace(it *domain.IntentTrace, assembly reviewer.Assembly) domain.Trace {
	return domain.Trace{
		Config:         domain.TraceConfig{Agent: "Sec", Version: "3", Provider: "openrouter", Model: "m", PR: 482, Source: "local"},
		Stats:          domain.TraceStats{DurationMS: 10, Grounding: "0/0 passed"},
		PromptAssembly: assembly,
		Intent:         it,
	}
}

const intentTraceHead = `{"config":{"agent":"Sec","version":"3","provider":"openrouter","model":"m","pr":482,"source":"local"},` +
	`"stats":{"duration_ms":10,"tokens_in":0,"tokens_out":0,"cost_usd":null,"findings":0,"grounding":"0/0 passed"},`

// AC5037, FR5026: a used intent shows every key but warning, in the order of the spec; the
// assembly carries the rendered text.
func TestTrace_UsedIntentKeepsTheSpecKeyOrder(t *testing.T) {
	detail := "too long"
	it := &domain.IntentTrace{
		Status: domain.IntentStatusUsed, CacheHit: false, Confidence: ptr("high"), DerivedFrom: ptr("explicit"),
		ChangeType: ptr("feature"), Provider: "mock", Model: "mock-1", TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042),
		Sources: []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}, {Kind: "body", Ref: "body", Status: "truncated", Detail: &detail}},
	}
	srv := newServer(t, &fakeAPI{trace: intentTrace(it, reviewer.Assembly{System: "sys", Intent: ptr("Add X."), User: "u"})})
	wantJSON(t, send(t, srv, "GET", "/runs/"+otherID+"/trace", ""), 200,
		intentTraceHead+
			`"prompt_assembly":{"system":"sys","skills":null,"memory":null,"specs":null,"callers":null,"repo_map":null,"pr_description":null,"intent":"Add X.","user":"u"},`+
			`"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[],"skills_used":[],`+
			`"intent":{"status":"used","cache_hit":false,"confidence":"high","derived_from":"explicit","change_type":"feature",`+
			`"provider":"mock","model":"mock-1","tokens_in":1200,"tokens_out":180,"cost_usd":0.00042,`+
			`"sources":[{"kind":"title","ref":"title","status":"used","detail":null},{"kind":"body","ref":"body","status":"truncated","detail":"too long"}]}}`)
}

// A used intent with unknown confidence, change type and cost writes null for each.
func TestTrace_UsedIntentWritesNullsForUnknowns(t *testing.T) {
	it := &domain.IntentTrace{Status: domain.IntentStatusUsed, CacheHit: true, Provider: "p", Model: "m", Sources: []reviewer.IntentSource{}}
	srv := newServer(t, &fakeAPI{trace: intentTrace(it, reviewer.Assembly{System: "sys"})})
	wantJSON(t, send(t, srv, "GET", "/runs/"+otherID+"/trace", ""), 200,
		intentTraceHead+
			`"prompt_assembly":{"system":"sys","skills":null,"memory":null,"specs":null,"callers":null,"repo_map":null,"pr_description":null,"intent":null,"user":""},`+
			`"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[],"skills_used":[],`+
			`"intent":{"status":"used","cache_hit":true,"confidence":null,"derived_from":null,"change_type":null,`+
			`"provider":"p","model":"m","tokens_in":0,"tokens_out":0,"cost_usd":null,"sources":[]}}`)
}

// AC5033: an unavailable intent is status and warning only.
func TestTrace_UnavailableIntentHasStatusAndWarning(t *testing.T) {
	it := domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured")
	srv := newServer(t, &fakeAPI{trace: intentTrace(it, reviewer.Assembly{System: "sys"})})
	wantJSON(t, send(t, srv, "GET", "/runs/"+otherID+"/trace", ""), 200,
		intentTraceHead+
			`"prompt_assembly":{"system":"sys","skills":null,"memory":null,"specs":null,"callers":null,"repo_map":null,"pr_description":null,"intent":null,"user":""},`+
			`"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[],"skills_used":[],`+
			`"intent":{"status":"unavailable","warning":"OPENROUTER_API_KEY is not configured"}}`)
}

// AC5035: an ended trace carries the intent too, next to its reduced assembly.
func TestTrace_EndedTraceCarriesTheIntent(t *testing.T) {
	tr := intentTrace(domain.UnavailableIntentTrace("boom"), reviewer.Assembly{System: "sys"})
	tr.Ended = true
	srv := newServer(t, &fakeAPI{trace: tr})
	wantJSON(t, send(t, srv, "GET", "/runs/"+otherID+"/trace", ""), 200,
		intentTraceHead+
			`"prompt_assembly":{"system":"sys","skills":null,"memory":null,"specs":null,"user":""},`+
			`"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[],"skills_used":[],`+
			`"intent":{"status":"unavailable","warning":"boom"}}`)
}

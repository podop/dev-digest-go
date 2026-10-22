package mock

// IntentClassification is the schema name of the intent layer's classification call.
const IntentClassification = "IntentClassification"

// intentFixture is the answer of the intent classification; the field order is the order of the
// Node fixture, so the serialized answer is byte-equal to its JSON.stringify.
type intentFixture struct {
	Intent     string   `json:"intent"`
	InScope    []string `json:"in_scope"`
	OutOfScope []string `json:"out_of_scope"`
	ChangeType string   `json:"change_type"`
}

// intent is the fixture of the seeded PR #482 (dev-digest server/src/adapters/llm/mock.ts
// MOCK_INTENT_CLASSIFICATION); the scope bullets are generic prose that reads sensibly on any PR.
func intent() intentFixture {
	return intentFixture{
		Intent:     "[mock LLM] Add rate limiting to public API endpoints to prevent abuse from unauthenticated clients.",
		InScope:    []string{"Add a rate-limiting middleware", "Apply it to public, unauthenticated API endpoints"},
		OutOfScope: []string{"Authentication or session changes", "Internal or admin-only endpoints"},
		ChangeType: "feature",
	}
}

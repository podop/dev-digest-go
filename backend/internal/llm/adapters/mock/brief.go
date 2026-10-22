package mock

// PrBriefLlm is the schema name of the PR brief call.
const PrBriefLlm = "PrBriefLlm"

type briefRisk struct {
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Severity    string   `json:"severity"`
	FileRefs    []string `json:"file_refs"`
}

type briefFocus struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// briefFixture is the answer of the PR brief call; the field order is the order of the Node
// fixture, so the serialized answer is byte-equal to its JSON.stringify.
type briefFixture struct {
	Summary     string       `json:"summary"`
	Risks       []briefRisk  `json:"risks"`
	ReviewFocus []briefFocus `json:"review_focus"`
}

// brief is the fixture of the seeded PR #482 (dev-digest server/src/adapters/llm/mock.ts
// MOCK_PR_BRIEF): its paths and lines are those of the seeded diff.
func brief() briefFixture {
	return briefFixture{
		Summary: "[mock LLM] Adds a token-bucket rate limiter in front of the public webhook endpoints and moves its limits into the config.",
		Risks: []briefRisk{
			{
				Kind:        "security",
				Title:       "Rate-limit key trusts a client-controlled header",
				Explanation: "The bucket key comes from `X-Forwarded-For`, so a client can dodge the limiter by sending a new value on every request.",
				Severity:    "high",
				FileRefs:    []string{"src/middleware/ratelimit.ts:6-9"},
			},
			{
				Kind:        "perf",
				Title:       "Per-user query inside a loop",
				Explanation: "Each user triggers its own `orgs` query, so the list endpoint now issues one query per row.",
				Severity:    "medium",
				FileRefs:    []string{"src/api/users.ts:45-50"},
			},
			{
				Kind:        "data",
				Title:       "Live-looking key committed in the config",
				Explanation: "A `stripeKey` literal is added to `config.ts`; check that it is a placeholder and not a real secret.",
				Severity:    "low",
				FileRefs:    []string{"src/config.ts:12"},
			},
		},
		ReviewFocus: []briefFocus{
			{File: "src/middleware/ratelimit.ts", Line: 7, Reason: "The bucket key is taken from a spoofable header."},
			{File: "src/api/public/webhooks.ts", Line: 4, Reason: "Where the limiter is attached to the public routes."},
			{File: "src/api/users.ts", Line: 46, Reason: "New per-user query in the loop."},
			{File: "src/config.ts", Line: 12, Reason: "A key-looking literal enters the config."},
		},
	}
}

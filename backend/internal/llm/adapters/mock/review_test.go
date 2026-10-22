package mock_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
)

// reviewSchema is the Review of contracts/findings.ts as Zod emits it: optional fields are not
// required, nullish ones allow null.
const reviewSchema = `{"type":"object","required":["verdict","summary","score","findings"],"properties":{
  "verdict":{"type":"string","enum":["request_changes","approve","comment"]},
  "summary":{"type":"string"},
  "score":{"type":"integer","minimum":0,"maximum":100},
  "findings":{"type":"array","items":{"type":"object",
    "required":["id","severity","category","title","file","start_line","end_line","rationale","confidence"],
    "properties":{
      "id":{"type":"string"},
      "severity":{"type":"string","enum":["CRITICAL","WARNING","SUGGESTION"]},
      "category":{"type":"string","enum":["bug","security","perf","style","test"]},
      "title":{"type":"string"},"file":{"type":"string"},
      "start_line":{"type":"integer"},"end_line":{"type":"integer"},
      "rationale":{"type":"string"},
      "suggestion":{"anyOf":[{"type":"string"},{"type":"null"}]},
      "confidence":{"type":"number","minimum":0,"maximum":1},
      "kind":{"anyOf":[{"type":"string","enum":["finding","secret_leak","lethal_trifecta","phantom","hook"]},{"type":"null"}]},
      "skill":{"anyOf":[{"type":"string"},{"type":"null"}]}}}}}}`

// The Node fixture (dev-digest server/src/adapters/llm/mock.ts MOCK_REVIEW) through JSON.stringify.
const (
	nodeReviewHead = `{"verdict":"comment","summary":"[mock LLM] The token-bucket limiter works, but its key trusts a client-controlled header and the bucket map never shrinks.","score":70,"findings":[{"id":"mock-1","severity":"WARNING","category":"security","title":"Rate-limit key trusts the spoofable X-Forwarded-For header","file":"src/middleware/ratelimit.ts","start_line":7,"end_line":7,"rationale":"Any client can send a fresh ` + "`X-Forwarded-For`" + ` value on every request and get a new bucket, which bypasses the limiter.","suggestion":"Key on ` + "`req.ip`" + ` with Fastify's ` + "`trustProxy`" + ` configured for your load balancer.","confidence":0.9,"kind":"finding"`
	nodeReviewTail = `},{"id":"mock-2","severity":"SUGGESTION","category":"perf","title":"Bucket map grows without eviction","file":"src/middleware/ratelimit.ts","start_line":4,"end_line":4,"rationale":"One entry is kept per distinct key forever, so memory grows with the number of clients.","suggestion":"Use an LRU / TTL map, or evict buckets that have refilled completely.","confidence":0.72,"kind":"finding"}]}`
)

func reviewRequest(user string) llm.Request {
	r := request(user)
	r.SchemaName = mock.Review
	r.Schema = json.RawMessage(reviewSchema)
	return r
}

func TestStructured_reviewFixtureIsByteEqualToNode(t *testing.T) {
	t.Parallel()
	res, err := newClient(t, 0).Structured(t.Context(), reviewRequest("no skills in this prompt"))
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if want := nodeReviewHead + nodeReviewTail; res.Raw != want || string(res.Data) != want {
		t.Errorf("Structured() raw = %s, want %s", res.Raw, want)
	}
	if res.TokensIn != mock.TokensIn || res.TokensOut != mock.TokensOut || res.Attempts != 1 ||
		res.CostUSD == nil || *res.CostUSD != mock.CostUSD {
		t.Errorf("Structured() usage = %d/%d cost %v attempts %d, want 1200/180 cost 0.00042 in 1 attempt",
			res.TokensIn, res.TokensOut, res.CostUSD, res.Attempts)
	}
}

func TestStructured_reviewFirstSkillAttribution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user string
		want string // skill of mock-1, "" = none
	}{
		{"first heading wins", "Task\n\n## Skills / rules\n### pr-quality-rubric\nbody\n\n### other\nmore\n\n## Diff to review\nx", "pr-quality-rubric"},
		{"heading after a blank body line", "## Skills / rules\nintro line\n\n### second-style  with words\nbody", "second-style"},
		{"later heading of the prompt is not a skill", "## Skills / rules\nno headings here\n\n## Diff to review\n### not-a-skill\nx", ""},
		{"a ### line inside the section is not an end", "## Skills / rules\n### a\n\n### b\n", "a"},
		{"no skills section", "## Diff to review\n### x\n", ""},
		{"empty name is skipped", "## Skills / rules\n### \n### real\n", "real"},
		{"no-break space ends the name", "## Skills / rules\n### na\u00a0me\n", "na"},
		{"carriage return starts a line", "## Skills / rules\nx\r### cr-skill\r", "cr-skill"},
		{"section ends before '## ' that is not ###", "## Skills / rules\nx\n## Other\n### y\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := newClient(t, 0).Structured(t.Context(), reviewRequest(tc.user))
			if err != nil {
				t.Fatalf("Structured() error = %v", err)
			}
			var got struct {
				Findings []struct {
					ID    string  `json:"id"`
					Skill *string `json:"skill"`
				} `json:"findings"`
			}
			if err := json.Unmarshal(res.Data, &got); err != nil || len(got.Findings) != 2 {
				t.Fatalf("Structured() data = %s (%v), want two findings", res.Data, err)
			}
			switch skill := got.Findings[0].Skill; {
			case tc.want == "" && skill != nil:
				t.Errorf("mock-1 skill = %q, want none", *skill)
			case tc.want != "" && (skill == nil || *skill != tc.want):
				t.Errorf("mock-1 skill = %v, want %q", skill, tc.want)
			}
			if got.Findings[1].Skill != nil {
				t.Errorf("mock-2 skill = %q, want none", *got.Findings[1].Skill)
			}
		})
	}
}

func TestStructured_reviewSkillIsAppendedLast(t *testing.T) {
	t.Parallel()
	res, err := newClient(t, 0).Structured(t.Context(), reviewRequest("## Skills / rules\n### pr-quality-rubric\n### other\n"))
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if want := nodeReviewHead + `,"skill":"pr-quality-rubric"` + nodeReviewTail; res.Raw != want {
		t.Errorf("Structured() raw = %s, want %s", res.Raw, want)
	}
}

func TestStructured_reviewDelayIsCancellable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := newClient(t, 2*time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(500 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		_, err := c.Structured(ctx, reviewRequest("x"))
		if !errors.Is(err, context.Canceled) || time.Since(start) != 500*time.Millisecond {
			t.Errorf("Structured() = %v after %v, want context.Canceled after 500ms", err, time.Since(start))
		}
	})
}

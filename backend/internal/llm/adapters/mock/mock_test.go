package mock_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"dev-digest-go/backend/internal/llm"
	"dev-digest-go/backend/internal/llm/adapters/mock"
)

type noKeys struct{}

func (noKeys) Get(string) string { return "" }

// extractionSchema is a stand-in for the conventions ConventionExtraction schema.
const extractionSchema = `{"type":"object","required":["candidates"],"properties":{"candidates":{"type":"array","items":{
  "type":"object","required":["category","rule","confidence","evidence"],"properties":{
    "category":{"type":"string","enum":["structure","imports","naming","other"]},
    "rule":{"type":"string","minLength":1},
    "confidence":{"type":"number","minimum":0,"maximum":1},
    "evidence":{"type":"array","items":{"type":"object","required":["path","start_line","end_line","snippet"],"properties":{
      "path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"},"snippet":{"type":"string"}}}}}}}}}`

func newClient(t *testing.T, delay time.Duration) *llm.Client {
	t.Helper()
	ads := map[llm.Provider]llm.Adapter{}
	for _, p := range llm.Providers() {
		ads[p] = mock.New(p, mock.Options{Delay: delay})
	}
	c := llm.New(llm.Options{Keys: noKeys{}, Adapters: ads, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(c.Close)
	return c
}

func request(user string) llm.Request {
	return llm.Request{
		Provider:   llm.OpenAI,
		Model:      "gpt-5.4",
		Messages:   []llm.Message{{Role: llm.System, Content: "sys"}, {Role: llm.User, Content: user}},
		SchemaName: mock.ConventionExtraction,
		Schema:     json.RawMessage(extractionSchema),
	}
}

const sample = `Task text

<untrusted>
=== FILE: package.json (config) ===
1| {
2|   "name": "demo-package-name"

=== FILE: src/app.ts (source) ===
 1| // x
 2| import { run } from "./run";
 3| run();

=== FILE: src/util/strings.go (source) ===
1| short
2| func Trim(s string) string { return s }

=== FILE: src/a.ts (source) ===
1| export const answer = 42;

=== FILE: src/b.ts (source) ===
1| export const other = 43;
</untrusted>`

type candidate struct {
	Category   string  `json:"category"`
	Rule       string  `json:"rule"`
	Confidence float64 `json:"confidence"`
	Evidence   []struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Snippet   string `json:"snippet"`
	} `json:"evidence"`
}

func TestStructured_conventionsFixture(t *testing.T) {
	t.Parallel()
	c := newClient(t, 0)
	res, err := c.Structured(t.Context(), request(sample))
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if res.TokensIn != 1200 || res.TokensOut != 180 || res.Attempts != 1 || res.CostUSD == nil || *res.CostUSD != 0.00042 {
		t.Errorf("Structured() = %+v, want 1200/180 tokens, cost 0.00042, 1 attempt", res)
	}
	var out struct {
		Candidates []candidate `json:"candidates"`
	}
	if err := json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	type view struct {
		Category, Rule, Path, Snippet string
		Confidence                    float64
		Line                          int
	}
	var got []view
	for _, c := range out.Candidates {
		got = append(got, view{c.Category, c.Rule, c.Evidence[0].Path, c.Evidence[0].Snippet, c.Confidence, c.Evidence[0].StartLine})
	}
	want := []view{
		{"structure", "[mock LLM] Follow the pattern used at the top of src/app.ts", "src/app.ts", `import { run } from "./run";`, 0.9, 2},
		{"imports", "[mock LLM] Follow the pattern used at the top of src/util/strings.go", "src/util/strings.go", "func Trim(s string) string { return s }", 0.78, 2},
		{"naming", "[mock LLM] Follow the pattern used at the top of src/a.ts", "src/a.ts", "export const answer = 42;", 0.66, 1},
		{"other", "[mock LLM] A rule whose evidence does not exist (dropped by the evidence gate)", "src/__mock__/does-not-exist.ts", "export {}", 0.5, 1},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fixture mismatch (-want +got):\n%s", diff)
	}
}

func TestStructured_noSampleStillYieldsTheOtherRule(t *testing.T) {
	t.Parallel()
	res, err := newClient(t, 0).Structured(t.Context(), request("nothing here"))
	if err != nil {
		t.Fatalf("Structured() error = %v", err)
	}
	if n := strings.Count(string(res.Data), "[mock LLM]"); n != 1 {
		t.Errorf("Structured() data = %s, want only the evidence-less rule", res.Data)
	}
}

func TestStructured_unknownSchema(t *testing.T) {
	t.Parallel()
	req := request("x")
	req.SchemaName = "OnboardingTour"
	_, err := newClient(t, 0).Structured(t.Context(), req)
	if err == nil || err.Error() != "Mock LLM provider has no fixture for structured output 'OnboardingTour'" {
		t.Errorf("Structured() error = %v, want the no-fixture text", err)
	}
}

func TestStructured_delayIsCancellable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := newClient(t, 30*time.Second)
		start := time.Now()
		if _, err := c.Structured(t.Context(), request(sample)); err != nil || time.Since(start) != 30*time.Second {
			t.Fatalf("Structured() = %v after %v, want success after the 30s delay", err, time.Since(start))
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := c.Structured(ctx, request(sample))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Structured() error = %v, want the caller's deadline", err)
		}
	})
}

func TestListModels(t *testing.T) {
	t.Parallel()
	got, err := newClient(t, 0).ListModels(t.Context(), llm.Anthropic)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	b, _ := json.Marshal(got)
	if want := `[{"id":"mock/reviewer","provider":"anthropic","label":"Mock reviewer (LLM_PROVIDER_OVERRIDE=mock)"}]`; string(b) != want {
		t.Errorf("ListModels() = %s, want %s", b, want)
	}
	got, _ = newClient(t, 0).ListModels(t.Context(), llm.OpenRouter)
	b, _ = json.Marshal(got)
	if want := `[{"id":"mock/reviewer","provider":"openrouter","label":"Mock reviewer (LLM_PROVIDER_OVERRIDE=mock)"}]`; string(b) != want {
		t.Errorf("ListModels(openrouter) = %s, want %s", b, want)
	}
}

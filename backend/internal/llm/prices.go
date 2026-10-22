package llm

// price is USD per 1,000,000 tokens.
type price struct{ in, out float64 }

// staticPrices is the fallback table, an exact copy of the Node adapters/llm/pricing.ts. One
// unpriced model makes a run's cost unknown, so every selectable model belongs here.
var staticPrices = map[string]price{
	// OpenAI
	"gpt-5.5":                {5.0, 30.0},
	"gpt-5.4":                {2.5, 15.0},
	"gpt-5.4-mini":           {0.75, 4.5},
	"gpt-5.4-nano":           {0.2, 1.25},
	"gpt-5.1":                {1.25, 10.0},
	"gpt-5":                  {1.25, 10.0},
	"gpt-4.1":                {2.0, 8.0},
	"gpt-4.1-mini":           {0.4, 1.6},
	"gpt-4o":                 {2.5, 10.0},
	"gpt-4o-mini":            {0.15, 0.6},
	"text-embedding-3-small": {0.02, 0},
	// Anthropic
	"claude-fable-5-1":          {10.0, 50.0},
	"claude-fable-5":            {10.0, 50.0},
	"claude-opus-5":             {5.0, 25.0},
	"claude-opus-4-8":           {5.0, 25.0},
	"claude-opus-4-7":           {5.0, 25.0},
	"claude-opus-4-6":           {5.0, 25.0},
	"claude-sonnet-5":           {2.0, 10.0},
	"claude-sonnet-4-6":         {3.0, 15.0},
	"claude-haiku-4-5":          {1.0, 5.0},
	"claude-haiku-4-5-20251001": {1.0, 5.0},
	// Retired 3.x aliases, kept so historical runs stay priced.
	"claude-3-5-sonnet-latest": {3.0, 15.0},
	"claude-3-5-haiku-latest":  {0.8, 4.0},
	"claude-3-opus-latest":     {15.0, 75.0},
	// OpenRouter (approximate)
	"z-ai/glm-4.7-flash":         {0, 0},
	"deepseek/deepseek-v4-flash": {0.14, 0.28},
	"z-ai/glm-4.7-flashx":        {0.15, 0.4},
	"minimax/minimax-m2.5":       {0.3, 1.2},
	"z-ai/glm-5.1":               {0.6, 2.2},
}

// cost prices the tokens: (in × in-price + out × out-price) / 1,000,000.
func (p price) cost(tokensIn, tokensOut int) float64 {
	return (float64(tokensIn)*p.in + float64(tokensOut)*p.out) / 1_000_000
}

// StaticCost prices tokens from the static table by exact model name; nil when the model is
// not listed.
func StaticCost(model string, tokensIn, tokensOut int) *float64 {
	p, ok := staticPrices[model]
	if !ok {
		return nil
	}
	c := p.cost(tokensIn, tokensOut)
	return &c
}

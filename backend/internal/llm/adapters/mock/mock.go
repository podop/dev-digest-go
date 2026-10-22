// Package mock implements llm.Adapter as a deterministic, key-less stand-in for a provider
// (LLM_PROVIDER_OVERRIDE=mock, spec FR216 and 7.2). It makes no network call.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dev-digest-go/backend/internal/llm"
)

// Usage and cost every mock structured call reports.
const (
	TokensIn  = 1200
	TokensOut = 180
	CostUSD   = 0.00042
)

// ConventionExtraction is the schema name of the conventions extractor; with Review (review.go)
// it is one of the two the mock has a fixture for.
const ConventionExtraction = "ConventionExtraction"

// ModelLabel is the label of the single model the mock lists.
const ModelLabel = "Mock reviewer (LLM_PROVIDER_OVERRIDE=mock)"

// Options configures an Adapter.
type Options struct {
	// Delay is how long each structured call waits first (cancellable); LLM_MOCK_DELAY_MS.
	Delay time.Duration
}

// Adapter answers as the given provider.
type Adapter struct {
	provider llm.Provider
	delay    time.Duration
}

var _ llm.Adapter = (*Adapter)(nil)

// New returns a mock that stands in for provider.
func New(provider llm.Provider, opts Options) *Adapter {
	return &Adapter{provider: provider, delay: opts.Delay}
}

// RequiresKey is false: the mock needs no key.
func (a *Adapter) RequiresKey() bool { return false }

// Policy has no retries: nothing the mock does is transient.
func (a *Adapter) Policy() llm.Policy {
	return llm.Policy{RequestTimeout: 120 * time.Second}
}

// ListModels returns the one mock model, labelled with the provider it replaces.
func (a *Adapter) ListModels(context.Context, string) ([]llm.ModelInfo, error) {
	label := ModelLabel
	return []llm.ModelInfo{{ID: "mock/reviewer", Provider: a.provider, Label: &label, Mock: true}}, nil
}

// Complete waits the configured delay, then answers from the fixture of the schema.
func (a *Adapter) Complete(ctx context.Context, _ string, at llm.Attempt) (llm.AttemptResult, error) {
	if a.delay > 0 {
		t := time.NewTimer(a.delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return llm.AttemptResult{}, ctx.Err()
		case <-t.C:
		}
	}
	var raw []byte
	var err error
	switch at.SchemaName {
	case ConventionExtraction:
		raw, err = json.Marshal(conventions(at.Messages))
	case Review:
		raw, err = reviewJSON(at.Messages)
	default:
		return llm.AttemptResult{}, fmt.Errorf("Mock LLM provider has no fixture for structured output '%s'", at.SchemaName)
	}
	if err != nil {
		return llm.AttemptResult{}, err
	}
	cost := CostUSD
	return llm.AttemptResult{Raw: string(raw), Usage: &llm.Usage{TokensIn: TokensIn, TokensOut: TokensOut}, CostUSD: &cost}, nil
}

type evidence struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Snippet   string `json:"snippet"`
}

type candidate struct {
	Category   string     `json:"category"`
	Rule       string     `json:"rule"`
	Confidence float64    `json:"confidence"`
	Evidence   []evidence `json:"evidence"`
}

var (
	fileHeader   = regexp.MustCompile(`^=== FILE: (.+) \(source\) ===$`)
	numberedLine = regexp.MustCompile(`^\s*(\d+)\| (.*)$`)
)

// conventions builds candidates from the sample in the prompt: each of the first three
// "=== FILE: <path> (source) ===" blocks yields a rule citing the block's first numbered line of
// at least 12 characters, plus one rule whose evidence does not exist (the evidence gate must
// drop it).
func conventions(messages []llm.Message) map[string]any {
	parts := make([]string, len(messages))
	for i, m := range messages {
		parts[i] = m.Content
	}
	categories := []string{"structure", "imports", "naming"}
	confidences := []float64{0.9, 0.78, 0.66}
	candidates := []candidate{}

	var path string
	found := false // the current block already produced its candidate
	inBlock := false
	for _, line := range strings.Split(strings.Join(parts, "\n"), "\n") {
		if m := fileHeader.FindStringSubmatch(line); m != nil {
			path, inBlock, found = m[1], true, false
			continue
		}
		if strings.HasPrefix(line, "=== FILE: ") || line == "</untrusted>" {
			inBlock = false
			continue
		}
		if !inBlock || found || len(candidates) >= len(categories) {
			continue
		}
		m := numberedLine.FindStringSubmatch(line)
		if m == nil || len(strings.TrimSpace(m[2])) < 12 {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		i := len(candidates)
		candidates = append(candidates, candidate{
			Category:   categories[i],
			Rule:       "[mock LLM] Follow the pattern used at the top of " + path,
			Confidence: confidences[i],
			Evidence:   []evidence{{Path: path, StartLine: n, EndLine: n, Snippet: m[2]}},
		})
		found = true
	}
	candidates = append(candidates, candidate{
		Category:   "other",
		Rule:       "[mock LLM] A rule whose evidence does not exist (dropped by the evidence gate)",
		Confidence: 0.5,
		Evidence:   []evidence{{Path: "src/__mock__/does-not-exist.ts", StartLine: 1, EndLine: 1, Snippet: "export {}"}},
	})
	return map[string]any{"candidates": candidates}
}

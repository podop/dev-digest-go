// Package llmcall implements the review engine's LLM port (reviewer/app.LLM) over the structured
// completion client of internal/llm. One Caller is bound to one agent run: it knows the provider,
// model, run id, OpenRouter session id and the usage observer, so the engine passes only the
// prompt and the chunk position. It builds the Review call of spec 7.9 (schema name and schema,
// temperature 0, two re-prompts, a 300 s request timeout, the prompt log's section metadata) and
// maps the validated JSON answer onto the engine's domain types.
package llmcall

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"dev-digest-go/backend/internal/llm"
	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	"dev-digest-go/backend/internal/reviewer/domain"
)

// Feature labels the prompt_assembled log line of a review call.
const Feature = "review"

// Structured is the part of *llm.Client the Caller uses.
type Structured interface {
	Structured(ctx context.Context, req llm.Request) (llm.Result, error)
}

// Config binds a Caller to one run.
type Config struct {
	Client   Structured
	Provider llm.Provider
	Model    string
	// RunID is the correlation id of the prompt log line.
	RunID string
	// SessionID is the OpenRouter session id "<owner>/<name>#<number>:<agent name>"; empty = none.
	SessionID string
	// OnUsage receives the usage of every response, schema-invalid attempts included, as it
	// arrives. It may be called from several goroutines (concurrent chunk calls); nil = none.
	OnUsage func(domain.Usage)
	// Charge labels every call of this run for the spend ledger (llm.Request.Charge). The callers
	// that build a Caller set it per run: WorkspaceID, Kind (llm.KindReviewRun for a review run,
	// llm.KindEvalCase for an eval case) and RefID (the run id or the eval case id). The Caller
	// copies it into each request, so later changes to the caller's value do not reach the calls.
	// nil = an unlabelled call (the llm client then warns once per call when a Recorder is wired).
	Charge *llm.Charge
}

// Caller is the engine's LLM port for one run. It is safe for concurrent use as far as
// Config.Client and Config.OnUsage are.
type Caller struct{ cfg Config }

var _ reviewerapp.LLM = (*Caller)(nil)

// New returns the Caller of a run. It panics when Client is nil: a wiring error caught at startup.
func New(cfg Config) *Caller {
	if cfg.Client == nil {
		panic("llmcall.New: Client is required")
	}
	return &Caller{cfg: cfg}
}

// Review makes the structured Review call of one chunk. A failed call returns the client's error
// unchanged (its text is already scrubbed of keys and is the run's failure note).
func (c *Caller) Review(ctx context.Context, call reviewerapp.Call) (reviewerapp.CallResult, error) {
	res, err := c.cfg.Client.Structured(ctx, c.request(call))
	if err != nil {
		return reviewerapp.CallResult{}, err
	}
	review, err := decodeReview(res.Data)
	if err != nil {
		return reviewerapp.CallResult{}, fmt.Errorf("decode validated review: %w", err)
	}
	return reviewerapp.CallResult{
		Review: review,
		Raw:    res.Raw,
		Usage:  domain.Usage{TokensIn: res.TokensIn, TokensOut: res.TokensOut, CostUSD: res.CostUSD},
	}, nil
}

func (c *Caller) request(call reviewerapp.Call) llm.Request {
	temperature := domain.Temperature
	req := llm.Request{
		Provider:      c.cfg.Provider,
		Model:         c.cfg.Model,
		Messages:      messages(call.Messages),
		SchemaName:    domain.ReviewSchemaName,
		Schema:        domain.ReviewSchemaJSON,
		Temperature:   &temperature,
		Timeout:       domain.RequestTimeout,
		Reprompts:     domain.Reprompts,
		Feature:       Feature,
		CorrelationID: c.cfg.RunID,
		Sections:      sections(call.Sections),
		Chunk:         &llm.Chunk{Index: call.Chunk.Index, Total: call.Chunk.Total},
		SessionID:     c.cfg.SessionID,
	}
	if c.cfg.Charge != nil {
		charge := *c.cfg.Charge
		req.Charge = &charge
	}
	if on := c.cfg.OnUsage; on != nil {
		req.OnUsage = func(u llm.AttemptUsage) {
			on(domain.Usage{TokensIn: u.TokensIn, TokensOut: u.TokensOut, CostUSD: u.CostUSD})
		}
	}
	return req
}

func messages(in []domain.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	for i, m := range in {
		out[i] = llm.Message{Role: llm.Role(m.Role), Content: m.Content}
	}
	return out
}

// sections maps the engine's content-free section metadata onto the prompt log's.
func sections(in []domain.SectionMeta) []llm.PromptSection {
	out := make([]llm.PromptSection, len(in))
	for i, s := range in {
		out[i] = llm.PromptSection{
			Name: string(s.Name), Role: string(s.Role), Source: string(s.Source), Trust: string(s.Trust),
			Chars: s.Chars, Items: s.Items, Truncated: s.Truncated,
		}
	}
	return out
}

// reviewJSON is the wire shape of the Review answer (frontend contract findings.ts). Numbers are
// float64 because the validator accepts an integer written as 5.0 or 5e0. Nullable fields are
// pointers; evidence is validated but not kept (it is not stored).
type reviewJSON struct {
	Verdict  string        `json:"verdict"`
	Summary  string        `json:"summary"`
	Score    float64       `json:"score"`
	Findings []findingJSON `json:"findings"`
}

type findingJSON struct {
	ID                 string   `json:"id"`
	Severity           string   `json:"severity"`
	Category           string   `json:"category"`
	Title              string   `json:"title"`
	File               string   `json:"file"`
	StartLine          float64  `json:"start_line"`
	EndLine            float64  `json:"end_line"`
	Rationale          string   `json:"rationale"`
	Suggestion         *string  `json:"suggestion"`
	Confidence         float64  `json:"confidence"`
	Kind               *string  `json:"kind"`
	TrifectaComponents []string `json:"trifecta_components"`
	Skill              *string  `json:"skill"`
	OutOfScope         *bool    `json:"out_of_scope"`
}

func decodeReview(data json.RawMessage) (domain.Review, error) {
	var w reviewJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return domain.Review{}, err
	}
	r := domain.Review{
		Verdict: domain.Verdict(w.Verdict), Summary: w.Summary, Score: toInt(w.Score),
		Findings: make([]domain.Finding, len(w.Findings)),
	}
	for i, f := range w.Findings {
		out := domain.Finding{
			ID: f.ID, Severity: domain.Severity(f.Severity), Category: domain.Category(f.Category),
			Title: f.Title, File: f.File, StartLine: toInt(f.StartLine), EndLine: toInt(f.EndLine),
			Rationale: f.Rationale, Suggestion: f.Suggestion, Confidence: f.Confidence,
			TrifectaComponents: f.TrifectaComponents, Skill: f.Skill, OutOfScope: f.OutOfScope,
		}
		if f.Kind != nil {
			out.Kind = domain.Kind(*f.Kind)
		}
		r.Findings[i] = out
	}
	return r, nil
}

// toInt converts a validated integer-valued JSON number, saturating instead of overflowing.
func toInt(f float64) int {
	switch {
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int(f)
}

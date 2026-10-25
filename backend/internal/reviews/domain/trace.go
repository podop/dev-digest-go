package domain

import (
	"strconv"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ToolReviewFile is the tool name of every ToolCall of a trace.
const ToolReviewFile = "review_file"

// The statuses of an IntentTrace.
const (
	IntentStatusUsed        = "used"
	IntentStatusUnavailable = "unavailable"
)

// IntentTrace is the intent layer's contribution to one run's trace (Phase 5 spec 7.5): the
// derivation that fed the run, or the reason there was none. Its tokens and cost are
// informational and never enter the run's usage. One IntentTrace is shared, read-only, by every
// run of a review request.
//
// Status IntentStatusUsed carries every field but Warning; IntentStatusUnavailable carries only
// Warning. On the wire (spec 7.5, key order status, cache_hit, confidence, derived_from,
// change_type, provider, model, tokens_in, tokens_out, cost_usd, sources, warning) only the keys
// of the status are present; Confidence, ChangeType and CostUSD are null when nil.
type IntentTrace struct {
	// Status is IntentStatusUsed or IntentStatusUnavailable.
	Status string
	// CacheHit is true when the run reused a stored derivation (no LLM call for it).
	CacheHit bool
	// Confidence, DerivedFrom and ChangeType come from the stored row.
	Confidence, DerivedFrom, ChangeType *string
	Provider, Model                     string
	TokensIn, TokensOut                 int
	// CostUSD is nil when the derivation was unpriced.
	CostUSD *float64
	Sources []reviewer.IntentSource
	// Warning is the message of the failure that made the intent unavailable.
	Warning string
}

// UnavailableIntentTrace is the IntentTrace of a derivation that failed with warning.
func UnavailableIntentTrace(warning string) *IntentTrace {
	return &IntentTrace{Status: IntentStatusUnavailable, Warning: warning}
}

// CompletedTraceInput is what the trace of a successfully persisted run is built from.
type CompletedTraceInput struct {
	Agent    ReviewAgent
	PRNumber int
	// Source is the run's source; empty = SourceLocal.
	Source     string
	DurationMS int
	// Usage is the engine's usage (its cost is the unrounded sum).
	Usage reviewer.Usage
	// Findings is the persisted finding count.
	Findings  int
	Grounding string
	Assembly  reviewer.Assembly
	// ChunkLabels are the engine's call labels, one tool call each.
	ChunkLabels []string
	Mode        reviewer.Mode
	Raw         string
	Log         []LogLine
	SkillsUsed  []SkillUsed
	// ProjectContext is nil when the run had no attached documents.
	ProjectContext *ContextTrace
	// Intent is the request's intent result; nil when the pre-work did not run (switch off).
	Intent *IntentTrace
}

// CompletedTrace builds the trace of a finished run (spec 7.6, "Completed"). Its slices are never
// nil.
func CompletedTrace(in CompletedTraceInput) Trace {
	chunks := len(in.ChunkLabels)
	calls := make([]ToolCall, chunks)
	ms := roundedShare(in.DurationMS, chunks)
	for i, label := range in.ChunkLabels {
		calls[i] = ToolCall{Tool: ToolReviewFile, Args: label, Meta: string(in.Mode), MS: ms}
	}
	return Trace{
		Config: traceConfig(in.Agent, in.PRNumber, in.Source),
		Stats: TraceStats{
			DurationMS: in.DurationMS,
			TokensIn:   in.Usage.TokensIn,
			TokensOut:  in.Usage.TokensOut,
			CostUSD:    copyFloat(in.Usage.CostUSD),
			Findings:   in.Findings,
			Grounding:  in.Grounding,
		},
		PromptAssembly: in.Assembly,
		ToolCalls:      calls,
		RawOutput:      in.Raw,
		SpecsRead:      SpecsRead(in.ProjectContext),
		Log:            nonNilLog(in.Log),
		SkillsUsed:     nonNilSkills(in.SkillsUsed),
		ProjectContext: in.ProjectContext,
		Intent:         in.Intent,
	}
}

// EndedTraceInput is what the minimal trace of a failed, cancelled or diff-failed run is built from.
type EndedTraceInput struct {
	Agent    ReviewAgent
	PRNumber int
	// Source is the run's source; empty = SourceLocal.
	Source     string
	Log        []LogLine
	DurationMS int
	// Usage is the usage spent so far; nil means none (zero tokens and a zero cost).
	Usage      *reviewer.Usage
	SkillsUsed []SkillUsed
	// ProjectContext is the context resolved before the run ended; nil when none.
	ProjectContext *ContextTrace
	// Intent is the intent result when the pre-work ran before the run ended; nil when it did
	// not (switch off, or the diff failed before the hook).
	Intent *IntentTrace
}

// EndedTrace builds the minimal trace of a run that ended abnormally (spec 7.6, "Ended"): its
// prompt assembly holds only the agent's own system prompt (no guard) and an empty user message.
// Its slices are never nil.
func EndedTrace(in EndedTraceInput) Trace {
	usage := NoUsage()
	if in.Usage != nil {
		usage = *in.Usage
	}
	return Trace{
		Ended:  true,
		Config: traceConfig(in.Agent, in.PRNumber, in.Source),
		Stats: TraceStats{
			DurationMS: in.DurationMS,
			TokensIn:   usage.TokensIn,
			TokensOut:  usage.TokensOut,
			CostUSD:    copyFloat(usage.CostUSD),
			Findings:   0,
			Grounding:  NoGrounding,
		},
		PromptAssembly: reviewer.Assembly{System: in.Agent.SystemPrompt},
		ToolCalls:      []ToolCall{},
		SpecsRead:      SpecsRead(in.ProjectContext),
		Log:            nonNilLog(in.Log),
		SkillsUsed:     nonNilSkills(in.SkillsUsed),
		ProjectContext: in.ProjectContext,
		Intent:         in.Intent,
	}
}

// NewContextTrace builds the trace's project_context record from the resolved documents; the
// budget is ProjectContextBudgetTokens. Docs are never nil.
func NewContextTrace(docs []ContextDoc, tokensTotal int) *ContextTrace {
	if docs == nil {
		docs = []ContextDoc{}
	}
	return &ContextTrace{BudgetTokens: ProjectContextBudgetTokens, TokensTotal: tokensTotal, Docs: docs}
}

// SpecsRead is the trace's specs_read: the paths of the included documents, in prompt order. It
// is never nil.
func SpecsRead(pc *ContextTrace) []string {
	paths := []string{}
	if pc == nil {
		return paths
	}
	for _, d := range pc.Docs {
		if d.Status == ContextIncluded {
			paths = append(paths, d.Path)
		}
	}
	return paths
}

// LogFromEvents turns a run's bus buffer into the trace's log lines.
func LogFromEvents(events []RunEvent) []LogLine {
	lines := make([]LogLine, len(events))
	for i, e := range events {
		lines[i] = LogLine{T: e.T, Kind: e.Kind, Msg: e.Msg}
	}
	return lines
}

func traceConfig(a ReviewAgent, prNumber int, source string) TraceConfig {
	if source == "" {
		source = SourceLocal
	}
	return TraceConfig{
		Agent:    a.Name,
		Version:  strconv.Itoa(a.Version),
		Provider: a.Provider,
		Model:    a.Model,
		PR:       prNumber,
		Source:   source,
	}
}

// roundedShare is round(total / parts) for non-negative total, rounding halves up as
// JavaScript's Math.round does; parts below 1 count as 1.
func roundedShare(total, parts int) int {
	if parts < 1 {
		parts = 1
	}
	return (2*total + parts) / (2 * parts)
}

func copyFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func nonNilLog(l []LogLine) []LogLine {
	if l == nil {
		return []LogLine{}
	}
	return l
}

func nonNilSkills(s []SkillUsed) []SkillUsed {
	if s == nil {
		return []SkillUsed{}
	}
	return s
}

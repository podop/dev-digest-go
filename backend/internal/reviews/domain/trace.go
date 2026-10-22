package domain

import (
	"strconv"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// ToolReviewFile is the tool name of every ToolCall of a trace.
const ToolReviewFile = "review_file"

// CompletedTraceInput is what the trace of a successfully persisted run is built from.
type CompletedTraceInput struct {
	Agent      ReviewAgent
	PRNumber   int
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
		Config: traceConfig(in.Agent, in.PRNumber),
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
	}
}

// EndedTraceInput is what the minimal trace of a failed, cancelled or diff-failed run is built from.
type EndedTraceInput struct {
	Agent      ReviewAgent
	PRNumber   int
	Log        []LogLine
	DurationMS int
	// Usage is the usage spent so far; nil means none (zero tokens and a zero cost).
	Usage      *reviewer.Usage
	SkillsUsed []SkillUsed
	// ProjectContext is the context resolved before the run ended; nil when none.
	ProjectContext *ContextTrace
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
		Config: traceConfig(in.Agent, in.PRNumber),
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

func traceConfig(a ReviewAgent, prNumber int) TraceConfig {
	return TraceConfig{
		Agent:    a.Name,
		Version:  strconv.Itoa(a.Version),
		Provider: a.Provider,
		Model:    a.Model,
		PR:       prNumber,
		Source:   SourceLocal,
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

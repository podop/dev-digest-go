package reviewshttp

import "dev-digest-go/backend/internal/reviews/domain"

// The RunTrace wire shape (spec 7.6, contract trace.ts). The domain trace is tag-free; this file
// is its only serialisation on the read path. memory_pulled ([]) is a constant; intent is null or
// the run's IntentTrace (Phase 5 spec 7.5), its keys in the order of the spec.

type traceDTO struct {
	Config         traceConfigDTO   `json:"config"`
	Stats          traceStatsDTO    `json:"stats"`
	PromptAssembly any              `json:"prompt_assembly"`
	ToolCalls      []toolCallDTO    `json:"tool_calls"`
	RawOutput      string           `json:"raw_output"`
	MemoryPulled   []struct{}       `json:"memory_pulled"`
	SpecsRead      []string         `json:"specs_read"`
	Log            []logLineDTO     `json:"log"`
	SkillsUsed     []skillUsedDTO   `json:"skills_used"`
	Intent         any              `json:"intent"` // nil (null), usedIntentDTO or unavailableIntentDTO
	ProjectContext *contextTraceDTO `json:"project_context,omitempty"`
}

type traceConfigDTO struct {
	Agent    string `json:"agent"`
	Version  string `json:"version"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	PR       int    `json:"pr"`
	Source   string `json:"source"`
}

type traceStatsDTO struct {
	DurationMS int      `json:"duration_ms"`
	TokensIn   int      `json:"tokens_in"`
	TokensOut  int      `json:"tokens_out"`
	CostUSD    *float64 `json:"cost_usd"`
	Findings   int      `json:"findings"`
	Grounding  string   `json:"grounding"`
}

// completedAssemblyDTO is the prompt_assembly of a completed run: every key present.
type completedAssemblyDTO struct {
	System        string  `json:"system"`
	Skills        *string `json:"skills"`
	Memory        any     `json:"memory"` // always null
	Specs         *string `json:"specs"`
	Callers       *string `json:"callers"`
	RepoMap       *string `json:"repo_map"`
	PRDescription *string `json:"pr_description"`
	Intent        *string `json:"intent"` // the rendered PR intent; null without one
	User          string  `json:"user"`
}

// endedAssemblyDTO is the prompt_assembly of a failed, cancelled or diff-failed run: the keys
// callers, repo_map, pr_description and intent are absent.
type endedAssemblyDTO struct {
	System string  `json:"system"`
	Skills *string `json:"skills"`
	Memory any     `json:"memory"` // always null
	Specs  *string `json:"specs"`
	User   string  `json:"user"`
}

type intentSourceDTO struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

// usedIntentDTO is the trace.intent of a run that had an intent: every key but warning.
type usedIntentDTO struct {
	Status      string            `json:"status"`
	CacheHit    bool              `json:"cache_hit"`
	Confidence  *string           `json:"confidence"`
	DerivedFrom *string           `json:"derived_from"`
	ChangeType  *string           `json:"change_type"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	TokensIn    int               `json:"tokens_in"`
	TokensOut   int               `json:"tokens_out"`
	CostUSD     *float64          `json:"cost_usd"`
	Sources     []intentSourceDTO `json:"sources"`
}

// unavailableIntentDTO is the trace.intent of a run whose derivation failed.
type unavailableIntentDTO struct {
	Status  string `json:"status"`
	Warning string `json:"warning"`
}

type toolCallDTO struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
	Meta string `json:"meta"`
	MS   int    `json:"ms"`
}

type logLineDTO struct {
	T    string `json:"t"`
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
}

type skillUsedDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type contextTraceDTO struct {
	BudgetTokens int               `json:"budget_tokens"`
	TokensTotal  int               `json:"tokens_total"`
	Docs         []contextTraceDoc `json:"docs"`
}

type contextTraceDoc struct {
	Path    string           `json:"path"`
	DocType string           `json:"doc_type"`
	Origin  contextOriginDTO `json:"origin"`
	Tokens  int              `json:"tokens"`
	Status  string           `json:"status"`
	Source  string           `json:"source,omitempty"`
	Text    *string          `json:"text,omitempty"`
}

type contextOriginDTO struct {
	Kind      string  `json:"kind"`
	SkillID   *string `json:"skill_id,omitempty"`
	SkillName *string `json:"skill_name,omitempty"`
}

func toTrace(t domain.Trace) traceDTO {
	calls := make([]toolCallDTO, len(t.ToolCalls))
	for i, c := range t.ToolCalls {
		calls[i] = toolCallDTO{Tool: c.Tool, Args: c.Args, Meta: c.Meta, MS: c.MS}
	}
	lines := make([]logLineDTO, len(t.Log))
	for i, l := range t.Log {
		lines[i] = logLineDTO{T: l.T, Kind: string(l.Kind), Msg: l.Msg}
	}
	skills := make([]skillUsedDTO, len(t.SkillsUsed))
	for i, s := range t.SkillsUsed {
		skills[i] = skillUsedDTO{ID: s.ID, Name: s.Name, Version: s.Version}
	}
	specs := t.SpecsRead
	if specs == nil {
		specs = []string{}
	}
	pa := t.PromptAssembly
	var assembly any = completedAssemblyDTO{
		System: pa.System, Skills: pa.Skills, Specs: pa.Specs, Callers: pa.Callers, RepoMap: pa.RepoMap,
		PRDescription: pa.PRDescription, Intent: pa.Intent, User: pa.User,
	}
	if t.Ended {
		assembly = endedAssemblyDTO{System: pa.System, Skills: pa.Skills, Specs: pa.Specs, User: pa.User}
	}
	return traceDTO{
		Config: traceConfigDTO{
			Agent: t.Config.Agent, Version: t.Config.Version, Provider: t.Config.Provider, Model: t.Config.Model,
			PR: t.Config.PR, Source: t.Config.Source,
		},
		Stats: traceStatsDTO{
			DurationMS: t.Stats.DurationMS, TokensIn: t.Stats.TokensIn, TokensOut: t.Stats.TokensOut,
			CostUSD: t.Stats.CostUSD, Findings: t.Stats.Findings, Grounding: t.Stats.Grounding,
		},
		PromptAssembly: assembly,
		ToolCalls:      calls,
		RawOutput:      t.RawOutput,
		MemoryPulled:   []struct{}{},
		SpecsRead:      specs,
		Log:            lines,
		SkillsUsed:     skills,
		Intent:         toIntentTrace(t.Intent),
		ProjectContext: toContextTrace(t.ProjectContext),
	}
}

// toIntentTrace maps the run's IntentTrace; nil is the null of a run without intent pre-work.
func toIntentTrace(it *domain.IntentTrace) any {
	if it == nil {
		return nil
	}
	if it.Status == domain.IntentStatusUnavailable {
		return unavailableIntentDTO{Status: domain.IntentStatusUnavailable, Warning: it.Warning}
	}
	out := usedIntentDTO{
		Status: domain.IntentStatusUsed, CacheHit: it.CacheHit, Confidence: it.Confidence, DerivedFrom: it.DerivedFrom,
		ChangeType: it.ChangeType, Provider: it.Provider, Model: it.Model, TokensIn: it.TokensIn, TokensOut: it.TokensOut,
		CostUSD: it.CostUSD,
	}
	if it.Sources != nil {
		out.Sources = make([]intentSourceDTO, len(it.Sources))
		for i, s := range it.Sources {
			out.Sources[i] = intentSourceDTO{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
		}
	}
	return out
}

func toContextTrace(c *domain.ContextTrace) *contextTraceDTO {
	if c == nil {
		return nil
	}
	docs := make([]contextTraceDoc, len(c.Docs))
	for i, d := range c.Docs {
		origin := contextOriginDTO{Kind: d.Origin.Kind}
		if d.Origin.Kind == "skill" {
			id, name := d.Origin.SkillID, d.Origin.SkillName
			origin.SkillID, origin.SkillName = &id, &name
		}
		docs[i] = contextTraceDoc{
			Path: d.Path, DocType: d.DocType, Origin: origin, Tokens: d.Tokens, Status: string(d.Status),
			Source: d.Source, Text: d.Text,
		}
	}
	return &contextTraceDTO{BudgetTokens: c.BudgetTokens, TokensTotal: c.TokensTotal, Docs: docs}
}

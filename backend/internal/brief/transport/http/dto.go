package briefhttp

import (
	"dev-digest-go/backend/internal/brief/domain"
	"dev-digest-go/backend/internal/platform/httpx"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// The wire shape of spec 7.3 (contract brief.ts). Struct field order is the key order of the
// JSON; nullable fields are always present and lists are never nil, so an empty list is [] and not
// null. "history" is never part of a brief.

type responseDTO struct {
	Brief *briefDTO `json:"brief"`
	Stale bool      `json:"stale"`
}

type briefDTO struct {
	Summary       string       `json:"summary"`
	Risks         risksDTO     `json:"risks"`
	ReviewFocus   []focusDTO   `json:"review_focus"`
	Intent        *intentDTO   `json:"intent"`
	Blast         *blastDTO    `json:"blast"`
	MissingInputs []missingDTO `json:"missing_inputs"`
	SpecsUsed     []string     `json:"specs_used"`
	HeadSHA       string       `json:"head_sha"`
	GeneratedAt   httpx.Time   `json:"generated_at"`
	PromptVersion string       `json:"prompt_version"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	TokensIn      int          `json:"tokens_in"`
	TokensOut     int          `json:"tokens_out"`
	CostUSD       *float64     `json:"cost_usd"`
	ModelRequests int          `json:"model_requests"`
}

type risksDTO struct {
	Risks []riskDTO `json:"risks"`
}

type riskDTO struct {
	Kind        string   `json:"kind"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Severity    string   `json:"severity"`
	FileRefs    []string `json:"file_refs"`
}

type focusDTO struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

type intentDTO struct {
	Intent      string      `json:"intent"`
	InScope     []string    `json:"in_scope"`
	OutOfScope  []string    `json:"out_of_scope"`
	ChangeType  *string     `json:"change_type"`
	Confidence  *string     `json:"confidence"`
	DerivedFrom *string     `json:"derived_from"`
	Sources     []sourceDTO `json:"sources"`
}

type sourceDTO struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

type blastDTO struct {
	ChangedSymbols []symbolDTO     `json:"changed_symbols"`
	Downstream     []downstreamDTO `json:"downstream"`
	Summary        string          `json:"summary"`
	Degraded       bool            `json:"degraded"`
	Reason         string          `json:"reason,omitempty"`
}

type symbolDTO struct {
	Name string `json:"name"`
	File string `json:"file"`
	Kind string `json:"kind"`
}

type downstreamDTO struct {
	Symbol            string      `json:"symbol"`
	Callers           []callerDTO `json:"callers"`
	EndpointsAffected []string    `json:"endpoints_affected"`
	CronsAffected     []string    `json:"crons_affected"`
}

type callerDTO struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type missingDTO struct {
	Input  string `json:"input"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

func toResponse(s domain.State) responseDTO {
	r := responseDTO{Stale: s.Stale}
	if s.Brief != nil {
		b := toBrief(*s.Brief)
		r.Brief = &b
	}
	return r
}

func toBrief(b domain.Brief) briefDTO {
	dto := briefDTO{
		Summary:       b.Summary,
		Risks:         risksDTO{Risks: make([]riskDTO, len(b.Risks))},
		ReviewFocus:   make([]focusDTO, len(b.ReviewFocus)),
		Intent:        toIntent(b.Intent),
		Blast:         toBlast(b.Blast),
		MissingInputs: make([]missingDTO, len(b.MissingInputs)),
		SpecsUsed:     list(b.SpecsUsed),
		HeadSHA:       b.HeadSHA,
		GeneratedAt:   httpx.NewTime(b.GeneratedAt),
		PromptVersion: b.PromptVersion,
		Provider:      b.Provider,
		Model:         b.Model,
		TokensIn:      b.TokensIn,
		TokensOut:     b.TokensOut,
		CostUSD:       b.CostUSD,
		ModelRequests: b.ModelRequests,
	}
	for i, r := range b.Risks {
		dto.Risks.Risks[i] = riskDTO{Kind: r.Kind, Title: r.Title, Explanation: r.Explanation, Severity: string(r.Severity), FileRefs: list(r.FileRefs)}
	}
	for i, f := range b.ReviewFocus {
		dto.ReviewFocus[i] = focusDTO{File: f.File, Line: f.Line, Reason: f.Reason}
	}
	for i, m := range b.MissingInputs {
		dto.MissingInputs[i] = missingDTO{Input: string(m.Input), Reason: string(m.Reason), Detail: m.Detail}
	}
	return dto
}

func toIntent(in *reviewer.Intent) *intentDTO {
	if in == nil {
		return nil
	}
	dto := &intentDTO{
		Intent: in.Text, InScope: list(in.InScope), OutOfScope: list(in.OutOfScope),
		ChangeType: in.ChangeType, Confidence: in.Confidence, Sources: make([]sourceDTO, len(in.Sources)),
	}
	if in.DerivedFrom != "" {
		dto.DerivedFrom = &in.DerivedFrom
	}
	for i, s := range in.Sources {
		dto.Sources[i] = sourceDTO{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
	}
	return dto
}

func toBlast(b *domain.Blast) *blastDTO {
	if b == nil {
		return nil
	}
	dto := &blastDTO{
		ChangedSymbols: make([]symbolDTO, len(b.ChangedSymbols)),
		Downstream:     make([]downstreamDTO, len(b.Downstream)),
		Summary:        b.Summary,
		Degraded:       b.Degraded,
	}
	if b.Degraded {
		dto.Reason = b.Reason
	}
	for i, s := range b.ChangedSymbols {
		dto.ChangedSymbols[i] = symbolDTO{Name: s.Name, File: s.File, Kind: s.Kind}
	}
	for i, g := range b.Downstream {
		d := downstreamDTO{Symbol: g.Symbol, Callers: make([]callerDTO, len(g.Callers)), EndpointsAffected: list(g.EndpointsAffected), CronsAffected: list(g.CronsAffected)}
		for j, c := range g.Callers {
			d.Callers[j] = callerDTO{Name: c.Name, File: c.File, Line: c.Line}
		}
		dto.Downstream[i] = d
	}
	return dto
}

// list returns s, or an empty list for nil, so that it encodes as [] and not null.
func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

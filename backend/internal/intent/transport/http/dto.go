package intenthttp

import (
	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	"dev-digest-go/backend/internal/platform/httpx"
)

// responseDTO is PrIntentResponse (frontend contract review-api.ts): the record or null, and
// whether it is stale.
type responseDTO struct {
	Intent *recordDTO `json:"intent"`
	Stale  bool       `json:"stale"`
}

// recordDTO is PrIntentRecord in the contract's key order: the Intent part, then the stored
// key material and usage. Nullable fields are always present; the arrays are never null.
type recordDTO struct {
	Intent        string      `json:"intent"`
	InScope       []string    `json:"in_scope"`
	OutOfScope    []string    `json:"out_of_scope"`
	ChangeType    *string     `json:"change_type"`
	Confidence    *string     `json:"confidence"`
	DerivedFrom   string      `json:"derived_from"`
	Sources       []sourceDTO `json:"sources"`
	PRID          string      `json:"pr_id"`
	HeadSHA       string      `json:"head_sha"`
	InputHash     string      `json:"input_hash"`
	PromptVersion int         `json:"prompt_version"`
	Provider      string      `json:"provider"`
	Model         string      `json:"model"`
	TokensIn      int         `json:"tokens_in"`
	TokensOut     int         `json:"tokens_out"`
	CostUSD       *float64    `json:"cost_usd"`
	DerivedAt     httpx.Time  `json:"derived_at"`
}

// sourceDTO is IntentSource; detail is always present, null when none.
type sourceDTO struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

func toResponse(r app.Response) responseDTO {
	out := responseDTO{Stale: r.Stale}
	if r.Intent != nil {
		rec := toRecord(*r.Intent)
		out.Intent = &rec
	}
	return out
}

func toRecord(r domain.Record) recordDTO {
	sources := make([]sourceDTO, len(r.Sources))
	for i, s := range r.Sources {
		sources[i] = sourceDTO{Kind: s.Kind, Ref: s.Ref, Status: s.Status, Detail: s.Detail}
	}
	return recordDTO{
		Intent: r.Text, InScope: nonNil(r.InScope), OutOfScope: nonNil(r.OutOfScope),
		ChangeType: r.ChangeType, Confidence: r.Confidence, DerivedFrom: r.DerivedFrom, Sources: sources,
		PRID: r.PRID, HeadSHA: r.HeadSHA, InputHash: r.InputHash, PromptVersion: r.PromptVersion,
		Provider: r.Provider, Model: r.Model, TokensIn: r.TokensIn, TokensOut: r.TokensOut, CostUSD: r.CostUSD,
		DerivedAt: httpx.NewTime(r.DerivedAt),
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

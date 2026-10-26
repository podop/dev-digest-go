package customprovidershttp

import (
	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/platform/httpx"
)

// customProviderResponse is the wire shape of a provider (contract: CustomProvider). No secret
// value is part of it: the key and the header values are reported as flags only.
type customProviderResponse struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Protocol         string           `json:"protocol"`
	BaseURL          string           `json:"base_url"`
	PlainHTTP        bool             `json:"plain_http"`
	AuthMode         string           `json:"auth_mode"`
	AuthHeader       *string          `json:"auth_header"`
	StructuredOutput string           `json:"structured_output"`
	KeyConfigured    bool             `json:"key_configured"`
	ExtraHeaders     []headerResponse `json:"extra_headers"`
	Models           []modelResponse  `json:"models"`
	CreatedAt        httpx.Time       `json:"created_at"`
	UpdatedAt        httpx.Time       `json:"updated_at"`
}

// headerResponse is an extra header (contract: CustomProviderHeader): the name only.
type headerResponse struct {
	Name            string `json:"name"`
	ValueConfigured bool   `json:"value_configured"`
}

// modelResponse is a manual model (contract: CustomProviderModel).
type modelResponse struct {
	ID      string           `json:"id"`
	Label   *string          `json:"label"`
	Pricing *pricingResponse `json:"pricing"`
}

// pricingResponse is the price in USD per 1M tokens (contract: ModelPricing).
type pricingResponse struct {
	PromptPerM     float64 `json:"promptPerM"`
	CompletionPerM float64 `json:"completionPerM"`
}

// deletedResponse is the answer of DELETE (contract: CustomProviderDeleted).
type deletedResponse struct {
	Deleted string `json:"deleted"`
}

func toResponse(v app.View) customProviderResponse {
	p := v.Provider
	out := customProviderResponse{
		ID: p.ID, Name: p.Name, Protocol: string(p.Protocol), BaseURL: p.BaseURL, PlainHTTP: v.PlainHTTP,
		AuthMode: string(p.AuthMode), StructuredOutput: string(p.StructuredOutput), KeyConfigured: v.KeyConfigured,
		ExtraHeaders: make([]headerResponse, len(v.ExtraHeaders)),
		Models:       make([]modelResponse, len(p.Models)),
		CreatedAt:    httpx.NewTime(p.CreatedAt), UpdatedAt: httpx.NewTime(p.UpdatedAt),
	}
	if p.AuthHeader != "" {
		header := p.AuthHeader
		out.AuthHeader = &header
	}
	for i, h := range v.ExtraHeaders {
		out.ExtraHeaders[i] = headerResponse{Name: h.Name, ValueConfigured: h.ValueConfigured}
	}
	for i, m := range p.Models {
		out.Models[i] = modelResponse{ID: m.ID, Label: m.Label}
		if m.Pricing != nil {
			out.Models[i].Pricing = &pricingResponse{PromptPerM: m.Pricing.PromptPerM, CompletionPerM: m.Pricing.CompletionPerM}
		}
	}
	return out
}

func toResponses(views []app.View) []customProviderResponse {
	out := make([]customProviderResponse, len(views))
	for i, v := range views {
		out[i] = toResponse(v)
	}
	return out
}

package app

import (
	"fmt"
	"strings"

	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Wire codes of the context (spec §7).
const (
	CodeNotFound = "custom_provider_not_found"
	CodeExists   = "custom_provider_exists"
	CodeLimit    = "custom_provider_limit"
	CodeInUse    = "provider_in_use"
)

// validationMessage is the message of every 422 `validation_error` (httpx.ValidationMessage; the
// app layer does not import the HTTP package).
const validationMessage = "Request validation failed"

func notFound(id string) *apperr.Error {
	return apperr.NotFound("Custom provider " + id + " not found").WithCode(CodeNotFound)
}

func exists(id string, cause error) *apperr.Error {
	return apperr.Conflict("Custom provider " + id + " already exists").WithCode(CodeExists).WithCause(cause)
}

func limitReached(cause error) *apperr.Error {
	return apperr.Validation(fmt.Sprintf("At most %d custom providers", domain.MaxProviders)).
		WithCode(CodeLimit).WithCause(cause)
}

// InUseDetails is the `details` of a 409 provider_in_use (spec FR6).
type InUseDetails struct {
	Agents        []InUseAgent `json:"agents"`
	FeatureModels []string     `json:"feature_models"`
	DefaultModel  bool         `json:"default_model"`
}

// InUseAgent is an agent that uses the provider.
type InUseAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func inUse(id string, u domain.Usage) *apperr.Error {
	d := InUseDetails{Agents: make([]InUseAgent, len(u.Agents)), FeatureModels: u.FeatureModels, DefaultModel: u.DefaultModel}
	for i, a := range u.Agents {
		d.Agents[i] = InUseAgent{ID: a.ID, Name: a.Name}
	}
	if d.FeatureModels == nil {
		d.FeatureModels = []string{}
	}
	return apperr.Conflict("Custom provider " + id + " is used and cannot be deleted").
		WithCode(CodeInUse).WithDetails(d)
}

// issueDetail is one item of the 422 `details` array, field for field what
// httpx.IssueDetail renders (Fastify + Zod type provider shape).
type issueDetail struct {
	Keyword      string      `json:"keyword"`
	InstancePath string      `json:"instancePath"`
	SchemaPath   string      `json:"schemaPath"`
	Message      string      `json:"message"`
	Params       issueParams `json:"params"`
}

type issueParams struct {
	Issue issueBody `json:"issue"`
}

type issueBody struct {
	Code    string `json:"code"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

// validationError is the 422 validation_error of issues, in the shape httpx.NewValidationError
// renders: one detail per issue, located by its path.
func validationError(issues []domain.Issue) *apperr.Error {
	details := make([]issueDetail, 0, len(issues))
	for _, is := range issues {
		path := is.Path
		if path == nil {
			path = []any{}
		}
		parts := make([]string, len(path))
		for i, p := range path {
			parts[i] = fmt.Sprint(p)
		}
		joined := strings.Join(parts, "/")
		details = append(details, issueDetail{
			Keyword:      is.Code,
			InstancePath: "/" + joined,
			SchemaPath:   "#/" + joined + "/" + is.Code,
			Message:      is.Message,
			Params:       issueParams{Issue: issueBody{Code: is.Code, Path: path, Message: is.Message}},
		})
	}
	return apperr.Validation(validationMessage).WithDetails(details)
}

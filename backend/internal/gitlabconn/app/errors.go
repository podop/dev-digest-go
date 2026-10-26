package app

import (
	"fmt"
	"strings"

	"dev-digest-go/backend/internal/gitlabconn/domain"
	"dev-digest-go/backend/internal/platform/apperr"
)

// Wire codes of the context (spec §7).
const (
	CodeExists   = "gitlab_connection_exists"
	CodeNotFound = "gitlab_connection_not_found"
	CodeLimit    = "gitlab_connection_limit"
)

// validationMessage is the message of every 422 `validation_error` (httpx.ValidationMessage; the
// app layer does not import the HTTP package).
const validationMessage = "Request validation failed"

// issueCode is the Zod issue code of every validation problem of this context.
const issueCode = "custom"

// MsgHostChange is the 422 message of a PUT that changes the host key (spec FR5).
const MsgHostChange = "The host of a connection cannot change; add a new connection instead"

func notFound(id string) *apperr.Error {
	return apperr.NotFound("GitLab connection " + id + " not found").WithCode(CodeNotFound)
}

func exists(host string, cause error) *apperr.Error {
	return apperr.Conflict("A GitLab connection for " + host + " already exists").
		WithCode(CodeExists).WithCause(cause)
}

func limitReached(cause error) *apperr.Error {
	return apperr.Validation(fmt.Sprintf("At most %d GitLab connections", domain.Limit)).
		WithCode(CodeLimit).WithCause(cause)
}

// issue is one validation problem: a path inside the request body and a message.
type issue struct {
	path    []any
	message string
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
func validationError(issues []issue) *apperr.Error {
	details := make([]issueDetail, 0, len(issues))
	for _, is := range issues {
		parts := make([]string, len(is.path))
		for i, p := range is.path {
			parts[i] = fmt.Sprint(p)
		}
		joined := strings.Join(parts, "/")
		details = append(details, issueDetail{
			Keyword:      issueCode,
			InstancePath: "/" + joined,
			SchemaPath:   "#/" + joined + "/" + issueCode,
			Message:      is.message,
			Params:       issueParams{Issue: issueBody{Code: issueCode, Path: is.path, Message: is.message}},
		})
	}
	return apperr.Validation(validationMessage).WithDetails(details)
}

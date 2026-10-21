package httpx

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
)

// ValidationMessage is the message of every 422 response.
const ValidationMessage = "Request validation failed"

// Issue is one validation problem. Path locates it inside the validated part: empty for the whole
// body, ["id"] for a path param, ["a", 0, "b"] for a nested field. Elements are string or int.
type Issue struct {
	Code    string // Zod-style kind, e.g. invalid_type, invalid_string, too_small
	Path    []any
	Message string
}

// IssueDetail is one item of the 422 `details` array (Fastify + Zod type provider shape).
type IssueDetail struct {
	Keyword      string      `json:"keyword"`
	InstancePath string      `json:"instancePath"`
	SchemaPath   string      `json:"schemaPath"`
	Message      string      `json:"message"`
	Params       IssueParams `json:"params"`
}

// IssueParams wraps the raw issue under `params.issue`.
type IssueParams struct {
	Issue IssueBody `json:"issue"`
}

// IssueBody is the raw issue echoed back to the client.
type IssueBody struct {
	Code    string `json:"code"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

// NewValidationError builds the 422 error for the issues of the first failing request part.
func NewValidationError(issues ...Issue) *apperr.Error {
	details := make([]IssueDetail, 0, len(issues))
	for _, is := range issues {
		path := is.Path
		if path == nil {
			path = []any{}
		}
		joined := joinPath(path)
		details = append(details, IssueDetail{
			Keyword:      is.Code,
			InstancePath: "/" + joined,
			SchemaPath:   "#/" + joined + "/" + is.Code,
			Message:      is.Message,
			Params:       IssueParams{Issue: IssueBody{Code: is.Code, Path: path, Message: is.Message}},
		})
	}
	return apperr.Validation(ValidationMessage).WithDetails(details)
}

func joinPath(path []any) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = anyString(p)
	}
	return strings.Join(parts, "/")
}

func anyString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// uuidPattern accepts any UUID version/variant, case-insensitive.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// PathUUID returns the path param name when it is a UUID, otherwise the 422 validation error
// for that param.
func PathUUID(c *echo.Context, name string) (string, error) {
	v := c.Param(name)
	if !uuidPattern.MatchString(v) {
		return "", NewValidationError(Issue{Code: "invalid_string", Path: []any{name}, Message: "Invalid uuid"})
	}
	return v, nil
}

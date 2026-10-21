// Package apperr defines the application error kinds that domain and app code return and the
// HTTP edge (internal/platform/httpx) maps to status codes. It has no HTTP dependency.
package apperr

import "errors"

// Kind classifies an Error. Each kind has a default wire code and a fixed HTTP status at the edge.
type Kind int

// The kinds of application errors.
const (
	KindInvalidInput Kind = iota + 1
	KindNotFound
	KindConflict
	KindPayloadTooLarge
	KindValidation
	KindInternal
	KindConfig
	KindExternalService
	KindForbidden
)

// String returns the kind name, for logs and test failures.
func (k Kind) String() string {
	switch k {
	case KindInvalidInput:
		return "invalid_input"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindPayloadTooLarge:
		return "payload_too_large"
	case KindValidation:
		return "validation"
	case KindInternal:
		return "internal"
	case KindConfig:
		return "config"
	case KindExternalService:
		return "external_service"
	case KindForbidden:
		return "forbidden"
	default:
		return "unknown"
	}
}

// defaultCode is the wire code a kind carries unless WithCode overrides it.
func defaultCode(k Kind) string {
	switch k {
	case KindValidation:
		return "validation_error"
	case KindInternal:
		return "internal_error"
	case KindConfig:
		return "config_error"
	case KindPayloadTooLarge:
		return "payload_too_large"
	default:
		return k.String()
	}
}

// Error is an application error whose code, message and details are safe to show to the client.
// Put anything that must stay server-side in the cause (WithCause), never in Message.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Details any
	cause   error
}

// New returns an Error of the given kind with that kind's default code.
func New(kind Kind, message string) *Error {
	return &Error{Kind: kind, Code: defaultCode(kind), Message: message}
}

// InvalidInput is a malformed or unacceptable request value (400).
func InvalidInput(message string) *Error { return New(KindInvalidInput, message) }

// NotFound is a missing resource (404).
func NotFound(message string) *Error { return New(KindNotFound, message) }

// Conflict is a state conflict such as a duplicate (409).
func Conflict(message string) *Error { return New(KindConflict, message) }

// PayloadTooLarge is a request over a size limit (413).
func PayloadTooLarge(message string) *Error { return New(KindPayloadTooLarge, message) }

// Validation is a request that failed schema validation (422).
func Validation(message string) *Error { return New(KindValidation, message) }

// Internal is a server-side failure with a client-safe message (500).
func Internal(message string) *Error { return New(KindInternal, message) }

// Config is a server misconfiguration (500).
func Config(message string) *Error { return New(KindConfig, message) }

// ExternalService is a failed call to an upstream service (502).
func ExternalService(message string) *Error { return New(KindExternalService, message) }

// Forbidden is a request the server refuses to serve, such as a foreign Origin or Host (403).
func Forbidden(message string) *Error { return New(KindForbidden, message) }

// WithCode replaces the wire code, e.g. github_unavailable.
func (e *Error) WithCode(code string) *Error {
	e.Code = code
	return e
}

// WithDetails attaches client-visible details.
func (e *Error) WithDetails(details any) *Error {
	e.Details = details
	return e
}

// WithCause attaches the underlying error for Unwrap and server-side logging only.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

// Error implements error. It includes the cause so that logs keep the full story.
func (e *Error) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

// Unwrap returns the cause, if any.
func (e *Error) Unwrap() error { return e.cause }

// As finds an *Error in err's chain.
func As(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

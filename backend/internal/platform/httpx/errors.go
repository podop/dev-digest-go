package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/apperr"
)

// ErrorBody is the error envelope every endpoint returns, identical to the Node API:
// {"error": {"code": "...", "message": "...", "details": ...}}.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the content of the error envelope.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// statusOf maps an application error kind to its HTTP status; ok is false for an unknown kind.
func statusOf(k apperr.Kind) (status int, ok bool) {
	switch k {
	case apperr.KindInvalidInput:
		return http.StatusBadRequest, true
	case apperr.KindNotFound:
		return http.StatusNotFound, true
	case apperr.KindConflict:
		return http.StatusConflict, true
	case apperr.KindPayloadTooLarge:
		return http.StatusRequestEntityTooLarge, true
	case apperr.KindForbidden:
		return http.StatusForbidden, true
	case apperr.KindPaymentRequired:
		return http.StatusPaymentRequired, true
	case apperr.KindValidation:
		return http.StatusUnprocessableEntity, true
	case apperr.KindInternal, apperr.KindConfig:
		return http.StatusInternalServerError, true
	case apperr.KindExternalService:
		return http.StatusBadGateway, true
	default:
		return 0, false
	}
}

// errorHandler renders every error in the envelope. Application errors (apperr) carry their own
// code, message and details; unknown routes and unknown methods are 404 not_found like Fastify;
// other framework errors keep their status with code internal_error (Node quirk the web app
// tolerates); anything else is a 500 without the raw message, which is only logged.
func errorHandler(log *slog.Logger) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		if resp, uerr := echo.UnwrapResponse(c.Response()); uerr == nil && resp.Committed {
			return
		}
		req := c.Request()
		status, body := http.StatusInternalServerError, ErrorDetail{Code: "internal_error", Message: "Internal error"}

		// Echo's own errors (ErrNotFound, ErrMethodNotAllowed, …) are unexported types that
		// only expose StatusCode(); *echo.HTTPError also carries a message.
		var coder echo.HTTPStatusCoder
		code := 0
		if errors.As(err, &coder) {
			code = coder.StatusCode()
		}
		ae, isApp := apperr.As(err)
		kindStatus, knownKind := 0, false
		if isApp {
			kindStatus, knownKind = statusOf(ae.Kind)
		}
		switch {
		case isApp && knownKind:
			status = kindStatus
			body = ErrorDetail{Code: ae.Code, Message: ae.Message, Details: ae.Details}
			if status >= http.StatusInternalServerError {
				log.ErrorContext(req.Context(), "request failed", "method", req.Method, "path", req.URL.Path, "err", err)
			}
		case code == http.StatusNotFound || code == http.StatusMethodNotAllowed:
			status = http.StatusNotFound
			body = ErrorDetail{Code: "not_found", Message: fmt.Sprintf("Route %s %s not found", req.Method, req.URL.Path)}
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError:
			status = code
			body.Message = http.StatusText(code)
			var he *echo.HTTPError
			if errors.As(err, &he) && he.Message != "" {
				body.Message = he.Message
			}
		default:
			log.ErrorContext(req.Context(), "request failed", "method", req.Method, "path", req.URL.Path, "err", err)
		}

		if req.Method == http.MethodHead {
			err = c.NoContent(status)
		} else {
			err = c.JSON(status, ErrorBody{Error: body})
		}
		if err != nil {
			log.ErrorContext(req.Context(), "write error response", "err", err)
		}
	}
}

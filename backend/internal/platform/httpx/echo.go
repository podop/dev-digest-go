// Package httpx builds the Echo instance and the API-wide HTTP behavior shared by all modules.
package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
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

// New returns an Echo instance with the error envelope, CORS for the web app and panic recovery.
func New(corsOrigins []string, log *slog.Logger) *echo.Echo {
	e := echo.NewWithConfig(echo.Config{
		Logger:           log,
		HTTPErrorHandler: errorHandler(log),
	})
	e.Use(middleware.Recover())
	if len(corsOrigins) > 0 { // Echo's CORS middleware panics on an empty origin list
		e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
			AllowOrigins:     corsOrigins,
			AllowCredentials: true,
			AllowMethods: []string{
				http.MethodGet, http.MethodHead, http.MethodPut,
				http.MethodPatch, http.MethodPost, http.MethodDelete,
			},
		}))
	}
	return e
}

// errorHandler renders every error in the envelope. Unknown routes and unknown methods are
// 404 not_found like Fastify; other framework errors keep their status with code
// internal_error (Node quirk the web app tolerates); anything else is a 500 without the raw
// message.
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
		switch {
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

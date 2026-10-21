package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/labstack/echo/v5"
)

// maxBodyBytes is the request body limit: 1 MiB is accepted, one more byte is a 413.
const maxBodyBytes = 1 << 20

// bodyRules applies the Fastify body behavior to POST, PUT, PATCH and DELETE on registered
// routes. The framework-level failures use code internal_error (see errorHandler):
//   - no body and no Content-Type: accepted;
//   - non-empty body without Content-Type, or a media type other than application/json and
//     text/plain: 415;
//   - body over the limit: 413;
//   - application/json that is empty or not valid JSON: 400.
//
// A valid body is buffered and put back, so handlers read it as usual.
func bodyRules() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if c.RouteInfo().Path == "" || !methodHasBody(req.Method) {
				return next(c)
			}

			hasBody := req.ContentLength != 0
			ct := req.Header.Get("Content-Type")
			if ct == "" && !hasBody {
				return next(c)
			}
			media := ""
			if ct != "" {
				if m, _, err := mime.ParseMediaType(ct); err == nil {
					media = m
				}
			}
			if media != "application/json" && media != "text/plain" {
				return echo.NewHTTPError(http.StatusUnsupportedMediaType, "Unsupported Media Type")
			}

			if req.ContentLength > maxBodyBytes {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "Request body is too large")
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes+1))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "Request body could not be read")
			}
			if len(body) > maxBodyBytes {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "Request body is too large")
			}
			if media == "application/json" {
				if len(body) == 0 {
					return echo.NewHTTPError(http.StatusBadRequest, "Body cannot be empty when content-type is set to 'application/json'")
				}
				if !json.Valid(body) {
					return echo.NewHTTPError(http.StatusBadRequest, "Body is not valid JSON but content-type is set to 'application/json'")
				}
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			return next(c)
		}
	}
}

func methodHasBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

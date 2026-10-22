package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/labstack/echo/v5"
)

// maxBodyBytes is the default request body limit: 1 MiB is accepted, one more byte is a 413.
// Options.BodyLimits raises it for single routes.
const maxBodyBytes = 1 << 20

// bodyRules applies the Fastify body behavior to POST, PUT, PATCH and DELETE on registered
// routes. The framework-level failures use code internal_error (see errorHandler):
//   - no body and no Content-Type: accepted;
//   - non-empty body without Content-Type, or a media type other than application/json and
//     text/plain: 415;
//   - body over the limit (maxBodyBytes, or the route's entry in limits): 413;
//   - application/json that is empty or not valid JSON: 400.
//
// A valid body is buffered and put back; handlers read it through ReadJSONBody.
//
// limits maps "METHOD /registered/route/path" (the route as registered, e.g.
// "POST /skills/import/preview" or "PUT /repos/:id/context/files") to that route's limit in
// bytes; a route without an entry uses maxBodyBytes.
func bodyRules(limits map[string]int64) echo.MiddlewareFunc {
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
			media := mediaType(ct)
			if media != "application/json" && media != "text/plain" {
				return echo.NewHTTPError(http.StatusUnsupportedMediaType, "Unsupported Media Type")
			}

			limit := int64(maxBodyBytes)
			if l, ok := limits[req.Method+" "+c.RouteInfo().Path]; ok {
				limit = l
			}
			if req.ContentLength > limit {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "Request body is too large")
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "Request body could not be read")
			}
			if int64(len(body)) > limit {
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

// mediaType returns the lower-case media type of a Content-Type value without its parameters;
// "" when the value is empty or malformed.
func mediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	m, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return m
}

// ReadJSONBody returns the request body of an endpoint whose body is a JSON document. Handlers
// call it instead of reading the request body themselves, so one rule decides what is JSON:
// only a body sent as application/json (parameters such as charset allowed) is returned as it
// came. A body of any other media type is returned as the JSON string holding its text, the way
// Fastify hands a text/plain body to the Zod schema; a parser that expects an object then
// answers "Expected object, received string". That keeps a cross-site "simple" request
// (text/plain, no preflight) from being read as a command. No body and no Content-Type
// yields an empty slice. The body rules have already limited the size.
func ReadJSONBody(c *echo.Context) ([]byte, error) {
	req := c.Request()
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	ct := req.Header.Get("Content-Type")
	if ct == "" && len(raw) == 0 {
		return raw, nil
	}
	if mediaType(ct) == "application/json" {
		return raw, nil
	}
	text, err := json.Marshal(string(raw))
	if err != nil {
		return nil, fmt.Errorf("encode request body as text: %w", err)
	}
	return text, nil
}

func methodHasBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

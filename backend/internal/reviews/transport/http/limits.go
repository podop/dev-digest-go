package reviewshttp

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/reviews/app"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// jsNumber matches the decimal literals JavaScript's Number() accepts (after trimming).
var jsNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// parseStartBody validates the body of POST /pulls/:id/review the way the Node schema does
// (a null or missing body is {}; unknown keys are dropped) and returns every issue, in field
// order. Only types and UUID format are checked here: how many ids, and which of agentId, all
// and agentIds may be combined, is the use case's 400 invalid_run_request.
//
//	agentId   string, optional
//	all       boolean, optional
//	agentIds  array of UUID strings, optional
func parseStartBody(raw []byte) (app.StartRequest, []httpx.Issue) {
	var req app.StartRequest
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return req, nil
	}
	var fields map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &fields) != nil {
		return req, []httpx.Issue{httpx.TypeIssue("object", jsonType(trimmed))}
	}

	var issues []httpx.Issue
	if v, ok := fields["agentId"]; ok {
		if t := jsonType(v); t != "string" {
			issues = append(issues, httpx.TypeIssue("string", t, "agentId"))
		} else {
			_ = json.Unmarshal(v, &req.AgentID) // a string by the check above
		}
	}
	if v, ok := fields["all"]; ok {
		if t := jsonType(v); t != "boolean" {
			issues = append(issues, httpx.TypeIssue("boolean", t, "all"))
		} else {
			_ = json.Unmarshal(v, &req.All)
		}
	}
	if v, ok := fields["agentIds"]; ok {
		ids, is := parseAgentIDs(v)
		issues = append(issues, is...)
		req.AgentIDs = ids
	}
	if len(issues) > 0 {
		return app.StartRequest{}, issues
	}
	return req, nil
}

// parseAgentIDs reads the agentIds array; the result is non-nil (possibly empty) when valid.
func parseAgentIDs(v json.RawMessage) ([]string, []httpx.Issue) {
	if t := jsonType(v); t != "array" {
		return nil, []httpx.Issue{httpx.TypeIssue("array", t, "agentIds")}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(v, &items); err != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("array", "undefined", "agentIds")}
	}
	ids := make([]string, 0, len(items))
	var issues []httpx.Issue
	for i, item := range items {
		if t := jsonType(item); t != "string" {
			issues = append(issues, httpx.TypeIssue("string", t, "agentIds", i))
			continue
		}
		var s string
		_ = json.Unmarshal(item, &s)
		if !uuidPattern.MatchString(s) {
			issues = append(issues, httpx.Issue{Code: httpx.CodeInvalidStr, Path: []any{"agentIds", i}, Message: "Invalid uuid"})
			continue
		}
		ids = append(ids, s)
	}
	return ids, issues
}

// jsonType names the JSON type of a raw value the way Zod reports it.
func jsonType(raw []byte) string {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '"':
			return "string"
		case '{':
			return "object"
		case '[':
			return "array"
		case 't', 'f':
			return "boolean"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	return "undefined"
}

// parseLimit reads the optional ?limit= of the multi-agent run list as z.coerce.number().int()
// does: the text goes through JavaScript's Number(), so "" is 0, "1e1" is 10 and "abc" is NaN
// (422). A value beyond the int32 range is clamped; the use case rejects it as invalid_limit.
func parseLimit(c *echo.Context) (*int, []httpx.Issue) {
	values, ok := c.QueryParams()["limit"]
	if !ok || len(values) == 0 {
		return nil, nil
	}
	f, ok := toJSNumber(values[0])
	if !ok {
		return nil, []httpx.Issue{httpx.TypeIssue("number", "nan", "limit")}
	}
	if math.IsInf(f, 0) || f != math.Trunc(f) {
		return nil, []httpx.Issue{httpx.TypeIssue("integer", "float", "limit")}
	}
	n := int(max(math.MinInt32, min(math.MaxInt32, f)))
	return &n, nil
}

// toJSNumber converts s as Number(s) does; ok is false for NaN.
func toJSNumber(s string) (f float64, ok bool) {
	t := httpx.JSTrim(s)
	switch t {
	case "":
		return 0, true
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(t) > 2 && t[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[t[1]]
		if base != 0 {
			if !isDigitsOf(t[2:], base) {
				return 0, false
			}
			n, _ := strconv.ParseUint(t[2:], base, 64) // beyond 64 bits it saturates, as a huge value does
			return float64(n), true
		}
	}
	if !jsNumber.MatchString(t) {
		return 0, false
	}
	f, _ = strconv.ParseFloat(t, 64) // out of range yields ±Inf, which Number() does too
	return f, true
}

// isDigitsOf reports whether s consists of digits of the given base only.
func isDigitsOf(s string, base int) bool {
	for _, r := range s {
		d := -1
		switch {
		case r >= '0' && r <= '9':
			d = int(r - '0')
		case r >= 'a' && r <= 'f':
			d = int(r-'a') + 10
		case r >= 'A' && r <= 'F':
			d = int(r-'A') + 10
		}
		if d < 0 || d >= base {
			return false
		}
	}
	return s != ""
}

package pullshttp

import (
	"encoding/json"
	"fmt"
	"math"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/pulls/app"
)

// maxSafeInt is the largest integer a JSON number carries exactly (Zod's int() limit).
const maxSafeInt = 1<<53 - 1

// parseCommentInput validates the body of POST /pulls/:id/comments (PrCommentInput) and returns
// every issue found, in field order. An empty issue list means the request is valid.
//
//	path        non-empty string
//	line        positive integer
//	side        "LEFT" | "RIGHT", optional
//	body        non-empty string
//	in_reply_to integer, optional
func parseCommentInput(raw []byte) (app.CommentRequest, []httpx.Issue) {
	var req app.CommentRequest
	if len(raw) == 0 {
		return req, []httpx.Issue{{Code: "invalid_type", Path: []any{}, Message: "Required"}}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		got := "string" // a body that is not JSON (text/plain)
		if json.Valid(raw) {
			got = jsonType(raw)
		}
		return req, []httpx.Issue{{Code: "invalid_type", Path: []any{}, Message: "Expected object, received " + got}}
	}

	var issues []httpx.Issue
	add := func(code, field, msg string) {
		issues = append(issues, httpx.Issue{Code: code, Path: []any{field}, Message: msg})
	}

	if s, code, msg := nonEmptyString(fields, "path"); code != "" {
		add(code, "path", msg)
	} else {
		req.Path = s
	}

	if n, code, msg := integer(fields, "line", true); code != "" {
		add(code, "line", msg)
	} else if n <= 0 {
		add("too_small", "line", "Number must be greater than 0")
	} else {
		req.Line = int(n)
	}

	if rawSide, ok := fields["side"]; ok {
		var side string
		switch {
		case jsonType(rawSide) != "string":
			add("invalid_type", "side", "Expected 'LEFT' | 'RIGHT', received "+jsonType(rawSide))
		case json.Unmarshal(rawSide, &side) != nil || (side != "LEFT" && side != "RIGHT"):
			add("invalid_enum_value", "side", fmt.Sprintf("Invalid enum value. Expected 'LEFT' | 'RIGHT', received %q", side))
		default:
			req.Side = side
		}
	}

	if s, code, msg := nonEmptyString(fields, "body"); code != "" {
		add(code, "body", msg)
	} else {
		req.Body = s
	}

	if _, ok := fields["in_reply_to"]; ok {
		if n, code, msg := integer(fields, "in_reply_to", false); code != "" {
			add(code, "in_reply_to", msg)
		} else {
			req.InReplyTo = &n
		}
	}
	return req, issues
}

// nonEmptyString reads a required string field of at least one character.
func nonEmptyString(fields map[string]json.RawMessage, name string) (value, code, msg string) {
	raw, ok := fields[name]
	if !ok {
		return "", "invalid_type", "Required"
	}
	if t := jsonType(raw); t != "string" {
		return "", "invalid_type", "Expected string, received " + t
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", "invalid_type", "Expected string, received invalid"
	}
	if value == "" {
		return "", "too_small", "String must contain at least 1 character(s)"
	}
	return value, "", ""
}

// integer reads an integer field. A missing field is only an issue when required.
func integer(fields map[string]json.RawMessage, name string, required bool) (n int64, code, msg string) {
	raw, ok := fields[name]
	if !ok {
		if required {
			return 0, "invalid_type", "Required"
		}
		return 0, "", ""
	}
	if t := jsonType(raw); t != "number" {
		return 0, "invalid_type", "Expected number, received " + t
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f != math.Trunc(f) || math.Abs(f) > maxSafeInt {
		return 0, "invalid_type", "Expected integer, received float"
	}
	return int64(f), "", ""
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

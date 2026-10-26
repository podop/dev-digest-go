package gitlabconnhttp

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"dev-digest-go/backend/internal/gitlabconn/app"
	"dev-digest-go/backend/internal/platform/httpx"
)

// bodyKeys are the keys of GitLabConnectionInput (and of the partial Update). A key outside the
// set is a 422 unrecognized_keys at the body.
var bodyKeys = map[string]bool{"name": true, "base_url": true, "token": true}

// fields are the decoded keys of a body; a nil pointer means the key was omitted.
type fields struct {
	name, baseURL *string
	token         app.TokenInput
}

// parseCreate reads a POST body: base_url is required. It checks the JSON types and the keys only;
// every rule on the values is the service's.
func parseCreate(body []byte) (app.CreateInput, []httpx.Issue) {
	f, issues := parse(body, true)
	if issues != nil {
		return app.CreateInput{}, issues
	}
	in := app.CreateInput{BaseURL: *f.baseURL, Token: f.token}
	if f.name != nil {
		in.Name = *f.name
	}
	return in, nil
}

// parseUpdate reads a PUT body: the POST body with every key optional.
func parseUpdate(body []byte) (app.UpdateInput, []httpx.Issue) {
	f, issues := parse(body, false)
	if issues != nil {
		return app.UpdateInput{}, issues
	}
	return app.UpdateInput{Name: f.name, BaseURL: f.baseURL, Token: f.token}, nil
}

// parse reports the issues of the fields in schema order (name, base_url, token), then the
// unrecognized keys, as Zod does.
func parse(body []byte, create bool) (fields, []httpx.Issue) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fields{}, []httpx.Issue{httpx.RequiredIssue()}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return fields{}, []httpx.Issue{httpx.TypeIssue("object", jsonType(trimmed))}
	}
	var (
		f      fields
		issues []httpx.Issue
	)
	if raw, ok := obj["name"]; ok {
		if s, ok := stringValue(raw, "name", &issues); ok {
			f.name = &s
		}
	}
	if raw, ok := obj["base_url"]; ok {
		if s, ok := stringValue(raw, "base_url", &issues); ok {
			f.baseURL = &s
		}
	} else if create {
		issues = append(issues, httpx.RequiredIssue("base_url"))
	}
	if raw, ok := obj["token"]; ok {
		if jsonType(raw) == "null" {
			f.token = app.TokenInput{Op: app.TokenRemove}
		} else if s, ok := stringValue(raw, "token", &issues); ok {
			f.token = app.TokenInput{Op: app.TokenReplace, Value: s}
		}
	}
	var extra []string
	for k := range obj {
		if !bodyKeys[k] {
			extra = append(extra, "'"+k+"'")
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		issues = append(issues, httpx.Issue{Code: "unrecognized_keys", Path: []any{},
			Message: "Unrecognized key(s) in object: " + strings.Join(extra, ", ")})
	}
	if len(issues) > 0 {
		return fields{}, issues
	}
	return f, nil
}

func stringValue(raw json.RawMessage, key string, issues *[]httpx.Issue) (string, bool) {
	var s string
	if jsonType(raw) != "string" || json.Unmarshal(raw, &s) != nil {
		*issues = append(*issues, httpx.TypeIssue("string", jsonType(raw), key))
		return "", false
	}
	return s, true
}

// jsonType names the JSON type of raw the way Zod does in "received …".
func jsonType(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "undefined"
	}
	switch c := raw[0]; {
	case c == '{':
		return "object"
	case c == '[':
		return "array"
	case c == '"':
		return "string"
	case c == 't' || c == 'f':
		return "boolean"
	case c == 'n':
		return "null"
	default:
		return "number"
	}
}

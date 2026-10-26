package customprovidershttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/httpx"
)

// Keys of the request bodies (contract: CustomProviderInput and its parts). A key outside these
// sets is a 422 unrecognized_keys at the object that holds it.
var (
	createKeys  = []string{"id", "name", "protocol", "base_url", "auth_mode", "auth_header", "structured_output", "api_key", "extra_headers", "models"}
	updateKeys  = createKeys[1:]
	headerKeys  = []string{"name", "value"}
	modelKeys   = []string{"id", "label", "pricing"}
	pricingKeys = []string{"promptPerM", "completionPerM"}
)

// parseCreate reads a POST body. It checks the JSON types and the keys only; every rule on the
// values (ids, URLs, lengths, limits, modes) is domain.Validate's, run by the service.
func parseCreate(body []byte) (domain.Input, []httpx.Issue) {
	return parseInput(body, true)
}

// parseUpdate reads a PUT body: the POST body without `id`, which is an unknown key here.
func parseUpdate(body []byte) (domain.Input, []httpx.Issue) {
	return parseInput(body, false)
}

func parseInput(body []byte, withID bool) (domain.Input, []httpx.Issue) {
	r := &reader{}
	keys := updateKeys
	if withID {
		keys = createKeys
	}
	obj, ok := r.object(body, keys)
	if !ok {
		return domain.Input{}, r.issues
	}
	var in domain.Input
	if withID {
		in.ID, _ = r.requiredString(obj, "id")
	}
	in.Name, _ = r.requiredString(obj, "name")
	protocol, _ := r.requiredString(obj, "protocol")
	in.Protocol = custom.Protocol(protocol)
	in.BaseURL, _ = r.requiredString(obj, "base_url")
	authMode, _ := r.optionalString(obj, "auth_mode")
	in.AuthMode = custom.AuthMode(authMode)
	in.AuthHeader = r.nullableString(obj, "auth_header")
	structured, _ := r.optionalString(obj, "structured_output")
	in.StructuredOutput = custom.Mode(structured)
	in.APIKey = r.apiKey(obj)
	in.ExtraHeaders = r.headers(obj)
	in.Models = r.models(obj)
	if len(r.issues) > 0 {
		return domain.Input{}, r.issues
	}
	return in, nil
}

// reader collects the issues of one body while it is decoded.
type reader struct{ issues []httpx.Issue }

func (r *reader) add(i httpx.Issue) { r.issues = append(r.issues, i) }

// object decodes raw as a JSON object and reports keys outside allowed. path locates it.
func (r *reader) object(raw []byte, allowed []string, path ...any) (map[string]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		r.add(httpx.RequiredIssue(path...))
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		r.add(httpx.TypeIssue("object", jsonType(trimmed), path...))
		return nil, false
	}
	var extra []string
	for k := range obj {
		if !contains(allowed, k) {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		quoted := make([]string, len(extra))
		for i, k := range extra {
			quoted[i] = "'" + k + "'"
		}
		r.add(httpx.Issue{Code: "unrecognized_keys", Path: append([]any{}, path...),
			Message: "Unrecognized key(s) in object: " + strings.Join(quoted, ", ")})
	}
	return obj, true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// requiredString reads a string field that must be present.
func (r *reader) requiredString(obj map[string]json.RawMessage, key string, path ...any) (string, bool) {
	p := append(append([]any{}, path...), key)
	raw, ok := obj[key]
	if !ok {
		r.add(httpx.RequiredIssue(p...))
		return "", false
	}
	return r.stringValue(raw, p)
}

// optionalString reads a string field that may be absent (null is not a string).
func (r *reader) optionalString(obj map[string]json.RawMessage, key string, path ...any) (string, bool) {
	raw, ok := obj[key]
	if !ok {
		return "", false
	}
	return r.stringValue(raw, append(append([]any{}, path...), key))
}

func (r *reader) stringValue(raw json.RawMessage, path []any) (string, bool) {
	var s string
	if jsonType(raw) != "string" || json.Unmarshal(raw, &s) != nil {
		r.add(httpx.TypeIssue("string", jsonType(raw), path...))
		return "", false
	}
	return s, true
}

// nullableString reads a string field that may be absent or null; both give nil.
func (r *reader) nullableString(obj map[string]json.RawMessage, key string) *string {
	raw, ok := obj[key]
	if !ok || jsonType(raw) == "null" {
		return nil
	}
	s, ok := r.stringValue(raw, []any{key})
	if !ok {
		return nil
	}
	return &s
}

// apiKey maps the write-only key: omitted keeps, null removes, a string replaces.
func (r *reader) apiKey(obj map[string]json.RawMessage) domain.KeyChange {
	raw, ok := obj["api_key"]
	switch {
	case !ok:
		return domain.KeyChange{Op: domain.KeyKeep}
	case jsonType(raw) == "null":
		return domain.KeyChange{Op: domain.KeyRemove}
	}
	s, ok := r.stringValue(raw, []any{"api_key"})
	if !ok {
		return domain.KeyChange{}
	}
	return domain.KeyChange{Op: domain.KeyReplace, Value: s}
}

// array returns the elements of an optional array field.
func (r *reader) array(obj map[string]json.RawMessage, key string) []json.RawMessage {
	raw, ok := obj[key]
	if !ok {
		return nil
	}
	var items []json.RawMessage
	if jsonType(raw) != "array" || json.Unmarshal(raw, &items) != nil {
		r.add(httpx.TypeIssue("array", jsonType(raw), key))
		return nil
	}
	return items
}

func (r *reader) headers(obj map[string]json.RawMessage) []domain.HeaderInput {
	items := r.array(obj, "extra_headers")
	if items == nil {
		return nil
	}
	out := make([]domain.HeaderInput, 0, len(items))
	for i, raw := range items {
		h, ok := r.object(raw, headerKeys, "extra_headers", i)
		if !ok {
			continue
		}
		name, _ := r.requiredString(h, "name", "extra_headers", i)
		in := domain.HeaderInput{Name: name}
		if v, ok := r.optionalString(h, "value", "extra_headers", i); ok {
			in.Value = &v
		}
		out = append(out, in)
	}
	return out
}

func (r *reader) models(obj map[string]json.RawMessage) []domain.Model {
	items := r.array(obj, "models")
	if items == nil {
		return nil
	}
	out := make([]domain.Model, 0, len(items))
	for i, raw := range items {
		m, ok := r.object(raw, modelKeys, "models", i)
		if !ok {
			continue
		}
		id, _ := r.requiredString(m, "id", "models", i)
		model := domain.Model{ID: id}
		if raw, ok := m["label"]; ok && jsonType(raw) != "null" {
			if s, ok := r.stringValue(raw, []any{"models", i, "label"}); ok {
				model.Label = &s
			}
		}
		if raw, ok := m["pricing"]; ok && jsonType(raw) != "null" {
			model.Pricing = r.pricing(raw, i)
		}
		out = append(out, model)
	}
	return out
}

func (r *reader) pricing(raw json.RawMessage, model int) *domain.Pricing {
	path := []any{"models", model, "pricing"}
	p, ok := r.object(raw, pricingKeys, path...)
	if !ok {
		return nil
	}
	prompt, okPrompt := r.number(p, "promptPerM", path)
	completion, okCompletion := r.number(p, "completionPerM", path)
	if !okPrompt || !okCompletion {
		return nil
	}
	return &domain.Pricing{PromptPerM: prompt, CompletionPerM: completion}
}

func (r *reader) number(obj map[string]json.RawMessage, key string, path []any) (float64, bool) {
	p := append(append([]any{}, path...), key)
	raw, ok := obj[key]
	if !ok {
		r.add(httpx.RequiredIssue(p...))
		return 0, false
	}
	if jsonType(raw) != "number" {
		r.add(httpx.TypeIssue("number", jsonType(raw), p...))
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		// A number outside float64 (1e999) is far above the price limit.
		r.add(httpx.Issue{Code: httpx.CodeTooBig, Path: p,
			Message: fmt.Sprintf("Number must be less than or equal to %d", int(domain.MaxPrice))})
		return 0, false
	}
	return f, true
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

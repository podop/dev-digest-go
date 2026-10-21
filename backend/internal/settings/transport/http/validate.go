package settingshttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/settings/app"
)

// Zod-style issue codes of the 422 details.
const (
	codeInvalidType = "invalid_type"
	codeInvalidEnum = "invalid_enum_value"
	codeTooSmall    = "too_small"
)

var (
	featureIDs  = []string{"onboarding", "review_intent", "risk_brief", "conformance", "conventions"}
	providerIDs = []string{"openai", "anthropic", "openrouter"}
	themes      = []string{"dark", "light"}
	densities   = []string{"regular", "compact"}

	// knownKeys are validated in this order; every other key is accepted as is.
	knownKeys = []string{"polling_interval_min", "theme", "density", "sync_to_folder", "automatic_reviews", "feature_models"}
)

// parseSettingsUpdate validates a PUT /settings body (section 7.2) and returns the entries to
// write. Known keys are checked and normalized, unknown keys pass through with any JSON value.
func parseSettingsUpdate(body []byte) (app.Bag, []httpx.Issue) {
	var obj map[string]json.RawMessage
	if issues := decodeObject(body, &obj); issues != nil {
		return nil, issues
	}

	out := make(app.Bag, len(obj))
	var issues []httpx.Issue
	for key, raw := range obj {
		if !slices.Contains(knownKeys, key) {
			out[key] = raw
		}
	}
	for _, key := range knownKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var (
			value json.RawMessage
			is    []httpx.Issue
		)
		switch key {
		case "polling_interval_min":
			value, is = pollingInterval(raw, key)
		case "theme":
			value, is = enumString(raw, themes, key)
		case "density":
			value, is = enumString(raw, densities, key)
		case "sync_to_folder", "automatic_reviews":
			value, is = boolean(raw, key)
		case "feature_models":
			value, is = featureModels(raw, key)
		}
		issues = append(issues, is...)
		if is == nil {
			out[key] = value
		}
	}
	if issues != nil {
		return nil, issues
	}
	return out, nil
}

// connTestRequest is a validated POST /settings/test-connection body.
type connTestRequest struct {
	Provider app.Provider
	Key      string
}

// parseConnTest validates {provider, key?}; other fields are ignored.
func parseConnTest(body []byte) (connTestRequest, []httpx.Issue) {
	var obj map[string]json.RawMessage
	if issues := decodeObject(body, &obj); issues != nil {
		return connTestRequest{}, issues
	}
	var req connTestRequest
	var issues []httpx.Issue

	if raw, ok := obj["provider"]; !ok {
		issues = append(issues, issue(codeInvalidType, "Required", "provider"))
	} else {
		var name string
		if !decodeString(raw, &name) {
			issues = append(issues, typeIssue("string", raw, "provider"))
		} else if !slices.Contains(app.ProviderNames(), name) {
			issues = append(issues, enumIssue(app.ProviderNames(), name, "provider"))
		} else {
			req.Provider = app.Provider(name)
		}
	}
	if raw, ok := obj["key"]; ok {
		var key string
		switch {
		case !decodeString(raw, &key):
			issues = append(issues, typeIssue("string", raw, "key"))
		case key == "":
			issues = append(issues, issue(codeTooSmall, "String must contain at least 1 character(s)", "key"))
		default:
			req.Key = key
		}
	}
	if issues != nil {
		return connTestRequest{}, issues
	}
	return req, nil
}

// decodeObject parses body as a JSON object.
func decodeObject(body []byte, dst *map[string]json.RawMessage) []httpx.Issue {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []httpx.Issue{{Code: codeInvalidType, Path: []any{}, Message: "Required"}}
	}
	if trimmed[0] != '{' {
		return []httpx.Issue{{Code: codeInvalidType, Path: []any{}, Message: "Expected object, received " + jsonType(trimmed)}}
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		// The body rules already accepted the bytes as JSON; this is a type problem.
		return []httpx.Issue{{Code: codeInvalidType, Path: []any{}, Message: "Expected object, received " + jsonType(trimmed)}}
	}
	return nil
}

func pollingInterval(raw json.RawMessage, path ...any) (json.RawMessage, []httpx.Issue) {
	t := jsonType(raw)
	if t != "number" {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected number, received "+t, path...)}
	}
	f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected number, received nan", path...)}
	}
	if f != math.Trunc(f) {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected integer, received float", path...)}
	}
	if f < 1 {
		return nil, []httpx.Issue{issue(codeTooSmall, "Number must be greater than or equal to 1", path...)}
	}
	return json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64)), nil
}

func enumString(raw json.RawMessage, allowed []string, path ...any) (json.RawMessage, []httpx.Issue) {
	var s string
	if !decodeString(raw, &s) {
		return nil, []httpx.Issue{typeIssue("string", raw, path...)}
	}
	if !slices.Contains(allowed, s) {
		return nil, []httpx.Issue{enumIssue(allowed, s, path...)}
	}
	return raw, nil
}

func boolean(raw json.RawMessage, path ...any) (json.RawMessage, []httpx.Issue) {
	if t := jsonType(raw); t != "boolean" {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected boolean, received "+t, path...)}
	}
	return raw, nil
}

type featureModelChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// featureModels validates feature_models: an object keyed by feature id (any subset) whose
// values are {provider, model}. Extra fields of a choice are dropped, like Zod does.
func featureModels(raw json.RawMessage, path ...any) (json.RawMessage, []httpx.Issue) {
	if t := jsonType(raw); t != "object" {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected object, received "+t, path...)}
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected object, received "+jsonType(raw), path...)}
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	var issues []httpx.Issue
	clean := make(map[string]featureModelChoice, len(entries))
	for _, id := range ids {
		at := append(slices.Clone(path), id)
		if !slices.Contains(featureIDs, id) {
			issues = append(issues, enumIssue(featureIDs, id, at...))
			continue
		}
		choice, is := featureModel(entries[id], at)
		issues = append(issues, is...)
		if is == nil {
			clean[id] = choice
		}
	}
	if issues != nil {
		return nil, issues
	}
	out, err := json.Marshal(clean)
	if err != nil {
		return nil, []httpx.Issue{issue(codeInvalidType, "Invalid feature_models", path...)}
	}
	return out, nil
}

func featureModel(raw json.RawMessage, path []any) (featureModelChoice, []httpx.Issue) {
	if t := jsonType(raw); t != "object" {
		return featureModelChoice{}, []httpx.Issue{issue(codeInvalidType, "Expected object, received "+t, path...)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return featureModelChoice{}, []httpx.Issue{issue(codeInvalidType, "Expected object, received "+jsonType(raw), path...)}
	}
	var choice featureModelChoice
	var issues []httpx.Issue

	at := append(slices.Clone(path), "provider")
	if p, ok := fields["provider"]; !ok {
		issues = append(issues, issue(codeInvalidType, "Required", at...))
	} else {
		var s string
		switch {
		case !decodeString(p, &s):
			issues = append(issues, typeIssue("string", p, at...))
		case !slices.Contains(providerIDs, s):
			issues = append(issues, enumIssue(providerIDs, s, at...))
		default:
			choice.Provider = s
		}
	}
	at = append(slices.Clone(path), "model")
	if m, ok := fields["model"]; !ok {
		issues = append(issues, issue(codeInvalidType, "Required", at...))
	} else {
		var s string
		switch {
		case !decodeString(m, &s):
			issues = append(issues, typeIssue("string", m, at...))
		case s == "":
			issues = append(issues, issue(codeTooSmall, "String must contain at least 1 character(s)", at...))
		default:
			choice.Model = s
		}
	}
	return choice, issues
}

func issue(code, message string, path ...any) httpx.Issue {
	if path == nil {
		path = []any{}
	}
	return httpx.Issue{Code: code, Path: path, Message: message}
}

func typeIssue(want string, raw json.RawMessage, path ...any) httpx.Issue {
	return issue(codeInvalidType, "Expected "+want+", received "+jsonType(raw), path...)
}

func enumIssue(allowed []string, got string, path ...any) httpx.Issue {
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "'" + a + "'"
	}
	return issue(codeInvalidEnum,
		fmt.Sprintf("Invalid enum value. Expected %s, received '%s'", strings.Join(quoted, " | "), got), path...)
}

// decodeString decodes raw into dst when it is a JSON string.
func decodeString(raw json.RawMessage, dst *string) bool {
	return jsonType(raw) == "string" && json.Unmarshal(raw, dst) == nil
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

package settingshttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/llm/custom"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/settings/app"
)

// Zod-style issue codes of the 422 details.
const (
	codeInvalidType  = "invalid_type"
	codeInvalidEnum  = "invalid_enum_value"
	codeTooSmall     = "too_small"
	codeTooBig       = "too_big"
	codeCustom       = "custom"
	codeInvalidUnion = "invalid_union"
	codeInvalidStr   = "invalid_string"
)

// Bounds and precision of monthly_spend_cap_usd (the UI contract's SPEND_CAP_* constants).
const (
	spendCapMin      = 0
	spendCapMax      = 1_000_000
	spendCapDecimals = 2
)

var (
	featureIDs  = []string{"onboarding", "review_intent", "risk_brief", "conformance", "conventions"}
	providerIDs = []string{"openai", "anthropic", "openrouter"}
	// retentionDays are the accepted retention_days values; null (keep forever) is accepted too.
	retentionDays = []int{30, 60, 90, 180, 365}
	themes        = []string{"dark", "light"}
	densities     = []string{"regular", "compact"}

	// knownKeys are validated in this order; every other key is accepted as is.
	knownKeys = []string{"polling_interval_min", "theme", "density", "sync_to_folder", "automatic_reviews", "feature_models", "default_model", "monthly_spend_cap_usd", "retention_days", "workspace_name", "plugin_last_export"}
)

// definedFunc reports whether a well-formed custom provider id is defined in the workspace; nil
// means none is (FR7).
type definedFunc func(id string) bool

func (d definedFunc) defined(id string) bool { return d != nil && d(id) }

// parseSettingsUpdate validates a PUT /settings body (section 7.2) and returns the entries to
// write. Known keys are checked and normalized, unknown keys pass through with any JSON value.
// A custom provider id in default_model or feature_models is accepted when defined says so.
func parseSettingsUpdate(body []byte, defined definedFunc) (app.Bag, []httpx.Issue) {
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
		case "sync_to_folder":
			value, is = boolean(raw, key)
		case "automatic_reviews":
			value, is = automaticReviews(raw, key)
		case "feature_models":
			value, is = featureModels(raw, defined, key)
		case "default_model":
			value, is = defaultModel(raw, defined, key)
		case "monthly_spend_cap_usd":
			value, is = spendCap(raw, key)
		case "retention_days":
			value, is = retention(raw, key)
		case "workspace_name":
			// The name is written by PUT /workspace only (FR1: trim, 1-80 runes, no control
			// characters), so PUT /settings cannot bypass those rules.
			is = []httpx.Issue{issue(codeCustom, "workspace_name is set with PUT /workspace", key)}
		case "plugin_last_export":
			// Written only by the plugin export, through a cmd adapter (spec P7); a client cannot
			// forge the instant the Plugins panel shows.
			is = []httpx.Issue{issue(codeCustom, "plugin_last_export is set by the plugin export", key)}
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
	// ConnectionID is set (and required) for the gitlab provider only.
	ConnectionID string
}

// isCustomID reports whether s is a well-formed custom provider id (`custom:<slug>`).
func isCustomID(s string) bool {
	_, ok := custom.ParseID(s)
	return ok
}

// parseConnTest validates {provider, key?, connection_id?}; other fields are ignored. The provider
// is a connection-test provider or a well-formed custom provider id (whether it is defined is the
// use case's 404, FR11). connection_id is required for gitlab (whether it exists is the use case's
// 404, FR9) and ignored for every other provider.
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
		} else if !slices.Contains(app.ConnTestProviderNames(), name) && !isCustomID(name) {
			issues = append(issues, enumIssue(app.ConnTestProviderNames(), name, "provider"))
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
	if req.Provider == app.GitLab {
		if raw, ok := obj["connection_id"]; !ok {
			issues = append(issues, issue(codeInvalidType, "Required", "connection_id"))
		} else {
			var id string
			switch {
			case !decodeString(raw, &id):
				issues = append(issues, typeIssue("string", raw, "connection_id"))
			case id == "":
				issues = append(issues, issue(codeTooSmall, "String must contain at least 1 character(s)", "connection_id"))
			default:
				req.ConnectionID = id
			}
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

// uuidPattern accepts any UUID version and variant, case-insensitive (same rule as httpx.PathUUID).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// automaticReviews validates the PUT input of automatic_reviews (spec 7.1): a boolean (stored as
// the patch {enabled: b}) or an object with any subset of enabled, on_new_pr, on_new_commits
// (booleans) and agent_ids (UUID strings, duplicates removed keeping the first, then at most
// app.MaxAutoReviewAgents). The returned patch holds only the fields that were sent; enabled_at
// and unknown fields are dropped, and the settings use case merges the patch into the stored value.
func automaticReviews(raw json.RawMessage, path ...any) (json.RawMessage, []httpx.Issue) {
	switch t := jsonType(raw); t {
	case "boolean":
		return json.RawMessage(`{"enabled":` + strings.TrimSpace(string(raw)) + `}`), nil
	case "object":
	default:
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected object, received "+t, path...)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected object, received "+jsonType(raw), path...)}
	}

	var issues []httpx.Issue
	patch := make(map[string]any, len(fields))
	for _, key := range []string{"enabled", "on_new_pr", "on_new_commits"} {
		v, ok := fields[key]
		if !ok {
			continue
		}
		if t := jsonType(v); t != "boolean" {
			issues = append(issues, issue(codeInvalidType, "Expected boolean, received "+t, append(slices.Clone(path), key)...))
			continue
		}
		patch[key] = strings.TrimSpace(string(v)) == "true"
	}
	if v, ok := fields["agent_ids"]; ok {
		ids, is := agentIDs(v, append(slices.Clone(path), "agent_ids"))
		issues = append(issues, is...)
		if is == nil {
			patch["agent_ids"] = ids
		}
	}
	if issues != nil {
		return nil, issues
	}
	out, err := json.Marshal(patch)
	if err != nil {
		return nil, []httpx.Issue{issue(codeInvalidType, "Invalid automatic_reviews", path...)}
	}
	return out, nil
}

// agentIDs validates an array of UUID strings: every element is checked, then duplicates are
// removed (keeping the first, ids compared case-insensitively and stored lower-case) and the
// remaining count is capped.
func agentIDs(raw json.RawMessage, path []any) ([]string, []httpx.Issue) {
	if t := jsonType(raw); t != "array" {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected array, received "+t, path...)}
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected array, received "+jsonType(raw), path...)}
	}
	var issues []httpx.Issue
	ids := make([]string, 0, len(elems))
	seen := make(map[string]struct{}, len(elems))
	for i, e := range elems {
		at := append(slices.Clone(path), i)
		var s string
		switch {
		case !decodeString(e, &s):
			issues = append(issues, typeIssue("string", e, at...))
		case !uuidPattern.MatchString(s):
			issues = append(issues, issue(codeInvalidStr, "Invalid uuid", at...))
		default:
			s = strings.ToLower(s)
			if _, dup := seen[s]; !dup {
				seen[s] = struct{}{}
				ids = append(ids, s)
			}
		}
	}
	if issues != nil {
		return nil, issues
	}
	if len(ids) > app.MaxAutoReviewAgents {
		return nil, []httpx.Issue{issue(codeTooBig,
			fmt.Sprintf("Array must contain at most %d element(s)", app.MaxAutoReviewAgents), path...)}
	}
	return ids, nil
}

type featureModelChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// featureModels validates feature_models: an object keyed by feature id (any subset) whose
// values are {provider, model}. Extra fields of a choice are dropped, like Zod does.
func featureModels(raw json.RawMessage, defined definedFunc, path ...any) (json.RawMessage, []httpx.Issue) {
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
		choice, is := featureModel(entries[id], defined, at)
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

// defaultModel validates default_model: null (clears it) or a {provider, model} choice, checked
// like a feature_models entry. Extra fields of the choice are dropped.
func defaultModel(raw json.RawMessage, defined definedFunc, path ...any) (json.RawMessage, []httpx.Issue) {
	if jsonType(raw) == "null" {
		return json.RawMessage("null"), nil
	}
	choice, issues := featureModel(raw, defined, path)
	if issues != nil {
		return nil, issues
	}
	out, err := json.Marshal(choice)
	if err != nil {
		return nil, []httpx.Issue{issue(codeInvalidType, "Invalid default_model", path...)}
	}
	return out, nil
}

// spendCap validates monthly_spend_cap_usd: null (no cap) or a JSON number from 0 to 1,000,000
// with at most two decimals. The decimals are checked on the exact decimal value, so 0.07
// passes and 1.234 does not; the issues are the ones Zod's min, max and refine produce.
func spendCap(raw json.RawMessage, path ...any) (json.RawMessage, []httpx.Issue) {
	t := jsonType(raw)
	if t == "null" {
		return json.RawMessage("null"), nil
	}
	if t != "number" {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected number, received "+t, path...)}
	}
	text := string(bytes.TrimSpace(raw))
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, []httpx.Issue{issue(codeInvalidType, "Expected number, received nan", path...)}
	}

	var issues []httpx.Issue
	if f < spendCapMin {
		issues = append(issues, issue(codeTooSmall, fmt.Sprintf("Number must be greater than or equal to %d", spendCapMin), path...))
	}
	if f > spendCapMax {
		issues = append(issues, issue(codeTooBig, fmt.Sprintf("Number must be less than or equal to %d", spendCapMax), path...))
	}
	if !atMostDecimals(text, spendCapDecimals) {
		issues = append(issues, issue(codeCustom, fmt.Sprintf("At most %d decimals", spendCapDecimals), path...))
	}
	if issues != nil {
		return nil, issues
	}
	if f == 0 {
		return json.RawMessage("0"), nil // no negative zero
	}
	return json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64)), nil
}

// retention validates retention_days: null (keep forever) or one of 30, 60, 90, 180, 365. Like
// a Zod union of literals, every other value (any type) is one invalid_union issue.
func retention(raw json.RawMessage, path ...any) (json.RawMessage, []httpx.Issue) {
	invalid := []httpx.Issue{issue(codeInvalidUnion, "Invalid input", path...)}
	switch jsonType(raw) {
	case "null":
		return json.RawMessage("null"), nil
	case "number":
		f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) {
			return nil, invalid
		}
		for _, d := range retentionDays {
			if f == float64(d) {
				return json.RawMessage(strconv.Itoa(d)), nil
			}
		}
	}
	return nil, invalid
}

// atMostDecimals reports whether the decimal number text has no more than n decimals, exactly
// (no float rounding): value × 10^n must be an integer.
func atMostDecimals(text string, n int) bool {
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return false
	}
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil))
	return r.Mul(r, scale).IsInt()
}

func featureModel(raw json.RawMessage, defined definedFunc, path []any) (featureModelChoice, []httpx.Issue) {
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
		case slices.Contains(providerIDs, s):
			choice.Provider = s
		case !isCustomID(s):
			issues = append(issues, enumIssue(providerIDs, s, at...))
		case !defined.defined(s):
			// FR7: a well-formed custom id the workspace does not define. A malformed one
			// (custom:Bad) is the enum issue above, as for any invalid value.
			issues = append(issues, issue(codeCustom, "Custom provider "+s+" is not configured", at...))
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

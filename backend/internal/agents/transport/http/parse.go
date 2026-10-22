package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dev-digest-go/backend/internal/agents/domain"
	"dev-digest-go/backend/internal/platform/httpx"
)

// maxOrder bounds a link position; any larger index is clamped to the list length anyway.
const maxOrder = 1 << 30

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// object is a decoded JSON object whose values are still raw.
type object map[string]json.RawMessage

// decodeObject parses body as a JSON object. An empty body is a missing object; any other
// document is reported with its Zod type name.
func decodeObject(body []byte) (object, []httpx.Issue) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, []httpx.Issue{httpx.RequiredIssue()}
	}
	var obj object
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &obj) != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("object", jsonType(trimmed))}
	}
	return obj, nil
}

// parseCreate validates a POST /agents body (CreateInput of the contract). Unknown keys are
// dropped, as Zod does.
func parseCreate(body []byte) (domain.CreateInput, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return domain.CreateInput{}, issues
	}
	var in domain.CreateInput
	var is []httpx.Issue
	nonEmpty := httpx.StringRule{Min: 1}

	in.Name, _, is = obj.str("name", true, nonEmpty)
	issues = append(issues, is...)
	in.Description, is = obj.optStr("description", httpx.StringRule{})
	issues = append(issues, is...)
	in.Provider, _, is = obj.enum("provider", true, domain.Providers())
	issues = append(issues, is...)
	in.Model, _, is = obj.str("model", true, nonEmpty)
	issues = append(issues, is...)
	in.SystemPrompt, _, is = obj.str("system_prompt", true, nonEmpty)
	issues = append(issues, is...)
	in.OutputSchema = obj["output_schema"]
	in.Strategy, is = obj.optEnum("strategy", domain.Strategies())
	issues = append(issues, is...)
	in.CIFailOn, is = obj.optEnum("ci_fail_on", domain.CIFailOns())
	issues = append(issues, is...)
	in.RepoIntel, is = obj.optBool("repo_intel")
	issues = append(issues, is...)
	in.Enabled, is = obj.optBool("enabled")
	issues = append(issues, is...)

	if issues != nil {
		return domain.CreateInput{}, issues
	}
	return in, nil
}

// parsePatch validates a PUT /agents/:id body: every CreateInput field optional, null invalid
// except for output_schema.
func parsePatch(body []byte) (domain.Patch, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return domain.Patch{}, issues
	}
	var p domain.Patch
	var is []httpx.Issue
	nonEmpty := httpx.StringRule{Min: 1}

	p.Name, is = obj.optStr("name", nonEmpty)
	issues = append(issues, is...)
	p.Description, is = obj.optStr("description", httpx.StringRule{})
	issues = append(issues, is...)
	p.Provider, is = obj.optEnum("provider", domain.Providers())
	issues = append(issues, is...)
	p.Model, is = obj.optStr("model", nonEmpty)
	issues = append(issues, is...)
	p.SystemPrompt, is = obj.optStr("system_prompt", nonEmpty)
	issues = append(issues, is...)
	if raw, ok := obj["output_schema"]; ok {
		p.SetOutputSchema, p.OutputSchema = true, raw
	}
	p.Strategy, is = obj.optEnum("strategy", domain.Strategies())
	issues = append(issues, is...)
	p.CIFailOn, is = obj.optEnum("ci_fail_on", domain.CIFailOns())
	issues = append(issues, is...)
	p.RepoIntel, is = obj.optBool("repo_intel")
	issues = append(issues, is...)
	p.Enabled, is = obj.optBool("enabled")
	issues = append(issues, is...)

	if issues != nil {
		return domain.Patch{}, issues
	}
	return p, nil
}

// skillsRequest is a validated POST /agents/:id/skills body. SkillIDs non-nil selects set mode
// (it may be empty); otherwise SkillID is linked at Order (nil = append).
type skillsRequest struct {
	SkillIDs []string
	SkillID  string
	Order    *int
}

const (
	msgNoSkills        = "Provide skill_ids (set/reorder) or skill_id (link one)"
	msgDuplicateSkills = "skill_ids must not contain duplicates"
)

// parseSkills validates {skill_ids?, skill_id?, order?} and the two refinements. As in Zod, the
// refinements only run when no field has the wrong type.
func parseSkills(body []byte) (skillsRequest, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return skillsRequest{}, issues
	}
	var req skillsRequest
	typeError := false

	idsRaw, idsPresent := obj["skill_ids"]
	if idsPresent {
		ids, is, aborted := uuidList(idsRaw, "skill_ids")
		issues, typeError = append(issues, is...), typeError || aborted
		req.SkillIDs = ids
	}
	idRaw, idPresent := obj["skill_id"]
	if idPresent {
		id, is, aborted := uuidValue(idRaw, "skill_id")
		issues, typeError = append(issues, is...), typeError || aborted
		req.SkillID = id
	}
	if orderRaw, ok := obj["order"]; ok {
		order, is, aborted := orderValue(orderRaw, "order")
		issues, typeError = append(issues, is...), typeError || aborted
		req.Order = order
	}

	if !typeError {
		if !idsPresent && !idPresent {
			issues = append(issues, httpx.RefineIssue(msgNoSkills))
		}
		if idsPresent && len(domain.Dedupe(req.SkillIDs)) != len(req.SkillIDs) {
			issues = append(issues, httpx.RefineIssue(msgDuplicateSkills))
		}
	}
	if issues != nil {
		return skillsRequest{}, issues
	}
	if idsPresent && req.SkillIDs == nil {
		req.SkillIDs = []string{}
	}
	return req, nil
}

// uuidList validates an array of UUID strings. aborted is true for a type error, which stops
// Zod from running refinements.
func uuidList(raw json.RawMessage, key string) (ids []string, issues []httpx.Issue, aborted bool) {
	if t := jsonType(raw); t != "array" {
		return nil, []httpx.Issue{httpx.TypeIssue("array", t, key)}, true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("array", "string", key)}, true
	}
	ids = make([]string, 0, len(items))
	for i, item := range items {
		id, is, ab := uuidValue(item, key, i)
		issues, aborted = append(issues, is...), aborted || ab
		ids = append(ids, id)
	}
	return ids, issues, aborted
}

func uuidValue(raw json.RawMessage, path ...any) (id string, issues []httpx.Issue, aborted bool) {
	if t := jsonType(raw); t != "string" {
		return "", []httpx.Issue{httpx.TypeIssue("string", t, path...)}, true
	}
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", []httpx.Issue{httpx.TypeIssue("string", "number", path...)}, true
	}
	if !uuidPattern.MatchString(id) {
		issues = append(issues, httpx.Issue{Code: httpx.CodeInvalidStr, Path: path, Message: "Invalid uuid"})
		return id, issues, false
	}
	// UUIDs are case-insensitive (FR203) and Postgres returns them lower-case, so fold here;
	// otherwise an existing skill sent upper-case would be reported as unknown.
	return strings.ToLower(id), issues, false
}

// orderValue validates z.number().int().min(0). A non-integer is a (non-aborting) invalid_type
// issue, like Zod's.
func orderValue(raw json.RawMessage, key string) (order *int, issues []httpx.Issue, aborted bool) {
	if t := jsonType(raw); t != "number" {
		return nil, []httpx.Issue{httpx.TypeIssue("number", t, key)}, true
	}
	f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	switch {
	case err != nil && !math.IsInf(f, 0):
		return nil, []httpx.Issue{httpx.TypeIssue("number", "nan", key)}, true
	case math.IsInf(f, 0) || f != math.Trunc(f):
		return nil, []httpx.Issue{httpx.TypeIssue("integer", "float", key)}, false
	case f < 0:
		return nil, []httpx.Issue{{Code: httpx.CodeTooSmall, Path: []any{key}, Message: "Number must be greater than or equal to 0"}}, false
	}
	n := int(math.Min(f, maxOrder))
	return &n, nil, false
}

// str reads a string field checked with rule. present is false for an absent optional field.
func (o object) str(key string, required bool, rule httpx.StringRule) (value string, present bool, issues []httpx.Issue) {
	raw, ok := o[key]
	if !ok {
		if required {
			return "", false, []httpx.Issue{httpx.RequiredIssue(key)}
		}
		return "", false, nil
	}
	s, ok := decodeString(raw)
	if !ok {
		return "", true, []httpx.Issue{httpx.TypeIssue("string", jsonType(raw), key)}
	}
	value, issues = rule.Check(s, key)
	return value, true, issues
}

func (o object) optStr(key string, rule httpx.StringRule) (*string, []httpx.Issue) {
	v, present, issues := o.str(key, false, rule)
	if !present || issues != nil {
		return nil, issues
	}
	return &v, nil
}

// enum reads a string field that must be one of allowed.
func (o object) enum(key string, required bool, allowed []string) (value string, present bool, issues []httpx.Issue) {
	raw, ok := o[key]
	if !ok {
		if required {
			return "", false, []httpx.Issue{httpx.RequiredIssue(key)}
		}
		return "", false, nil
	}
	s, ok := decodeString(raw)
	switch {
	case !ok:
		return "", true, []httpx.Issue{httpx.TypeIssue("string", jsonType(raw), key)}
	case !slices.Contains(allowed, s):
		return "", true, []httpx.Issue{httpx.EnumIssue(allowed, s, key)}
	}
	return s, true, nil
}

func (o object) optEnum(key string, allowed []string) (*string, []httpx.Issue) {
	v, present, issues := o.enum(key, false, allowed)
	if !present || issues != nil {
		return nil, issues
	}
	return &v, nil
}

func (o object) optBool(key string) (*bool, []httpx.Issue) {
	raw, ok := o[key]
	if !ok {
		return nil, nil
	}
	if t := jsonType(raw); t != "boolean" {
		return nil, []httpx.Issue{httpx.TypeIssue("boolean", t, key)}
	}
	b := bytes.TrimSpace(raw)[0] == 't'
	return &b, nil
}

func decodeString(raw json.RawMessage) (string, bool) {
	if jsonType(raw) != "string" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
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

package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/skills/app"
	"dev-digest-go/backend/internal/skills/domain"
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Field rules of the contract (frontend/src/vendor/shared/contracts/knowledge.ts).
var (
	nameRule = httpx.StringRule{
		Min: 1, Max: domain.SkillNameMax, Pattern: namePattern,
		PatternMessage: "Use a kebab-case slug: lowercase letters, digits and single dashes",
	}
	descriptionRule = httpx.StringRule{Max: domain.SkillDescriptionMax}
	bodyRule        = httpx.StringRule{Min: 1, Max: domain.SkillBodyMax}
	sourceRefRule   = httpx.StringRule{Max: 500}
	filenameRule    = httpx.StringRule{Min: 1, Max: 255}
	urlRule         = httpx.StringRule{Min: 1, Max: 2000}
	communityIDRule = httpx.StringRule{Min: 1, Max: 100}
	nonEmptyRule    = httpx.StringRule{Min: 1}
)

// Bounds of the numeric fields.
const (
	maxConventionIDs = 100
	maxAgentIDs      = 50
)

var importKinds = []string{app.KindFile, app.KindURL, app.KindCommunity}

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

// parseCreate validates a POST /skills body (CreateSkillInput). Unknown keys are dropped.
func parseCreate(body []byte) (app.CreateInput, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return app.CreateInput{}, issues
	}
	var in app.CreateInput
	var is []httpx.Issue

	in.Name, _, is = obj.str("name", true, nameRule)
	issues = append(issues, is...)
	if d, is := obj.optStr("description", descriptionRule); is != nil {
		issues = append(issues, is...)
	} else if d != nil {
		in.Description = *d
	}
	typ, _, is := obj.enum("type", true, domain.SkillTypeNames())
	issues = append(issues, is...)
	in.Type = domain.SkillType(typ)
	in.Body, _, is = obj.str("body", true, bodyRule)
	issues = append(issues, is...)
	in.Enabled, is = obj.optBool("enabled")
	issues = append(issues, is...)
	source, is := obj.optEnum("source", domain.CreatableSourceNames())
	issues = append(issues, is...)
	if source != nil {
		in.Source = domain.Source(*source)
	}
	in.SourceRef, is = obj.optStr("source_ref", sourceRefRule)
	issues = append(issues, is...)

	if issues != nil {
		return app.CreateInput{}, issues
	}
	return in, nil
}

// parsePatch validates a PUT /skills/:id body (UpdateSkillInput): every field optional.
func parsePatch(body []byte) (domain.Patch, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return domain.Patch{}, issues
	}
	var p domain.Patch
	var is []httpx.Issue

	p.Name, is = obj.optStr("name", nameRule)
	issues = append(issues, is...)
	p.Description, is = obj.optStr("description", descriptionRule)
	issues = append(issues, is...)
	typ, is := obj.optEnum("type", domain.SkillTypeNames())
	issues = append(issues, is...)
	if typ != nil {
		t := domain.SkillType(*typ)
		p.Type = &t
	}
	p.Body, is = obj.optStr("body", bodyRule)
	issues = append(issues, is...)
	p.Enabled, is = obj.optBool("enabled")
	issues = append(issues, is...)
	if raw, ok := obj["base_version"]; ok {
		v, is := integer(raw, numberSpec{min: 0, hasMin: true, minExclusive: true}, "base_version")
		issues = append(issues, is...)
		if is == nil {
			n := int(v)
			p.BaseVersion = &n
		}
	}

	if issues != nil {
		return domain.Patch{}, issues
	}
	return p, nil
}

// parseImport validates a POST /skills/import/preview body (SkillImportRequest, a union
// discriminated by kind).
func parseImport(body []byte) (app.ImportRequest, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return app.ImportRequest{}, issues
	}
	var kind string
	if raw, ok := obj["kind"]; ok {
		kind, _ = decodeString(raw)
	}
	if !slices.Contains(importKinds, kind) {
		quoted := make([]string, len(importKinds))
		for i, k := range importKinds {
			quoted[i] = "'" + k + "'"
		}
		return app.ImportRequest{}, []httpx.Issue{{
			Code: "invalid_union_discriminator", Path: []any{"kind"},
			Message: "Invalid discriminator value. Expected " + strings.Join(quoted, " | "),
		}}
	}

	req := app.ImportRequest{Kind: kind}
	var is []httpx.Issue
	switch kind {
	case app.KindFile:
		req.Filename, _, is = obj.str("filename", true, filenameRule)
		issues = append(issues, is...)
		req.ContentBase64, _, is = obj.str("content_base64", true, nonEmptyRule)
		issues = append(issues, is...)
	case app.KindURL:
		req.URL, _, is = obj.str("url", true, urlRule)
		issues = append(issues, is...)
	case app.KindCommunity:
		req.ID, _, is = obj.str("id", true, communityIDRule)
		issues = append(issues, is...)
	}
	if issues != nil {
		return app.ImportRequest{}, issues
	}
	return req, nil
}

// parseMerge validates a POST /repos/:id/conventions/skill body (CreateConventionSkillInput)
// and applies its defaults: type convention, enabled true, no agents.
func parseMerge(body []byte) (app.MergeInput, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return app.MergeInput{}, issues
	}
	in := app.MergeInput{Type: domain.TypeConvention, Enabled: true}
	var is []httpx.Issue

	if raw, ok := obj["convention_ids"]; !ok {
		issues = append(issues, httpx.RequiredIssue("convention_ids"))
	} else {
		in.ConventionIDs, is = uuidList(raw, "convention_ids", 1, maxConventionIDs)
		issues = append(issues, is...)
	}
	in.Name, _, is = obj.str("name", true, nameRule)
	issues = append(issues, is...)
	if d, is := obj.optStr("description", descriptionRule); is != nil {
		issues = append(issues, is...)
	} else if d != nil {
		in.Description = *d
	}
	typ, is := obj.optEnum("type", domain.SkillTypeNames())
	issues = append(issues, is...)
	if typ != nil {
		in.Type = domain.SkillType(*typ)
	}
	in.Body, _, is = obj.str("body", true, bodyRule)
	issues = append(issues, is...)
	enabled, is := obj.optBool("enabled")
	issues = append(issues, is...)
	if enabled != nil {
		in.Enabled = *enabled
	}
	if raw, ok := obj["agent_ids"]; ok {
		in.AgentIDs, is = uuidList(raw, "agent_ids", 0, maxAgentIDs)
		issues = append(issues, is...)
	}

	if issues != nil {
		return app.MergeInput{}, issues
	}
	return in, nil
}

// uuidList validates an array of UUID strings with a length range. As in Zod the length checks
// come before the items.
func uuidList(raw json.RawMessage, key string, minLen, maxLen int) ([]string, []httpx.Issue) {
	if t := jsonType(raw); t != "array" {
		return nil, []httpx.Issue{httpx.TypeIssue("array", t, key)}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("array", "object", key)}
	}
	var issues []httpx.Issue
	if len(items) < minLen {
		issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{key},
			Message: fmt.Sprintf("Array must contain at least %d element(s)", minLen)})
	}
	if len(items) > maxLen {
		issues = append(issues, httpx.Issue{Code: httpx.CodeTooBig, Path: []any{key},
			Message: fmt.Sprintf("Array must contain at most %d element(s)", maxLen)})
	}
	ids := make([]string, 0, len(items))
	for i, item := range items {
		s, ok := decodeString(item)
		switch {
		case !ok:
			issues = append(issues, httpx.TypeIssue("string", jsonType(item), key, i))
		case !uuidPattern.MatchString(s):
			issues = append(issues, httpx.Issue{Code: httpx.CodeInvalidStr, Path: []any{key, i}, Message: "Invalid uuid"})
		default:
			ids = append(ids, s)
		}
	}
	if issues != nil {
		return nil, issues
	}
	return ids, nil
}

// numberSpec holds the checks of a Zod integer: z.number().int().min()/gt().max().
type numberSpec struct {
	min          float64
	hasMin       bool
	minExclusive bool // the value must be greater than min, not equal to it
	max          float64
	hasMax       bool
}

// integer validates a JSON number as an integer within spec; every failing check is reported.
func integer(raw json.RawMessage, spec numberSpec, key string) (float64, []httpx.Issue) {
	if t := jsonType(raw); t != "number" {
		return 0, []httpx.Issue{httpx.TypeIssue("number", t, key)}
	}
	f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	if err != nil && !math.IsInf(f, 0) {
		return 0, []httpx.Issue{httpx.TypeIssue("number", "nan", key)}
	}
	if issues := checkNumber(f, spec, key); issues != nil {
		return 0, issues
	}
	return f, nil
}

func checkNumber(f float64, spec numberSpec, key string) []httpx.Issue {
	var issues []httpx.Issue
	if math.IsInf(f, 0) || f != math.Trunc(f) {
		issues = append(issues, httpx.TypeIssue("integer", "float", key))
	}
	if spec.hasMin {
		switch {
		case spec.minExclusive && f <= spec.min:
			issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{key},
				Message: fmt.Sprintf("Number must be greater than %v", spec.min)})
		case !spec.minExclusive && f < spec.min:
			issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{key},
				Message: fmt.Sprintf("Number must be greater than or equal to %v", spec.min)})
		}
	}
	if spec.hasMax && f > spec.max {
		issues = append(issues, httpx.Issue{Code: httpx.CodeTooBig, Path: []any{key},
			Message: fmt.Sprintf("Number must be less than or equal to %v", spec.max)})
	}
	return issues
}

// parseDays reads the days query parameter the way z.coerce.number().int().min(1).max(365)
// .default(30) does: absent is 30, the text is converted with JavaScript's Number().
func parseDays(c *echo.Context) (int, []httpx.Issue) {
	values, ok := c.QueryParams()["days"]
	if !ok || len(values) == 0 {
		return domain.StatsDefaultDays, nil
	}
	text := strings.TrimSpace(values[0])
	f := 0.0 // Number("") is 0
	if text != "" {
		var err error
		if f, err = strconv.ParseFloat(text, 64); err != nil && !math.IsInf(f, 0) {
			return 0, []httpx.Issue{httpx.TypeIssue("number", "nan", "days")}
		}
	}
	spec := numberSpec{min: 1, hasMin: true, max: domain.StatsMaxDays, hasMax: true}
	if issues := checkNumber(f, spec, "days"); issues != nil {
		return 0, issues
	}
	return int(f), nil
}

// parseCommunityFilter reads q, tag and lang of GET /skills/community.
func parseCommunityFilter(c *echo.Context) (domain.CatalogFilter, []httpx.Issue) {
	var f domain.CatalogFilter
	var issues, is []httpx.Issue
	f.Q, is = httpx.QueryString(c, "q", httpx.StringRule{Max: 200})
	issues = append(issues, is...)
	f.Tag, is = httpx.QueryString(c, "tag", httpx.StringRule{Max: 50})
	issues = append(issues, is...)
	f.Lang, is = httpx.QueryString(c, "lang", httpx.StringRule{Max: 50})
	issues = append(issues, is...)
	if issues != nil {
		return domain.CatalogFilter{}, issues
	}
	return f, nil
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

// optStr reads an optional string field; nil when absent or invalid.
func (o object) optStr(key string, rule httpx.StringRule) (*string, []httpx.Issue) {
	v, present, issues := o.str(key, false, rule)
	if !present || issues != nil {
		return nil, issues
	}
	return &v, nil
}

// enum reads a string field that must be one of allowed. A value that is not a string is an
// invalid_type issue naming the allowed values, as Zod's enum reports it.
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
		quoted := make([]string, len(allowed))
		for i, a := range allowed {
			quoted[i] = "'" + a + "'"
		}
		return "", true, []httpx.Issue{httpx.TypeIssue(strings.Join(quoted, " | "), jsonType(raw), key)}
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

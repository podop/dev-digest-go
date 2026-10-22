package conventionshttp

import (
	"bytes"
	"encoding/json"
	"slices"

	"dev-digest-go/backend/internal/conventions/app"
	"dev-digest-go/backend/internal/conventions/domain"
	"dev-digest-go/backend/internal/platform/httpx"
)

// ruleMaxChars is CONVENTION_RULE_MAX of the contract (UTF-16 code units).
const ruleMaxChars = 500

// nothingToUpdate is the message of the refine of UpdateConventionInput.
const nothingToUpdate = "Nothing to update"

var statusNames = func() []string {
	var out []string
	for _, s := range domain.Statuses() {
		out = append(out, string(s))
	}
	return out
}()

// parseUpdate validates a PATCH /conventions/:id body like the Zod schema UpdateConventionInput:
// {status?, rule? (trimmed, 1..500), category?} with at least one field. Unknown keys are
// ignored. Issues come in the schema's field order; the refine runs only when every field is
// valid.
func parseUpdate(body []byte) (app.Patch, []httpx.Issue) {
	var obj map[string]json.RawMessage
	if issues := decodeObject(body, &obj); issues != nil {
		return app.Patch{}, issues
	}
	var (
		patch  app.Patch
		issues []httpx.Issue
	)
	if raw, ok := obj["status"]; ok {
		var s string
		switch {
		case !decodeString(raw, &s):
			issues = append(issues, typeIssue("string", raw, "status"))
		case !slices.Contains(statusNames, s):
			issues = append(issues, httpx.EnumIssue(statusNames, s, "status"))
		default:
			st := domain.Status(s)
			patch.Status = &st
		}
	}
	if raw, ok := obj["rule"]; ok {
		var s string
		if !decodeString(raw, &s) {
			issues = append(issues, typeIssue("string", raw, "rule"))
		} else {
			trimmed, is := httpx.StringRule{Trim: true, Min: 1, Max: ruleMaxChars}.Check(s, "rule")
			issues = append(issues, is...)
			if is == nil {
				patch.Rule = &trimmed
			}
		}
	}
	if raw, ok := obj["category"]; ok {
		var s string
		switch {
		case !decodeString(raw, &s):
			issues = append(issues, typeIssue("string", raw, "category"))
		case !slices.Contains(domain.Categories, s):
			issues = append(issues, httpx.EnumIssue(domain.Categories, s, "category"))
		default:
			patch.Category = &s
		}
	}
	if issues != nil {
		return app.Patch{}, issues
	}
	if patch.Status == nil && patch.Rule == nil && patch.Category == nil {
		return app.Patch{}, []httpx.Issue{httpx.RefineIssue(nothingToUpdate)}
	}
	return patch, nil
}

// decodeObject parses body as a JSON object, with Zod's issue for anything else.
func decodeObject(body []byte, dst *map[string]json.RawMessage) []httpx.Issue {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []httpx.Issue{httpx.RequiredIssue()}
	}
	if err := json.Unmarshal(trimmed, dst); err != nil || *dst == nil {
		return []httpx.Issue{httpx.TypeIssue("object", jsonType(trimmed))}
	}
	return nil
}

func typeIssue(want string, raw json.RawMessage, path ...any) httpx.Issue {
	return httpx.TypeIssue(want, jsonType(raw), path...)
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

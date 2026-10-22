package httpapi

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/projectcontext/app"
)

// maxQueryPath is the longest ?path= (z.string().max(4096), in UTF-16 code units).
const maxQueryPath = 4096

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// attachBody is a validated PUT /agents|skills/:id/context body.
type attachBody struct {
	RepoID string
	Paths  []string
}

// saveBody is a validated PUT /repos/:id/context/files body.
type saveBody struct {
	Content     string
	BaseVersion int64
}

// renameBody is a validated POST /repos/:id/context/files/rename body.
type renameBody struct {
	Path, NewPath string
	BaseVersion   int64
}

// parseAttachBody validates {repo_id: uuid, paths: string[]}; paths stays loose because the
// use case owns invalid_path, duplicate_path and too_many_paths.
func parseAttachBody(body []byte) (attachBody, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return attachBody{}, issues
	}
	var out attachBody
	if v, is := stringField(obj, "repo_id", true); is != nil {
		issues = append(issues, is...)
	} else if !uuidPattern.MatchString(v) {
		issues = append(issues, httpx.Issue{Code: httpx.CodeInvalidStr, Path: []any{"repo_id"}, Message: "Invalid uuid"})
	} else {
		out.RepoID = v
	}

	raw, ok := obj["paths"]
	switch {
	case !ok:
		issues = append(issues, httpx.RequiredIssue("paths"))
	case jsonType(raw) != "array":
		issues = append(issues, httpx.TypeIssue("array", jsonType(raw), "paths"))
	default:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			issues = append(issues, httpx.TypeIssue("array", jsonType(raw), "paths"))
			break
		}
		out.Paths = make([]string, 0, len(items))
		for i, item := range items {
			var s string
			if jsonType(item) != "string" || json.Unmarshal(item, &s) != nil {
				issues = append(issues, httpx.TypeIssue("string", jsonType(item), "paths", i))
				continue
			}
			out.Paths = append(out.Paths, s)
		}
	}
	if issues != nil {
		return attachBody{}, issues
	}
	return out, nil
}

// parseCreateBody validates the optional {path?, content?, on_conflict?} body; an absent or
// null body is {}.
func parseCreateBody(body []byte) (app.CreateInput, []httpx.Issue) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return app.CreateInput{}, nil
	}
	obj, issues := decodeObject(trimmed)
	if issues != nil {
		return app.CreateInput{}, issues
	}
	var in app.CreateInput
	if v, is := optionalString(obj, "path"); is != nil {
		issues = append(issues, is...)
	} else {
		in.Path = v
	}
	if v, is := optionalString(obj, "content"); is != nil {
		issues = append(issues, is...)
	} else {
		in.Content = v
	}
	if raw, ok := obj["on_conflict"]; ok {
		allowed := []string{app.OnConflictFail, app.OnConflictSuffix}
		var s string
		switch {
		case jsonType(raw) != "string" || json.Unmarshal(raw, &s) != nil:
			issues = append(issues, httpx.TypeIssue("'fail' | 'suffix'", jsonType(raw), "on_conflict"))
		case !slices.Contains(allowed, s):
			issues = append(issues, httpx.EnumIssue(allowed, s, "on_conflict"))
		default:
			in.OnConflict = &s
		}
	}
	if issues != nil {
		return app.CreateInput{}, issues
	}
	return in, nil
}

// parseSaveBody validates {content: string, base_version: int}.
func parseSaveBody(body []byte) (saveBody, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return saveBody{}, issues
	}
	var out saveBody
	var is []httpx.Issue
	out.Content, is = stringField(obj, "content", true)
	issues = append(issues, is...)
	out.BaseVersion, is = intField(obj, "base_version")
	issues = append(issues, is...)
	if issues != nil {
		return saveBody{}, issues
	}
	return out, nil
}

// parseRenameBody validates {path: string, new_path: string, base_version: int}.
func parseRenameBody(body []byte) (renameBody, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return renameBody{}, issues
	}
	var out renameBody
	var is []httpx.Issue
	out.Path, is = stringField(obj, "path", true)
	issues = append(issues, is...)
	out.NewPath, is = stringField(obj, "new_path", true)
	issues = append(issues, is...)
	out.BaseVersion, is = intField(obj, "base_version")
	issues = append(issues, is...)
	if issues != nil {
		return renameBody{}, issues
	}
	return out, nil
}

// decodeObject parses body as a JSON object.
func decodeObject(body []byte) (map[string]json.RawMessage, []httpx.Issue) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, []httpx.Issue{httpx.RequiredIssue()}
	}
	var obj map[string]json.RawMessage
	if t := jsonType(trimmed); t != "object" || json.Unmarshal(trimmed, &obj) != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("object", jsonType(trimmed))}
	}
	return obj, nil
}

// stringField reads a string property; required adds the "Required" issue when it is missing.
func stringField(obj map[string]json.RawMessage, key string, required bool) (string, []httpx.Issue) {
	raw, ok := obj[key]
	if !ok {
		if required {
			return "", []httpx.Issue{httpx.RequiredIssue(key)}
		}
		return "", nil
	}
	var s string
	if jsonType(raw) != "string" || json.Unmarshal(raw, &s) != nil {
		return "", []httpx.Issue{httpx.TypeIssue("string", jsonType(raw), key)}
	}
	return s, nil
}

// optionalString reads an optional string property: nil when absent.
func optionalString(obj map[string]json.RawMessage, key string) (*string, []httpx.Issue) {
	if _, ok := obj[key]; !ok {
		return nil, nil
	}
	s, issues := stringField(obj, key, false)
	if issues != nil {
		return nil, issues
	}
	return &s, nil
}

// intField reads a required integer property (z.number().int()). A value beyond ±2^40 is
// clamped there: it fits no database version either way.
func intField(obj map[string]json.RawMessage, key string) (int64, []httpx.Issue) {
	raw, ok := obj[key]
	if !ok {
		return 0, []httpx.Issue{httpx.RequiredIssue(key)}
	}
	var f float64
	if jsonType(raw) != "number" {
		return 0, []httpx.Issue{httpx.TypeIssue("number", jsonType(raw), key)}
	}
	if err := json.Unmarshal(raw, &f); err != nil || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, []httpx.Issue{httpx.TypeIssue("integer", "float", key)}
	}
	const limit = 1 << 40
	return int64(math.Max(-limit, math.Min(limit, f))), nil
}

// jsonType names a JSON value the way Zod's "received" does.
func jsonType(raw []byte) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return "undefined"
	}
	switch t[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

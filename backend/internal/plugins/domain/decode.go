package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxIgnored is the most ignored parts a preview lists; the rest are summed in a last entry.
const MaxIgnored = 200

func validUTF8(b []byte) bool { return utf8.Valid(b) }

// capIgnored keeps the first MaxIgnored entries and sums the rest in one last entry.
func capIgnored(in []Ignored) []Ignored {
	if in == nil {
		return []Ignored{}
	}
	if len(in) <= MaxIgnored {
		return in
	}
	out := slices.Clone(in[:MaxIgnored])
	return append(out, Ignored{PathOrSection: "...", Reason: fmt.Sprintf("%d more ignored parts", len(in)-MaxIgnored)})
}

// jsonKeys lists the JSON object keys of struct type T.
func jsonKeys[T any]() map[string]struct{} {
	t := reflect.TypeFor[T]()
	keys := make(map[string]struct{}, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = struct{}{}
		}
	}
	return keys
}

var (
	topKeys      = jsonKeys[Bundle]()
	manifestKeys = jsonKeys[manifestWire]()
	agentKeys    = jsonKeys[BundleAgent]()
	skillKeys    = jsonKeys[BundleSkill]()
	caseKeys     = jsonKeys[BundleCase]()
	learningKeys = jsonKeys[BundleLearning]()
)

// manifestWire is the part of the manifest an import reads; counts are recomputed.
type manifestWire struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Format      string          `json:"format"`
	Kind        string          `json:"kind"`
	ExportedAt  string          `json:"exported_at"`
	Description *string         `json:"description"`
	Counts      json.RawMessage `json:"counts"` // never read: counts are recomputed; raw so it is not decoded
}

// isNull reports an absent or JSON null value.
func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

// nullToNil maps the JSON literal null to nil, so that nil means null everywhere in the bundle.
func nullToNil(raw json.RawMessage) json.RawMessage {
	if isNull(raw) {
		return nil
	}
	return raw
}

// typeError turns a JSON decoding failure into the first-broken-rule message.
func typeError(path string, err error) *InvalidError {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		field := path
		if ute.Field != "" {
			// Field is the dotted path below the decoded value, without array indexes.
			field = path + "." + ute.Field
		}
		return invalid("%s must be %s (found %s)", field, jsonTypeName(ute.Type), strings.SplitN(ute.Value, " ", 2)[0])
	}
	return invalid("%s is not valid JSON", path)
}

func jsonTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Float32, reflect.Float64, reflect.Int, reflect.Int64, reflect.Int32:
		return "a number"
	case reflect.Pointer:
		return jsonTypeName(t.Elem())
	default:
		return "a different type"
	}
}

// ignores collects ignored parts in a stable order.
type ignores []Ignored

// unknown lists the keys of obj that are not in known, sorted, under prefix.
func (l *ignores) unknown(prefix string, obj map[string]json.RawMessage, known map[string]struct{}) {
	var extra []string
	for k := range obj {
		if _, ok := known[k]; !ok {
			extra = append(extra, k)
		}
	}
	slices.Sort(extra)
	for _, k := range extra {
		if len(*l) > MaxIgnored*2 {
			return // enough to fill the capped list; do not grow without bound
		}
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		*l = append(*l, Ignored{PathOrSection: p, Reason: ReasonUnknownKey})
	}
}

// decodeBundle reads the JSON text of a bundle. Missing sections are empty lists, absent item
// fields take their defaults (A3), unknown keys are ignored and listed (A7). A wrong type or a
// list over its limit is the first broken rule. checkBudget runs first, so no list or object is
// built that is over its limit (M1).
func decodeBundle(text []byte) (Bundle, []Ignored, error) {
	if err := checkBudget(text); err != nil {
		return Bundle{}, nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(text, &top); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return Bundle{}, nil, invalid("%s is not valid JSON (%s)", BundleFile, se.Error())
		}
		return Bundle{}, nil, invalid("%s must be a JSON object", BundleFile)
	}
	if top == nil {
		return Bundle{}, nil, invalid("%s must be a JSON object", BundleFile)
	}
	var ign ignores
	ign.unknown("", top, topKeys)

	var b Bundle
	mraw := top["manifest"]
	if isNull(mraw) {
		return Bundle{}, nil, invalid("manifest is required")
	}
	var mobj map[string]json.RawMessage
	if err := json.Unmarshal(mraw, &mobj); err != nil || mobj == nil {
		return Bundle{}, nil, invalid("manifest must be an object")
	}
	ign.unknown("manifest", mobj, manifestKeys)
	var mw manifestWire
	if err := json.Unmarshal(mraw, &mw); err != nil {
		return Bundle{}, nil, typeError("manifest", err)
	}
	b.Manifest = Manifest{
		Name: mw.Name, Version: mw.Version, Format: mw.Format, Kind: mw.Kind,
		ExportedAt: mw.ExportedAt, Description: mw.Description,
	}

	var err error
	if b.Agents, err = decodeList("agents", top["agents"], MaxAgents, agentKeys, &ign, agentDefaults); err != nil {
		return Bundle{}, nil, err
	}
	if b.Skills, err = decodeList("skills", top["skills"], MaxSkills, skillKeys, &ign, skillDefaults); err != nil {
		return Bundle{}, nil, err
	}
	if b.EvalCases, err = decodeList("eval_cases", top["eval_cases"], MaxEvalCases, caseKeys, &ign, caseDefaults); err != nil {
		return Bundle{}, nil, err
	}
	if b.Learnings, err = decodeList("learnings", top["learnings"], MaxLearnings, learningKeys, &ign, learningDefaults); err != nil {
		return Bundle{}, nil, err
	}
	if b.Conventions, err = rawList("conventions", top["conventions"], MaxConventions); err != nil {
		return Bundle{}, nil, err
	}
	if b.ContextFiles, err = rawList("context_files", top["context_files"], MaxContextFiles); err != nil {
		return Bundle{}, nil, err
	}
	if len(b.ContextFiles) == 0 {
		b.ContextFiles = nil // omitted on export, like the other workspace-only sections
	}
	if !isNull(top["settings"]) {
		if err := json.Unmarshal(top["settings"], &b.Settings); err != nil {
			return Bundle{}, nil, invalid("settings must be an object")
		}
	}

	b.Manifest.Counts = CountsOf(b)
	return b, []Ignored(ign), nil
}

// rawList reads a list whose items are never installed.
func rawList(section string, raw json.RawMessage, limit int) ([]json.RawMessage, error) {
	if isNull(raw) {
		return []json.RawMessage{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, invalid("%s must be an array", section)
	}
	if len(items) > limit {
		return nil, invalid("%s has %d entries; the limit is %d", section, len(items), limit)
	}
	return items, nil
}

// decodeList reads a list of objects of type T. present lists the non-null keys of each item,
// for defaults.
func decodeList[T any](section string, raw json.RawMessage, limit int, known map[string]struct{}, ign *ignores, defaults func(*T, map[string]bool)) ([]T, error) {
	if isNull(raw) {
		return []T{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, invalid("%s must be an array", section)
	}
	if len(items) > limit {
		return nil, invalid("%s has %d entries; the limit is %d", section, len(items), limit)
	}
	out := make([]T, len(items))
	for i, item := range items {
		path := fmt.Sprintf("%s[%d]", section, i)
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
			return nil, invalid("%s must be an object", path)
		}
		ign.unknown(path, obj, known)
		present := make(map[string]bool, len(obj))
		for k, v := range obj {
			present[k] = !isNull(v)
		}
		if err := json.Unmarshal(item, &out[i]); err != nil {
			return nil, typeError(path, err)
		}
		defaults(&out[i], present)
	}
	return out, nil
}

// agentDefaults applies A3: an agent is enabled, single-pass, critical-gated and uses repo
// intelligence unless the bundle says otherwise.
func agentDefaults(a *BundleAgent, present map[string]bool) {
	if !present["enabled"] {
		a.Enabled = true
	}
	if !present["strategy"] {
		a.Strategy = DefaultStrategy
	}
	if !present["ci_fail_on"] {
		a.CIFailOn = DefaultCIFailOn
	}
	if !present["repo_intel"] {
		a.RepoIntel = true
	}
	if a.Skills == nil {
		a.Skills = []string{}
	}
	a.OutputSchema = nullToNil(a.OutputSchema)
}

// skillDefaults applies A3: a skill is enabled, of type custom and from a file unless the bundle
// says otherwise.
func skillDefaults(s *BundleSkill, present map[string]bool) {
	if !present["enabled"] {
		s.Enabled = true
	}
	if !present["type"] {
		s.Type = "custom"
	}
	if !present["source"] {
		s.Source = "imported_file"
	}
}

// caseDefaults normalises the JSON fields so that nil means null.
func caseDefaults(c *BundleCase, _ map[string]bool) {
	c.InputFiles = nullToNil(c.InputFiles)
	c.InputMeta = nullToNil(c.InputMeta)
	c.ExpectedOutput = nullToNil(c.ExpectedOutput)
}

// learningDefaults applies A3: a learning is global and of kind learning unless it says otherwise.
func learningDefaults(l *BundleLearning, present map[string]bool) {
	if !present["scope"] {
		l.Scope = "global"
	}
	if !present["kind"] {
		l.Kind = "learning"
	}
}

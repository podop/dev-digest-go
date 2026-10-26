package pluginshttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/plugins/app"
	"dev-digest-go/backend/internal/plugins/domain"
)

// ---- request bodies --------------------------------------------------------------------------

var (
	// exportNamePattern is the kebab-case rule of the export name (contract: PLUGIN_EXPORT_NAME_RE).
	exportNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// uuidPattern accepts any UUID version and variant, in either case.
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Defaults of the export flags when the key is absent (spec section 7).
const (
	includeEvalCasesDefault = true
	includeLearningsDefault = false
)

// parseExportBody reads the POST /plugins/export body, a strict Zod-style object: name (kebab-case
// 1-64), description (at most 300), agent_ids (1-200 UUIDs), skill_ids (1-500 UUIDs),
// include_eval_cases (default true) and include_learnings (default false); every key optional and
// no other key. Ids are lower-cased and de-duplicated in request order. Failures are 422 issues
// in Zod's wording.
func parseExportBody(body []byte) (app.ExportInput, error) {
	in := app.ExportInput{EvalCases: includeEvalCasesDefault, Learnings: includeLearningsDefault}
	r, issues := newObjectReader(body, "name", "description", "agent_ids", "skill_ids", "include_eval_cases", "include_learnings")
	if r == nil {
		return in, httpx.NewValidationError(issues...)
	}
	if v, ok := r.str("name"); ok {
		rule := httpx.StringRule{Min: 1, Max: domain.ExportNameMax, Pattern: exportNamePattern}
		_, is := rule.Check(v, "name")
		r.issues = append(r.issues, is...)
		in.Name = v
	}
	if v, ok := r.str("description"); ok {
		rule := httpx.StringRule{Max: domain.ExportDescriptionMax}
		_, is := rule.Check(v, "description")
		r.issues = append(r.issues, is...)
		in.Description = v
	}
	if v, ok := r.uuids("agent_ids", domain.ExportAgentIDsMax); ok {
		in.AgentIDs = v
	}
	if v, ok := r.uuids("skill_ids", domain.ExportSkillIDsMax); ok {
		in.SkillIDs = v
	}
	if v, ok := r.boolean("include_eval_cases"); ok {
		in.EvalCases = v
	}
	if v, ok := r.boolean("include_learnings"); ok {
		in.Learnings = v
	}
	if is := r.finish(); len(is) > 0 {
		return app.ExportInput{}, httpx.NewValidationError(is...)
	}
	return in, nil
}

// parseEnabledBody reads the PATCH /plugins/:id body {enabled: boolean}: required, no other key.
func parseEnabledBody(body []byte) (bool, error) {
	r, issues := newObjectReader(body, "enabled")
	if r == nil {
		return false, httpx.NewValidationError(issues...)
	}
	v, ok := r.boolean("enabled")
	if !ok {
		r.require("enabled")
	}
	if is := r.finish(); len(is) > 0 {
		return false, httpx.NewValidationError(is...)
	}
	return v, nil
}

// parseConfirmBody reads the DELETE /plugins/:id body {confirm: string}: required, no other key.
// The text is passed on as sent: the service compares it untrimmed and case-sensitive.
func parseConfirmBody(body []byte) (string, error) {
	r, issues := newObjectReader(body, "confirm")
	if r == nil {
		return "", httpx.NewValidationError(issues...)
	}
	v, ok := r.str("confirm")
	if !ok {
		r.require("confirm")
	}
	if is := r.finish(); len(is) > 0 {
		return "", httpx.NewValidationError(is...)
	}
	return v, nil
}

// objectReader reads the fields of one strict JSON object and collects the issues in the order
// Zod reports them: the fields in declaration order, then the unrecognized keys.
type objectReader struct {
	obj    map[string]any
	known  []string
	issues []httpx.Issue
}

// newObjectReader decodes body as exactly one JSON object. When it is not one, the reader is nil
// and issues holds the 422 issue: "Required" for no body at all, "Expected object, received X"
// otherwise (a text/plain body arrives as a JSON string, see httpx.ReadJSONBody).
func newObjectReader(body []byte, known ...string) (*objectReader, []httpx.Issue) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, []httpx.Issue{httpx.RequiredIssue()}
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("object", "string")}
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, []httpx.Issue{httpx.TypeIssue("object", zodType(doc))}
	}
	return &objectReader{obj: obj, known: known}, nil
}

// require records the "Required" issue of an absent key.
func (r *objectReader) require(key string) {
	if _, present := r.obj[key]; !present {
		r.issues = append(r.issues, httpx.RequiredIssue(key))
	}
}

// str returns the string at key; ok is false when the key is absent or not a string (the latter
// records the issue).
func (r *objectReader) str(key string) (string, bool) {
	raw, present := r.obj[key]
	if !present {
		return "", false
	}
	s, isString := raw.(string)
	if !isString {
		r.issues = append(r.issues, httpx.TypeIssue("string", zodType(raw), key))
		return "", false
	}
	return s, true
}

// boolean returns the boolean at key; ok is false when the key is absent or not a boolean (the
// latter records the issue).
func (r *objectReader) boolean(key string) (bool, bool) {
	raw, present := r.obj[key]
	if !present {
		return false, false
	}
	b, isBool := raw.(bool)
	if !isBool {
		r.issues = append(r.issues, httpx.TypeIssue("boolean", zodType(raw), key))
		return false, false
	}
	return b, true
}

// uuids returns the array of UUID strings at key, lower-cased and de-duplicated in order; ok is
// false when the key is absent or the value has an issue. The array has 1 to max items. Over max
// only the size issue is reported, so a huge array cannot produce a huge answer.
func (r *objectReader) uuids(key string, max int) ([]string, bool) {
	raw, present := r.obj[key]
	if !present {
		return nil, false
	}
	items, isArray := raw.([]any)
	if !isArray {
		r.issues = append(r.issues, httpx.TypeIssue("array", zodType(raw), key))
		return nil, false
	}
	switch {
	case len(items) < 1:
		r.issues = append(r.issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{key},
			Message: "Array must contain at least 1 element(s)"})
		return nil, false
	case len(items) > max:
		r.issues = append(r.issues, httpx.Issue{Code: httpx.CodeTooBig, Path: []any{key},
			Message: fmt.Sprintf("Array must contain at most %d element(s)", max)})
		return nil, false
	}
	out := make([]string, 0, len(items))
	bad := false
	for i, it := range items {
		s, isString := it.(string)
		switch {
		case !isString:
			r.issues = append(r.issues, httpx.TypeIssue("string", zodType(it), key, i))
			bad = true
		case !uuidPattern.MatchString(s):
			r.issues = append(r.issues, httpx.Issue{Code: httpx.CodeInvalidStr, Path: []any{key, i}, Message: "Invalid uuid"})
			bad = true
		default:
			s = strings.ToLower(s)
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out, !bad
}

// finish adds the unrecognized-keys issue and returns every issue.
func (r *objectReader) finish() []httpx.Issue {
	var extra []string
	for k := range r.obj {
		if !slices.Contains(r.known, k) {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		quoted := make([]string, len(extra))
		for i, k := range extra {
			quoted[i] = "'" + k + "'"
		}
		r.issues = append(r.issues, httpx.Issue{
			Code: "unrecognized_keys", Path: []any{},
			Message: fmt.Sprintf("Unrecognized key(s) in object: %s", strings.Join(quoted, ", ")),
		})
	}
	return r.issues
}

// zodType names a decoded JSON value the way Zod's "received" does.
func zodType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// ---- import query ----------------------------------------------------------------------------

// fileNameIssue is the 422 issue of an upload file name the service would refuse (domain.CheckFileName).
func fileNameIssue(name string) []httpx.Issue {
	if domain.CheckFileName(name) == nil {
		return nil
	}
	n := domain.UTF16Len(name)
	switch {
	case n < 1:
		return []httpx.Issue{{Code: httpx.CodeTooSmall, Path: []any{"filename"}, Message: "String must contain at least 1 character(s)"}}
	case n > domain.FileNameMax:
		return []httpx.Issue{{Code: httpx.CodeTooBig, Path: []any{"filename"},
			Message: fmt.Sprintf("String must contain at most %d character(s)", domain.FileNameMax)}}
	default:
		return []httpx.Issue{{Code: httpx.CodeInvalidStr, Path: []any{"filename"}, Message: "Invalid file name"}}
	}
}

// ---- responses -------------------------------------------------------------------------------

// countsResponse is PluginCounts of the contract.
type countsResponse struct {
	Agents    int `json:"agents"`
	Skills    int `json:"skills"`
	EvalCases int `json:"eval_cases"`
	Learnings int `json:"learnings"`
}

// pluginResponse is InstalledPlugin of the contract: the row of GET /plugins, PATCH /plugins/:id
// and the install answer. All keys are always present.
type pluginResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     *string        `json:"version"`
	Source      *string        `json:"source"`
	InstalledAt httpx.Time     `json:"installed_at"`
	Enabled     bool           `json:"enabled"`
	Description *string        `json:"description"`
	Counts      countsResponse `json:"counts"`
}

func toPlugin(p domain.Plugin) pluginResponse {
	return pluginResponse{
		ID: p.ID, Name: p.Name, Version: p.Version, Source: p.Source,
		InstalledAt: httpx.NewTime(p.InstalledAt), Enabled: p.Enabled, Description: p.Description,
		Counts: countsResponse{
			Agents: p.Counts.Agents, Skills: p.Counts.Skills, EvalCases: p.Counts.EvalCases, Learnings: p.Counts.Learnings,
		},
	}
}

// lastExportResponse is PluginLastExport of the contract.
type lastExportResponse struct {
	FileName   string         `json:"file_name"`
	ExportedAt string         `json:"exported_at"`
	Counts     countsResponse `json:"counts"`
}

// listResponse is PluginList of the contract.
type listResponse struct {
	Plugins    []pluginResponse    `json:"plugins"`
	LastExport *lastExportResponse `json:"last_export"`
}

func toList(l app.Listing) listResponse {
	out := listResponse{Plugins: make([]pluginResponse, len(l.Plugins))}
	for i, p := range l.Plugins {
		out.Plugins[i] = toPlugin(p)
	}
	if l.LastExport != nil {
		c := l.LastExport.Counts
		out.LastExport = &lastExportResponse{
			FileName: l.LastExport.FileName, ExportedAt: l.LastExport.ExportedAt,
			Counts: countsResponse{Agents: c.Agents, Skills: c.Skills, EvalCases: c.EvalCases, Learnings: c.Learnings},
		}
	}
	return out
}

// createdResponse is the `created` object of PluginImportResult.
type createdResponse struct {
	Agents      int `json:"agents"`
	Skills      int `json:"skills"`
	EvalCases   int `json:"eval_cases"`
	Conventions int `json:"conventions"`
	Learnings   int `json:"learnings"`
}

// installResponse is PluginImportResult of the contract.
type installResponse struct {
	Installed pluginResponse  `json:"installed"`
	Created   createdResponse `json:"created"`
}

func toInstall(in domain.Installed) installResponse {
	c := in.Created
	return installResponse{
		Installed: toPlugin(in.Plugin),
		Created: createdResponse{
			Agents: c.Agents, Skills: c.Skills, EvalCases: c.EvalCases, Conventions: c.Conventions, Learnings: c.Learnings,
		},
	}
}

// removeCountsResponse is the `removed` object of PluginRemoveResult.
type removeCountsResponse struct {
	Agents          int `json:"agents"`
	Skills          int `json:"skills"`
	EvalCases       int `json:"eval_cases"`
	Learnings       int `json:"learnings"`
	CIInstallations int `json:"ci_installations"`
}

// removeResponse is PluginRemoveResult of the contract.
type removeResponse struct {
	OK      bool                 `json:"ok"`
	Removed removeCountsResponse `json:"removed"`
}

func toRemove(r domain.Removed) removeResponse {
	return removeResponse{OK: true, Removed: removeCountsResponse{
		Agents: r.Agents, Skills: r.Skills, EvalCases: r.EvalCases, Learnings: r.Learnings, CIInstallations: r.CIInstallations,
	}}
}

// manifestCountsResponse is the counts of the preview's manifest.
type manifestCountsResponse struct {
	Agents       int `json:"agents"`
	Skills       int `json:"skills"`
	EvalCases    int `json:"eval_cases"`
	Conventions  int `json:"conventions"`
	Learnings    int `json:"learnings"`
	ContextFiles int `json:"context_files,omitempty"`
}

// manifestResponse is PluginManifest of the contract as the preview shows it.
type manifestResponse struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Format      string                 `json:"format"`
	Kind        string                 `json:"kind,omitempty"`
	ExportedAt  string                 `json:"exported_at"`
	Description *string                `json:"description"`
	Counts      manifestCountsResponse `json:"counts"`
}

// previewCountsResponse is PluginPreviewCounts of the contract.
type previewCountsResponse struct {
	Agents      int `json:"agents"`
	Skills      int `json:"skills"`
	EvalCases   int `json:"eval_cases"`
	Conventions int `json:"conventions"`
	Learnings   int `json:"learnings"`
}

type previewAgentResponse struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Enabled  bool     `json:"enabled"`
	Skills   []string `json:"skills"`
}

type previewSkillResponse struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type conflictsResponse struct {
	Plugin bool     `json:"plugin"`
	Skills []string `json:"skills"`
}

type ignoredResponse struct {
	PathOrSection string `json:"path_or_section"`
	Reason        string `json:"reason"`
}

// previewResponse is PluginImportPreview of the contract, the answer of a dry run.
type previewResponse struct {
	Manifest  manifestResponse       `json:"manifest"`
	Kind      string                 `json:"kind"`
	Counts    previewCountsResponse  `json:"counts"`
	Agents    []previewAgentResponse `json:"agents"`
	Skills    []previewSkillResponse `json:"skills"`
	Conflicts conflictsResponse      `json:"conflicts"`
	Ignored   []ignoredResponse      `json:"ignored"`
	Warnings  []string               `json:"warnings"`
}

func toPreview(d domain.PreviewDoc) previewResponse {
	m, c := d.Manifest, d.Manifest.Counts
	out := previewResponse{
		Manifest: manifestResponse{
			Name: m.Name, Version: m.Version, Format: m.Format, Kind: m.Kind, ExportedAt: m.ExportedAt, Description: m.Description,
			Counts: manifestCountsResponse{
				Agents: c.Agents, Skills: c.Skills, EvalCases: c.EvalCases, Conventions: c.Conventions,
				Learnings: c.Learnings, ContextFiles: c.ContextFiles,
			},
		},
		Kind: d.Kind,
		Counts: previewCountsResponse{
			Agents: d.Counts.Agents, Skills: d.Counts.Skills, EvalCases: d.Counts.EvalCases,
			Conventions: d.Counts.Conventions, Learnings: d.Counts.Learnings,
		},
		Agents:    make([]previewAgentResponse, len(d.Agents)),
		Skills:    make([]previewSkillResponse, len(d.Skills)),
		Conflicts: conflictsResponse{Plugin: d.Conflicts.Plugin, Skills: nonNil(d.Conflicts.Skills)},
		Ignored:   make([]ignoredResponse, len(d.Ignored)),
		Warnings:  nonNil(d.Warnings),
	}
	for i, a := range d.Agents {
		out.Agents[i] = previewAgentResponse{Name: a.Name, Provider: a.Provider, Model: a.Model, Enabled: a.Enabled, Skills: nonNil(a.Skills)}
	}
	for i, s := range d.Skills {
		out.Skills[i] = previewSkillResponse{Name: s.Name, Type: s.Type, Description: s.Description, Enabled: s.Enabled}
	}
	for i, ig := range d.Ignored {
		out.Ignored[i] = ignoredResponse{PathOrSection: ig.PathOrSection, Reason: ig.Reason}
	}
	return out
}

// nonNil keeps a list that must be `[]` on the wire from encoding as null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- errors ----------------------------------------------------------------------------------

// invalidDetails is `details` of a 422 invalid_plugin (contract: PluginInvalidDetails).
type invalidDetails struct {
	Issues []string `json:"issues"`
}

// conflictDetails is `details` of a 409 plugin_conflict (contract: PluginConflictDetails).
type conflictDetails struct {
	Plugin bool     `json:"plugin"`
	Skills []string `json:"skills"`
}

// mapError turns a failure of the plugins use cases into the error httpx renders. The service
// returns application errors, which pass through unchanged; a domain error that reaches the
// transport as it is gets the same status, code and details here, so a use case that returns
// one stays correct on the wire. Unmatched errors pass through (httpx renders a 500 for them).
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	var invalid *domain.InvalidError
	var conflict *domain.ConflictError
	switch {
	case errors.As(err, &invalid):
		return apperr.Validation(invalid.Error()).WithCode(app.CodeInvalidPlugin).
			WithDetails(invalidDetails{Issues: nonNil(invalid.Issues)})
	case errors.As(err, &conflict):
		return apperr.Conflict(conflict.Error()).WithCode(app.CodePluginConflict).
			WithDetails(conflictDetails{Plugin: conflict.Conflicts.Plugin, Skills: nonNil(conflict.Conflicts.Skills)})
	case errors.Is(err, domain.ErrNotFound):
		return apperr.NotFound("Plugin not found")
	case errors.Is(err, domain.ErrConfirmationMismatch):
		return apperr.Validation(domain.ErrConfirmationMismatch.Error()).WithCode(app.CodeConfirmationMismatch)
	case errors.Is(err, domain.ErrExportTooLarge):
		return apperr.Validation(domain.ErrExportTooLarge.Error()).WithCode(app.CodeExportTooLarge)
	case errors.Is(err, domain.ErrInvalidName), errors.Is(err, domain.ErrInvalidExportName), errors.Is(err, domain.ErrInvalidFileName):
		return apperr.Validation(err.Error())
	}
	return err
}

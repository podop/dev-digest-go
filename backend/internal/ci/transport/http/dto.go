package cihttp

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	"dev-digest-go/backend/internal/platform/httpx"
)

// The request schemas (contract eval-ci.ts CiExportInput, CiRefreshInput and the route-local
// schemas of the Node module, which are stricter than the contract) and the wire shapes of spec
// 7.3. Response struct field order is the key order of the JSON; nullable fields are always
// present and lists are never nil, so an empty list is [] and not null.

const (
	// maxWorkflowChars is the hard cap of the edited workflow in the request (UTF-16 units); the
	// 65,536-byte rule limit belongs to the use case, which answers 422 workflow_insecure.
	maxWorkflowChars = 1_048_576
	// Query limits of GET /ci/runs.
	defaultRunsLimit = 100
	minRunsLimit     = 1
	maxRunsLimit     = 200
)

var (
	targets  = []string{"gha", "circle", "jenkins", "cli"}
	actions  = []string{string(domain.ActionOpenPR), string(domain.ActionFiles)}
	postAses = []string{string(domain.PostAsGitHubReview), string(domain.PostAsPRComment), string(domain.PostAsNone)}

	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	jsNumber    = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
)

// parseExportBody validates a POST /agents/:id/export-ci body (CiExportBody): repo (non-empty
// string), target, action and post_as (enums with defaults), triggers (array of strings, default
// all three), base (string, accepted and dropped: the repository's default branch is used) and
// workflow (string up to 1,048,576 units, null or absent). Unknown keys are ignored. The default
// triggers are applied here; the use case checks their values.
func parseExportBody(body []byte) (app.ExportRequest, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return app.ExportRequest{}, issues
	}
	req := app.ExportRequest{
		Target:   "gha",
		Action:   domain.ActionOpenPR,
		PostAs:   domain.PostAsGitHubReview,
		Triggers: []string{string(domain.TriggerOpened), string(domain.TriggerSynchronize), string(domain.TriggerReopened)},
	}

	var is []httpx.Issue
	if raw, ok := obj["repo"]; !ok {
		issues = append(issues, httpx.RequiredIssue("repo"))
	} else if req.Repo, is = readString(raw, "repo"); is != nil {
		issues = append(issues, is...)
	} else if req.Repo == "" {
		issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{"repo"}, Message: "String must contain at least 1 character(s)"})
	}
	if v, present, is := readEnum(obj, "target", targets); present {
		issues = append(issues, is...)
		req.Target = v
	}
	if v, present, is := readEnum(obj, "action", actions); present {
		issues = append(issues, is...)
		req.Action = domain.ExportAction(v)
	}
	if v, present, is := readEnum(obj, "post_as", postAses); present {
		issues = append(issues, is...)
		req.PostAs = domain.PostAs(v)
	}
	if raw, ok := obj["triggers"]; ok {
		list, is := readStringList(raw, "triggers")
		issues = append(issues, is...)
		if is == nil {
			req.Triggers = list
		}
	}
	if raw, ok := obj["base"]; ok {
		_, is := readString(raw, "base")
		issues = append(issues, is...)
	}
	if raw, ok := obj["workflow"]; ok && jsonType(raw) != "null" {
		w, is := readString(raw, "workflow")
		issues = append(issues, is...)
		if is == nil {
			if httpx.UTF16Len(w) > maxWorkflowChars {
				issues = append(issues, httpx.Issue{Code: httpx.CodeTooBig, Path: []any{"workflow"}, Message: "String must contain at most 1048576 character(s)"})
			}
			req.Workflow = &w
		}
	}
	if issues != nil {
		return app.ExportRequest{}, issues
	}
	return req, nil
}

// parseRefreshBody validates a POST /ci/refresh body: absent or JSON null is {}; otherwise an
// object whose optional agent_id is a UUID string. It returns the agent id, "" for all.
func parseRefreshBody(body []byte) (string, []httpx.Issue) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", nil
	}
	obj, issues := decodeObject(trimmed)
	if issues != nil {
		return "", issues
	}
	raw, ok := obj["agent_id"]
	if !ok {
		return "", nil
	}
	id, issues := readUUID(raw, "agent_id")
	if issues != nil {
		return "", issues
	}
	return id, nil
}

// parseRunsQuery validates ?agent_id= (optional UUID) and ?limit= (z.coerce.number().int()
// .min(1).max(200), default 100): the text goes through JavaScript's Number(), so "" is 0 and
// "1e2" is 100. Unknown keys are ignored.
func parseRunsQuery(c *echo.Context) (app.RunFilter, []httpx.Issue) {
	f := app.RunFilter{Limit: defaultRunsLimit}
	var issues []httpx.Issue
	params := c.QueryParams()

	if values, ok := params["agent_id"]; ok && len(values) > 0 {
		switch {
		case len(values) > 1:
			issues = append(issues, httpx.TypeIssue("string", "array", "agent_id"))
		case !uuidPattern.MatchString(values[0]):
			issues = append(issues, httpx.Issue{Code: httpx.CodeInvalidStr, Path: []any{"agent_id"}, Message: "Invalid uuid"})
		default:
			f.AgentID = values[0]
		}
	}
	if values, ok := params["limit"]; ok && len(values) > 0 {
		n, is := coerceLimit(values)
		issues = append(issues, is...)
		if is == nil {
			f.Limit = n
		}
	}
	if issues != nil {
		return app.RunFilter{}, issues
	}
	return f, nil
}

// coerceLimit applies z.coerce.number().int().min(1).max(200) to the query values of limit.
func coerceLimit(values []string) (int, []httpx.Issue) {
	f, ok := toJSNumber(values)
	if !ok {
		return 0, []httpx.Issue{httpx.TypeIssue("number", "nan", "limit")}
	}
	var issues []httpx.Issue
	if math.IsInf(f, 0) || f != math.Trunc(f) {
		issues = append(issues, httpx.TypeIssue("integer", "float", "limit"))
	}
	if f < minRunsLimit {
		issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{"limit"}, Message: "Number must be greater than or equal to 1"})
	}
	if f > maxRunsLimit {
		issues = append(issues, httpx.Issue{Code: httpx.CodeTooBig, Path: []any{"limit"}, Message: "Number must be less than or equal to 200"})
	}
	if issues != nil {
		return 0, issues
	}
	return int(f), nil
}

// toJSNumber converts query values as JavaScript's Number() does; ok is false for NaN. Several
// values (a repeated key reaches the schema as an array) are NaN.
func toJSNumber(values []string) (f float64, ok bool) {
	if len(values) != 1 {
		return 0, false
	}
	t := httpx.JSTrim(values[0])
	switch t {
	case "":
		return 0, true
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(t) > 2 && t[0] == '0' {
		if base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[t[1]]; base != 0 {
			n, err := strconv.ParseUint(t[2:], base, 64)
			if err != nil && !isRangeError(err) {
				return 0, false
			}
			return float64(n), true
		}
	}
	if !jsNumber.MatchString(t) {
		return 0, false
	}
	f, _ = strconv.ParseFloat(t, 64) // out of range yields ±Inf, which Number() does too
	return f, true
}

func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// decodeObject parses body as a JSON object; an empty body is the "Required" issue of an absent
// body.
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

func readString(raw json.RawMessage, key string, path ...any) (string, []httpx.Issue) {
	path = append([]any{key}, path...)
	var s string
	if jsonType(raw) != "string" || json.Unmarshal(raw, &s) != nil {
		return "", []httpx.Issue{httpx.TypeIssue("string", jsonType(raw), path...)}
	}
	return s, nil
}

func readUUID(raw json.RawMessage, key string) (string, []httpx.Issue) {
	s, issues := readString(raw, key)
	if issues != nil {
		return "", issues
	}
	if !uuidPattern.MatchString(s) {
		return "", []httpx.Issue{{Code: httpx.CodeInvalidStr, Path: []any{key}, Message: "Invalid uuid"}}
	}
	return s, nil
}

// readEnum reads an optional enum property: present is false when the key is absent (the default
// applies). A value that is not a string is an invalid_type issue naming the allowed values, as
// Zod's enum reports it.
func readEnum(obj map[string]json.RawMessage, key string, allowed []string) (value string, present bool, issues []httpx.Issue) {
	raw, ok := obj[key]
	if !ok {
		return "", false, nil
	}
	var s string
	if jsonType(raw) != "string" || json.Unmarshal(raw, &s) != nil {
		quoted := make([]string, len(allowed))
		for i, a := range allowed {
			quoted[i] = "'" + a + "'"
		}
		return "", true, []httpx.Issue{httpx.TypeIssue(joinAlternatives(quoted), jsonType(raw), key)}
	}
	if !slices.Contains(allowed, s) {
		return "", true, []httpx.Issue{httpx.EnumIssue(allowed, s, key)}
	}
	return s, true, nil
}

func joinAlternatives(quoted []string) string {
	var b bytes.Buffer
	for i, q := range quoted {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(q)
	}
	return b.String()
}

// readStringList reads an array of strings; every non-string element is its own issue.
func readStringList(raw json.RawMessage, key string) ([]string, []httpx.Issue) {
	var items []json.RawMessage
	if jsonType(raw) != "array" || json.Unmarshal(raw, &items) != nil {
		return nil, []httpx.Issue{httpx.TypeIssue("array", jsonType(raw), key)}
	}
	out := make([]string, 0, len(items))
	var issues []httpx.Issue
	for i, item := range items {
		s, is := readString(item, key, i)
		if is != nil {
			issues = append(issues, is...)
			continue
		}
		out = append(out, s)
	}
	if issues != nil {
		return nil, issues
	}
	return out, nil
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

// ---- responses ----

type exportDTO struct {
	Installation *installationDTO `json:"installation"`
	Files        []fileDTO        `json:"files"`
	PRURL        *string          `json:"pr_url"`
}

type fileDTO struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
	Editable bool   `json:"editable"`
}

type installationListDTO struct {
	Installations []installationDTO `json:"installations"`
}

type installationDTO struct {
	ID              string     `json:"id"`
	AgentID         string     `json:"agent_id"`
	Repo            string     `json:"repo"`
	TargetType      string     `json:"target_type"`
	InstalledAt     httpx.Time `json:"installed_at"`
	Slug            *string    `json:"slug"`
	WorkflowPath    *string    `json:"workflow_path"`
	ManifestVersion *int       `json:"manifest_version"`
	LastRun         *runDTO    `json:"last_run"`
}

type runListDTO struct {
	Runs []runDTO `json:"runs"`
}

type runDTO struct {
	ID              string      `json:"id"`
	InstallationID  *string     `json:"ci_installation_id"`
	PRNumber        *int        `json:"pr_number"`
	RanAt           *httpx.Time `json:"ran_at"`
	Status          *string     `json:"status"`
	FindingsCount   *int        `json:"findings_count"`
	CostUSD         *float64    `json:"cost_usd"`
	GithubURL       *string     `json:"github_url"`
	Source          *string     `json:"source"`
	Agent           *string     `json:"agent"`
	DurationS       *float64    `json:"duration_s"`
	AgentID         *string     `json:"agent_id"`
	Repo            *string     `json:"repo"`
	Verdict         *string     `json:"verdict"`
	Blockers        *int        `json:"blockers"`
	HeadSHA         *string     `json:"head_sha"`
	ManifestVersion *int        `json:"manifest_version"`
}

type refreshDTO struct {
	Ingested      int                   `json:"ingested"`
	Degraded      bool                  `json:"degraded"`
	Installations []refreshInstallation `json:"installations"`
}

type refreshInstallation struct {
	InstallationID string       `json:"installation_id"`
	Repo           string       `json:"repo"`
	State          string       `json:"state"`
	ErrorCode      string       `json:"error_code,omitempty"`
	Ingested       int          `json:"ingested"`
	Skipped        []skippedDTO `json:"skipped"`
}

type skippedDTO struct {
	GithubRunID int64  `json:"github_run_id"`
	Reason      string `json:"reason"`
}

func toExport(e domain.Export) exportDTO {
	out := exportDTO{Files: make([]fileDTO, len(e.Files)), PRURL: e.PRURL}
	for i, f := range e.Files {
		out.Files[i] = fileDTO{Path: f.Path, Contents: f.Contents, Editable: f.Editable}
	}
	if e.Installation != nil {
		inst := toInstallation(*e.Installation)
		out.Installation = &inst
	}
	return out
}

func toInstallations(list []domain.Installation) installationListDTO {
	out := installationListDTO{Installations: make([]installationDTO, len(list))}
	for i, in := range list {
		out.Installations[i] = toInstallation(in)
	}
	return out
}

func toInstallation(in domain.Installation) installationDTO {
	dto := installationDTO{
		ID:              in.ID,
		AgentID:         in.AgentID,
		Repo:            in.Repo,
		TargetType:      in.TargetType,
		InstalledAt:     httpx.NewTime(in.InstalledAt),
		Slug:            in.Slug,
		WorkflowPath:    domain.WorkflowPathOf(in),
		ManifestVersion: in.ManifestVersion,
	}
	if in.LastRun != nil {
		r := toRun(*in.LastRun)
		dto.LastRun = &r
	}
	return dto
}

func toRuns(list []domain.Run) runListDTO {
	out := runListDTO{Runs: make([]runDTO, len(list))}
	for i, r := range list {
		out.Runs[i] = toRun(r)
	}
	return out
}

func toRun(r domain.Run) runDTO {
	dto := runDTO{
		ID:              r.ID,
		InstallationID:  r.InstallationID,
		PRNumber:        r.PRNumber,
		Status:          r.Status,
		FindingsCount:   r.FindingsCount,
		CostUSD:         r.CostUSD,
		GithubURL:       r.GithubURL,
		Source:          r.Source,
		Agent:           r.AgentName,
		DurationS:       domain.RunDurationSeconds(r),
		AgentID:         r.AgentID,
		Repo:            domain.RunRepo(r),
		Verdict:         domain.RunVerdict(r),
		Blockers:        r.Blockers,
		HeadSHA:         r.HeadSHA,
		ManifestVersion: r.ManifestVersion,
	}
	if r.RanAt != nil {
		t := httpx.NewTime(*r.RanAt)
		dto.RanAt = &t
	}
	return dto
}

func toRefresh(r domain.RefreshResult) refreshDTO {
	out := refreshDTO{Ingested: r.Ingested, Degraded: r.Degraded, Installations: make([]refreshInstallation, len(r.Installations))}
	for i, in := range r.Installations {
		item := refreshInstallation{
			InstallationID: in.InstallationID,
			Repo:           in.Repo,
			State:          string(in.State),
			Ingested:       in.Ingested,
			Skipped:        make([]skippedDTO, len(in.Skipped)),
		}
		if in.State == domain.StateError {
			item.ErrorCode = string(in.ErrorCode)
		}
		for j, s := range in.Skipped {
			item.Skipped[j] = skippedDTO{GithubRunID: s.GithubRunID, Reason: string(s.Reason)}
		}
		out.Installations[i] = item
	}
	return out
}

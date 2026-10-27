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
	// targets is CiTarget of the contract, in its order (the issue text of an unknown value lists them).
	targets  = []string{"gha", "circle", "jenkins", "cli", domain.TargetGitLabCI}
	actions  = []string{string(domain.ActionOpenPR), string(domain.ActionFiles)}
	postAses = []string{string(domain.PostAsGitHubReview), string(domain.PostAsPRComment), string(domain.PostAsNone)}
	runners  = []string{string(domain.RunnerGitHubHosted), string(domain.RunnerSelfHosted)}

	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	jsNumber    = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
)

// githubHost is the host key of github.com, which the wire omits (refresh) or sends as the default
// host of a GitHub repository.
const githubHost = "github.com"

// parseExportBody validates a POST /agents/:id/export-ci body (CiExportBody): repo (non-empty
// string, required even when repo_id is given), repo_id (optional UUID; it wins over repo in the
// use case), target (optional enum without a default: omitted, the use case picks it by the
// repository's host), action, post_as and runner (enums with defaults), triggers (array of strings, default
// all three), base (string, accepted and dropped: the repository's default branch is used) and
// workflow (string up to 1,048,576 units, null or absent). Unknown keys are ignored. The default
// triggers are applied here; the use case checks their values.
func parseExportBody(body []byte) (app.ExportRequest, []httpx.Issue) {
	obj, issues := decodeObject(body)
	if issues != nil {
		return app.ExportRequest{}, issues
	}
	req := app.ExportRequest{
		Action:   domain.ActionOpenPR,
		PostAs:   domain.PostAsGitHubReview,
		Runner:   domain.RunnerGitHubHosted,
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
	if raw, ok := obj["repo_id"]; ok {
		id, is := readUUID(raw, "repo_id")
		issues = append(issues, is...)
		req.RepoID = id
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
	if v, present, is := readEnum(obj, "runner", runners); present {
		issues = append(issues, is...)
		req.Runner = domain.Runner(v)
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
	// RequiredSecrets and RequiredVariables are the repository secrets and variables the user
	// must add (FR29); always arrays.
	RequiredSecrets   []string `json:"required_secrets"`
	RequiredVariables []string `json:"required_variables"`
	// GitLab is the gitlab_ci object (spec C 7.1): null for every other target.
	GitLab *gitlabExportDTO `json:"gitlab_ci"`
}

type gitlabExportDTO struct {
	JobFile        string  `json:"job_file"`
	RootConfig     string  `json:"root_config"`
	ConfigPath     string  `json:"config_path"`
	IncludeSnippet string  `json:"include_snippet"`
	TriggersNote   *string `json:"triggers_note"`
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
	// Provider ("github" | "gitlab") is the CI platform of the repository, not the agent's LLM
	// provider; Host is its host key (spec C 7.1).
	Provider string `json:"provider"`
	Host     string `json:"host"`
}

// repoInstallationsDTO is the contract CiRepoInstallations (eval-ci.ts).
type repoInstallationsDTO struct {
	Repos []repoEntryDTO `json:"repos"`
}

type repoEntryDTO struct {
	Repo          string                `json:"repo"`
	Installations []repoInstallationDTO `json:"installations"`
	LastRun       *repoLastRunDTO       `json:"last_run"`
	// Provider, Host and TargetType identify the repository of the entry (spec C FR46).
	Provider   string `json:"provider"`
	Host       string `json:"host"`
	TargetType string `json:"target_type"`
}

type repoInstallationDTO struct {
	ID          string     `json:"id"`
	AgentID     string     `json:"agent_id"`
	AgentName   string     `json:"agent_name"`
	Slug        *string    `json:"slug"`
	InstalledAt httpx.Time `json:"installed_at"`
}

type repoLastRunDTO struct {
	Status    *string     `json:"status"`
	RanAt     *httpx.Time `json:"ran_at"`
	GithubURL *string     `json:"github_url"`
	AgentID   *string     `json:"agent_id"`
	// PRNumber and PRURL are the pull/merge request of the run; PRURL is the merge request link of
	// a GitLab run (null for GitHub and for a host that is not connected), as on runDTO (GitLab C FR45).
	PRNumber *int    `json:"pr_number"`
	PRURL    *string `json:"pr_url"`
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
	// Provider and Host as in installationDTO; PRURL is the merge request link of a GitLab run,
	// null for GitHub (whose link the UI builds) and for a host that is not connected.
	Provider string  `json:"provider"`
	Host     string  `json:"host"`
	PRURL    *string `json:"pr_url"`
}

type refreshDTO struct {
	Ingested      int                   `json:"ingested"`
	Degraded      bool                  `json:"degraded"`
	Installations []refreshInstallation `json:"installations"`
}

type refreshInstallation struct {
	InstallationID string       `json:"installation_id"`
	Repo           string       `json:"repo"`
	Host           string       `json:"host,omitempty"` // GitLab hosts only: GitHub results keep today's shape
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
	out := exportDTO{
		Files: make([]fileDTO, len(e.Files)), PRURL: e.PRURL,
		RequiredSecrets:   append([]string{}, e.RequiredSecrets...),
		RequiredVariables: append([]string{}, e.RequiredVariables...),
	}
	for i, f := range e.Files {
		out.Files[i] = fileDTO{Path: f.Path, Contents: f.Contents, Editable: f.Editable}
	}
	if e.Installation != nil {
		inst := toInstallation(*e.Installation)
		out.Installation = &inst
	}
	if g := e.GitLab; g != nil {
		out.GitLab = &gitlabExportDTO{
			JobFile: g.JobFile, RootConfig: string(g.RootConfig), ConfigPath: g.ConfigPath, IncludeSnippet: g.IncludeSnippet,
		}
		if g.TriggersNote != "" {
			note := g.TriggersNote
			out.GitLab.TriggersNote = &note
		}
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
		WorkflowPath:    installationPath(in),
		ManifestVersion: in.ManifestVersion,
		Provider:        platformOf(in.TargetType),
		Host:            hostOf(in.Host),
	}
	if in.LastRun != nil {
		r := toRun(*in.LastRun)
		dto.LastRun = &r
	}
	return dto
}

// installationPath is the workflow path of an installation: the GitHub workflow file, or the
// GitLab job file (spec C FR37); nil for a row without a slug.
func installationPath(in domain.Installation) *string {
	if in.TargetType != domain.TargetGitLabCI || in.Slug == nil || *in.Slug == "" {
		return domain.WorkflowPathOf(in)
	}
	p := domain.GitLabJobPath(*in.Slug)
	return &p
}

// platformOf is the CI platform of a target type: "gitlab" for gitlab_ci, "github" otherwise (a
// target type that is not set is a gha installation).
func platformOf(targetType string) string {
	if targetType == domain.TargetGitLabCI {
		return "gitlab"
	}
	return "github"
}

// hostOf is the host key a store reports; "" is github.com.
func hostOf(host string) string {
	if host == "" {
		return githubHost
	}
	return host
}

func toRepoInstallations(groups []domain.RepoGroup) repoInstallationsDTO {
	out := repoInstallationsDTO{Repos: make([]repoEntryDTO, len(groups))}
	for i, g := range groups {
		targetType := g.TargetType
		if targetType == "" {
			targetType = domain.TargetGHA
		}
		entry := repoEntryDTO{
			Repo: g.Repo, Installations: make([]repoInstallationDTO, len(g.Installations)),
			Provider: platformOf(targetType), Host: hostOf(g.Host), TargetType: targetType,
		}
		for j, in := range g.Installations {
			entry.Installations[j] = repoInstallationDTO{
				ID: in.ID, AgentID: in.AgentID, AgentName: in.AgentName, Slug: in.Slug,
				InstalledAt: httpx.NewTime(in.InstalledAt),
			}
		}
		if g.LastRun != nil {
			last := repoLastRunDTO{
				Status: g.LastRun.Status, GithubURL: g.LastRun.GithubURL, AgentID: g.LastRun.AgentID,
				PRNumber: g.LastRun.PRNumber, PRURL: g.LastRun.PRURL,
			}
			if g.LastRun.RanAt != nil {
				t := httpx.NewTime(*g.LastRun.RanAt)
				last.RanAt = &t
			}
			entry.LastRun = &last
		}
		out.Repos[i] = entry
	}
	return out
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
		Provider:        platformOfRun(r),
		Host:            hostOf(r.Host),
		PRURL:           r.PRURL,
	}
	if r.RanAt != nil {
		t := httpx.NewTime(*r.RanAt)
		dto.RanAt = &t
	}
	return dto
}

// platformOfRun is "gitlab" for a run ingested from GitLab CI (source gitlab_ci), else "github".
func platformOfRun(r domain.Run) string {
	if r.Source != nil && *r.Source == domain.RunSourceGitLab {
		return "gitlab"
	}
	return "github"
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
		if in.Host != githubHost {
			item.Host = in.Host
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

package evalshttp

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/platform/httpx"
)

// ---- request parsing -------------------------------------------------------------------------
//
// The Node server validates every request with Zod schemas and answers 422 with one issue per
// failed check. The parsers below reproduce those schemas (eval-pipeline.ts) issue by issue, in
// shape order, with Zod's codes and messages; unknown keys are dropped. Zod keeps checking after
// a failed length/range check ("dirty") but not after a wrong type or enum value ("aborted"); the
// reader tracks that, because the end_line refinement only runs on an object that is not aborted.

var (
	severities = []string{"CRITICAL", "WARNING", "SUGGESTION"}
	categories = []string{"bug", "security", "perf", "style", "test"}
	kinds      = []string{string(domain.MustFind), string(domain.MustNotFlag)}
	periods    = []string{string(domain.Period7d), string(domain.Period30d), string(domain.Period90d), string(domain.PeriodAll)}

	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// jsNumber matches the decimal literals JavaScript's Number() accepts (after trimming).
	jsNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
)

const endBeforeStart = "end_line must be >= start_line"

// presence says what a lookup found.
type presence int

const (
	absent presence = iota // key not in the object
	null                   // JSON null
	value                  // a value of the wanted JSON type
	bad                    // a type issue was recorded
)

type issueList struct{ all []httpx.Issue }

// reader reads the keys of one JSON object and records Zod-style issues.
type reader struct {
	issues  *issueList
	parent  *reader
	fields  map[string]json.RawMessage
	prefix  []any
	aborted bool
}

// parseObject opens the body of a request: the root object. A missing body is "Required"; a body
// that is not an object (including text/plain, which ReadJSONBody hands over as a JSON string) is
// "Expected object, received <type>".
func parseObject(raw []byte) (*reader, *issueList) {
	list := &issueList{}
	r := &reader{issues: list, prefix: []any{}}
	if len(raw) == 0 {
		r.abort(httpx.RequiredIssue())
		return nil, list
	}
	var m map[string]json.RawMessage
	if t := jsonType(raw); t != "object" || json.Unmarshal(raw, &m) != nil {
		r.abort(httpx.TypeIssue("object", jsonType(raw)))
		return nil, list
	}
	r.fields = m
	return r, list
}

func (r *reader) at(name string) []any {
	p := make([]any, 0, len(r.prefix)+1)
	p = append(p, r.prefix...)
	return append(p, name)
}

// abort records an issue after which Zod stops the object (wrong type, wrong enum value).
func (r *reader) abort(is httpx.Issue) {
	r.issues.all = append(r.issues.all, is)
	for x := r; x != nil; x = x.parent {
		x.aborted = true
	}
}

// dirty records a failed check; Zod goes on.
func (r *reader) dirty(is httpx.Issue) { r.issues.all = append(r.issues.all, is) }

// lookup finds a key. expected names the wanted type for the null message.
func (r *reader) lookup(name, expected string, optional, nullable bool) (json.RawMessage, presence) {
	raw, ok := r.fields[name]
	switch {
	case !ok:
		if optional {
			return nil, absent
		}
		r.abort(httpx.RequiredIssue(r.at(name)...))
		return nil, bad
	case jsonType(raw) == "null":
		if nullable {
			return nil, null
		}
		r.abort(httpx.TypeIssue(expected, "null", r.at(name)...))
		return nil, bad
	}
	return raw, value
}

// str reads a string checked with rule (Trim is applied to the returned value).
func (r *reader) str(name string, rule httpx.StringRule, optional, nullable bool) (string, presence) {
	raw, p := r.lookup(name, "string", optional, nullable)
	if p != value {
		return "", p
	}
	if t := jsonType(raw); t != "string" {
		r.abort(httpx.TypeIssue("string", t, r.at(name)...))
		return "", bad
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		r.abort(httpx.TypeIssue("string", "undefined", r.at(name)...))
		return "", bad
	}
	s, issues := rule.Check(s, r.at(name)...)
	for _, is := range issues {
		r.dirty(is)
	}
	return s, value
}

// line reads z.number().int().min(1): n is the value for the end_line comparison, v the value
// clamped to the int32 range of the database columns.
func (r *reader) line(name string, optional bool) (n float64, v int, p presence) {
	raw, p := r.lookup(name, "number", optional, false)
	if p != value {
		return 0, 0, p
	}
	if t := jsonType(raw); t != "number" {
		r.abort(httpx.TypeIssue("number", t, r.at(name)...))
		return 0, 0, bad
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil { // beyond float64: JavaScript reads Infinity
		f = math.Inf(1)
	}
	if f != math.Trunc(f) || math.IsInf(f, 0) {
		r.dirty(httpx.TypeIssue("integer", "float", r.at(name)...))
	}
	if f < 1 {
		r.dirty(httpx.Issue{Code: httpx.CodeTooSmall, Path: r.at(name), Message: "Number must be greater than or equal to 1"})
	}
	return f, int(max(math.MinInt32, min(math.MaxInt32, f))), value
}

// enum reads one of allowed.
func (r *reader) enum(name string, allowed []string, optional, nullable bool) (string, presence) {
	expected := "'" + strings.Join(allowed, "' | '") + "'"
	raw, p := r.lookup(name, expected, optional, nullable)
	if p != value {
		return "", p
	}
	if t := jsonType(raw); t != "string" {
		r.abort(httpx.TypeIssue(expected, t, r.at(name)...))
		return "", bad
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || !slices.Contains(allowed, s) {
		r.abort(httpx.EnumIssue(allowed, s, r.at(name)...))
		return "", bad
	}
	return s, value
}

// object reads a nested object; nil when absent (optional) or invalid.
func (r *reader) object(name string, optional bool) *reader {
	raw, p := r.lookup(name, "object", optional, false)
	if p != value {
		return nil
	}
	var m map[string]json.RawMessage
	if t := jsonType(raw); t != "object" || json.Unmarshal(raw, &m) != nil {
		r.abort(httpx.TypeIssue("object", jsonType(raw), r.at(name)...))
		return nil
	}
	return &reader{issues: r.issues, parent: r, fields: m, prefix: r.at(name)}
}

// optString turns a nullish string into a pointer: nil = absent or null.
func optString(s string, p presence) *string {
	if p != value {
		return nil
	}
	return &s
}

// field turns a nullish value into a PATCH field: absent keeps, null clears.
func field(s string, p presence) domain.Field[string] {
	switch p {
	case value:
		return domain.Field[string]{Set: true, Value: &s}
	case null:
		return domain.Field[string]{Set: true}
	default:
		return domain.Field[string]{}
	}
}

var (
	nameRule  = httpx.StringRule{Trim: true, Min: 1, Max: domain.CaseNameMax}
	fileRule  = httpx.StringRule{Min: 1, Max: domain.ExpectationFileMax}
	titleRule = httpx.StringRule{Max: domain.ExpectationTitleMax}
	diffRule  = httpx.StringRule{Min: 1}
	prTitle   = httpx.StringRule{Max: domain.PRTitleMax}
	prDesc    = httpx.StringRule{Max: domain.PRDescriptionMax}
	anyString = httpx.StringRule{}
)

// parseCreate validates EvalCaseCreateInput (POST /agents|skills/:id/eval-cases).
func parseCreate(raw []byte) (domain.CreateInput, []httpx.Issue) {
	var in domain.CreateInput
	r, list := parseObject(raw)
	if r == nil {
		return in, list.all
	}
	in.Name, _ = r.str("name", nameRule, false, false)
	notes, p := r.str("notes", anyString, true, true)
	in.Notes = optString(notes, p)

	if e := r.object("expectation", false); e != nil {
		kind, _ := e.enum("kind", kinds, false, false)
		file, _ := e.str("file", fileRule, false, false)
		startF, start, _ := e.line("start_line", false)
		endF, end, _ := e.line("end_line", false)
		sev, sp := e.enum("severity", severities, true, true)
		cat, cp := e.enum("category", categories, true, true)
		title, tp := e.str("title", titleRule, true, true)
		if !e.aborted && endF < startF { // the schema's refine; it does not run on an aborted object
			e.dirty(httpx.Issue{Code: httpx.CodeCustom, Path: e.at("end_line"), Message: endBeforeStart})
		}
		in.Expectation = domain.Expectation{
			Kind: domain.ExpectationKind(kind), File: file, StartLine: start, EndLine: end,
			Severity: optString(sev, sp), Category: optString(cat, cp), Title: optString(title, tp),
		}
	}

	in.InputDiff, _ = r.str("input_diff", diffRule, false, false)
	if m := r.object("input_meta", true); m != nil {
		t, tp := m.str("pr_title", prTitle, true, true)
		d, dp := m.str("pr_description", prDesc, true, true)
		in.PRTitle, in.PRDescription = optString(t, tp), optString(d, dp)
	}
	if len(list.all) > 0 {
		return in, list.all
	}
	return in, nil
}

// parseUpdate validates AgentEvalCaseUpdateInput (PATCH /eval-cases/:id): field shapes only.
func parseUpdate(raw []byte) (domain.UpdateInput, []httpx.Issue) {
	var in domain.UpdateInput
	r, list := parseObject(raw)
	if r == nil {
		return in, list.all
	}
	if s, p := r.str("name", nameRule, true, false); p == value {
		in.Name = &s
	}
	notes, p := r.str("notes", anyString, true, true)
	in.Notes = field(notes, p)
	if s, p := r.enum("kind", kinds, true, false); p == value {
		k := domain.ExpectationKind(s)
		in.Kind = &k
	}
	if s, p := r.str("file", fileRule, true, false); p == value {
		in.File = &s
	}
	if _, v, p := r.line("start_line", true); p == value {
		in.StartLine = &v
	}
	if _, v, p := r.line("end_line", true); p == value {
		in.EndLine = &v
	}
	sev, p := r.enum("severity", severities, true, true)
	in.Severity = field(sev, p)
	cat, p := r.enum("category", categories, true, true)
	in.Category = field(cat, p)
	title, p := r.str("title", titleRule, true, true)
	in.Title = field(title, p)
	if s, p := r.str("input_diff", diffRule, true, false); p == value {
		in.InputDiff = &s
	}
	t, p := r.str("pr_title", prTitle, true, true)
	in.PRTitle = field(t, p)
	d, p := r.str("pr_description", prDesc, true, true)
	in.PRDescription = field(d, p)
	if len(list.all) > 0 {
		return in, list.all
	}
	return in, nil
}

// parseSkillRunStart validates EvalSkillRunStartInput: agent_id is a UUID.
func parseSkillRunStart(raw []byte) (string, []httpx.Issue) {
	r, list := parseObject(raw)
	if r == nil {
		return "", list.all
	}
	id, p := r.str("agent_id", anyString, false, false)
	if p == value && !uuidPattern.MatchString(id) {
		r.dirty(httpx.Issue{Code: httpx.CodeInvalidStr, Path: r.at("agent_id"), Message: "Invalid uuid"})
	}
	if len(list.all) > 0 {
		return "", list.all
	}
	return id, nil
}

// runListQuery is a validated EvalRunListQuery.
type runListQuery struct {
	limit  int
	period domain.Period
}

// parseRunListQuery validates EvalRunListQuery: limit is z.coerce.number().int().min(1).max(100)
// (the text goes through JavaScript's Number(), default 20), period one of 7d|30d|90d|all.
func parseRunListQuery(c *echo.Context) (runListQuery, []httpx.Issue) {
	q := runListQuery{limit: domain.RunListDefault}
	var issues []httpx.Issue
	if values := c.QueryParams()["limit"]; len(values) > 0 {
		f, ok := toJSNumber(values[0])
		if len(values) > 1 { // an array: Number([...]) is NaN
			ok = false
		}
		switch {
		case !ok:
			issues = append(issues, httpx.TypeIssue("number", "nan", "limit"))
		default:
			if f != math.Trunc(f) || math.IsInf(f, 0) {
				issues = append(issues, httpx.TypeIssue("integer", "float", "limit"))
			}
			switch {
			case f < 1:
				issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{"limit"}, Message: "Number must be greater than or equal to 1"})
			case f > domain.RunListMax:
				issues = append(issues, httpx.Issue{Code: httpx.CodeTooBig, Path: []any{"limit"}, Message: "Number must be less than or equal to 100"})
			default:
				q.limit = int(f)
			}
		}
	}
	p, pi := periodQuery(c)
	q.period = p
	return q, append(issues, pi...)
}

// periodQuery reads the optional ?period=.
func periodQuery(c *echo.Context) (domain.Period, []httpx.Issue) {
	values := c.QueryParams()["period"]
	if len(values) == 0 {
		return domain.PeriodNone, nil
	}
	if len(values) > 1 {
		return domain.PeriodNone, []httpx.Issue{httpx.TypeIssue("'"+strings.Join(periods, "' | '")+"'", "array", "period")}
	}
	if !slices.Contains(periods, values[0]) {
		return domain.PeriodNone, []httpx.Issue{httpx.EnumIssue(periods, values[0], "period")}
	}
	return domain.Period(values[0]), nil
}

// parseCompareQuery validates EvalCompareQuery: a and b are non-empty strings (not UUID-checked).
func parseCompareQuery(c *echo.Context) (a, b string, issues []httpx.Issue) {
	read := func(name string) string {
		values, ok := c.QueryParams()[name]
		switch {
		case !ok || len(values) == 0:
			issues = append(issues, httpx.RequiredIssue(name))
		case len(values) > 1:
			issues = append(issues, httpx.TypeIssue("string", "array", name))
		case values[0] == "":
			issues = append(issues, httpx.Issue{Code: httpx.CodeTooSmall, Path: []any{name}, Message: "String must contain at least 1 character(s)"})
		default:
			return values[0]
		}
		return ""
	}
	a = read("a")
	b = read("b")
	return a, b, issues
}

// jsonType names the JSON type of a raw value the way Zod reports it.
func jsonType(raw []byte) string {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '"':
			return "string"
		case '{':
			return "object"
		case '[':
			return "array"
		case 't', 'f':
			return "boolean"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	return "undefined"
}

// toJSNumber converts s as JavaScript's Number(s) does; ok is false for NaN.
func toJSNumber(s string) (f float64, ok bool) {
	t := httpx.JSTrim(s)
	switch t {
	case "":
		return 0, true
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(t) > 2 && t[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[t[1]]
		if base != 0 {
			n, err := strconv.ParseUint(t[2:], base, 64)
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				return 0, false
			}
			return float64(n), true // beyond 64 bits it saturates, as a huge value does
		}
	}
	if !jsNumber.MatchString(t) {
		return 0, false
	}
	f, _ = strconv.ParseFloat(t, 64) // out of range yields ±Inf, which Number() does too
	return f, true
}

// ---- errors ----------------------------------------------------------------------------------

// rangeDTO is one {start,end} run of expectation_lines_not_in_diff's details.
type rangeDTO struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// wireDetails gives the details of a rule failure their wire shape: the domain's line ranges have
// no JSON tags, the contract wants {"start","end"}.
func wireDetails(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if rs, ok := v.([]domain.LineRange); ok {
			dto := make([]rangeDTO, len(rs))
			for i, r := range rs {
				dto[i] = rangeDTO{Start: r.Start, End: r.End}
			}
			v = dto
		}
		out[k] = v
	}
	return out
}

// mapError makes a use-case failure renderable: a pure-rule failure that reaches transport as
// *domain.RuleError becomes its application error (422 for RuleUnprocessable, 400 for
// RuleBadRequest, with the rule's code and details), and the details of an application error get
// the wire shape of the line ranges. Everything else passes through (httpx renders it).
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var re *domain.RuleError
	if errors.As(err, &re) {
		var ae *apperr.Error
		if re.Kind == domain.RuleBadRequest {
			ae = apperr.InvalidInput(re.Message)
		} else {
			ae = apperr.Validation(re.Message)
		}
		ae.WithCode(re.Code)
		if len(re.Details) > 0 {
			ae.WithDetails(wireDetails(re.Details))
		}
		return ae
	}
	if ae, ok := apperr.As(err); ok {
		if m, isMap := ae.Details.(map[string]any); isMap {
			cp := *ae
			cp.Details = wireDetails(m)
			return &cp
		}
	}
	return err
}

// ---- responses -------------------------------------------------------------------------------

type expectationDTO struct {
	Kind      string  `json:"kind"`
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Severity  *string `json:"severity"`
	Category  *string `json:"category"`
	Title     *string `json:"title"`
}

type inputMetaDTO struct {
	PRTitle       string  `json:"pr_title"`
	PRDescription *string `json:"pr_description"`
	Repo          *string `json:"repo"`
	PRNumber      *int    `json:"pr_number"`
	HeadSHA       *string `json:"head_sha"`
}

type lastResultDTO struct {
	RunID  string     `json:"run_id"`
	Status string     `json:"status"`
	RanAt  httpx.Time `json:"ran_at"`
}

type caseDTO struct {
	ID              string         `json:"id"`
	OwnerKind       string         `json:"owner_kind"`
	OwnerID         string         `json:"owner_id"`
	AgentID         *string        `json:"agent_id"`
	Name            string         `json:"name"`
	Notes           *string        `json:"notes"`
	Expectation     expectationDTO `json:"expectation"`
	InputDiff       string         `json:"input_diff"`
	InputMeta       inputMetaDTO   `json:"input_meta"`
	SourceFindingID *string        `json:"source_finding_id"`
	SourcePRID      *string        `json:"source_pr_id"`
	Origin          string         `json:"origin"`
	ContentEditedAt *httpx.Time    `json:"content_edited_at"`
	CreatedAt       httpx.Time     `json:"created_at"`
	LastResult      *lastResultDTO `json:"last_result"`
}

type caseListDTO struct {
	Cases []caseDTO `json:"cases"`
}

type runDTO struct {
	ID                string      `json:"id"`
	OwnerKind         string      `json:"owner_kind"`
	SkillID           *string     `json:"skill_id"`
	SkillVersion      *int        `json:"skill_version"`
	AgentID           string      `json:"agent_id"`
	AgentVersion      int         `json:"agent_version"`
	AgentName         string      `json:"agent_name"`
	Model             string      `json:"model"`
	Status            string      `json:"status"`
	StartedAt         httpx.Time  `json:"started_at"`
	FinishedAt        *httpx.Time `json:"finished_at"`
	CasesTotal        int         `json:"cases_total"`
	CasesFinished     int         `json:"cases_finished"`
	CasesPassed       int         `json:"cases_passed"`
	CasesErrored      int         `json:"cases_errored"`
	Recall            *float64    `json:"recall"`
	Precision         *float64    `json:"precision"`
	CitationAccuracy  *float64    `json:"citation_accuracy"`
	UnlabeledFindings int         `json:"unlabeled_findings"`
	CostUSD           *float64    `json:"cost_usd"`
	DurationMS        *int        `json:"duration_ms"`
	Error             *string     `json:"error"`
}

type runListDTO struct {
	Runs []runDTO `json:"runs"`
}

type resultFindingDTO struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Severity  *string `json:"severity"`
	Category  *string `json:"category"`
	Title     *string `json:"title"`
	Match     string  `json:"match"`
}

type resultDTO struct {
	CaseID       string             `json:"case_id"`
	CaseName     string             `json:"case_name"`
	Expectation  expectationDTO     `json:"expectation"`
	Status       string             `json:"status"`
	Findings     []resultFindingDTO `json:"findings"`
	DroppedCount int                `json:"dropped_count"`
	DurationMS   int                `json:"duration_ms"`
	CostUSD      *float64           `json:"cost_usd"`
	Error        *string            `json:"error"`
}

type runDetailDTO struct {
	Run     runDTO      `json:"run"`
	Results []resultDTO `json:"results"`
}

type deltaDTO struct {
	Recall           *float64 `json:"recall"`
	Precision        *float64 `json:"precision"`
	CitationAccuracy *float64 `json:"citation_accuracy"`
	CostUSD          *float64 `json:"cost_usd"`
}

type compareCaseDTO struct {
	CaseID   string `json:"case_id"`
	CaseName string `json:"case_name"`
	A        string `json:"a"`
	B        string `json:"b"`
	Flipped  bool   `json:"flipped"`
}

type diffLineDTO struct {
	Op   string `json:"op"`
	Line string `json:"line"`
}

type valueChangeDTO struct {
	A string `json:"a"`
	B string `json:"b"`
}

type configDiffDTO struct {
	SystemPrompt  []diffLineDTO   `json:"system_prompt"`
	Model         *valueChangeDTO `json:"model"`
	Strategy      *valueChangeDTO `json:"strategy"`
	SkillsAdded   []string        `json:"skills_added"`
	SkillsRemoved []string        `json:"skills_removed"`
}

type compareDTO struct {
	A          runDTO           `json:"a"`
	B          runDTO           `json:"b"`
	Delta      deltaDTO         `json:"delta"`
	Cases      []compareCaseDTO `json:"cases"`
	ConfigDiff configDiffDTO    `json:"config_diff"`
}

type sparkPointDTO struct {
	RunID            string   `json:"run_id"`
	Recall           *float64 `json:"recall"`
	Precision        *float64 `json:"precision"`
	CitationAccuracy *float64 `json:"citation_accuracy"`
}

type agentOverviewDTO struct {
	AgentID    string          `json:"agent_id"`
	Name       string          `json:"name"`
	Model      string          `json:"model"`
	CasesTotal int             `json:"cases_total"`
	Latest     *runDTO         `json:"latest"`
	Sparkline  []sparkPointDTO `json:"sparkline"`
}

type overviewDTO struct {
	Agents     []agentOverviewDTO `json:"agents"`
	RecentRuns []runDTO           `json:"recent_runs"`
}

type metricDeltaDTO struct {
	Metric string  `json:"metric"`
	Delta  float64 `json:"delta"`
}

type alertDTO struct {
	Metrics []metricDeltaDTO `json:"metrics"`
}

type agentDashboardDTO struct {
	AgentID    string    `json:"agent_id"`
	CasesTotal int       `json:"cases_total"`
	Latest     *runDTO   `json:"latest"`
	Previous   *runDTO   `json:"previous"`
	Trend      []runDTO  `json:"trend"`
	Alert      *alertDTO `json:"alert"`
}

type skipDTO struct {
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason"`
}

type runAllDTO struct {
	Started []runDTO  `json:"started"`
	Skipped []skipDTO `json:"skipped"`
}

func timePtr(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	v := httpx.NewTime(*t)
	return &v
}

func toExpectation(e domain.Expectation) expectationDTO {
	return expectationDTO{
		Kind: string(e.Kind), File: e.File, StartLine: e.StartLine, EndLine: e.EndLine,
		Severity: e.Severity, Category: e.Category, Title: e.Title,
	}
}

func toCase(v domain.CaseView) caseDTO {
	c := v.Case
	d := caseDTO{
		ID: c.ID, OwnerKind: string(c.OwnerKind), OwnerID: c.OwnerID, Name: c.Name, Notes: c.Notes,
		Expectation: toExpectation(c.Expectation), InputDiff: c.InputDiff,
		InputMeta: inputMetaDTO{
			PRTitle: c.InputMeta.PRTitle, PRDescription: c.InputMeta.PRDescription, Repo: c.InputMeta.Repo,
			PRNumber: c.InputMeta.PRNumber, HeadSHA: c.InputMeta.HeadSHA,
		},
		SourceFindingID: c.SourceFindingID, SourcePRID: c.SourcePRID, Origin: "manual",
		ContentEditedAt: timePtr(c.ContentEditedAt), CreatedAt: httpx.NewTime(c.CreatedAt),
	}
	if c.OwnerKind == domain.OwnerAgent {
		id := c.OwnerID
		d.AgentID = &id
	}
	if c.SourceFindingID != nil {
		d.Origin = "finding"
	}
	if lr := v.LastResult; lr != nil {
		d.LastResult = &lastResultDTO{RunID: lr.RunID, Status: string(lr.Status), RanAt: httpx.NewTime(lr.RanAt)}
	}
	return d
}

func toCaseList(views []domain.CaseView) caseListDTO {
	out := caseListDTO{Cases: make([]caseDTO, len(views))}
	for i, v := range views {
		out.Cases[i] = toCase(v)
	}
	return out
}

func toRun(r domain.Run) runDTO {
	return runDTO{
		ID: r.ID, OwnerKind: string(r.OwnerKind), SkillID: r.SkillID, SkillVersion: r.SkillVersion,
		AgentID: r.AgentID, AgentVersion: r.AgentVersion, AgentName: r.AgentName, Model: r.Config.Model,
		Status: string(r.Status), StartedAt: httpx.NewTime(r.StartedAt), FinishedAt: timePtr(r.FinishedAt),
		CasesTotal: r.CasesTotal, CasesFinished: r.CasesFinished, CasesPassed: r.CasesPassed, CasesErrored: r.CasesErrored,
		Recall: r.Metrics.Recall, Precision: r.Metrics.Precision, CitationAccuracy: r.Metrics.CitationAccuracy,
		UnlabeledFindings: r.Metrics.UnlabeledFindings, CostUSD: r.Metrics.CostUSD,
		DurationMS: r.DurationMS, Error: r.Error,
	}
}

func toRunPtr(r *domain.Run) *runDTO {
	if r == nil {
		return nil
	}
	d := toRun(*r)
	return &d
}

func toRuns(rs []domain.Run) []runDTO {
	out := make([]runDTO, len(rs))
	for i, r := range rs {
		out[i] = toRun(r)
	}
	return out
}

func toRunList(rs []domain.Run) runListDTO { return runListDTO{Runs: toRuns(rs)} }

func toRunDetail(d domain.RunDetail) runDetailDTO {
	out := runDetailDTO{Run: toRun(d.Run), Results: make([]resultDTO, len(d.Results))}
	for i, r := range d.Results {
		findings := make([]resultFindingDTO, len(r.Findings))
		for j, f := range r.Findings {
			findings[j] = resultFindingDTO{
				File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
				Severity: f.Severity, Category: f.Category, Title: f.Title, Match: string(f.Match),
			}
		}
		out.Results[i] = resultDTO{
			CaseID: r.CaseID, CaseName: r.CaseName, Expectation: toExpectation(r.Expectation),
			Status: string(r.Status), Findings: findings, DroppedCount: r.DroppedCount,
			DurationMS: r.DurationMS, CostUSD: r.CostUSD, Error: r.Error,
		}
	}
	return out
}

func toChange(v *domain.ValueChange) *valueChangeDTO {
	if v == nil {
		return nil
	}
	return &valueChangeDTO{A: v.A, B: v.B}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toCompare(c domain.Compare) compareDTO {
	cases := make([]compareCaseDTO, len(c.Cases))
	for i, k := range c.Cases {
		cases[i] = compareCaseDTO{CaseID: k.CaseID, CaseName: k.CaseName, A: string(k.A), B: string(k.B), Flipped: k.Flipped}
	}
	lines := make([]diffLineDTO, len(c.ConfigDiff.SystemPrompt))
	for i, l := range c.ConfigDiff.SystemPrompt {
		lines[i] = diffLineDTO{Op: string(l.Op), Line: l.Line}
	}
	return compareDTO{
		A: toRun(c.A), B: toRun(c.B),
		Delta: deltaDTO{Recall: c.Delta.Recall, Precision: c.Delta.Precision, CitationAccuracy: c.Delta.CitationAccuracy, CostUSD: c.Delta.CostUSD},
		Cases: cases,
		ConfigDiff: configDiffDTO{
			SystemPrompt: lines, Model: toChange(c.ConfigDiff.Model), Strategy: toChange(c.ConfigDiff.Strategy),
			SkillsAdded: nonNil(c.ConfigDiff.SkillsAdded), SkillsRemoved: nonNil(c.ConfigDiff.SkillsRemoved),
		},
	}
}

func toOverview(o domain.Overview) overviewDTO {
	out := overviewDTO{Agents: make([]agentOverviewDTO, len(o.Agents)), RecentRuns: make([]runDTO, len(o.RecentRuns))}
	for i, a := range o.Agents {
		spark := make([]sparkPointDTO, len(a.Sparkline))
		for j, p := range a.Sparkline {
			spark[j] = sparkPointDTO{RunID: p.RunID, Recall: p.Recall, Precision: p.Precision, CitationAccuracy: p.CitationAccuracy}
		}
		out.Agents[i] = agentOverviewDTO{
			AgentID: a.ID, Name: a.Name, Model: a.Model, CasesTotal: a.CasesTotal,
			Latest: toRunPtr(a.Latest), Sparkline: spark,
		}
	}
	for i, r := range o.RecentRuns {
		d := toRun(r.Run)
		d.AgentName = r.AgentName // the agent's current name
		out.RecentRuns[i] = d
	}
	return out
}

func toAgentDashboard(d domain.AgentDashboard) agentDashboardDTO {
	out := agentDashboardDTO{
		AgentID: d.AgentID, CasesTotal: d.CasesTotal, Latest: toRunPtr(d.Latest), Previous: toRunPtr(d.Previous),
		Trend: toRuns(d.Trend),
	}
	if len(d.Alert) > 0 {
		a := &alertDTO{Metrics: make([]metricDeltaDTO, len(d.Alert))}
		for i, m := range d.Alert {
			a.Metrics[i] = metricDeltaDTO{Metric: string(m.Metric), Delta: m.Delta}
		}
		out.Alert = a
	}
	return out
}

func toRunAll(r domain.RunAllResult) runAllDTO {
	out := runAllDTO{Started: toRuns(r.Started), Skipped: make([]skipDTO, len(r.Skipped))}
	for i, s := range r.Skipped {
		out.Skipped[i] = skipDTO{AgentID: s.AgentID, Reason: string(s.Reason)}
	}
	return out
}

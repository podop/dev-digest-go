package reviewshttp

import (
	"bytes"
	"encoding/json"
	"time"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// The wire shapes of spec 7.3. Struct field order is the key order of the JSON; nullable fields
// are pointers without omitempty (always present), optional ones are left out only where the
// spec says so. Slices are never nil so that an empty list is [] and not null.

type okDTO struct {
	OK bool `json:"ok"`
}

type findingResponse struct {
	Finding findingDTO `json:"finding"`
}

type learnResponse struct {
	Finding  findingDTO `json:"finding"`
	MemoryID string     `json:"memoryId"`
}

// ---- POST /pulls/:id/review ----

type runTargetDTO struct {
	RunID     string `json:"run_id"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
}

type startResponse struct {
	PRID            string         `json:"pr_id"`
	MultiAgentRunID *string        `json:"multi_agent_run_id"`
	Runs            []runTargetDTO `json:"runs"`
	// Reviews is always empty: the run is asynchronous.
	Reviews []reviewDTO `json:"reviews"`
}

func toStartResponse(r app.StartResult) startResponse {
	runs := make([]runTargetDTO, len(r.Runs))
	for i, t := range r.Runs {
		runs[i] = runTargetDTO{RunID: t.RunID, AgentID: t.AgentID, AgentName: t.AgentName}
	}
	return startResponse{PRID: r.PRID, MultiAgentRunID: r.MultiAgentRunID, Runs: runs, Reviews: []reviewDTO{}}
}

// ---- runs ----

type activeRunDTO struct {
	RunID     string      `json:"run_id"`
	AgentID   *string     `json:"agent_id"`
	AgentName *string     `json:"agent_name"`
	RanAt     *httpx.Time `json:"ran_at"`
}

func toActiveRuns(runs []domain.ActiveRun) []activeRunDTO {
	out := make([]activeRunDTO, len(runs))
	for i, r := range runs {
		out[i] = activeRunDTO{RunID: r.RunID, AgentID: r.AgentID, AgentName: r.AgentName, RanAt: wireTime(r.RanAt)}
	}
	return out
}

type runSummaryDTO struct {
	RunID         string      `json:"run_id"`
	AgentID       *string     `json:"agent_id"`
	AgentName     *string     `json:"agent_name"`
	Provider      *string     `json:"provider"`
	Model         *string     `json:"model"`
	Status        *string     `json:"status"`
	Error         *string     `json:"error"`
	DurationMS    *int        `json:"duration_ms"`
	TokensIn      *int        `json:"tokens_in"`
	TokensOut     *int        `json:"tokens_out"`
	CostUSD       *float64    `json:"cost_usd"`
	FindingsCount *int        `json:"findings_count"`
	Grounding     *string     `json:"grounding"`
	RanAt         *httpx.Time `json:"ran_at"`
	Score         *int        `json:"score"`
	Blockers      *int        `json:"blockers"`
}

func toRunSummaries(runs []domain.RunSummary) []runSummaryDTO {
	out := make([]runSummaryDTO, len(runs))
	for i, r := range runs {
		out[i] = runSummaryDTO{
			RunID: r.RunID, AgentID: r.AgentID, AgentName: r.AgentName, Provider: r.Provider, Model: r.Model,
			Status: statusPtr(r.Status), Error: r.Error, DurationMS: r.DurationMS, TokensIn: r.TokensIn,
			TokensOut: r.TokensOut, CostUSD: r.CostUSD, FindingsCount: r.FindingsCount, Grounding: r.Grounding,
			RanAt: wireTime(r.RanAt), Score: r.Score, Blockers: r.Blockers,
		}
	}
	return out
}

// ---- findings ----

// memoryRef carries memory_id: present (a pointer to it) only where the spec says so, and then
// either the memory entry's id or null.
type memoryRef struct{ ID *string }

// MarshalJSON writes the id or null.
func (m memoryRef) MarshalJSON() ([]byte, error) {
	if m.ID == nil {
		return []byte("null"), nil
	}
	return jsonString(*m.ID), nil
}

type findingDTO struct {
	ID                 string      `json:"id"`
	Severity           string      `json:"severity"`
	Category           string      `json:"category"`
	Title              string      `json:"title"`
	File               string      `json:"file"`
	StartLine          int         `json:"start_line"`
	EndLine            int         `json:"end_line"`
	Rationale          string      `json:"rationale"`
	Suggestion         *string     `json:"suggestion"`
	Confidence         float64     `json:"confidence"`
	Kind               string      `json:"kind"`
	TrifectaComponents []string    `json:"trifecta_components"`
	Evidence           any         `json:"evidence"` // always null
	Skill              *string     `json:"skill"`
	OutOfScope         bool        `json:"out_of_scope"`
	ReviewID           string      `json:"review_id"`
	AcceptedAt         *httpx.Time `json:"accepted_at"`
	DismissedAt        *httpx.Time `json:"dismissed_at"`
	EvalCaseID         *string     `json:"eval_case_id"`
	MemoryID           *memoryRef  `json:"memory_id,omitempty"`
}

// toFinding maps a stored finding without memory_id.
func toFinding(f domain.FindingRecord) findingDTO {
	return findingDTO{
		ID: f.ID, Severity: string(f.Severity), Category: string(f.Category), Title: f.Title, File: f.File,
		StartLine: f.StartLine, EndLine: f.EndLine, Rationale: f.Rationale, Suggestion: f.Suggestion,
		Confidence: f.Confidence, Kind: string(f.Kind), TrifectaComponents: f.TrifectaComponents,
		Skill: f.Skill, OutOfScope: f.OutOfScope, ReviewID: f.ReviewID,
		AcceptedAt: wireTime(f.AcceptedAt), DismissedAt: wireTime(f.DismissedAt), EvalCaseID: f.EvalCaseID,
	}
}

// toColumnFinding maps a finding of a multi-agent column: memory_id is always present.
func toColumnFinding(f domain.FindingRecord) findingDTO {
	d := toFinding(f)
	d.MemoryID = &memoryRef{ID: f.MemoryID}
	return d
}

// ---- reviews ----

// reviewHead holds the keys of a review up to created_at; reviewDTO and reviewWithUsageDTO add
// what follows. cost_usd, tokens_in and tokens_out are present only when the review has a run.
type reviewHead struct {
	ID        string     `json:"id"`
	PRID      string     `json:"pr_id"`
	AgentID   *string    `json:"agent_id"`
	RunID     *string    `json:"run_id"`
	AgentName *string    `json:"agent_name"`
	Kind      string     `json:"kind"`
	Verdict   *string    `json:"verdict"`
	Summary   *string    `json:"summary"`
	Score     *int       `json:"score"`
	Model     *string    `json:"model"`
	CreatedAt httpx.Time `json:"created_at"`
}

type reviewDTO struct {
	reviewHead
	Findings []findingDTO `json:"findings"`
}

type reviewWithUsageDTO struct {
	reviewHead
	CostUSD   *float64     `json:"cost_usd"`
	TokensIn  *int         `json:"tokens_in"`
	TokensOut *int         `json:"tokens_out"`
	Findings  []findingDTO `json:"findings"`
}

func toReviews(rs []domain.ReviewRecord) []any {
	out := make([]any, len(rs))
	for i, r := range rs {
		head := reviewHead{
			ID: r.ID, PRID: r.PRID, AgentID: r.AgentID, RunID: r.RunID, AgentName: r.AgentName, Kind: r.Kind,
			Verdict: r.Verdict, Summary: r.Summary, Score: r.Score, Model: r.Model, CreatedAt: httpx.NewTime(r.CreatedAt),
		}
		findings := make([]findingDTO, len(r.Findings))
		for j, f := range r.Findings {
			findings[j] = toFinding(f)
		}
		if r.RunID == nil || r.Usage == nil {
			out[i] = reviewDTO{reviewHead: head, Findings: findings}
			continue
		}
		out[i] = reviewWithUsageDTO{
			reviewHead: head, CostUSD: r.Usage.CostUSD, TokensIn: r.Usage.TokensIn, TokensOut: r.Usage.TokensOut,
			Findings: findings,
		}
	}
	return out
}

// ---- multi-agent ----

type agentColumnDTO struct {
	RunID      string       `json:"run_id"`
	AgentID    string       `json:"agent_id"`
	AgentName  string       `json:"agent_name"`
	Provider   *string      `json:"provider"`
	Model      *string      `json:"model"`
	Status     string       `json:"status"`
	Error      *string      `json:"error"`
	Verdict    *string      `json:"verdict"`
	Score      *int         `json:"score"`
	Summary    *string      `json:"summary"`
	DurationMS *int         `json:"duration_ms"`
	CostUSD    *float64     `json:"cost_usd"`
	Findings   []findingDTO `json:"findings"`
}

type conflictTakeDTO struct {
	AgentID    string   `json:"agent_id"`
	Persona    string   `json:"persona"`
	RunID      string   `json:"run_id"`
	Verdict    string   `json:"verdict"`
	Note       string   `json:"note"`
	FindingIDs []string `json:"finding_ids"`
}

type conflictDTO struct {
	File       string            `json:"file"`
	Line       int               `json:"line"`
	EndLine    int               `json:"end_line"`
	Title      string            `json:"title"`
	IsConflict bool              `json:"is_conflict"`
	Takes      []conflictTakeDTO `json:"takes"`
}

type multiAgentRunDTO struct {
	ID              string           `json:"id"`
	PRID            string           `json:"pr_id"`
	RepoID          string           `json:"repo_id"`
	PRNumber        *int             `json:"pr_number"`
	PRTitle         *string          `json:"pr_title"`
	RanAt           httpx.Time       `json:"ran_at"`
	AgentCount      int              `json:"agent_count"`
	TotalDurationMS int              `json:"total_duration_ms"`
	TotalCostUSD    *float64         `json:"total_cost_usd"`
	Columns         []agentColumnDTO `json:"columns"`
	Conflicts       []conflictDTO    `json:"conflicts"`
}

func toMultiAgentRun(r domain.MultiAgentRun) multiAgentRunDTO {
	columns := make([]agentColumnDTO, len(r.Columns))
	for i, c := range r.Columns {
		findings := make([]findingDTO, len(c.Findings))
		for j, f := range c.Findings {
			findings[j] = toColumnFinding(f)
		}
		columns[i] = agentColumnDTO{
			RunID: c.RunID, AgentID: c.AgentID, AgentName: c.AgentName, Provider: c.Provider, Model: c.Model,
			Status: string(c.Status), Error: c.Error, Verdict: c.Verdict, Score: c.Score, Summary: c.Summary,
			DurationMS: c.DurationMS, CostUSD: c.CostUSD, Findings: findings,
		}
	}
	conflicts := make([]conflictDTO, len(r.Conflicts))
	for i, c := range r.Conflicts {
		takes := make([]conflictTakeDTO, len(c.Takes))
		for j, t := range c.Takes {
			ids := t.FindingIDs
			if ids == nil {
				ids = []string{}
			}
			takes[j] = conflictTakeDTO{
				AgentID: t.AgentID, Persona: t.Persona, RunID: t.RunID, Verdict: t.Verdict, Note: t.Note, FindingIDs: ids,
			}
		}
		conflicts[i] = conflictDTO{
			File: c.File, Line: c.Line, EndLine: c.EndLine, Title: c.Title, IsConflict: c.IsConflict, Takes: takes,
		}
	}
	return multiAgentRunDTO{
		ID: r.ID, PRID: r.PRID, RepoID: r.RepoID, PRNumber: r.PRNumber, PRTitle: r.PRTitle,
		RanAt: httpx.NewTime(r.RanAt), AgentCount: r.AgentCount, TotalDurationMS: r.TotalDurationMS,
		TotalCostUSD: r.TotalCostUSD, Columns: columns, Conflicts: conflicts,
	}
}

type multiAgentSummaryDTO struct {
	ID           string     `json:"id"`
	PRID         string     `json:"pr_id"`
	PRNumber     *int       `json:"pr_number"`
	PRTitle      *string    `json:"pr_title"`
	RanAt        httpx.Time `json:"ran_at"`
	AgentCount   int        `json:"agent_count"`
	RunningCount int        `json:"running_count"`
	TotalCostUSD *float64   `json:"total_cost_usd"`
}

func toMultiAgentSummaries(rs []domain.MultiAgentSummary) []multiAgentSummaryDTO {
	out := make([]multiAgentSummaryDTO, len(rs))
	for i, r := range rs {
		out[i] = multiAgentSummaryDTO{
			ID: r.ID, PRID: r.PRID, PRNumber: r.PRNumber, PRTitle: r.PRTitle, RanAt: httpx.NewTime(r.RanAt),
			AgentCount: r.AgentCount, RunningCount: r.RunningCount, TotalCostUSD: r.TotalCostUSD,
		}
	}
	return out
}

type estimateDTO struct {
	AgentID       string   `json:"agent_id"`
	SampleSize    int      `json:"sample_size"`
	AvgDurationMS *float64 `json:"avg_duration_ms"`
	AvgCostUSD    *float64 `json:"avg_cost_usd"`
}

func toEstimates(es []domain.AgentRunEstimate) []estimateDTO {
	out := make([]estimateDTO, len(es))
	for i, e := range es {
		out[i] = estimateDTO{
			AgentID: e.AgentID, SampleSize: e.SampleSize, AvgDurationMS: e.AvgDurationMS, AvgCostUSD: e.AvgCostUSD,
		}
	}
	return out
}

// ---- helpers ----

// wireTime maps a nullable timestamp to the wire type.
func wireTime(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	w := httpx.NewTime(*t)
	return &w
}

// statusPtr maps a run status to a nullable string: the empty status is a NULL column.
func statusPtr(s domain.RunStatus) *string {
	if s == "" {
		return nil
	}
	v := string(s)
	return &v
}

// jsonString encodes s as a JSON string.
func jsonString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

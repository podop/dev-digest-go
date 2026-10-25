package workspacehttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"dev-digest-go/backend/internal/platform/httpx"
	"dev-digest-go/backend/internal/workspace/app"
	"dev-digest-go/backend/internal/workspace/domain"
)

// repoResponse is one repository of GET /workspace (contract: WorkspaceRepo in
// contracts/workspace.ts); all keys are always present.
type repoResponse struct {
	ID           string      `json:"id"`
	FullName     string      `json:"full_name"`
	ClonePath    *string     `json:"clone_path"`
	LastPolledAt *httpx.Time `json:"last_polled_at"`
	Cloned       bool        `json:"cloned"`
}

// infoResponse is the wire shape of GET /workspace and of the PUT /workspace answer (contract:
// WorkspaceInfo). The camelCase keys are the pre-existing casing of this endpoint.
type infoResponse struct {
	WorkspaceID string         `json:"workspaceId"`
	Name        string         `json:"name"`
	DataDir     string         `json:"dataDir"`
	CloneDir    string         `json:"cloneDir"`
	Repos       []repoResponse `json:"repos"`
}

func toInfoResponse(in app.Info) infoResponse {
	repos := make([]repoResponse, len(in.Repos))
	for i, r := range in.Repos {
		repos[i] = repoResponse{ID: r.ID, FullName: r.FullName, ClonePath: r.ClonePath, Cloned: r.Cloned()}
		if r.LastPolledAt != nil {
			t := httpx.NewTime(*r.LastPolledAt)
			repos[i].LastPolledAt = &t
		}
	}
	return infoResponse{WorkspaceID: in.WorkspaceID, Name: in.Name, DataDir: in.DataDir, CloneDir: in.CloneDir, Repos: repos}
}

// statsResponse is the wire shape of GET /workspace/stats (contract: WorkspaceStats).
type statsResponse struct {
	Agents    int64 `json:"agents"`
	Skills    int64 `json:"skills"`
	EvalCases int64 `json:"eval_cases"`
	Learnings int64 `json:"learnings"`
	Repos     int64 `json:"repos"`
	Runs      int64 `json:"runs"`
}

func toStatsResponse(s domain.Stats) statsResponse {
	return statsResponse{
		Agents: s.Agents, Skills: s.Skills, EvalCases: s.EvalCases,
		Learnings: s.Learnings, Repos: s.Repos, Runs: s.Runs,
	}
}

// deletedResponse holds the per-kind counts of a reset (contract: WorkspaceResetDeleted).
type deletedResponse struct {
	AgentRuns       int64 `json:"agent_runs"`
	RunTraces       int64 `json:"run_traces"`
	Reviews         int64 `json:"reviews"`
	Findings        int64 `json:"findings"`
	MultiAgentRuns  int64 `json:"multi_agent_runs"`
	EvalSuiteRuns   int64 `json:"eval_suite_runs"`
	EvalRuns        int64 `json:"eval_runs"`
	CIRuns          int64 `json:"ci_runs"`
	ComposedReviews int64 `json:"composed_reviews"`
	Learnings       int64 `json:"learnings"`
}

// resetResponse is the wire shape of the POST /workspace/reset-history answer (contract:
// WorkspaceResetResult).
type resetResponse struct {
	Deleted     deletedResponse `json:"deleted"`
	PRsUnmarked int64           `json:"prs_unmarked"`
}

func toResetResponse(d domain.Deleted) resetResponse {
	return resetResponse{
		Deleted: deletedResponse{
			AgentRuns: d.AgentRuns, RunTraces: d.RunTraces, Reviews: d.Reviews, Findings: d.Findings,
			MultiAgentRuns: d.MultiAgentRuns, EvalSuiteRuns: d.EvalSuiteRuns, EvalRuns: d.EvalRuns,
			CIRuns: d.CIRuns, ComposedReviews: d.ComposedReviews, Learnings: d.Learnings,
		},
		PRsUnmarked: d.PRsUnmarked,
	}
}

// parseRenameBody reads the PUT /workspace body {name: string} (no other key), like the strict
// Zod object of the contract. The name rules (trim, 1 to 80 characters, no control character)
// are the service's; the text is passed on as sent.
func parseRenameBody(body []byte) (string, error) {
	return parseStringField(body, "name")
}

// parseResetBody reads the POST /workspace/reset-history body {confirm: string} (no other key).
// The text is passed on as sent: the confirmation is compared untrimmed and case-sensitive.
func parseResetBody(body []byte) (string, error) {
	return parseStringField(body, "confirm")
}

// parseStringField validates a body that is exactly one object with the single string key
// field. Failures are 422 issues in Zod's wording.
func parseStringField(body []byte, field string) (string, error) {
	var doc any
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&doc); err != nil {
		// No body at all is a missing object; any other unparsable text is a string.
		if errors.Is(err, io.EOF) {
			return "", httpx.NewValidationError(httpx.RequiredIssue())
		}
		return "", httpx.NewValidationError(httpx.TypeIssue("object", "string"))
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return "", httpx.NewValidationError(httpx.TypeIssue("object", zodType(doc)))
	}
	var issues []httpx.Issue
	value, present := obj[field]
	text, isString := value.(string)
	switch {
	case !present:
		issues = append(issues, httpx.RequiredIssue(field))
	case !isString:
		issues = append(issues, httpx.TypeIssue("string", zodType(value), field))
	}
	var extra []string
	for k := range obj {
		if k != field {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		quoted := make([]string, len(extra))
		for i, k := range extra {
			quoted[i] = "'" + k + "'"
		}
		issues = append(issues, httpx.Issue{
			Code: "unrecognized_keys", Path: []any{},
			Message: fmt.Sprintf("Unrecognized key(s) in object: %s", strings.Join(quoted, ", ")),
		})
	}
	if len(issues) > 0 {
		return "", httpx.NewValidationError(issues...)
	}
	return text, nil
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

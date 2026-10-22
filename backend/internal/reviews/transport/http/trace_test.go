package reviewshttp_test

import (
	"testing"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

func TestTrace_CompletedKeepsEveryKeyInContractOrder(t *testing.T) {
	api := &fakeAPI{trace: domain.Trace{
		Config: domain.TraceConfig{Agent: "Sec", Version: "3", Provider: "openrouter", Model: "m", PR: 482, Source: "local"},
		Stats:  domain.TraceStats{DurationMS: 10, TokensIn: 2, TokensOut: 1, CostUSD: ptr(0.00093), Findings: 3, Grounding: "3/5 passed"},
		PromptAssembly: reviewer.Assembly{
			System: "sys", Skills: ptr("sk"), Specs: ptr("sp"), Callers: ptr("ca"), RepoMap: nil, PRDescription: ptr("d"), User: "u",
		},
		ToolCalls:  []domain.ToolCall{{Tool: "review_file", Args: "all files", Meta: "single-pass", MS: 10}},
		RawOutput:  "raw",
		SpecsRead:  []string{"docs/a.md"},
		Log:        []domain.LogLine{{T: "12:00:00", Kind: reviewer.EventTool, Msg: "Loading PR diff…"}},
		SkillsUsed: []domain.SkillUsed{{ID: "s1", Name: "rubric", Version: 5}},
		ProjectContext: &domain.ContextTrace{BudgetTokens: 16000, TokensTotal: 20, Docs: []domain.ContextDoc{
			{Path: "docs/a.md", DocType: "architecture", Origin: domain.ContextOrigin{Kind: "agent"}, Tokens: 12, Status: domain.ContextIncluded, Source: "repo", Text: ptr("")},
			{Path: "b.md", DocType: "spec", Origin: domain.ContextOrigin{Kind: "skill", SkillID: "s1", SkillName: "rubric"}, Tokens: 8, Status: domain.ContextIncluded, Source: "store", Text: ptr("T")},
			{Path: "huge.md", DocType: "other", Origin: domain.ContextOrigin{Kind: "agent"}, Status: domain.ContextTooLarge, Source: "repo"},
		}},
	}}
	srv := newServer(t, api)
	wantJSON(t, send(t, srv, "GET", "/runs/"+otherID+"/trace", ""), 200,
		`{"config":{"agent":"Sec","version":"3","provider":"openrouter","model":"m","pr":482,"source":"local"},`+
			`"stats":{"duration_ms":10,"tokens_in":2,"tokens_out":1,"cost_usd":0.00093,"findings":3,"grounding":"3/5 passed"},`+
			`"prompt_assembly":{"system":"sys","skills":"sk","memory":null,"specs":"sp","callers":"ca","repo_map":null,"pr_description":"d","intent":null,"user":"u"},`+
			`"tool_calls":[{"tool":"review_file","args":"all files","meta":"single-pass","ms":10}],"raw_output":"raw","memory_pulled":[],`+
			`"specs_read":["docs/a.md"],"log":[{"t":"12:00:00","kind":"tool","msg":"Loading PR diff…"}],`+
			`"skills_used":[{"id":"s1","name":"rubric","version":5}],"intent":null,`+
			`"project_context":{"budget_tokens":16000,"tokens_total":20,"docs":[`+
			`{"path":"docs/a.md","doc_type":"architecture","origin":{"kind":"agent"},"tokens":12,"status":"included","source":"repo","text":""},`+
			`{"path":"b.md","doc_type":"spec","origin":{"kind":"skill","skill_id":"s1","skill_name":"rubric"},"tokens":8,"status":"included","source":"store","text":"T"},`+
			`{"path":"huge.md","doc_type":"other","origin":{"kind":"agent"},"tokens":0,"status":"too_large","source":"repo"}]}}`)
}

// The ended trace of a failed, cancelled or diff-failed run: a reduced prompt_assembly, a null
// cost of 0, no project_context, and empty (not null) lists.
func TestTrace_EndedHasTheReducedAssemblyAndEmptyLists(t *testing.T) {
	api := &fakeAPI{trace: domain.Trace{
		Ended:          true,
		Config:         domain.TraceConfig{Agent: "Sec", Version: "3", Provider: "openrouter", Model: "m", PR: 482, Source: "local"},
		Stats:          domain.TraceStats{DurationMS: 33, CostUSD: ptr(0.0), Grounding: domain.NoGrounding},
		PromptAssembly: reviewer.Assembly{System: "You are the Security Reviewer."},
	}}
	srv := newServer(t, api)
	wantJSON(t, send(t, srv, "GET", "/runs/"+otherID+"/trace", ""), 200,
		`{"config":{"agent":"Sec","version":"3","provider":"openrouter","model":"m","pr":482,"source":"local"},`+
			`"stats":{"duration_ms":33,"tokens_in":0,"tokens_out":0,"cost_usd":0,"findings":0,"grounding":"0/0 passed"},`+
			`"prompt_assembly":{"system":"You are the Security Reviewer.","skills":null,"memory":null,"specs":null,"user":""},`+
			`"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[],"skills_used":[],"intent":null}`)
}

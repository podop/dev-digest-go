package postgres_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/adapters/postgres"
	"dev-digest-go/backend/internal/reviews/domain"
)

func completedTrace() domain.Trace {
	agent := domain.ReviewAgent{Name: "Alpha", Version: 3, Provider: "openai", Model: "gpt", SystemPrompt: "be careful"}
	callers := "### a.go\n- `f` — func f()"
	return domain.CompletedTrace(domain.CompletedTraceInput{
		Agent: agent, PRNumber: 482, DurationMS: 900, Usage: reviewer.Usage{TokensIn: 10, TokensOut: 4, CostUSD: floatp(0.0000012)},
		Findings: 2, Grounding: "2/2 passed",
		Assembly:    reviewer.Assembly{System: "be careful\n\nguard", Skills: strp("### s"), Callers: &callers, User: "user <message> & more"},
		ChunkLabels: []string{"a.go", "b.go"}, Mode: reviewer.Mode("map-reduce"), Raw: "raw",
		Log:        []domain.LogLine{{T: "10:00:00", Kind: reviewer.EventInfo, Msg: "hi — there…"}},
		SkillsUsed: []domain.SkillUsed{{ID: "s1", Name: "s", Version: 2}},
		ProjectContext: domain.NewContextTrace([]domain.ContextDoc{
			{Path: "docs/a.md", DocType: "spec", Origin: domain.ContextOrigin{Kind: "agent"}, Tokens: 5, Status: domain.ContextIncluded, Source: "repo", Text: strp("text")},
			{Path: "docs/b.md", DocType: "doc", Origin: domain.ContextOrigin{Kind: "skill", SkillID: "s1", SkillName: "s"}, Tokens: 9, Status: domain.ContextOverBudget, Source: "store"},
		}, 5),
	})
}

func endedTrace() domain.Trace {
	usage := reviewer.Usage{TokensIn: 3, TokensOut: 1, CostUSD: floatp(0.5)}
	return domain.EndedTrace(domain.EndedTraceInput{
		Agent:    domain.ReviewAgent{Name: "Alpha", Version: 1, Provider: "openai", Model: "gpt", SystemPrompt: "be careful"},
		PRNumber: 482, DurationMS: 40, Usage: &usage,
		Log: []domain.LogLine{{T: "10:00:01", Kind: reviewer.EventError, Msg: "Run cancelled by user"}},
	})
}

func TestTraceDoc_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		trace domain.Trace
	}{
		{"completed", completedTrace()},
		{"ended", endedTrace()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := postgres.EncodeTrace(tc.trace)
			if err != nil {
				t.Fatalf("EncodeTrace() error = %v", err)
			}
			got, err := postgres.DecodeTrace(raw)
			if err != nil {
				t.Fatalf("DecodeTrace(EncodeTrace()) error = %v", err)
			}
			if diff := cmp.Diff(tc.trace, got); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEncodeTrace_Shape(t *testing.T) {
	keys := func(t *testing.T, raw []byte) map[string]json.RawMessage {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	assembly := func(t *testing.T, doc map[string]json.RawMessage) map[string]json.RawMessage {
		t.Helper()
		return keys(t, doc["prompt_assembly"])
	}

	t.Run("completed has every assembly key and the constants", func(t *testing.T) {
		raw, err := postgres.EncodeTrace(completedTrace())
		if err != nil {
			t.Fatal(err)
		}
		doc := keys(t, raw)
		if string(doc["memory_pulled"]) != "[]" || string(doc["intent"]) != "null" {
			t.Errorf("memory_pulled/intent = %s/%s, want []/null", doc["memory_pulled"], doc["intent"])
		}
		a := assembly(t, doc)
		for _, k := range []string{"system", "skills", "memory", "specs", "callers", "repo_map", "pr_description", "intent", "user"} {
			if _, ok := a[k]; !ok {
				t.Errorf("completed prompt_assembly lacks key %q", k)
			}
		}
		if string(a["memory"]) != "null" || string(a["repo_map"]) != "null" || string(a["intent"]) != "null" {
			t.Errorf("memory/repo_map/intent = %s/%s/%s, want null", a["memory"], a["repo_map"], a["intent"])
		}
		if _, ok := doc["project_context"]; !ok {
			t.Errorf("project_context missing")
		}
		if !strings.Contains(string(raw), "<message> & more") {
			t.Errorf("document escapes HTML characters: %s", raw)
		}
	})

	t.Run("ended assembly has only system, skills, memory, specs and user", func(t *testing.T) {
		raw, err := postgres.EncodeTrace(endedTrace())
		if err != nil {
			t.Fatal(err)
		}
		doc := keys(t, raw)
		a := assembly(t, doc)
		if len(a) != 5 {
			t.Errorf("ended prompt_assembly keys = %v, want system, skills, memory, specs, user", a)
		}
		for _, k := range []string{"skills", "memory", "specs"} {
			if string(a[k]) != "null" {
				t.Errorf("ended %s = %s, want null", k, a[k])
			}
		}
		if string(a["user"]) != `""` || string(a["system"]) != `"be careful"` {
			t.Errorf("ended system/user = %s/%s", a["system"], a["user"])
		}
		if _, ok := doc["project_context"]; ok {
			t.Errorf("project_context present without documents")
		}
		if string(doc["tool_calls"]) != "[]" || string(doc["raw_output"]) != `""` {
			t.Errorf("tool_calls/raw_output = %s/%s", doc["tool_calls"], doc["raw_output"])
		}
	})
}

func TestEncodeTrace_MakesTextStorable(t *testing.T) {
	tr := completedTrace()
	tr.RawOutput = "a\x00b\xffc"
	tr.Log = []domain.LogLine{{T: "t", Kind: reviewer.EventInfo, Msg: "x\x00y"}}
	tr.PromptAssembly.User = "u\x00"
	raw, err := postgres.EncodeTrace(tr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u0000`) || strings.ContainsRune(string(raw), 0) {
		t.Errorf("document still holds a NUL: %q", raw)
	}
	got, err := postgres.DecodeTrace(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.RawOutput != "ab�c" || got.Log[0].Msg != "xy" || got.PromptAssembly.User != "u" {
		t.Errorf("sanitised fields = %q, %q, %q", got.RawOutput, got.Log[0].Msg, got.PromptAssembly.User)
	}
}

const minimalDoc = `{"config":{"agent":"A","model":"m"},"stats":{"duration_ms":1,"tokens_in":2,"tokens_out":3,"findings":0,"grounding":"0/0 passed"},
"prompt_assembly":{"system":"s","user":"u"},"tool_calls":[],"raw_output":"","memory_pulled":[],"specs_read":[],"log":[]}`

func TestDecodeTrace_AcceptsTheContract(t *testing.T) {
	got, err := postgres.DecodeTrace([]byte(minimalDoc))
	if err != nil {
		t.Fatalf("DecodeTrace(minimal) error = %v", err)
	}
	if got.Config.Source != "local" {
		t.Errorf("config.source = %q, want local when absent", got.Config.Source)
	}
	if got.SkillsUsed != nil || got.ProjectContext != nil || got.Config.PR != 0 || got.Stats.CostUSD != nil {
		t.Errorf("optional parts = %+v", got)
	}
	if got.Ended {
		t.Errorf("Ended = true for a document with a user message")
	}
	ended := strings.Replace(minimalDoc, `"user":"u"`, `"user":""`, 1)
	if got, err := postgres.DecodeTrace([]byte(ended)); err != nil || !got.Ended {
		t.Errorf("DecodeTrace(ended shape) Ended = %v, %v, want true", got.Ended, err)
	}

	// 1.0 is an integer in JavaScript, and unknown keys are ignored.
	doc := strings.Replace(minimalDoc, `"duration_ms":1`, `"duration_ms":1.0,"extra":true`, 1)
	if got, err := postgres.DecodeTrace([]byte(doc)); err != nil || got.Stats.DurationMS != 1 {
		t.Errorf("DecodeTrace(1.0) = %d, %v, want 1, nil", got.Stats.DurationMS, err)
	}
	doc = strings.Replace(minimalDoc, `"model":"m"`, `"model":"m","source":"ci"`, 1)
	if got, err := postgres.DecodeTrace([]byte(doc)); err != nil || got.Config.Source != "ci" {
		t.Errorf("DecodeTrace(source ci) = %q, %v", got.Config.Source, err)
	}
	doc = strings.Replace(minimalDoc, `"model":"m"`, `"model":"m","source":"auto"`, 1)
	if got, err := postgres.DecodeTrace([]byte(doc)); err != nil || got.Config.Source != "auto" {
		t.Errorf("DecodeTrace(source auto) = %q, %v", got.Config.Source, err)
	}
	doc = strings.Replace(minimalDoc, `"model":"m"`, `"model":"m","source":"cron"`, 1)
	if _, err := postgres.DecodeTrace([]byte(doc)); !errors.Is(err, domain.ErrTraceInvalid) {
		t.Errorf("DecodeTrace(source cron) error = %v, want ErrTraceInvalid", err)
	}
}

func TestDecodeTrace_RejectsContractViolations(t *testing.T) {
	tests := []struct {
		name, old, new string
		wantPath       string
	}{
		{"duration is a string", `"duration_ms":1`, `"duration_ms":"x"`, "stats.duration_ms"},
		{"duration is a quoted number", `"duration_ms":1`, `"duration_ms":"12"`, "stats.duration_ms"},
		{"duration is fractional", `"duration_ms":1`, `"duration_ms":1.5`, "stats.duration_ms"},
		{"agent missing", `"agent":"A",`, ``, "config.agent"},
		{"model null", `"model":"m"`, `"model":null`, "config.model"},
		{"source unknown", `"model":"m"`, `"model":"m","source":"cloud"`, "config.source"},
		{"config missing", `"config":{"agent":"A","model":"m"},`, ``, "config"},
		{"tool_calls null", `"tool_calls":[]`, `"tool_calls":null`, "tool_calls"},
		{"memory_pulled missing", `"memory_pulled":[],`, ``, "memory_pulled"},
		{"log kind unknown", `"log":[]`, `"log":[{"t":"1","kind":"debug","msg":"m"}]`, "log.kind"},
		{"log entry null", `"log":[]`, `"log":[null]`, "log[0]"},
		{"specs_read not strings", `"specs_read":[]`, `"specs_read":[1]`, "specs_read"},
		{"system missing", `"system":"s",`, ``, "prompt_assembly.system"},
		{"user is a number", `"user":"u"`, `"user":5`, "prompt_assembly.user"},
		{"raw_output missing", `"raw_output":"",`, ``, "raw_output"},
		{"intent has no status", `"log":[]`, `"log":[],"intent":{}`, "intent.status"},
		{"project context doc status", `"log":[]`, `"log":[],"project_context":{"budget_tokens":1,"tokens_total":1,"docs":[{"path":"p","doc_type":"d","origin":{"kind":"agent"},"tokens":1,"status":"weird"}]}`, "project_context.docs.status"},
		{"skill origin without name", `"log":[]`, `"log":[],"project_context":{"budget_tokens":1,"tokens_total":1,"docs":[{"path":"p","doc_type":"d","origin":{"kind":"skill","skill_id":"x"},"tokens":1,"status":"included"}]}`, "origin.skill_name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(minimalDoc, tc.old) {
				t.Fatalf("test bug: %q not in the base document", tc.old)
			}
			doc := strings.Replace(minimalDoc, tc.old, tc.new, 1)
			_, err := postgres.DecodeTrace([]byte(doc))
			if !errors.Is(err, domain.ErrTraceInvalid) {
				t.Fatalf("DecodeTrace() error = %v, want ErrTraceInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Errorf("error %q does not name %q", err, tc.wantPath)
			}
		})
	}
	for _, raw := range []string{`[]`, `"x"`, `{}`, `null`} {
		if _, err := postgres.DecodeTrace([]byte(raw)); !errors.Is(err, domain.ErrTraceInvalid) {
			t.Errorf("DecodeTrace(%s) error = %v, want ErrTraceInvalid", raw, err)
		}
	}
}

func TestDecodeTrace_ErrorDoesNotQuoteTheDocument(t *testing.T) {
	doc := strings.Replace(minimalDoc, `"grounding":"0/0 passed"`, `"grounding":"SECRET-VALUE-123"`, 1)
	doc = strings.Replace(doc, `"duration_ms":1`, `"duration_ms":"SECRET-VALUE-456"`, 1)
	_, err := postgres.DecodeTrace([]byte(doc))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("DecodeTrace() error = %v, want an error without document content", err)
	}
}

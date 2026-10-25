package domain_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

var traceAgent = domain.ReviewAgent{ID: "a1", Name: "General Reviewer", Provider: "openrouter", Model: "m/x", SystemPrompt: "Be careful.", Version: 3}

func TestCompletedTrace(t *testing.T) {
	skills := []domain.SkillUsed{{ID: "s1", Name: "rubric", Version: 5}}
	included := "doc text"
	pc := domain.NewContextTrace([]domain.ContextDoc{
		{Path: "docs/a.md", Status: domain.ContextIncluded, Text: &included},
		{Path: "docs/b.md", Status: domain.ContextTooLarge},
		{Path: "docs/c.md", Status: domain.ContextIncluded, Text: &included},
	}, 40)
	asm := reviewer.Assembly{System: "sys", User: "usr"}
	log := []domain.LogLine{{T: "10:00:00", Kind: reviewer.EventInfo, Msg: "x"}}

	got := domain.CompletedTrace(domain.CompletedTraceInput{
		Agent: traceAgent, PRNumber: 482, DurationMS: 1000,
		Usage:    reviewer.Usage{TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042)},
		Findings: 2, Grounding: "2/2 passed", Assembly: asm,
		ChunkLabels: []string{"all files"}, Mode: reviewer.ModeSinglePass, Raw: "raw",
		Log: log, SkillsUsed: skills, ProjectContext: pc,
	})

	want := domain.Trace{
		Config:         domain.TraceConfig{Agent: "General Reviewer", Version: "3", Provider: "openrouter", Model: "m/x", PR: 482, Source: "local"},
		Stats:          domain.TraceStats{DurationMS: 1000, TokensIn: 1200, TokensOut: 180, CostUSD: ptr(0.00042), Findings: 2, Grounding: "2/2 passed"},
		PromptAssembly: asm,
		ToolCalls:      []domain.ToolCall{{Tool: "review_file", Args: "all files", Meta: "single-pass", MS: 1000}},
		RawOutput:      "raw",
		SpecsRead:      []string{"docs/a.md", "docs/c.md"},
		Log:            log,
		SkillsUsed:     skills,
		ProjectContext: &domain.ContextTrace{BudgetTokens: 16000, TokensTotal: 40, Docs: pc.Docs},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CompletedTrace() mismatch (-want +got):\n%s", diff)
	}
}

func TestCompletedTrace_ToolCallTimeIsTheRoundedShare(t *testing.T) {
	tests := []struct {
		name     string
		duration int
		chunks   int
		want     int
	}{
		{"exact", 900, 3, 300},
		{"rounds down", 100, 3, 33},
		{"rounds up", 200, 3, 67},
		{"half rounds up like Math.round", 5, 2, 3},
		{"half rounds up again", 7, 2, 4},
		{"zero", 0, 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			labels := make([]string, tc.chunks)
			for i := range labels {
				labels[i] = "f"
			}
			got := domain.CompletedTrace(domain.CompletedTraceInput{Agent: traceAgent, DurationMS: tc.duration, ChunkLabels: labels, Mode: reviewer.ModeMapReduce})
			if len(got.ToolCalls) != tc.chunks {
				t.Fatalf("len(ToolCalls) = %d, want %d", len(got.ToolCalls), tc.chunks)
			}
			if got.ToolCalls[0].MS != tc.want || got.ToolCalls[0].Meta != "map-reduce" {
				t.Errorf("ToolCalls[0] = %+v, want ms %d, meta map-reduce", got.ToolCalls[0], tc.want)
			}
		})
	}
}

func TestCompletedTrace_SlicesAreNeverNil(t *testing.T) {
	got := domain.CompletedTrace(domain.CompletedTraceInput{Agent: traceAgent})
	if got.ToolCalls == nil || got.SpecsRead == nil || got.Log == nil || got.SkillsUsed == nil {
		t.Errorf("CompletedTrace() slices nil: tool calls %v, specs %v, log %v, skills %v", got.ToolCalls == nil, got.SpecsRead == nil, got.Log == nil, got.SkillsUsed == nil)
	}
	if got.ProjectContext != nil || got.Ended {
		t.Errorf("CompletedTrace() project context %v, ended %v, want nil, false", got.ProjectContext, got.Ended)
	}
}

func TestEndedTrace(t *testing.T) {
	log := []domain.LogLine{{T: "10:00:00", Kind: reviewer.EventError, Msg: "Run failed: x"}}
	t.Run("usage so far", func(t *testing.T) {
		got := domain.EndedTrace(domain.EndedTraceInput{
			Agent: traceAgent, PRNumber: 482, Log: log, DurationMS: 250,
			Usage: &reviewer.Usage{TokensIn: 10, TokensOut: 2, CostUSD: ptr(0.01)},
		})
		want := domain.Trace{
			Ended:          true,
			Config:         domain.TraceConfig{Agent: "General Reviewer", Version: "3", Provider: "openrouter", Model: "m/x", PR: 482, Source: "local"},
			Stats:          domain.TraceStats{DurationMS: 250, TokensIn: 10, TokensOut: 2, CostUSD: ptr(0.01), Grounding: "0/0 passed"},
			PromptAssembly: reviewer.Assembly{System: "Be careful."},
			ToolCalls:      []domain.ToolCall{},
			SpecsRead:      []string{},
			Log:            log,
			SkillsUsed:     []domain.SkillUsed{},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("EndedTrace() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("diff failure has zero usage and a zero cost", func(t *testing.T) {
		got := domain.EndedTrace(domain.EndedTraceInput{Agent: traceAgent, PRNumber: 1})
		if got.Stats.CostUSD == nil || *got.Stats.CostUSD != 0 || got.Stats.TokensIn != 0 || got.Stats.DurationMS != 0 {
			t.Errorf("EndedTrace().Stats = %+v (cost %v), want 0, 0, 0 and cost 0", got.Stats, got.Stats.CostUSD)
		}
	})
	t.Run("unpriced usage stays null", func(t *testing.T) {
		got := domain.EndedTrace(domain.EndedTraceInput{Agent: traceAgent, Usage: &reviewer.Usage{TokensIn: 5}})
		if got.Stats.CostUSD != nil {
			t.Errorf("EndedTrace().Stats.CostUSD = %v, want nil", *got.Stats.CostUSD)
		}
	})
	t.Run("keeps the context resolved before the end", func(t *testing.T) {
		pc := domain.NewContextTrace([]domain.ContextDoc{{Path: "d.md", Status: domain.ContextIncluded}}, 3)
		got := domain.EndedTrace(domain.EndedTraceInput{Agent: traceAgent, ProjectContext: pc})
		if diff := cmp.Diff([]string{"d.md"}, got.SpecsRead); diff != "" {
			t.Errorf("EndedTrace().SpecsRead mismatch (-want +got):\n%s", diff)
		}
		if got.ProjectContext != pc {
			t.Errorf("EndedTrace().ProjectContext = %v, want the given context", got.ProjectContext)
		}
	})
}

func TestNewContextTrace_DocsAreNeverNil(t *testing.T) {
	got := domain.NewContextTrace(nil, 0)
	if got.Docs == nil || got.BudgetTokens != 16000 {
		t.Errorf("NewContextTrace(nil, 0) = %+v, want empty non-nil docs and budget 16000", got)
	}
}

func TestLogFromEvents(t *testing.T) {
	events := []domain.RunEvent{
		{RunID: "r", Seq: 1, Kind: reviewer.EventTool, Msg: "a", T: "01:02:03", Data: []byte(`{"x":1}`)},
		{RunID: "r", Seq: 2, Kind: reviewer.EventResult, Msg: "b", T: "01:02:04"},
	}
	want := []domain.LogLine{{T: "01:02:03", Kind: reviewer.EventTool, Msg: "a"}, {T: "01:02:04", Kind: reviewer.EventResult, Msg: "b"}}
	if diff := cmp.Diff(want, domain.LogFromEvents(events)); diff != "" {
		t.Errorf("LogFromEvents() mismatch (-want +got):\n%s", diff)
	}
	if got := domain.LogFromEvents(nil); got == nil || len(got) != 0 {
		t.Errorf("LogFromEvents(nil) = %#v, want empty non-nil", got)
	}
}

func TestTraces_CarryTheIntentTrace(t *testing.T) {
	conf, derived, change := "medium", "explicit", "feature"
	used := &domain.IntentTrace{
		Status: domain.IntentStatusUsed, CacheHit: true, Confidence: &conf, DerivedFrom: &derived, ChangeType: &change,
		Provider: "openrouter", Model: "m/x", TokensIn: 10, TokensOut: 5,
		Sources: []reviewer.IntentSource{{Kind: "title", Ref: "title", Status: "used"}},
	}
	tests := []struct {
		name   string
		intent *domain.IntentTrace
	}{
		{"used", used},
		{"unavailable", domain.UnavailableIntentTrace("OPENROUTER_API_KEY is not configured")},
		{"none", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			completed := domain.CompletedTrace(domain.CompletedTraceInput{Agent: traceAgent, ChunkLabels: []string{"all files"}, Mode: reviewer.ModeSinglePass, Intent: tc.intent})
			if diff := cmp.Diff(tc.intent, completed.Intent); diff != "" {
				t.Errorf("CompletedTrace().Intent mismatch (-want +got):\n%s", diff)
			}
			ended := domain.EndedTrace(domain.EndedTraceInput{Agent: traceAgent, Intent: tc.intent})
			if diff := cmp.Diff(tc.intent, ended.Intent); diff != "" {
				t.Errorf("EndedTrace().Intent mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUnavailableIntentTrace(t *testing.T) {
	got := domain.UnavailableIntentTrace("boom")
	want := &domain.IntentTrace{Status: "unavailable", Warning: "boom"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("UnavailableIntentTrace(boom) mismatch (-want +got):\n%s", diff)
	}
}

func TestTraceConfigSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"empty is local", "", domain.SourceLocal},
		{"local", domain.SourceLocal, "local"},
		{"auto", domain.SourceAuto, "auto"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			done := domain.CompletedTrace(domain.CompletedTraceInput{Agent: traceAgent, PRNumber: 7, Source: tc.source})
			if done.Config.Source != tc.want {
				t.Errorf("CompletedTrace(source %q).Config.Source = %q, want %q", tc.source, done.Config.Source, tc.want)
			}
			ended := domain.EndedTrace(domain.EndedTraceInput{Agent: traceAgent, PRNumber: 7, Source: tc.source})
			if ended.Config.Source != tc.want {
				t.Errorf("EndedTrace(source %q).Config.Source = %q, want %q", tc.source, ended.Config.Source, tc.want)
			}
		})
	}
}

package domain_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/domain"
)

func TestFinalStatusEvent(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 5, 7, 0, time.UTC)
	boom := "OPENROUTER_API_KEY is not configured"
	tests := []struct {
		name   string
		status domain.RunStatus
		err    *string
		want   domain.RunEvent
	}{
		{
			name: "done is a result", status: domain.StatusDone,
			want: domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventResult, Msg: "Run finished", T: "09:05:07", Data: []byte(`{"status":"done","error":null}`)},
		},
		{
			name: "failed is an info, never an error, and carries the note", status: domain.StatusFailed, err: &boom,
			want: domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventInfo, Msg: "Run failed: " + boom, T: "09:05:07", Data: []byte(`{"status":"failed","error":"OPENROUTER_API_KEY is not configured"}`)},
		},
		{
			name: "cancelled without a note", status: domain.StatusCancelled,
			want: domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventInfo, Msg: "Run cancelled", T: "09:05:07", Data: []byte(`{"status":"cancelled","error":null}`)},
		},
		{
			name: "NULL status", status: "",
			want: domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventInfo, Msg: "Run ended", T: "09:05:07", Data: []byte(`{"status":null,"error":null}`)},
		},
		{
			name: "empty note is no note", status: domain.StatusFailed, err: ptr(""),
			want: domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventInfo, Msg: "Run failed", T: "09:05:07", Data: []byte(`{"status":"failed","error":""}`)},
		},
		{
			name: "html in the note is not escaped", status: domain.StatusFailed, err: ptr("a<b>&c"),
			want: domain.RunEvent{RunID: "r1", Seq: 1, Kind: reviewer.EventInfo, Msg: "Run failed: a<b>&c", T: "09:05:07", Data: []byte(`{"status":"failed","error":"a<b>&c"}`)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.FinalStatusEvent("r1", tc.status, tc.err, now)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FinalStatusEvent(%q) mismatch (-want +got):\n%s", tc.status, diff)
			}
		})
	}
}

func TestClockStamp_UsesTheTimesOwnZone(t *testing.T) {
	zone := time.FixedZone("x", 2*3600)
	if got, want := domain.ClockStamp(time.Date(2026, 1, 2, 23, 4, 5, 999, zone)), "23:04:05"; got != want {
		t.Errorf("ClockStamp() = %q, want %q", got, want)
	}
}

func TestEventMessages(t *testing.T) {
	skills := []domain.ReviewSkill{{ID: "s1", Name: "rubric", Version: 5}, {ID: "s2", Name: "other", Version: 1}}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"step start", domain.StepStart(domain.LoadingDiffLabel), "Loading PR diff…"},
		{"step done", domain.StepDone(domain.LoadingDiffLabel, 42), "Loading PR diff done (42ms)"},
		{"step failed", domain.StepFailed(domain.LoadingDiffLabel, 7, "no clone"), "Loading PR diff failed (7ms): no clone"},
		{"diff ready", domain.DiffReadyMsg(3, 2), "Diff ready — 3 changed file(s); starting 2 agent run(s)"},
		{"diff failed", domain.DiffFailedMsg("no clone"), "Failed to load PR diff: no clone"},
		{"agent start", domain.AgentStartMsg("General Reviewer", "openrouter", "m/x"), `Starting review with agent "General Reviewer" (openrouter/m/x)`},
		{"resolve provider", domain.StepStart(domain.ResolveProviderLabel("openrouter")), "Resolving openrouter provider…"},
		{"skills single pass", domain.SkillsLogLine(skills, 12, false), "skills: 2 attached (rubric v5, other v1) · ~12 tokens"},
		{"skills map-reduce", domain.SkillsLogLine(skills[:1], 3, true), "skills: 1 attached (rubric v5) · ~3 tokens (repeated in every map-reduce chunk)"},
		{"project context", domain.ProjectContextMsg(1, 2, 300), "project context: 1 included, 2 skipped · ~300 tokens"},
		{"project context unavailable", domain.ProjectContextUnavailableMsg("db down"), "warning: project context unavailable — db down"},
		{"repo intel off", domain.RepoIntelDisabledMsg, "Repo intel disabled for this agent — skipping context enrichment"},
		{"callers", domain.CallersAttachedMsg(4), "callers digest: 4 caller signature(s) attached"},
		{"callers failed", domain.CallersFailedMsg("timeout"), "callers digest: repoIntel failed — timeout"},
		{"repo map", domain.RepoMapAttachedMsg(900, false), "repo map: 900 token(s) attached (cached=false)"},
		{"repo map cached", domain.RepoMapAttachedMsg(900, true), "repo map: 900 token(s) attached (cached=true)"},
		{"repo map failed", domain.RepoMapFailedMsg("boom"), "repo map: repoIntel failed — boom"},
		{"file rank", domain.FileRankMsg(2, 4), "file rank: 2/4 changed file(s) in top 5%"},
		{"persisted", domain.PersistedMsg("rev-1", 2), "Persisted review rev-1 with 2 finding(s)"},
		{"complete", domain.RunCompleteMsg, "Run complete; trace persisted"},
		{"cancel route", domain.CancellationRequestedMsg, "Cancellation requested — stopping…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("message = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestEventData(t *testing.T) {
	skills := []domain.ReviewSkill{{ID: "s1", Name: "a<b", Version: 5, Body: "ignored"}}
	if got, want := string(domain.SkillsEventData(skills)), `{"skills":[{"id":"s1","name":"a<b","version":5}]}`; got != want {
		t.Errorf("SkillsEventData() = %s, want %s", got, want)
	}
	if got, want := string(domain.ProjectContextWarningData()), `{"warning":"project_context_unavailable"}`; got != want {
		t.Errorf("ProjectContextWarningData() = %s, want %s", got, want)
	}
}

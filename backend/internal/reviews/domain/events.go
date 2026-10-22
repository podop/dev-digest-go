package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// The fixed run-event messages of spec 7.5. "…" is U+2026, "—" U+2014.
const (
	// LoadingDiffLabel is the label of the shared diff step.
	LoadingDiffLabel = "Loading PR diff"
	// CancellationRequestedMsg is published by the cancel route.
	CancellationRequestedMsg = "Cancellation requested — stopping…"
	// RunCancelledMsg is the error event of a cancelled run.
	RunCancelledMsg = "Run cancelled by user"
	// RunCompleteMsg is the last event of a successful run, published after the trace log was taken.
	RunCompleteMsg = "Run complete; trace persisted"
	// RepoIntelDisabledMsg is published for an agent whose repo-intel toggle is off.
	RepoIntelDisabledMsg = "Repo intel disabled for this agent — skipping context enrichment"
	// RunFinishedMsg is the message of the synthetic event of a finished (done) run.
	RunFinishedMsg = "Run finished"
)

// ProjectContextWarning is the data.warning of the "project context unavailable" event.
const ProjectContextWarning = "project_context_unavailable"

// StepStart is the message announcing a step: "<label>…".
func StepStart(label string) string { return label + "…" }

// StepDone is the message of a finished step: "<label> done (<ms>ms)".
func StepDone(label string, ms int) string { return fmt.Sprintf("%s done (%dms)", label, ms) }

// StepFailed is the error message of a failed step: "<label> failed (<ms>ms): <message>".
func StepFailed(label string, ms int, message string) string {
	return fmt.Sprintf("%s failed (%dms): %s", label, ms, message)
}

// DiffReadyMsg announces the loaded diff and the number of runs about to start.
func DiffReadyMsg(files, runs int) string {
	return fmt.Sprintf("Diff ready — %d changed file(s); starting %d agent run(s)", files, runs)
}

// DiffFailedMsg is the error event that follows a failed diff step.
func DiffFailedMsg(message string) string { return "Failed to load PR diff: " + message }

// AgentStartMsg is the first per-agent event.
func AgentStartMsg(name, provider, model string) string {
	return fmt.Sprintf("Starting review with agent \"%s\" (%s/%s)", name, provider, model)
}

// ResolveProviderLabel is the label of the provider-resolution step.
func ResolveProviderLabel(provider string) string { return "Resolving " + provider + " provider" }

// ProjectContextMsg summarises the resolved documents.
func ProjectContextMsg(included, skipped, tokens int) string {
	return fmt.Sprintf("project context: %d included, %d skipped · ~%d tokens", included, skipped, tokens)
}

// ProjectContextUnavailableMsg is the info event for an internal context error; its data is
// ProjectContextWarningData.
func ProjectContextUnavailableMsg(reason string) string {
	return "warning: project context unavailable — " + reason
}

// CallersAttachedMsg reports the callers digest.
func CallersAttachedMsg(n int) string {
	return fmt.Sprintf("callers digest: %d caller signature(s) attached", n)
}

// CallersFailedMsg reports a failed callers lookup.
func CallersFailedMsg(message string) string {
	return "callers digest: repoIntel failed — " + message
}

// RepoMapAttachedMsg reports the attached repo map.
func RepoMapAttachedMsg(tokens int, cached bool) string {
	return fmt.Sprintf("repo map: %d token(s) attached (cached=%t)", tokens, cached)
}

// RepoMapFailedMsg reports a failed repo-map lookup.
func RepoMapFailedMsg(message string) string { return "repo map: repoIntel failed — " + message }

// FileRankMsg reports the hot changed files.
func FileRankMsg(hot, changed int) string {
	return fmt.Sprintf("file rank: %d/%d changed file(s) in top 5%%", hot, changed)
}

// PersistedMsg reports the stored review.
func PersistedMsg(reviewID string, findings int) string {
	return fmt.Sprintf("Persisted review %s with %d finding(s)", reviewID, findings)
}

// RunFailedMsg is the error event of a failed run.
func RunFailedMsg(note string) string { return "Run failed: " + note }

// EndMsg is the error event published when a run ends abnormally.
func EndMsg(e Ending) string {
	if e.Status == StatusCancelled {
		return RunCancelledMsg
	}
	return RunFailedMsg(e.Note)
}

// SkillsLogLine is the info event listing the attached skills:
// "skills: 3 attached (a v5, b v1) · ~N tokens". mayRepeatPerChunk is true when the agent's
// strategy is not single-pass: the block is then sent in every map-reduce chunk.
func SkillsLogLine(skills []ReviewSkill, tokens int, mayRepeatPerChunk bool) string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = fmt.Sprintf("%s v%d", s.Name, s.Version)
	}
	repeat := ""
	if mayRepeatPerChunk {
		repeat = " (repeated in every map-reduce chunk)"
	}
	return fmt.Sprintf("skills: %d attached (%s) · ~%d tokens%s", len(skills), strings.Join(names, ", "), tokens, repeat)
}

// SkillsEventData is the data of the skills event: {"skills":[{"id","name","version"}]}.
func SkillsEventData(skills []ReviewSkill) json.RawMessage {
	type item struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	items := make([]item, len(skills))
	for i, s := range skills {
		items[i] = item{ID: s.ID, Name: s.Name, Version: s.Version}
	}
	return marshal(struct {
		Skills []item `json:"skills"`
	}{items})
}

// ProjectContextWarningData is the data of the "project context unavailable" event.
func ProjectContextWarningData() json.RawMessage {
	return marshal(struct {
		Warning string `json:"warning"`
	}{ProjectContextWarning})
}

// ClockStamp is the event time "HH:MM:SS" in the time's own location (the process's local zone).
func ClockStamp(t time.Time) string { return t.Format("15:04:05") }

// FinalStatusEvent is the single event sent for a run that finished but whose live buffer is gone
// (restart, TTL eviction): it carries the stored status so the client can settle. It is a result
// for a done run and an info otherwise (an error kind would toast an old failure again on
// reconnect). status is empty for a NULL column; errNote nil for no error. The data is
// {"status":<status|null>,"error":<note|null>}.
func FinalStatusEvent(runID string, status RunStatus, errNote *string, now time.Time) RunEvent {
	kind := reviewer.EventInfo
	msg := "Run ended"
	switch {
	case status == StatusDone:
		kind = reviewer.EventResult
		msg = RunFinishedMsg
	case status != "":
		msg = "Run " + string(status)
	}
	if status != StatusDone && errNote != nil && *errNote != "" {
		msg += ": " + *errNote
	}
	var st *string
	if status != "" {
		s := string(status)
		st = &s
	}
	data := marshal(struct {
		Status *string `json:"status"`
		Error  *string `json:"error"`
	}{st, errNote})
	return RunEvent{RunID: runID, Seq: 1, Kind: kind, Msg: msg, T: ClockStamp(now), Data: data}
}

// marshal encodes a value made of strings, ints and slices of them; encoding such a value cannot
// fail (invalid UTF-8 is replaced). HTML escaping is off so the bytes equal what the SSE writer
// emits for the same value.
func marshal(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

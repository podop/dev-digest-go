package executor

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"

	reviewerapp "dev-digest-go/backend/internal/reviewer/app"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// runLog is the single sink for what a run does: it publishes each event on the run bus (the Live
// Log, and later the trace's log) and mirrors it to the server log. One logger can fan out to
// several runs: the shared pre-work of a request lands in every queued run's stream.
//
// Every message passes the provider-key redaction before it is published or logged. The mirror
// logs an error event only up to its first newline (D4017): a schema-failure note carries a
// snippet of the model's raw output on the following lines.
type runLog struct {
	bus    app.Bus
	redact func(string) string
	log    *slog.Logger
	runIDs []string
	attrs  []any
}

func newRunLog(bus app.Bus, redact func(string) string, log *slog.Logger, runIDs []string, attrs ...any) *runLog {
	return &runLog{bus: bus, redact: redact, log: log, runIDs: runIDs, attrs: attrs}
}

// forRun narrows the logger to one run and adds log attributes.
func (l *runLog) forRun(runID string, attrs ...any) *runLog {
	return &runLog{bus: l.bus, redact: l.redact, log: l.log, runIDs: []string{runID}, attrs: append(append([]any{}, l.attrs...), attrs...)}
}

// event publishes to every target run and mirrors once.
func (l *runLog) event(kind reviewer.EventKind, msg string, data json.RawMessage) {
	msg = l.redact(msg)
	for _, id := range l.runIDs {
		l.bus.Publish(id, kind, msg, data)
	}
	attrs := append(append([]any{}, l.attrs...), "runIds", l.runIDs, "kind", string(kind))
	if len(data) > 0 {
		attrs = append(attrs, "data", string(data))
	}
	switch kind {
	case reviewer.EventTool:
		l.log.Debug(msg, attrs...)
	case reviewer.EventError:
		l.log.Error(firstLine(msg), attrs...)
	default:
		l.log.Info(msg, attrs...)
	}
}

// intentEmit is the app.IntentEmit of the request's intent pre-work: it publishes the resolver's
// events to every queued run like any shared event. An unknown kind counts as info.
func (l *runLog) intentEmit(kind, msg string, data json.RawMessage) {
	k := reviewer.EventKind(kind)
	switch k {
	case reviewer.EventInfo, reviewer.EventResult, reviewer.EventError, reviewer.EventTool:
	default:
		k = reviewer.EventInfo
	}
	l.event(k, msg, data)
}

func (l *runLog) info(msg string, data json.RawMessage) { l.event(reviewer.EventInfo, msg, data) }
func (l *runLog) result(msg string)                     { l.event(reviewer.EventResult, msg, nil) }
func (l *runLog) errorf(msg string)                     { l.event(reviewer.EventError, msg, nil) }

// step times fn: "<label>…" up front, then "<label> done (<ms>ms)" in the given kind, or a
// "<label> failed (<ms>ms): <message>" error event; the error is returned.
func step[T any](e *Executor, l *runLog, kind reviewer.EventKind, label string, fn func() (T, error)) (T, error) {
	t0 := e.clock()
	l.event(kind, domain.StepStart(label), nil)
	v, err := fn()
	ms := e.sinceMS(t0)
	if err != nil {
		l.event(reviewer.EventError, domain.StepFailed(label, ms, err.Error()), nil)
		var zero T
		return zero, err
	}
	l.event(kind, domain.StepDone(label, ms), nil)
	return v, nil
}

// engineEvents adapts the engine's progress port to a run's logger.
type engineEvents struct{ log *runLog }

var _ reviewerapp.Events = engineEvents{}

// Emit publishes one engine line; data is marshalled with HTML escaping off (the SSE writer's
// encoding) and a nil data adds no key.
func (e engineEvents) Emit(kind reviewer.EventKind, msg string, data any) {
	e.log.event(kind, msg, marshalData(data))
}

func marshalData(v any) json.RawMessage {
	switch d := v.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return d
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// firstLine cuts s at its first newline.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// logText is a text safe to put in a log line: redacted, first line only.
func (e *Executor) logText(s string) string { return firstLine(e.redact(s)) }

package app

// The run events of a derivation (spec 7.4.7; Node intent-service.ts). The texts are shown in the
// review's log and are compared byte for byte with Node's.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"dev-digest-go/backend/internal/intent/domain"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// event is one run event as the flights carry it to the attached callers.
type event struct {
	kind, msg string
	data      json.RawMessage
}

// Warning texts JavaScript gives to the cancellation reasons Go has no message for.
const (
	abortedWarning = "This operation was aborted"
	timeoutWarning = "The operation was aborted due to timeout"
)

// warningData is the data of the "intent unavailable" event.
var warningData = json.RawMessage(`{"warning":"` + domain.WarningCode + `"}`)

// cachedEvent is the event of a stored record that matches the key. A record without confidence
// prints it as JavaScript prints null.
func cachedEvent(confidence *string) event {
	c := "null"
	if confidence != nil {
		c = *confidence
	}
	return event{kind: domain.EventInfo, msg: "PR intent ready (cached, confidence=" + c + ")"}
}

// sourcesEvent reports how many sources were used, skipped and failed.
func sourcesEvent(sources []domain.Source) event {
	used, skipped, failed := domain.StatusCounts(sources)
	return event{
		kind: domain.EventInfo,
		msg: "PR intent sources: " + strconv.Itoa(used) + " used, " + strconv.Itoa(skipped) + " skipped, " +
			strconv.Itoa(failed) + " failed",
	}
}

// derivedEvent reports a finished derivation. The cost part is there only for a priced call, and
// comes from the call's own figure (JavaScript toFixed(4) of it), not from the rounded column.
func derivedEvent(confidence string, tokensIn, tokensOut int, cost *float64) event {
	msg := "PR intent derived (confidence=" + confidence + ", " + strconv.Itoa(tokensIn) + "+" + strconv.Itoa(tokensOut) + " tok"
	if cost != nil {
		msg += ", $" + reviewer.ToFixed(*cost, 4)
	}
	return event{kind: domain.EventResult, msg: msg + ")"}
}

// unavailableEvent is the warning of a derivation that failed with the given message.
func unavailableEvent(warning string) event {
	return event{kind: domain.EventInfo, msg: "warning: intent unavailable — " + warning, data: warningData}
}

// warningText is the message of the failure that made the intent unavailable. A bare context
// cancellation or deadline (the caller's own, with no reason attached) reads as JavaScript's
// AbortError and TimeoutError; every other error, a cancellation reason included, is its text.
func warningText(err error) string {
	if errors.Unwrap(err) == nil { // a sentinel itself, not a failure that wraps one
		switch {
		case errors.Is(err, context.Canceled):
			return abortedWarning
		case errors.Is(err, context.DeadlineExceeded):
			return timeoutWarning
		}
	}
	return err.Error()
}

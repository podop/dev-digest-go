package domain

import (
	"errors"
)

// ErrRunCancelled is returned by a run that stopped because the user cancelled it. Any error
// raised while the run is flagged cancelled is a cancel too (see EndingFor).
var ErrRunCancelled = errors.New("run cancelled")

// Ending is how a run that stopped abnormally is recorded: its terminal status and the note
// stored in agent_runs.error.
type Ending struct {
	// Status is StatusFailed or StatusCancelled.
	Status RunStatus
	Note   string
}

// EndingFor decides the terminal status and stored note of a run that returned err. An error
// raised while the run was cancelled (for example the aborted LLM call's context error) is a
// cancel, not a failure; so is ErrRunCancelled. A failure's note is the error text.
func EndingFor(err error, cancelled bool) Ending {
	if cancelled || errors.Is(err, ErrRunCancelled) {
		return Ending{Status: StatusCancelled, Note: CancelledNote}
	}
	if err == nil {
		return Ending{Status: StatusFailed, Note: "unknown error"}
	}
	return Ending{Status: StatusFailed, Note: err.Error()}
}

// IsTerminal reports whether the status is a final one. The empty status (NULL column) is not.
func (s RunStatus) IsTerminal() bool {
	return s == StatusDone || s == StatusFailed || s == StatusCancelled
}

// ColumnStatus is the status a multi-agent column shows: the row status, failed when the column
// is NULL (a legacy row) or unknown.
func ColumnStatus(s RunStatus) RunStatus {
	switch s {
	case StatusRunning, StatusDone, StatusFailed, StatusCancelled:
		return s
	}
	return StatusFailed
}

// EndedNote is the note an ending leaves on a run row whose status is already cancelled (a
// cancel that landed first keeps its own note, defaulting to CancelledNote).
func EndedNote(current *string) string {
	if current == nil || *current == "" {
		return CancelledNote
	}
	return *current
}

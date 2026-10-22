package domain

import (
	"encoding/json"
	"math"
	"time"
)

// TimeFormat is the wire format of timestamps: ISO-8601 UTC with milliseconds.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// StateView is the bounded view of an index state that the API answers (section 7.2). It carries
// no wire tags: transport/http owns the JSON shape.
type StateView struct {
	RepoID       string
	Status       Status
	FilesIndexed int
	FilesSkipped int
	DurationMs   int
	// Reason is a pointer so that an empty stats.reason is still told apart from none (Node sends
	// it either way).
	Reason         *string
	LastIndexedSha string
	IndexerVersion int
	UpdatedAt      string
	Degraded       bool
	DegradedReason DegradedReason
}

// NewStateView builds the view of a persisted state row (FR3002): bounded and every stats key
// other than durationMs, reason and degradedReason stay out.
func NewStateView(st IndexState) StateView {
	v := StateView{
		RepoID:         st.RepoID,
		Status:         st.Status,
		FilesIndexed:   st.FilesIndexed,
		FilesSkipped:   st.FilesSkipped,
		DurationMs:     statInt(st.Stats[StatDurationMs]),
		LastIndexedSha: st.LastIndexedSHA,
		IndexerVersion: st.IndexerVersion,
		UpdatedAt:      st.UpdatedAt.UTC().Format(TimeFormat),
	}
	if s, ok := st.Stats[StatReason].(string); ok {
		v.Reason = &s
	}
	if st.Status == StatusDegraded || st.Status == StatusFailed {
		v.Degraded = true
		v.DegradedReason = ReasonIndexFailed
		if s, ok := st.Stats[StatDegradedReason].(string); ok && validReason(DegradedReason(s)) {
			v.DegradedReason = DegradedReason(s)
		}
	}
	return v
}

// Synthesised is the state answered when a repo has no state row or the row cannot be read
// (FR3003). repoID is the id as the caller sent it.
func Synthesised(repoID string) StateView {
	reason := string(ReasonNoData)
	return StateView{
		RepoID:         repoID,
		Status:         StatusDegraded,
		Reason:         &reason,
		LastIndexedSha: "",
		IndexerVersion: IndexerVersion,
		UpdatedAt:      time.UnixMilli(0).UTC().Format(TimeFormat),
		Degraded:       true,
		DegradedReason: ReasonNoData,
	}
}

func validReason(r DegradedReason) bool {
	switch r {
	case ReasonFlagOff, ReasonIndexFailed, ReasonIndexPartial, ReasonRepoTooLarge, ReasonNoData:
		return true
	}
	return false
}

// statInt reads a stats value that is a JSON number (a decoded float64 or a Go integer) as an
// int, truncating a fraction; anything else, and a number outside the int range, is 0.
func statInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		if n < math.MinInt || n > math.MaxInt {
			return 0
		}
		return int(n)
	case float64:
		if math.IsNaN(n) || n < math.MinInt32 || n > math.MaxInt32 {
			return 0
		}
		return int(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0
		}
		return statInt(f)
	}
	return 0
}

// FullStatus is the status of a full index (FR3015): full unless the soft budget was reached, the
// graph build failed or a file failed.
func FullStatus(softBudgetReached, graphFailed bool, parseDegraded int) Status {
	if softBudgetReached || graphFailed || parseDegraded > 0 {
		return StatusPartial
	}
	return StatusFull
}

// IncrementalStatus is the status of an incremental index (FR3021): full only when the previous
// status was full, no file failed and the graph build did not fail.
func IncrementalStatus(prev Status, graphFailed bool, parseDegraded int) Status {
	if prev == StatusFull && !graphFailed && parseDegraded == 0 {
		return StatusFull
	}
	return StatusPartial
}

// ShouldRecordFailure reports whether a failed index attempt replaces the stored state with a
// failed one (FR3037): when there is no row, or the row is degraded or failed. A full or partial
// row is left unchanged.
func ShouldRecordFailure(prev *IndexState) bool {
	return prev == nil || prev.Status == StatusDegraded || prev.Status == StatusFailed
}

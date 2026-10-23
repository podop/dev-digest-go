package executor

import (
	"context"
	"errors"

	"dev-digest-go/backend/internal/evals/domain"
)

// record stores a case result with the run counters in one transaction (spec 7.5.5). A result
// whose case or run was deleted meanwhile is not stored: the case leaves the run's total instead
// and recorded is false. Any other store error stops the run.
func (e *Executor) record(base context.Context, run domain.Run, r domain.NewResult) (recorded bool, err error) {
	err = e.store.RecordResult(base, run.WorkspaceID, r)
	if errors.Is(err, domain.ErrRowGone) {
		return false, e.store.DropRunCase(base, run.WorkspaceID, run.ID)
	}
	return err == nil, err
}

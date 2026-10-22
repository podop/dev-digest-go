package app

import (
	"context"
	"log/slog"

	"dev-digest-go/backend/internal/repointel/domain"
)

// stateReader is what Service and Reader need to read the state row.
type stateReader interface {
	GetState(ctx context.Context, repoID string) (st domain.IndexState, ok bool, err error)
}

// Service answers the two repo-intel routes (spec section 7.2): the index state, which ignores
// REPO_INTEL_ENABLED and never fails, and the resync request.
type Service struct {
	states stateReader
	enq    Enqueuer
	log    *slog.Logger
}

// NewService builds the Service; a nil log discards warnings.
func NewService(states stateReader, enq Enqueuer, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{states: states, enq: enq, log: log}
}

// IndexState returns the persisted state of the repo, or the synthesised degraded state when
// there is no row or the read failed (logged at warn, FR3003). It never fails and does not
// check that the repo exists; repoID is used as sent.
func (s *Service) IndexState(ctx context.Context, repoID string) domain.StateView {
	st, ok, err := s.states.GetState(ctx, repoID)
	if err != nil {
		s.log.WarnContext(ctx, "repo intel index state unreadable", "repo_id", repoID,
			"error", domain.ClampUTF16(err.Error(), domain.MaxErrorChars))
		return domain.Synthesised(repoID)
	}
	if !ok {
		return domain.Synthesised(repoID)
	}
	return domain.NewStateView(st)
}

// resyncPayload is the payload of a repo-intel-resync job.
type resyncPayload struct {
	RepoID string `json:"repoId"`
}

// Resync enqueues a repo-intel-resync job for the repo in the workspace and returns its id
// (FR3004). It does not check that the repo exists; the job ends without writing for an unknown
// or uncloned repo. An enqueue failure is logged at warn and returned; the HTTP handler answers
// the degraded 202.
func (s *Service) Resync(ctx context.Context, workspaceID, repoID string) (string, error) {
	id, err := s.enq.Enqueue(ctx, workspaceID, domain.KindResync, resyncPayload{RepoID: repoID})
	if err != nil {
		s.log.WarnContext(ctx, "repo intel resync not enqueued", "repo_id", repoID,
			"error", domain.ClampUTF16(err.Error(), domain.MaxErrorChars))
		return "", err
	}
	return id, nil
}

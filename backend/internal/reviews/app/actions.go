package app

import (
	"context"
	"fmt"

	"dev-digest-go/backend/internal/platform/apperr"
	"dev-digest-go/backend/internal/reviews/domain"
)

// DeleteRun deletes a run with its trace, review and findings in one transaction; false when
// the run does not exist.
func (s *Service) DeleteRun(ctx context.Context, workspaceID, runID string) (bool, error) {
	var deleted bool
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		ok, err := s.store.DeleteRun(ctx, workspaceID, runID)
		deleted = ok
		return err
	})
	if err != nil {
		return false, fmt.Errorf("delete run %s: %w", runID, err)
	}
	return deleted, nil
}

// Reviews returns the pull request's reviews, newest first.
func (s *Service) Reviews(ctx context.Context, workspaceID, prID string) ([]domain.ReviewRecord, error) {
	if _, err := s.store.FindPull(ctx, workspaceID, prID); err != nil {
		return nil, notFound(err, "Pull request not found", "find pull request "+prID)
	}
	reviews, err := s.store.ReviewsForPull(ctx, prID)
	if err != nil {
		return nil, fmt.Errorf("list reviews of pull request %s: %w", prID, err)
	}
	if reviews == nil {
		reviews = []domain.ReviewRecord{}
	}
	return reviews, nil
}

// DeleteReview deletes a review of the workspace with its findings.
func (s *Service) DeleteReview(ctx context.Context, workspaceID, reviewID string) error {
	ok, err := s.store.DeleteReview(ctx, workspaceID, reviewID)
	if err != nil {
		return fmt.Errorf("delete review %s: %w", reviewID, err)
	}
	if !ok {
		return apperr.NotFound("Review not found")
	}
	return nil
}

// AcceptFinding marks a finding accepted now.
func (s *Service) AcceptFinding(ctx context.Context, workspaceID, findingID string) (domain.FindingRecord, error) {
	f, err := s.store.AcceptFinding(ctx, workspaceID, findingID, s.now())
	if err != nil {
		return domain.FindingRecord{}, notFound(err, "Finding not found", "accept finding "+findingID)
	}
	return f, nil
}

// DismissFinding marks a finding dismissed now.
func (s *Service) DismissFinding(ctx context.Context, workspaceID, findingID string) (domain.FindingRecord, error) {
	f, err := s.store.DismissFinding(ctx, workspaceID, findingID, s.now())
	if err != nil {
		return domain.FindingRecord{}, notFound(err, "Finding not found", "dismiss finding "+findingID)
	}
	return f, nil
}

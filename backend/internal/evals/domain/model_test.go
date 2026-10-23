package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"dev-digest-go/backend/internal/evals/domain"
)

func TestCaseExistsError(t *testing.T) {
	var err error = fmt.Errorf("create case: %w", &domain.CaseExistsError{CaseID: "c1"})

	if !errors.Is(err, domain.ErrCaseExists) {
		t.Errorf("errors.Is(%v, ErrCaseExists) = false, want true", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Errorf("errors.Is(%v, ErrNotFound) = true, want false", err)
	}
	var ce *domain.CaseExistsError
	if !errors.As(err, &ce) || ce.CaseID != "c1" {
		t.Errorf("errors.As(%v, *CaseExistsError) = %v, want case id c1", err, ce)
	}
}

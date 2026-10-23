package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"dev-digest-go/backend/internal/evals/domain"
	"dev-digest-go/backend/internal/platform/apperr"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Messages and codes of the case use cases (spec section 7.2), byte for byte as the UI shows them.
const (
	msgFindingNotFound = "Finding not found"
	msgCaseNotFound    = "Eval case not found"
	msgAgentNotFound   = "Agent not found"
	msgSkillNotFound   = "Skill not found"

	codeCaseExists = "eval_case_exists"
	codeCaseLimit  = "eval_case_limit"
)

// CaseService implements Cases over a CaseStore and the Findings reader.
type CaseService struct {
	store    CaseStore
	findings Findings
	now      Clock
	log      *slog.Logger
}

var _ Cases = (*CaseService)(nil)

// NewCaseService builds the case use cases. A nil now means time.Now; a nil log discards.
func NewCaseService(store CaseStore, findings Findings, now Clock, log *slog.Logger) *CaseService {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &CaseService{store: store, findings: findings, now: now, log: log}
}

// ownerNotFound is the 404 of a missing agent or skill.
func ownerNotFound(kind domain.OwnerKind) *apperr.Error {
	if kind == domain.OwnerSkill {
		return apperr.NotFound(msgSkillNotFound)
	}
	return apperr.NotFound(msgAgentNotFound)
}

// rangeDetail is one entry of the details.ranges of expectation_lines_not_in_diff on the wire.
type rangeDetail struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// ruleErr maps a domain.RuleError anywhere in err's chain to the application error that carries
// its code, message and details (422 for RuleUnprocessable, 400 for RuleBadRequest); any other
// error is returned as is.
func ruleErr(err error) error {
	var re *domain.RuleError
	if !errors.As(err, &re) {
		return err
	}
	var ae *apperr.Error
	if re.Kind == domain.RuleBadRequest {
		ae = apperr.InvalidInput(re.Message)
	} else {
		ae = apperr.Validation(re.Message)
	}
	ae.WithCode(re.Code)
	if len(re.Details) > 0 {
		details := make(map[string]any, len(re.Details))
		for k, v := range re.Details {
			if ranges, ok := v.([]domain.LineRange); ok {
				out := make([]rangeDetail, len(ranges))
				for i, r := range ranges {
					out[i] = rangeDetail{Start: r.Start, End: r.End}
				}
				v = out
			}
			details[k] = v
		}
		ae.WithDetails(details)
	}
	return ae
}

// caseExists is the 409 eval_case_exists carrying the id of the existing case.
func caseExists(caseID string) *apperr.Error {
	return apperr.Conflict("An eval case already exists for this finding").
		WithCode(codeCaseExists).
		WithDetails(map[string]any{"case_id": caseID})
}

// assertOwner is the 404 unless the agent or skill belongs to the workspace.
func (s *CaseService) assertOwner(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) error {
	ok, err := s.store.OwnerExists(ctx, workspaceID, kind, ownerID)
	if err != nil {
		return fmt.Errorf("check %s owner: %w", kind, err)
	}
	if !ok {
		return ownerNotFound(kind)
	}
	return nil
}

// assertUnderLimit rejects the 201st case of an owner. Count then insert is not atomic (two
// creates racing at 199 may both succeed), as in the Node server.
func (s *CaseService) assertUnderLimit(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) error {
	n, err := s.store.CountCases(ctx, workspaceID, kind, ownerID)
	if err != nil {
		return fmt.Errorf("count cases: %w", err)
	}
	if n >= domain.CasesPerOwnerMax {
		return apperr.Validation("An agent or skill can have at most " + strconv.Itoa(domain.CasesPerOwnerMax) + " eval cases").
			WithCode(codeCaseLimit)
	}
	return nil
}

// insert stores the case; a lost race on source_finding_id is the 409 of the winning case.
func (s *CaseService) insert(ctx context.Context, c domain.NewCase) (domain.CaseView, error) {
	created, err := s.store.CreateCase(ctx, c)
	if err != nil {
		var exists *domain.CaseExistsError
		if errors.As(err, &exists) {
			return domain.CaseView{}, caseExists(exists.CaseID)
		}
		return domain.CaseView{}, fmt.Errorf("create case: %w", err)
	}
	s.log.Info("eval case created",
		"caseId", created.ID,
		"ownerKind", string(created.OwnerKind),
		"ownerId", created.OwnerID,
		"diffLength", reviewer.Len16(created.InputDiff))
	return domain.CaseView{Case: created}, nil
}

// FromFinding implements Cases.
func (s *CaseService) FromFinding(ctx context.Context, workspaceID, findingID string) (domain.CaseView, error) {
	src, err := s.findings.Source(ctx, workspaceID, findingID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.CaseView{}, apperr.NotFound(msgFindingNotFound)
	}
	if err != nil {
		return domain.CaseView{}, fmt.Errorf("read finding: %w", err)
	}
	existing, found, err := s.store.CaseBySourceFinding(ctx, workspaceID, findingID)
	if err != nil {
		return domain.CaseView{}, fmt.Errorf("find case of finding: %w", err)
	}
	if found {
		return domain.CaseView{}, caseExists(existing)
	}
	nc, err := domain.BuildFromFinding(workspaceID, src)
	if err != nil {
		return domain.CaseView{}, ruleErr(err)
	}
	if err := s.assertUnderLimit(ctx, workspaceID, nc.OwnerKind, nc.OwnerID); err != nil {
		return domain.CaseView{}, err
	}
	return s.insert(ctx, nc)
}

// List implements Cases.
func (s *CaseService) List(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string) ([]domain.CaseView, error) {
	if err := s.assertOwner(ctx, workspaceID, kind, ownerID); err != nil {
		return nil, err
	}
	views, err := s.store.ListCases(ctx, workspaceID, kind, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list cases: %w", err)
	}
	if views == nil {
		views = []domain.CaseView{}
	}
	return views, nil
}

// Create implements Cases: owner, diff normalisation, content check, limit, insert.
func (s *CaseService) Create(ctx context.Context, workspaceID string, kind domain.OwnerKind, ownerID string, in domain.CreateInput) (domain.CaseView, error) {
	if err := s.assertOwner(ctx, workspaceID, kind, ownerID); err != nil {
		return domain.CaseView{}, err
	}
	diff := domain.NormalizeCaseDiff(in.InputDiff, in.Expectation.File)
	if err := domain.CheckCaseContent(diff, in.Expectation); err != nil {
		return domain.CaseView{}, ruleErr(err)
	}
	if err := s.assertUnderLimit(ctx, workspaceID, kind, ownerID); err != nil {
		return domain.CaseView{}, err
	}
	return s.insert(ctx, domain.BuildManualCase(workspaceID, kind, ownerID, in, diff))
}

// Update implements Cases: the merge and the content re-check run inside the store's row lock.
func (s *CaseService) Update(ctx context.Context, workspaceID, caseID string, in domain.UpdateInput) (domain.CaseView, error) {
	_, err := s.store.UpdateCase(ctx, workspaceID, caseID, func(stored domain.Case) (domain.Case, error) {
		merge, err := domain.MergeEdit(stored, in, s.now())
		if err != nil {
			return domain.Case{}, err
		}
		if merge.Recheck {
			if err := domain.CheckCaseContent(merge.Case.InputDiff, merge.Case.Expectation); err != nil {
				return domain.Case{}, err
			}
		}
		return merge.Case, nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.CaseView{}, apperr.NotFound(msgCaseNotFound)
		}
		var re *domain.RuleError
		if errors.As(err, &re) {
			return domain.CaseView{}, ruleErr(err)
		}
		return domain.CaseView{}, fmt.Errorf("update case: %w", err)
	}
	view, err := s.store.GetCaseView(ctx, workspaceID, caseID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.CaseView{}, apperr.NotFound(msgCaseNotFound)
	}
	if err != nil {
		return domain.CaseView{}, fmt.Errorf("read updated case: %w", err)
	}
	return view, nil
}

// Delete implements Cases.
func (s *CaseService) Delete(ctx context.Context, workspaceID, caseID string) error {
	err := s.store.DeleteCase(ctx, workspaceID, caseID)
	if errors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound(msgCaseNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete case: %w", err)
	}
	return nil
}

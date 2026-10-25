package postgres

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
)

// StoredRunKeys implements app.Store.
func (s *Store) StoredRunKeys(ctx context.Context, keys []domain.RunKey) (map[domain.RunKey]bool, error) {
	out := make(map[domain.RunKey]bool, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	ids := make([]int64, len(keys))
	for i, k := range keys {
		ids[i] = k.GithubRunID
	}
	var rows []struct {
		GithubRunID int64 `gorm:"column:github_run_id"`
		RunAttempt  *int  `gorm:"column:run_attempt"`
	}
	err := s.conn(ctx).Table("ci_runs").Select("github_run_id, run_attempt").
		Where("github_run_id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select stored ci run keys: %w", err)
	}
	for _, r := range rows {
		if r.RunAttempt != nil {
			out[domain.RunKey{GithubRunID: r.GithubRunID, Attempt: *r.RunAttempt}] = true
		}
	}
	return out, nil
}

// RecordRun implements app.Store. The unique (github_run_id, run_attempt) is the idempotency key:
// a second writer of the same attempt waits for the first one's transaction and then inserts
// nothing, so exactly one of two concurrent refreshes stores the run and the other gets Stored
// false. A stored run reports the id of its agent_runs row. When ctx carries a caller's
// transaction (platform postgres InTx), the writes join it: nothing is committed here, so the
// caller can write the ledger entry of the run in the same transaction.
func (s *Store) RecordRun(ctx context.Context, run domain.NewRun) (app.Recorded, error) {
	if err := checkRun(run); err != nil {
		return app.Recorded{}, err
	}
	var rec app.Recorded
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		rec = app.Recorded{}
		ci := ciRunInsertRow{
			InstallationID:  run.InstallationID,
			PRNumber:        run.PRNumber,
			RanAt:           run.RanAt,
			Status:          run.Status,
			FindingsCount:   run.FindingsCount,
			CostUSD:         run.CostUSD,
			GithubURL:       run.GithubURL,
			Source:          domain.RunSource,
			GithubRunID:     run.GithubRunID,
			RunAttempt:      run.RunAttempt,
			HeadSHA:         run.HeadSHA,
			ManifestVersion: run.ManifestVersion,
		}
		res := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "github_run_id"}, {Name: "run_attempt"}},
			DoNothing: true,
		}).Create(&ci)
		if res.Error != nil {
			return fmt.Errorf("insert ci run: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return nil
		}

		agentStatus := "done"
		if run.Status == domain.RunFailed {
			agentStatus = "failed"
		}
		agentRun := agentRunInsertRow{
			WorkspaceID:   run.WorkspaceID,
			AgentID:       run.AgentID,
			RanAt:         run.RanAt,
			Provider:      domain.ProviderOpenRouter,
			Model:         run.Model,
			DurationMS:    run.DurationMS,
			Status:        agentStatus,
			Source:        "ci",
			FindingsCount: run.FindingsCount,
			Error:         run.Error,
			Blockers:      run.Blockers,
			CostUSD:       run.CostUSD,
		}
		if err := tx.Create(&agentRun).Error; err != nil {
			return fmt.Errorf("insert agent run: %w", err)
		}
		link := tx.Table("ci_runs").Where("id = ?", ci.ID).Update("agent_run_id", agentRun.ID)
		if link.Error != nil {
			return fmt.Errorf("link ci run to agent run: %w", link.Error)
		}
		rec = app.Recorded{AgentRunID: agentRun.ID, Stored: true}
		return nil
	})
	if err != nil {
		return app.Recorded{}, err
	}
	return rec, nil
}

// Column limits. A verified artifact is hostile input: a value its column cannot hold would fail
// the insert with a database error on every refresh, so the values are checked before any write.
const (
	// maxCost is the exclusive bound of numeric(12,6) after rounding to six decimals.
	maxCost = 999999.9999995
)

// checkRun rejects a run whose values no column can hold (app.ErrNotStorable).
func checkRun(r domain.NewRun) error {
	texts := []struct {
		name string
		val  string
	}{
		{"installation id", r.InstallationID}, {"agent id", r.AgentID}, {"workspace id", r.WorkspaceID},
		{"head sha", r.HeadSHA}, {"github url", r.GithubURL}, {"status", r.Status},
	}
	for _, t := range texts {
		if err := checkText(t.name, t.val); err != nil {
			return err
		}
	}
	for _, t := range []struct {
		name string
		val  *string
	}{{"model", r.Model}, {"error", r.Error}} {
		if t.val == nil {
			continue
		}
		if err := checkText(t.name, *t.val); err != nil {
			return err
		}
	}
	ints := []struct {
		name string
		val  int
	}{{"run attempt", r.RunAttempt}, {"findings count", r.FindingsCount}, {"blockers", r.Blockers}}
	for _, t := range ints {
		if err := checkInt32(t.name, t.val); err != nil {
			return err
		}
	}
	for _, t := range []struct {
		name string
		val  *int
	}{{"pr number", r.PRNumber}, {"manifest version", r.ManifestVersion}, {"duration", r.DurationMS}} {
		if t.val == nil {
			continue
		}
		if err := checkInt32(t.name, *t.val); err != nil {
			return err
		}
	}
	if c := r.CostUSD; c != nil && (math.IsNaN(*c) || math.Abs(*c) >= maxCost) {
		return fmt.Errorf("cost: %w", app.ErrNotStorable)
	}
	return nil
}

// checkInstallation rejects an installation whose values no column can hold.
func checkInstallation(in domain.NewInstallation) error {
	for _, t := range []struct{ name, val string }{{"agent id", in.AgentID}, {"repo", in.Repo}, {"slug", in.Slug}} {
		if err := checkText(t.name, t.val); err != nil {
			return err
		}
	}
	return checkInt32("manifest version", in.ManifestVersion)
}

func checkInt32(name string, v int) error {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return fmt.Errorf("%s out of range: %w", name, app.ErrNotStorable)
	}
	return nil
}

// checkText rejects what a text column refuses: NUL (SQLSTATE 22021) and invalid UTF-8.
func checkText(name, s string) error {
	if !utf8.ValidString(s) || strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("%s is not valid text: %w", name, app.ErrNotStorable)
	}
	return nil
}

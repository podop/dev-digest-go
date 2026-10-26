// Package postgres stores pull requests, their files and commits, and reads the review and run
// rollups of the list, with GORM. Every query is scoped to the workspace.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/pulls/domain"
)

const insertBatch = 100

type repoRow struct {
	ID          string     `gorm:"column:id;primaryKey"`
	WorkspaceID string     `gorm:"column:workspace_id"`
	Owner       string     `gorm:"column:owner"`
	Name        string     `gorm:"column:name"`
	FullName    string     `gorm:"column:full_name"`
	Provider    string     `gorm:"column:provider"`
	Host        string     `gorm:"column:host"`
	ProjectID   *int64     `gorm:"column:project_id"`
	WebURL      *string    `gorm:"column:web_url"`
	LastPolled  *time.Time `gorm:"column:last_polled_at"`
}

// toDomain is the code-host address of the row: GitHub by (owner, name), GitLab by project id.
func (r repoRow) toDomain() domain.Repo {
	repo := domain.Repo{ID: r.ID, Owner: r.Owner, Name: r.Name, Provider: r.Provider, Host: r.Host}
	if r.ProjectID != nil {
		repo.ProjectID = *r.ProjectID
	}
	return repo
}

func (repoRow) TableName() string { return "repos" }

type pullRow struct {
	ID              string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID     string     `gorm:"column:workspace_id"`
	RepoID          string     `gorm:"column:repo_id"`
	Number          int        `gorm:"column:number"`
	Title           string     `gorm:"column:title"`
	Author          string     `gorm:"column:author"`
	Branch          string     `gorm:"column:branch"`
	Base            string     `gorm:"column:base"`
	HeadSHA         string     `gorm:"column:head_sha"`
	LastReviewedSHA *string    `gorm:"column:last_reviewed_sha"`
	Additions       int        `gorm:"column:additions"`
	Deletions       int        `gorm:"column:deletions"`
	FilesCount      int        `gorm:"column:files_count"`
	Status          string     `gorm:"column:status"`
	Body            *string    `gorm:"column:body"`
	OpenedAt        *time.Time `gorm:"column:opened_at"`
	UpdatedAt       *time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
	MergedAt        *time.Time `gorm:"column:merged_at"`
	ClosedAt        *time.Time `gorm:"column:closed_at"`
	Draft           bool       `gorm:"column:draft"`
	// HeadIsFork is NULL until the GitHub poll has written it (migration 0007).
	HeadIsFork *bool `gorm:"column:head_is_fork"`
}

func (pullRow) TableName() string { return "pull_requests" }

func (r pullRow) toDomain() domain.Pull {
	return domain.Pull{
		ID: r.ID, RepoID: r.RepoID, Number: r.Number, Title: r.Title, Author: r.Author,
		Branch: r.Branch, Base: r.Base, HeadSHA: r.HeadSHA, Additions: r.Additions,
		Deletions: r.Deletions, FilesCount: r.FilesCount, Status: r.Status, Body: r.Body,
		LastReviewedSHA: r.LastReviewedSHA, OpenedAt: r.OpenedAt, UpdatedAt: r.UpdatedAt,
	}
}

type fileRow struct {
	ID        string  `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	PRID      string  `gorm:"column:pr_id"`
	Path      string  `gorm:"column:path"`
	Additions int     `gorm:"column:additions"`
	Deletions int     `gorm:"column:deletions"`
	Patch     *string `gorm:"column:patch"`
}

func (fileRow) TableName() string { return "pr_files" }

type commitRow struct {
	ID          string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	PRID        string     `gorm:"column:pr_id"`
	SHA         string     `gorm:"column:sha"`
	Message     string     `gorm:"column:message"`
	Author      string     `gorm:"column:author"`
	CommittedAt *time.Time `gorm:"column:committed_at"`
}

func (commitRow) TableName() string { return "pr_commits" }

type reviewRow struct {
	ID        string    `gorm:"column:id"`
	PRID      string    `gorm:"column:pr_id"`
	AgentID   *string   `gorm:"column:agent_id"`
	Score     *int      `gorm:"column:score"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (reviewRow) TableName() string { return "reviews" }

type findingRow struct {
	ReviewID string `gorm:"column:review_id"`
	Severity string `gorm:"column:severity"`
}

func (findingRow) TableName() string { return "findings" }

type agentRunRow struct {
	WorkspaceID string   `gorm:"column:workspace_id"`
	PRID        *string  `gorm:"column:pr_id"`
	CostUSD     *float64 `gorm:"column:cost_usd"`
}

func (agentRunRow) TableName() string { return "agent_runs" }

// Store reads and writes pull requests in Postgres.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// conn is the handle every statement uses: the open transaction when ctx came from InTx.
func (s *Store) conn(ctx context.Context) *gorm.DB { return postgres.Conn(ctx, s.db) }

// FindRepo returns the code-host address (provider, host, project id) of a repo of the workspace, or domain.ErrRepoNotFound.
func (s *Store) FindRepo(ctx context.Context, workspaceID, repoID string) (domain.Repo, error) {
	var row repoRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, repoID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Repo{}, domain.ErrRepoNotFound
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("find repo: %w", err)
	}
	return row.toDomain(), nil
}

// uniqueViolation is SQLSTATE 23505.
const uniqueViolation = "23505"

// UpdateRepoPath sets full_name, owner, name and web_url of a GitLab repository of the workspace
// to the project's current path (spec FR47). When another repository of the same workspace and
// host already stores the new full_name (repos_ws_host_fullname_uq) nothing changes and that
// repository's id is returned; "" means the path was stored. An unknown repository, a GitHub one,
// or one of another project is domain.ErrRepoNotFound. The statement runs in a savepoint, so a
// lost race on the unique index does not abort the caller's transaction. The store logs nothing:
// the use case reports the collision.
func (s *Store) UpdateRepoPath(ctx context.Context, workspaceID, repoID string, p domain.GitLabProject) (string, error) {
	if p.ID <= 0 || p.FullName == "" || p.Owner == "" || p.Name == "" || p.WebURL == "" {
		return "", fmt.Errorf("update repo path: incomplete project %+v", p)
	}
	var updated bool
	err := s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`UPDATE repos SET full_name = ?, owner = ?, name = ?, web_url = ?
			WHERE workspace_id = ? AND id = ? AND provider = ? AND project_id = ?
			AND NOT EXISTS (
				SELECT 1 FROM repos o
				WHERE o.workspace_id = repos.workspace_id AND o.host = repos.host
				AND o.full_name = ? AND o.id <> repos.id
			)`,
			p.FullName, p.Owner, p.Name, p.WebURL,
			workspaceID, repoID, domain.ProviderGitLab, p.ID,
			p.FullName)
		if res.Error != nil {
			return res.Error
		}
		updated = res.RowsAffected > 0
		return nil
	})
	if err != nil && !isUniqueViolation(err) {
		return "", fmt.Errorf("update repo path: %w", err)
	}
	if updated {
		return "", nil
	}
	// Nothing changed: the new path is taken, or the repository is not this workspace's GitLab
	// project. One query tells both: the repository, with the id of the one that holds the path.
	var found []struct{ HeldBy *string }
	err = s.conn(ctx).Raw(`SELECT (
			SELECT o.id FROM repos o
			WHERE o.workspace_id = r.workspace_id AND o.host = r.host AND o.full_name = ? AND o.id <> r.id
			LIMIT 1) AS held_by
		FROM repos r
		WHERE r.workspace_id = ? AND r.id = ? AND r.provider = ? AND r.project_id = ?`,
		p.FullName, workspaceID, repoID, domain.ProviderGitLab, p.ID).Scan(&found).Error
	if err != nil {
		return "", fmt.Errorf("update repo path: %w", err)
	}
	if len(found) == 0 {
		return "", domain.ErrRepoNotFound
	}
	if found[0].HeldBy == nil {
		return "", nil // the holder went away in between: the next sync stores the path
	}
	return *found[0].HeldBy, nil
}

// isUniqueViolation reports whether err carries SQLSTATE 23505, without importing the driver.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}

// MarkPolled sets the repo's last_polled_at.
func (s *Store) MarkPolled(ctx context.Context, workspaceID, repoID string, at time.Time) error {
	res := s.conn(ctx).Model(&repoRow{}).
		Where("workspace_id = ? AND id = ?", workspaceID, repoID).
		Update("last_polled_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark repo polled: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrRepoNotFound
	}
	return nil
}

// ListPollTargets returns every repository of every workspace for the background poll, grouped by
// workspace and ordered by full name inside it.
func (s *Store) ListPollTargets(ctx context.Context) ([]domain.PollTarget, error) {
	var rows []repoRow
	err := s.conn(ctx).
		Select("id", "workspace_id", "owner", "name", "provider", "host", "project_id").
		Order("workspace_id, full_name, id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list poll targets: %w", err)
	}
	out := make([]domain.PollTarget, len(rows))
	for i, r := range rows {
		out[i] = domain.PollTarget{WorkspaceID: r.WorkspaceID, Repo: r.toDomain()}
	}
	return out, nil
}

// FindPull returns a PR of the workspace, or domain.ErrPullNotFound.
func (s *Store) FindPull(ctx context.Context, workspaceID, prID string) (domain.Pull, error) {
	var row pullRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, prID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Pull{}, domain.ErrPullNotFound
	}
	if err != nil {
		return domain.Pull{}, fmt.Errorf("find pull request: %w", err)
	}
	return row.toDomain(), nil
}

// ListPulls returns every stored PR of the repo, most recently updated first (null update
// times last), then by number descending.
func (s *Store) ListPulls(ctx context.Context, workspaceID, repoID string) ([]domain.Pull, error) {
	var rows []pullRow
	err := s.conn(ctx).
		Where("workspace_id = ? AND repo_id = ?", workspaceID, repoID).
		Order("updated_at DESC NULLS LAST, number DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list pull requests: %w", err)
	}
	out := make([]domain.Pull, len(rows))
	for i, r := range rows {
		out[i] = r.toDomain()
	}
	return out, nil
}

// UpsertListed inserts the listed PRs and, for PRs that exist (by repo and number), updates only
// title, head_sha, status, updated_at, merged_at, closed_at, draft and head_is_fork. Rows are
// never deleted.
func (s *Store) UpsertListed(ctx context.Context, workspaceID, repoID string, listed []domain.Listed) error {
	if len(listed) == 0 {
		return nil
	}
	// A repeated number would make one statement update a row twice; the last entry wins.
	last := make(map[int]int, len(listed))
	for i, l := range listed {
		last[l.Number] = i
	}
	rows := make([]pullRow, 0, len(last))
	for i, l := range listed {
		if last[l.Number] != i {
			continue
		}
		rows = append(rows, pullRow{
			WorkspaceID: workspaceID, RepoID: repoID, Number: l.Number, Title: l.Title,
			Author: l.Author, Branch: l.Branch, Base: l.Base, HeadSHA: l.HeadSHA,
			Status: l.Status, OpenedAt: l.OpenedAt, UpdatedAt: l.UpdatedAt,
			MergedAt: l.MergedAt, ClosedAt: l.ClosedAt, Draft: l.Draft, HeadIsFork: &l.HeadIsFork,
		})
	}
	err := s.conn(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "repo_id"}, {Name: "number"}},
			DoUpdates: clause.AssignmentColumns([]string{"title", "head_sha", "status", "updated_at", "merged_at", "closed_at", "draft", "head_is_fork"}),
		}).
		CreateInBatches(&rows, insertBatch).Error
	if err != nil {
		return fmt.Errorf("upsert listed pull requests: %w", err)
	}
	return nil
}

// ListWithoutDiffStats returns at most limit PRs of the repo that still have the zeroed stats of a
// list import (the rule of domain.NeedsDiffStats) and were updated at or after updatedSince, most
// recently updated first, then by number descending. Only ID and Number are set.
func (s *Store) ListWithoutDiffStats(ctx context.Context, workspaceID, repoID string, updatedSince time.Time, limit int) ([]domain.Pull, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []pullRow
	err := s.conn(ctx).
		Select("id", "number").
		Where("workspace_id = ? AND repo_id = ?", workspaceID, repoID).
		Where("updated_at >= ?", updatedSince).
		Where("additions = 0 AND deletions = 0 AND files_count = 0").
		Order("updated_at DESC, number DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list pull requests without diff stats: %w", err)
	}
	out := make([]domain.Pull, len(rows))
	for i, r := range rows {
		out[i] = domain.Pull{ID: r.ID, Number: r.Number}
	}
	return out, nil
}

// SetDiffStats writes the diff stats of one PR.
func (s *Store) SetDiffStats(ctx context.Context, prID string, stats domain.DiffStats) error {
	err := s.conn(ctx).Model(&pullRow{}).Where("id = ?", prID).
		Updates(map[string]any{
			"additions": stats.Additions, "deletions": stats.Deletions, "files_count": stats.FilesCount,
		}).Error
	if err != nil {
		return fmt.Errorf("set diff stats: %w", err)
	}
	return nil
}

// ReplaceDetail writes the body and stats of a PR and replaces its files and commits, all in one
// transaction: rows absent from the mirror are deleted, the others are upserted by path or sha.
func (s *Store) ReplaceDetail(ctx context.Context, prID string, m domain.DetailMirror) error {
	return s.conn(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Model(&pullRow{}).Where("id = ?", prID).Updates(map[string]any{
			"body": m.Body, "additions": m.Additions, "deletions": m.Deletions, "files_count": m.FilesCount,
		}).Error
		if err != nil {
			return fmt.Errorf("update pull request detail: %w", err)
		}
		if err := replaceFiles(tx, prID, m.Files); err != nil {
			return err
		}
		return replaceCommits(tx, prID, m.Commits)
	})
}

func replaceFiles(tx *gorm.DB, prID string, files []domain.File) error {
	del := tx.Where("pr_id = ?", prID)
	if len(files) > 0 {
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		del = del.Where("path NOT IN ?", paths)
	}
	if err := del.Delete(&fileRow{}).Error; err != nil {
		return fmt.Errorf("delete stale files: %w", err)
	}
	if len(files) == 0 {
		return nil
	}
	rows := make([]fileRow, len(files))
	for i, f := range files {
		rows[i] = fileRow{PRID: prID, Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch}
	}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "pr_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"additions", "deletions", "patch"}),
	}).CreateInBatches(&rows, insertBatch).Error
	if err != nil {
		return fmt.Errorf("upsert files: %w", err)
	}
	return nil
}

func replaceCommits(tx *gorm.DB, prID string, commits []domain.Commit) error {
	del := tx.Where("pr_id = ?", prID)
	if len(commits) > 0 {
		shas := make([]string, len(commits))
		for i, c := range commits {
			shas[i] = c.SHA
		}
		del = del.Where("sha NOT IN ?", shas)
	}
	if err := del.Delete(&commitRow{}).Error; err != nil {
		return fmt.Errorf("delete stale commits: %w", err)
	}
	if len(commits) == 0 {
		return nil
	}
	rows := make([]commitRow, len(commits))
	for i, c := range commits {
		rows[i] = commitRow{PRID: prID, SHA: c.SHA, Message: c.Message, Author: c.Author, CommittedAt: c.CommittedAt}
	}
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "pr_id"}, {Name: "sha"}},
		DoUpdates: clause.AssignmentColumns([]string{"message", "author", "committed_at"}),
	}).CreateInBatches(&rows, insertBatch).Error
	if err != nil {
		return fmt.Errorf("upsert commits: %w", err)
	}
	return nil
}

// ListFiles returns the stored files of a PR in path order.
func (s *Store) ListFiles(ctx context.Context, prID string) ([]domain.File, error) {
	var rows []fileRow
	if err := s.conn(ctx).Where("pr_id = ?", prID).Order("path").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	out := make([]domain.File, len(rows))
	for i, r := range rows {
		out[i] = domain.File{Path: r.Path, Additions: r.Additions, Deletions: r.Deletions, Patch: r.Patch}
	}
	return out, nil
}

// ListCommits returns the stored commits of a PR, oldest commit time first (unknown times
// last), then by sha.
func (s *Store) ListCommits(ctx context.Context, prID string) ([]domain.Commit, error) {
	var rows []commitRow
	err := s.conn(ctx).Where("pr_id = ?", prID).Order("committed_at ASC NULLS LAST, sha").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}
	out := make([]domain.Commit, len(rows))
	for i, r := range rows {
		out[i] = domain.Commit{SHA: r.SHA, Message: r.Message, Author: r.Author, CommittedAt: r.CommittedAt}
	}
	return out, nil
}

// Rollups computes the review and cost rollup of each PR on read. A PR with no review and no
// known run cost gets the zero Rollup, so every requested id is present in the result.
func (s *Store) Rollups(ctx context.Context, workspaceID string, prIDs []string) (map[string]domain.Rollup, error) {
	out := make(map[string]domain.Rollup, len(prIDs))
	if len(prIDs) == 0 {
		return out, nil
	}
	db := s.conn(ctx)

	var reviews []reviewRow
	err := db.Where("workspace_id = ? AND kind = 'review' AND pr_id IN ?", workspaceID, prIDs).Find(&reviews).Error
	if err != nil {
		return nil, fmt.Errorf("load reviews: %w", err)
	}
	byPR := make(map[string][]domain.Review)
	for _, r := range reviews {
		agent := ""
		if r.AgentID != nil {
			agent = *r.AgentID
		}
		byPR[r.PRID] = append(byPR[r.PRID], domain.Review{ID: r.ID, PRID: r.PRID, AgentID: agent, Score: r.Score, CreatedAt: r.CreatedAt})
	}
	currentByPR := make(map[string][]domain.Review, len(byPR))
	var currentIDs []string
	for prID, rs := range byPR {
		current := domain.CurrentReviews(rs)
		currentByPR[prID] = current
		for _, r := range current {
			currentIDs = append(currentIDs, r.ID)
		}
	}

	counts, err := s.findingCounts(db, currentIDs)
	if err != nil {
		return nil, err
	}
	costs, err := s.runCosts(db, workspaceID, prIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range prIDs {
		out[id] = domain.NewRollup(currentByPR[id], counts, costs[id])
	}
	return out, nil
}

func (s *Store) findingCounts(db *gorm.DB, reviewIDs []string) (map[string]domain.FindingsCounts, error) {
	counts := make(map[string]domain.FindingsCounts, len(reviewIDs))
	if len(reviewIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		ReviewID string
		Severity string
		N        int
	}
	err := db.Model(&findingRow{}).
		Select("review_id, severity, count(*) AS n").
		Where("review_id IN ?", reviewIDs).
		Group("review_id, severity").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("count findings: %w", err)
	}
	for _, r := range rows {
		c := counts[r.ReviewID]
		switch r.Severity {
		case "CRITICAL":
			c.Critical += r.N
		case "WARNING":
			c.Warning += r.N
		case "SUGGESTION":
			c.Suggestion += r.N
		}
		counts[r.ReviewID] = c
	}
	return counts, nil
}

// runCosts sums the known cost of all agent runs per PR (any status, local and ci). A PR whose
// runs have no known cost is absent from the result.
func (s *Store) runCosts(db *gorm.DB, workspaceID string, prIDs []string) (map[string]*float64, error) {
	var rows []struct {
		PRID  string   `gorm:"column:pr_id"`
		Total *float64 `gorm:"column:total"`
	}
	err := db.Model(&agentRunRow{}).
		Select("pr_id, SUM(cost_usd)::float8 AS total").
		Where("workspace_id = ? AND pr_id IN ?", workspaceID, prIDs).
		Group("pr_id").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("sum run costs: %w", err)
	}
	out := make(map[string]*float64, len(rows))
	for _, r := range rows {
		if r.Total != nil {
			out[r.PRID] = r.Total
		}
	}
	return out, nil
}

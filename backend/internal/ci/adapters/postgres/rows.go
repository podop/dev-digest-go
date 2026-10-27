// Package postgres is the GORM-backed store of the ci context: the installations of an agent in a
// repository (ci_installations) and the CI runs ingested from GitHub or GitLab (ci_runs, plus the
// agent_runs row each one links to). A row's repository identity is host + repo for GitHub and host
// + project id for GitLab (migration 0012): every GitLab write names its host, every GitHub one
// names github.com. ci_runs has no workspace column: runs are scoped through
// their agent_runs row and installations through their agent. Every method takes its handle from
// platformpg.Conn, so a store called inside Tx.InTx joins the caller's transaction.
//
// Row types are declared in this file and read or write only the columns the context owns or
// needs of agents and agent_runs.
package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"dev-digest-go/backend/internal/ci/app"
	"dev-digest-go/backend/internal/ci/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
)

// Store implements app.Store.
type Store struct {
	db *gorm.DB
}

var (
	_ app.Store             = (*Store)(nil)
	_ app.GitLabIngestStore = (*Store)(nil)
)

// githubHost is the host key of every GitHub row. The columns default to it, but the store names it
// in every GitHub write and filter, so a GitLab row never shares an identity with a GitHub one
// (spec C FR39, EC30).
const githubHost = "github.com"

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// uniqueViolation is the SQLSTATE of a unique constraint violation.
const uniqueViolation = "23505"

// isUniqueViolation reports whether err is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}

// installationRow is a ci_installations row as the lists read it.
type installationRow struct {
	ID              string    `gorm:"column:id"`
	AgentID         string    `gorm:"column:agent_id"`
	Repo            string    `gorm:"column:repo"`
	TargetType      string    `gorm:"column:target_type"`
	InstalledAt     time.Time `gorm:"column:installed_at"`
	Slug            *string   `gorm:"column:slug"`
	ManifestVersion *int      `gorm:"column:manifest_version"`
	Provider        string    `gorm:"column:provider"`
	Host            string    `gorm:"column:host"`
	ProjectID       *int64    `gorm:"column:project_id"`
}

func (r installationRow) toDomain(last *domain.Run) domain.Installation {
	var projectID int64
	if r.ProjectID != nil {
		projectID = *r.ProjectID
	}
	return domain.Installation{
		Host:            r.Host,
		ProjectID:       projectID,
		ID:              r.ID,
		AgentID:         r.AgentID,
		Repo:            r.Repo,
		TargetType:      r.TargetType,
		InstalledAt:     r.InstalledAt.UTC(),
		Slug:            r.Slug,
		ManifestVersion: r.ManifestVersion,
		Provider:        r.Provider,
		LastRun:         last,
	}
}

// installationInsertRow is the ci_installations row an export writes. The database fills id and
// installed_at.
type installationInsertRow struct {
	ID              string    `gorm:"column:id;default:gen_random_uuid()"`
	AgentID         string    `gorm:"column:agent_id"`
	Repo            string    `gorm:"column:repo"`
	TargetType      string    `gorm:"column:target_type"`
	InstalledAt     time.Time `gorm:"column:installed_at;default:now()"`
	Slug            string    `gorm:"column:slug"`
	ManifestVersion int       `gorm:"column:manifest_version"`
	Provider        string    `gorm:"column:provider"`
	Host            string    `gorm:"column:host"`
	ProjectID       *int64    `gorm:"column:project_id"`
}

func (installationInsertRow) TableName() string { return "ci_installations" }

// runRow is a ci_runs row joined to its agent_runs row, the agent and the installation.
type runRow struct {
	ID              string     `gorm:"column:id"`
	InstallationID  *string    `gorm:"column:installation_id"`
	PRNumber        *int       `gorm:"column:pr_number"`
	RanAt           *time.Time `gorm:"column:ran_at"`
	Status          *string    `gorm:"column:status"`
	FindingsCount   *int       `gorm:"column:findings_count"`
	CostUSD         *float64   `gorm:"column:cost_usd"`
	GithubURL       *string    `gorm:"column:github_url"`
	Source          *string    `gorm:"column:source"`
	AgentName       *string    `gorm:"column:agent_name"`
	DurationMS      *int       `gorm:"column:duration_ms"`
	AgentID         *string    `gorm:"column:agent_id"`
	Repo            *string    `gorm:"column:repo"`
	Blockers        *int       `gorm:"column:blockers"`
	HeadSHA         *string    `gorm:"column:head_sha"`
	ManifestVersion *int       `gorm:"column:manifest_version"`
	Host            string     `gorm:"column:host"`
	PRURL           *string    `gorm:"column:pr_url"`
}

func (r runRow) toDomain() domain.Run {
	run := domain.Run{
		Host:            r.Host,
		PRURL:           r.PRURL,
		ID:              r.ID,
		InstallationID:  r.InstallationID,
		PRNumber:        r.PRNumber,
		Status:          r.Status,
		FindingsCount:   r.FindingsCount,
		CostUSD:         r.CostUSD,
		GithubURL:       r.GithubURL,
		Source:          r.Source,
		AgentName:       r.AgentName,
		DurationMS:      r.DurationMS,
		AgentID:         r.AgentID,
		Repo:            r.Repo,
		Blockers:        r.Blockers,
		HeadSHA:         r.HeadSHA,
		ManifestVersion: r.ManifestVersion,
	}
	if r.RanAt != nil {
		at := r.RanAt.UTC()
		run.RanAt = &at
	}
	return run
}

// ciRunInsertRow is the ci_runs row of a stored run. The database fills id; agent_run_id is set
// once the agent_runs row exists.
type ciRunInsertRow struct {
	ID              string    `gorm:"column:id;default:gen_random_uuid()"`
	InstallationID  string    `gorm:"column:ci_installation_id"`
	PRNumber        *int      `gorm:"column:pr_number"`
	RanAt           time.Time `gorm:"column:ran_at"`
	Status          string    `gorm:"column:status"`
	FindingsCount   int       `gorm:"column:findings_count"`
	CostUSD         *float64  `gorm:"column:cost_usd"`
	GithubURL       string    `gorm:"column:github_url"`
	Source          string    `gorm:"column:source"`
	GithubRunID     int64     `gorm:"column:github_run_id"`
	RunAttempt      int       `gorm:"column:run_attempt"`
	HeadSHA         string    `gorm:"column:head_sha"`
	ManifestVersion *int      `gorm:"column:manifest_version"`
	Host            string    `gorm:"column:host"`
}

func (ciRunInsertRow) TableName() string { return "ci_runs" }

// agentRunInsertRow is the agent_runs row of a stored CI run (pr_id stays null).
type agentRunInsertRow struct {
	ID            string    `gorm:"column:id;default:gen_random_uuid()"`
	WorkspaceID   string    `gorm:"column:workspace_id"`
	AgentID       string    `gorm:"column:agent_id"`
	RanAt         time.Time `gorm:"column:ran_at"`
	Provider      string    `gorm:"column:provider"`
	Model         *string   `gorm:"column:model"`
	DurationMS    *int      `gorm:"column:duration_ms"`
	Status        string    `gorm:"column:status"`
	Source        string    `gorm:"column:source"`
	FindingsCount int       `gorm:"column:findings_count"`
	Error         *string   `gorm:"column:error"`
	Blockers      int       `gorm:"column:blockers"`
	CostUSD       *float64  `gorm:"column:cost_usd"`
}

func (agentRunInsertRow) TableName() string { return "agent_runs" }

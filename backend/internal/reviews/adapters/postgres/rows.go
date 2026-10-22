// Package postgres persists the reviews context with GORM: agent runs and their skills and
// traces, reviews and findings, the multi-agent read model and Learn. Every method takes its
// handle from platformpg.Conn, so a store called inside Tx.InTx joins the caller's transaction.
//
// Row structs are declared here, per adapter: besides the tables of the context (agent_runs,
// reviews, findings, run_traces, ...) the store reads pull_requests, repos, agents, skills,
// agent_skills, pr_files, eval_cases and memory with row types of its own.
package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	platformpg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/reviews/app"
	"dev-digest-go/backend/internal/reviews/domain"
)

// SQLSTATE values the store translates.
const (
	foreignKeyViolation = "23503"
)

// Store is the GORM-backed implementation of app.Store.
type Store struct {
	db *gorm.DB
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

// sqlState returns the SQLSTATE of a database error, "" for any other error. It does not import
// the driver: pgconn.PgError is reachable through its SQLState method.
func sqlState(err error) string {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return coded.SQLState()
	}
	return ""
}

// notFoundOnForeignKey turns a foreign-key violation (the run, review or agent a write refers to
// was deleted meanwhile) into domain.ErrNotFound and wraps any other error with what failed.
func notFoundOnForeignKey(what string, err error) error {
	if sqlState(err) == foreignKeyViolation {
		return fmt.Errorf("%s: %w", what, domain.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// isNoRows reports whether err is the "no row" error of a Take/First.
func isNoRows(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// clean makes a string storable in a Postgres text or jsonb column: U+0000 is dropped and invalid
// UTF-8 becomes U+FFFD (both are rejected by the server, SQLSTATE 22021/22P05).
func clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "�")
}

func cleanPtr(s *string) *string {
	if s == nil {
		return nil
	}
	c := clean(*s)
	return &c
}

// isUUID reports whether s is a canonical hyphenated UUID, the only form an id of this API takes.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

// jsonb maps a nullable jsonb column to raw JSON text. A nil value is SQL NULL. GORM does not
// call Scan for a NULL column, so the field then simply stays nil.
type jsonb []byte

// Scan implements sql.Scanner.
func (v *jsonb) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*v = nil
	case []byte:
		*v = append((*v)[:0], s...)
	case string:
		*v = jsonb(s)
	default:
		return fmt.Errorf("scan jsonb: unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer; the text form is cast to jsonb by the server.
func (v jsonb) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return string(v), nil
}

// utc returns t in UTC.
func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// ---- rows of the tables the context owns ----

// runRow maps agent_runs for inserts.
type runRow struct {
	ID              string  `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID     string  `gorm:"column:workspace_id"`
	AgentID         *string `gorm:"column:agent_id"`
	PRID            *string `gorm:"column:pr_id"`
	Provider        *string `gorm:"column:provider"`
	Model           *string `gorm:"column:model"`
	Status          *string `gorm:"column:status"`
	Source          string  `gorm:"column:source"`
	MultiAgentRunID *string `gorm:"column:multi_agent_run_id"`
}

func (runRow) TableName() string { return "agent_runs" }

// multiAgentRunRow maps multi_agent_runs.
type multiAgentRunRow struct {
	ID          string `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string `gorm:"column:workspace_id"`
	PRID        string `gorm:"column:pr_id"`
}

func (multiAgentRunRow) TableName() string { return "multi_agent_runs" }

// runSkillRow maps agent_run_skills.
type runSkillRow struct {
	RunID        string `gorm:"column:run_id;primaryKey;autoIncrement:false"`
	SkillID      string `gorm:"column:skill_id;primaryKey;autoIncrement:false"`
	SkillVersion int    `gorm:"column:skill_version"`
	Order        int    `gorm:"column:order"`
}

func (runSkillRow) TableName() string { return "agent_run_skills" }

// traceRow maps run_traces.
type traceRow struct {
	RunID string `gorm:"column:run_id;primaryKey;autoIncrement:false"`
	Trace jsonb  `gorm:"column:trace;type:jsonb"`
}

func (traceRow) TableName() string { return "run_traces" }

// reviewRow maps reviews for inserts and deletes.
type reviewRow struct {
	ID          string  `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string  `gorm:"column:workspace_id"`
	PRID        string  `gorm:"column:pr_id"`
	AgentID     *string `gorm:"column:agent_id"`
	RunID       *string `gorm:"column:run_id"`
	Kind        string  `gorm:"column:kind"`
	Verdict     *string `gorm:"column:verdict"`
	Summary     *string `gorm:"column:summary"`
	Score       *int    `gorm:"column:score"`
	Model       *string `gorm:"column:model"`
}

func (reviewRow) TableName() string { return "reviews" }

// findingRow maps findings.
type findingRow struct {
	ID                 string     `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	ReviewID           string     `gorm:"column:review_id"`
	File               string     `gorm:"column:file"`
	StartLine          int        `gorm:"column:start_line"`
	EndLine            int        `gorm:"column:end_line"`
	Severity           string     `gorm:"column:severity"`
	Category           string     `gorm:"column:category"`
	Title              string     `gorm:"column:title"`
	Rationale          string     `gorm:"column:rationale"`
	Suggestion         *string    `gorm:"column:suggestion"`
	Confidence         float64    `gorm:"column:confidence"`
	Kind               string     `gorm:"column:kind"`
	TrifectaComponents jsonb      `gorm:"column:trifecta_components;type:jsonb"`
	AcceptedAt         *time.Time `gorm:"column:accepted_at"`
	DismissedAt        *time.Time `gorm:"column:dismissed_at"`
	SkillID            *string    `gorm:"column:skill_id"`
	SkillName          *string    `gorm:"column:skill_name"`
	OutOfScope         bool       `gorm:"column:out_of_scope"`
}

func (findingRow) TableName() string { return "findings" }

// memoryRow maps memory for inserts; the embedding column is never written.
type memoryRow struct {
	ID          string   `gorm:"column:id;primaryKey;default:gen_random_uuid()"`
	WorkspaceID string   `gorm:"column:workspace_id"`
	RepoID      *string  `gorm:"column:repo_id"`
	Scope       string   `gorm:"column:scope"`
	Kind        string   `gorm:"column:kind"`
	Content     string   `gorm:"column:content"`
	Confidence  *float64 `gorm:"column:confidence"`
	Sources     jsonb    `gorm:"column:sources;type:jsonb"`
}

func (memoryRow) TableName() string { return "memory" }

// ---- rows of tables other contexts own (read only here, except last_reviewed_sha) ----

// pullRow maps pull_requests.
type pullRow struct {
	ID          string  `gorm:"column:id;primaryKey"`
	WorkspaceID string  `gorm:"column:workspace_id"`
	RepoID      string  `gorm:"column:repo_id"`
	Number      int     `gorm:"column:number"`
	Title       string  `gorm:"column:title"`
	Author      string  `gorm:"column:author"`
	Base        string  `gorm:"column:base"`
	HeadSHA     string  `gorm:"column:head_sha"`
	Body        *string `gorm:"column:body"`
}

func (pullRow) TableName() string { return "pull_requests" }

// repoRow maps repos.
type repoRow struct {
	ID          string `gorm:"column:id;primaryKey"`
	WorkspaceID string `gorm:"column:workspace_id"`
	Owner       string `gorm:"column:owner"`
	Name        string `gorm:"column:name"`
}

func (repoRow) TableName() string { return "repos" }

// agentRow maps agents.
type agentRow struct {
	ID           string `gorm:"column:id;primaryKey"`
	WorkspaceID  string `gorm:"column:workspace_id"`
	Name         string `gorm:"column:name"`
	Provider     string `gorm:"column:provider"`
	Model        string `gorm:"column:model"`
	SystemPrompt string `gorm:"column:system_prompt"`
	Enabled      bool   `gorm:"column:enabled"`
	Version      int    `gorm:"column:version"`
	Strategy     string `gorm:"column:strategy"`
	CIFailOn     string `gorm:"column:ci_fail_on"`
	RepoIntel    bool   `gorm:"column:repo_intel"`
}

func (agentRow) TableName() string { return "agents" }

// skillRow is the skills columns a run needs, read through agent_skills.
type skillRow struct {
	ID          string `gorm:"column:id"`
	Name        string `gorm:"column:name"`
	Description string `gorm:"column:description"`
	Body        string `gorm:"column:body"`
	Version     int    `gorm:"column:version"`
}

// prFileRow is the pr_files columns the review diff is built from.
type prFileRow struct {
	Path  string `gorm:"column:path"`
	Patch string `gorm:"column:patch"`
}

// evalCaseRow is the eval_cases columns that link a finding to its eval case.
type evalCaseRow struct {
	ID              string `gorm:"column:id"`
	SourceFindingID string `gorm:"column:source_finding_id"`
}

// Package postgres persists the intent context with GORM: it owns the pr_intent table and reads
// pull_requests, repos, pr_commits and pr_files with row types of its own (spec 7.4.1, 7.4.5).
// Every method takes its handle from platformpg.Conn, so a store called inside a transaction joins
// it. Row structs are declared here, per adapter.
package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/intent/app"
	"dev-digest-go/backend/internal/intent/domain"
	platformpg "dev-digest-go/backend/internal/platform/postgres"
	reviewer "dev-digest-go/backend/internal/reviewer/domain"
)

// Store is the GORM-backed implementation of app.Pulls and app.Store.
type Store struct {
	db *gorm.DB
}

var (
	_ app.Pulls = (*Store)(nil)
	_ app.Store = (*Store)(nil)
)

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) conn(ctx context.Context) *gorm.DB { return platformpg.Conn(ctx, s.db) }

func isNoRows(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// storable makes a string storable in a Postgres text or jsonb column: U+0000, which neither
// accepts, becomes U+FFFD, and so does every byte run that is not valid UTF-8 (SQLSTATE 22021,
// 22P05). The text of a pull request or of a model answer can hold either.
func storable(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	return strings.ToValidUTF8(s, "�")
}

func storableList(items []string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = storable(it)
	}
	return out
}

// jsonDoc maps a jsonb column to its raw JSON text.
type jsonDoc []byte

// Scan implements sql.Scanner.
func (v *jsonDoc) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*v = nil
	case []byte:
		*v = append((*v)[:0], s...)
	case string:
		*v = jsonDoc(s)
	default:
		return fmt.Errorf("scan jsonb: unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer; the text form is cast to jsonb by the server.
func (v jsonDoc) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return string(v), nil
}

// ---- app.Pulls ----

// pullRow maps the columns of pull_requests the derivation reads.
type pullRow struct {
	ID      string  `gorm:"column:id"`
	RepoID  string  `gorm:"column:repo_id"`
	Number  int     `gorm:"column:number"`
	Title   string  `gorm:"column:title"`
	Body    *string `gorm:"column:body"`
	Branch  string  `gorm:"column:branch"`
	HeadSHA string  `gorm:"column:head_sha"`
}

func (pullRow) TableName() string { return "pull_requests" }

// repoRow maps the columns of repos the derivation reads.
type repoRow struct {
	ID    string `gorm:"column:id"`
	Owner string `gorm:"column:owner"`
	Name  string `gorm:"column:name"`
	// Provider, Host and ProjectID locate the repository on its code host (0010).
	Provider  string `gorm:"column:provider"`
	Host      string `gorm:"column:host"`
	ProjectID *int64 `gorm:"column:project_id"`
	// WebURL is the project's web address (0010, refreshed by every sync); NULL for GitHub. The
	// GitLab "same project" rule of the link extraction reads it (spec B P7).
	WebURL *string `gorm:"column:web_url"`
}

func (repoRow) TableName() string { return "repos" }

type commitRow struct {
	SHA     string `gorm:"column:sha"`
	Message string `gorm:"column:message"`
}

type pathRow struct {
	Path string `gorm:"column:path"`
}

// Pull returns a pull request of the workspace; a NULL body reads as "".
func (s *Store) Pull(ctx context.Context, workspaceID, prID string) (domain.Pull, error) {
	var row pullRow
	err := s.conn(ctx).Where("workspace_id = ? AND id = ?", workspaceID, prID).Take(&row).Error
	if isNoRows(err) {
		return domain.Pull{}, fmt.Errorf("pull request %s: %w", prID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Pull{}, fmt.Errorf("select pull request: %w", err)
	}
	pull := domain.Pull{ID: row.ID, RepoID: row.RepoID, Number: row.Number, Title: row.Title, Branch: row.Branch, HeadSHA: row.HeadSHA}
	if row.Body != nil {
		pull.Body = *row.Body
	}
	return pull, nil
}

// Repo returns a repository by id. For a GitLab project Owner is the namespace path, Name the last
// path segment, Host the connection's host key and WebURL its web address (all stored by the sync).
func (s *Store) Repo(ctx context.Context, repoID string) (domain.Repo, error) {
	var row repoRow
	err := s.conn(ctx).Where("id = ?", repoID).Take(&row).Error
	if isNoRows(err) {
		return domain.Repo{}, fmt.Errorf("repo %s: %w", repoID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("select repo: %w", err)
	}
	repo := domain.Repo{ID: row.ID, Owner: row.Owner, Name: row.Name, Provider: row.Provider, Host: row.Host}
	if row.ProjectID != nil {
		repo.ProjectID = *row.ProjectID
	}
	if row.WebURL != nil {
		repo.WebURL = *row.WebURL
	}
	return repo, nil
}

// Commits returns the pull request's commits by committed_at ascending (NULL last), then id: the
// order is part of the cache key's ticket extraction (spec D5009).
func (s *Store) Commits(ctx context.Context, prID string) ([]domain.Commit, error) {
	var rows []commitRow
	err := s.conn(ctx).Table("pr_commits").Select("sha, message").Where("pr_id = ?", prID).
		Order("committed_at ASC NULLS LAST, id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select commits: %w", err)
	}
	out := make([]domain.Commit, len(rows))
	for i, r := range rows {
		out[i] = domain.Commit{SHA: r.SHA, Message: r.Message}
	}
	return out, nil
}

// ChangedFiles returns the pull request's stored file paths in byte order.
func (s *Store) ChangedFiles(ctx context.Context, prID string) ([]string, error) {
	var rows []pathRow
	err := s.conn(ctx).Table("pr_files").Select("path").Where("pr_id = ?", prID).
		Order(`path COLLATE "C", id`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select changed files: %w", err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out, nil
}

// ---- app.Store ----

// intentRow maps pr_intent. The columns Node leaves NULL until a derivation are pointers.
type intentRow struct {
	PRID          string     `gorm:"column:pr_id;primaryKey"`
	Intent        string     `gorm:"column:intent"`
	InScope       jsonDoc    `gorm:"column:in_scope;type:jsonb"`
	OutOfScope    jsonDoc    `gorm:"column:out_of_scope;type:jsonb"`
	ChangeType    *string    `gorm:"column:change_type"`
	Confidence    *string    `gorm:"column:confidence"`
	DerivedFrom   string     `gorm:"column:derived_from"`
	Sources       jsonDoc    `gorm:"column:sources;type:jsonb"`
	HeadSHA       *string    `gorm:"column:head_sha"`
	InputHash     *string    `gorm:"column:input_hash"`
	PromptVersion *int       `gorm:"column:prompt_version"`
	Provider      *string    `gorm:"column:provider"`
	Model         *string    `gorm:"column:model"`
	TokensIn      *int       `gorm:"column:tokens_in"`
	TokensOut     *int       `gorm:"column:tokens_out"`
	CostUSD       *float64   `gorm:"column:cost_usd"`
	DerivedAt     *time.Time `gorm:"column:derived_at"`
}

func (intentRow) TableName() string { return "pr_intent" }

// updatedColumns are every column but the key: an upsert replaces the whole row.
var updatedColumns = []string{
	"intent", "in_scope", "out_of_scope", "change_type", "confidence", "derived_from", "sources", "head_sha",
	"input_hash", "prompt_version", "provider", "model", "tokens_in", "tokens_out", "cost_usd", "derived_at",
}

// sourceDoc is a source as the sources column holds it: detail is always present, null when none.
type sourceDoc struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Status string  `json:"status"`
	Detail *string `json:"detail"`
}

// Get returns the pull request's record; a row without derived_at counts as none.
func (s *Store) Get(ctx context.Context, prID string) (domain.Record, bool, error) {
	var row intentRow
	err := s.conn(ctx).Where("pr_id = ?", prID).Take(&row).Error
	if isNoRows(err) {
		return domain.Record{}, false, nil
	}
	if err != nil {
		return domain.Record{}, false, fmt.Errorf("select pr_intent: %w", err)
	}
	if row.DerivedAt == nil {
		return domain.Record{}, false, nil
	}
	rec, err := row.toDomain()
	if err != nil {
		return domain.Record{}, false, err
	}
	return rec, true, nil
}

// Upsert writes every column of the row (insert, or update on pr_id) and returns the stored row,
// whose cost has the column's six decimals.
func (s *Store) Upsert(ctx context.Context, rec domain.Record) (domain.Record, error) {
	row, err := fromDomain(rec)
	if err != nil {
		return domain.Record{}, err
	}
	err = s.conn(ctx).Clauses(
		clause.OnConflict{Columns: []clause.Column{{Name: "pr_id"}}, DoUpdates: clause.AssignmentColumns(updatedColumns)},
		clause.Returning{},
	).Create(&row).Error
	if err != nil {
		return domain.Record{}, fmt.Errorf("upsert pr_intent: %w", err)
	}
	return row.toDomain()
}

func fromDomain(rec domain.Record) (intentRow, error) {
	sources := make([]sourceDoc, len(rec.Sources))
	for i, src := range rec.Sources {
		sources[i] = sourceDoc{Kind: src.Kind, Ref: storable(src.Ref), Status: src.Status}
		if src.Detail != nil {
			d := storable(*src.Detail)
			sources[i].Detail = &d
		}
	}
	inScope, err := json.Marshal(storableList(rec.InScope))
	if err != nil {
		return intentRow{}, fmt.Errorf("encode in_scope: %w", err)
	}
	outOfScope, err := json.Marshal(storableList(rec.OutOfScope))
	if err != nil {
		return intentRow{}, fmt.Errorf("encode out_of_scope: %w", err)
	}
	srcJSON, err := json.Marshal(sources)
	if err != nil {
		return intentRow{}, fmt.Errorf("encode sources: %w", err)
	}
	derivedAt := rec.DerivedAt.UTC()
	return intentRow{
		PRID: rec.PRID, Intent: storable(rec.Text), InScope: inScope, OutOfScope: outOfScope,
		ChangeType: rec.ChangeType, Confidence: rec.Confidence, DerivedFrom: rec.DerivedFrom, Sources: srcJSON,
		HeadSHA: &rec.HeadSHA, InputHash: &rec.InputHash, PromptVersion: &rec.PromptVersion,
		Provider: &rec.Provider, Model: &rec.Model, TokensIn: &rec.TokensIn, TokensOut: &rec.TokensOut,
		CostUSD: rec.CostUSD, DerivedAt: &derivedAt,
	}, nil
}

// toDomain maps a row; NULL text columns read as "" and NULL numbers as 0 (spec 7.3). The caller
// has checked derived_at.
func (r intentRow) toDomain() (domain.Record, error) {
	inScope, outOfScope := []string{}, []string{}
	if err := decodeList(r.InScope, &inScope); err != nil {
		return domain.Record{}, fmt.Errorf("decode in_scope: %w", err)
	}
	if err := decodeList(r.OutOfScope, &outOfScope); err != nil {
		return domain.Record{}, fmt.Errorf("decode out_of_scope: %w", err)
	}
	var docs []sourceDoc
	if len(r.Sources) > 0 {
		if err := json.Unmarshal(r.Sources, &docs); err != nil {
			return domain.Record{}, fmt.Errorf("decode sources: %w", err)
		}
	}
	sources := make([]reviewer.IntentSource, len(docs))
	for i, d := range docs {
		sources[i] = reviewer.IntentSource{Kind: d.Kind, Ref: d.Ref, Status: d.Status, Detail: d.Detail}
	}
	rec := domain.Record{
		PRID: r.PRID, Text: r.Intent, InScope: inScope, OutOfScope: outOfScope, ChangeType: r.ChangeType,
		Confidence: r.Confidence, DerivedFrom: r.DerivedFrom, Sources: sources,
		HeadSHA: deref(r.HeadSHA), InputHash: deref(r.InputHash), PromptVersion: deref(r.PromptVersion),
		Provider: deref(r.Provider), Model: deref(r.Model), TokensIn: deref(r.TokensIn), TokensOut: deref(r.TokensOut),
		CostUSD: r.CostUSD,
	}
	if r.DerivedAt != nil {
		rec.DerivedAt = r.DerivedAt.UTC()
	}
	return rec, nil
}

func decodeList(doc jsonDoc, out *[]string) error {
	if len(doc) == 0 {
		return nil
	}
	var items []string
	if err := json.Unmarshal(doc, &items); err != nil {
		return err
	}
	if items != nil {
		*out = items
	}
	return nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

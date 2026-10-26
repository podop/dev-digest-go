// Package postgres stores the custom model providers of a workspace and their manual models with
// GORM. Every statement gets its handle from pg.Conn, so it joins the transaction a use case opened
// through InTx. The database keeps the bare slug as the provider id; this package takes and returns
// the public id (`custom:<slug>`).
package postgres

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/customproviders/app"
	"dev-digest-go/backend/internal/customproviders/domain"
	"dev-digest-go/backend/internal/llm/custom"
	pg "dev-digest-go/backend/internal/platform/postgres"
)

// Store implements app.Store.
type Store struct {
	db *gorm.DB
	tx *pg.Tx
}

var _ app.Store = (*Store)(nil)

// NewStore builds a Store on a GORM handle. The transaction runner is the same one a use case gets
// from pg.NewTx(db): a transaction opened with either is joined by the other.
func NewStore(db *gorm.DB) *Store { return &Store{db: db, tx: pg.NewTx(db)} }

// InTx runs fn in one transaction (see pg.Tx.InTx).
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.tx.InTx(ctx, fn)
}

// List returns every provider of the workspace, ordered by name then id (byte order), with its
// manual models in their stored order.
func (s *Store) List(ctx context.Context, workspaceID string) ([]domain.Provider, error) {
	conn := pg.Conn(ctx, s.db)
	var rows []providerRow
	err := conn.Table(providersTable).Select(providerColumns).Where("workspace_id = ?", workspaceID).
		Order(`name COLLATE "C", id COLLATE "C"`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("select custom providers: %w", err)
	}
	var models []modelRow
	err = conn.Table(modelsTable).Select(modelColumns).Where("workspace_id = ?", workspaceID).
		Order(`provider_id COLLATE "C", "position"`).Scan(&models).Error
	if err != nil {
		return nil, fmt.Errorf("select custom provider models: %w", err)
	}
	byProvider := make(map[string][]modelRow, len(rows))
	for _, m := range models {
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}
	out := make([]domain.Provider, 0, len(rows))
	for _, r := range rows {
		p, err := r.toDomain(byProvider[r.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Get returns one provider; domain.ErrNotFound when the workspace does not define it.
func (s *Store) Get(ctx context.Context, workspaceID, id string) (domain.Provider, error) {
	return s.get(ctx, workspaceID, id, nil)
}

// GetForUpdate is Get that locks the provider row FOR UPDATE until the transaction ends.
func (s *Store) GetForUpdate(ctx context.Context, workspaceID, id string) (domain.Provider, error) {
	return s.get(ctx, workspaceID, id, &clause.Locking{Strength: clause.LockingStrengthUpdate})
}

// get reads the provider and its models; a non-nil lock is applied to the provider row.
func (s *Store) get(ctx context.Context, workspaceID, id string, lock *clause.Locking) (domain.Provider, error) {
	slug, ok := custom.ParseID(id)
	if !ok {
		return domain.Provider{}, domain.ErrNotFound
	}
	conn := pg.Conn(ctx, s.db)
	var rows []providerRow
	q := conn.Table(providersTable).Select(providerColumns).Where("workspace_id = ? AND id = ?", workspaceID, slug)
	if lock != nil {
		q = q.Clauses(*lock)
	}
	if err := q.Scan(&rows).Error; err != nil {
		return domain.Provider{}, fmt.Errorf("select custom provider: %w", err)
	}
	if len(rows) == 0 {
		return domain.Provider{}, domain.ErrNotFound
	}
	var models []modelRow
	err := conn.Table(modelsTable).Select(modelColumns).Where("workspace_id = ? AND provider_id = ?", workspaceID, slug).
		Order(`"position"`).Scan(&models).Error
	if err != nil {
		return domain.Provider{}, fmt.Errorf("select custom provider models: %w", err)
	}
	return rows[0].toDomain(models)
}

// Exists reports whether the workspace defines the provider.
func (s *Store) Exists(ctx context.Context, workspaceID, id string) (bool, error) {
	return s.exists(ctx, workspaceID, id, nil)
}

// LockDefined is Exists that locks the provider row FOR SHARE until the transaction ends.
func (s *Store) LockDefined(ctx context.Context, workspaceID, id string) (bool, error) {
	return s.exists(ctx, workspaceID, id, &clause.Locking{Strength: clause.LockingStrengthShare})
}

// exists looks the provider id up; a non-nil lock is applied to the provider row.
func (s *Store) exists(ctx context.Context, workspaceID, id string, lock *clause.Locking) (bool, error) {
	slug, ok := custom.ParseID(id)
	if !ok {
		return false, nil
	}
	var found []string
	q := pg.Conn(ctx, s.db).Table(providersTable).Where("workspace_id = ? AND id = ?", workspaceID, slug)
	if lock != nil {
		q = q.Clauses(*lock)
	}
	if err := q.Pluck("id", &found).Error; err != nil {
		return false, fmt.Errorf("select custom provider id: %w", err)
	}
	return len(found) > 0, nil
}

// Insert creates the provider and its models. It takes a lock that serializes the creates of the
// workspace (a transaction-level advisory lock keyed by the workspace; the workspace row itself is
// left alone so foreign-key checks of other writers never wait on it), then refuses a taken id
// (domain.ErrExists) and a full workspace (domain.ErrLimit). A duplicate-key error from the insert
// itself, which only a writer outside this method could cause, is ErrExists too. Called outside a
// transaction it opens one, so the lock covers the whole check-and-insert.
func (s *Store) Insert(ctx context.Context, workspaceID string, p domain.Provider, limit int) error {
	slug, ok := custom.ParseID(p.ID)
	if !ok {
		return fmt.Errorf("insert custom provider: malformed id %q", p.ID)
	}
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		conn := pg.Conn(ctx, s.db)
		// Raw SQL: an advisory lock and the ::text[] cast have no builder form.
		if err := conn.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`,
			"custom_providers:"+workspaceID).Error; err != nil {
			return fmt.Errorf("lock custom providers of workspace: %w", err)
		}
		var ids []string
		if err := conn.Table(providersTable).Where("workspace_id = ?", workspaceID).Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("select custom provider ids: %w", err)
		}
		for _, id := range ids {
			if id == slug {
				return domain.ErrExists
			}
		}
		if len(ids) >= limit {
			return domain.ErrLimit
		}
		err := conn.Exec(`INSERT INTO custom_providers
			(workspace_id, id, name, protocol, base_url, auth_mode, auth_header, extra_header_names,
			 structured_output, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?::text[], ?, COALESCE(?, now()), COALESCE(?, now()))`,
			workspaceID, slug, p.Name, string(p.Protocol), p.BaseURL, string(p.AuthMode), nullIfEmpty(p.AuthHeader),
			textArray(p.ExtraHeaderNames), string(p.StructuredOutput), nullTime(p.CreatedAt), nullTime(p.UpdatedAt)).Error
		if err != nil {
			if isUniqueViolation(err) {
				return domain.ErrExists
			}
			return fmt.Errorf("insert custom provider: %w", err)
		}
		return s.insertModels(ctx, workspaceID, slug, p.Models)
	})
}

// Replace overwrites every field except id and created_at and replaces the models in order.
func (s *Store) Replace(ctx context.Context, workspaceID string, p domain.Provider) error {
	slug, ok := custom.ParseID(p.ID)
	if !ok {
		return domain.ErrNotFound
	}
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		conn := pg.Conn(ctx, s.db)
		// Raw SQL: the ::text[] cast of the header names has no builder form.
		res := conn.Exec(`UPDATE custom_providers SET name = ?, protocol = ?, base_url = ?, auth_mode = ?,
			auth_header = ?, extra_header_names = ?::text[], structured_output = ?, updated_at = COALESCE(?, now())
			WHERE workspace_id = ? AND id = ?`,
			p.Name, string(p.Protocol), p.BaseURL, string(p.AuthMode), nullIfEmpty(p.AuthHeader),
			textArray(p.ExtraHeaderNames), string(p.StructuredOutput), nullTime(p.UpdatedAt), workspaceID, slug)
		if res.Error != nil {
			return fmt.Errorf("update custom provider: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return domain.ErrNotFound
		}
		if err := conn.Table(modelsTable).Where("workspace_id = ? AND provider_id = ?", workspaceID, slug).
			Delete(&modelRow{}).Error; err != nil {
			return fmt.Errorf("delete custom provider models: %w", err)
		}
		return s.insertModels(ctx, workspaceID, slug, p.Models)
	})
}

// Delete removes the provider; its models go with it (ON DELETE CASCADE).
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	slug, ok := custom.ParseID(id)
	if !ok {
		return domain.ErrNotFound
	}
	res := pg.Conn(ctx, s.db).Table(providersTable).Where("workspace_id = ? AND id = ?", workspaceID, slug).
		Delete(&providerRow{})
	if res.Error != nil {
		return fmt.Errorf("delete custom provider: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// insertModels writes the models in one statement, numbering them by their place in the slice.
func (s *Store) insertModels(ctx context.Context, workspaceID, slug string, models []domain.Model) error {
	if len(models) == 0 {
		return nil
	}
	// Raw SQL: one multi-row insert with ::numeric casts so a price never passes through a float.
	var b strings.Builder
	b.WriteString(`INSERT INTO custom_provider_models
		(workspace_id, provider_id, model_id, label, prompt_per_m, completion_per_m, "position") VALUES `)
	args := make([]any, 0, len(models)*7)
	for i, m := range models {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?, ?::numeric, ?::numeric, ?)")
		var prompt, completion *string
		if m.Pricing != nil {
			ps, cs := priceText(m.Pricing.PromptPerM), priceText(m.Pricing.CompletionPerM)
			prompt, completion = &ps, &cs
		}
		args = append(args, workspaceID, slug, m.ID, m.Label, prompt, completion, i)
	}
	if err := pg.Conn(ctx, s.db).Exec(b.String(), args...).Error; err != nil {
		return fmt.Errorf("insert custom provider models: %w", err)
	}
	return nil
}

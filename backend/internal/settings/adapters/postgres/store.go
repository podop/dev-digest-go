// Package postgres stores the per-workspace settings bag with GORM.
package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dev-digest-go/backend/internal/settings/app"
)

// jsonValue maps a jsonb column to raw JSON. An empty value is a SQL NULL (GORM does not call
// Scan for it); List turns it into the JSON literal null.
type jsonValue []byte

// Scan implements sql.Scanner.
func (v *jsonValue) Scan(src any) error {
	switch s := src.(type) {
	case nil:
		*v = nil
	case []byte:
		*v = append((*v)[:0], s...)
	case string:
		*v = jsonValue(s)
	default:
		return fmt.Errorf("scan settings value: unsupported type %T", src)
	}
	return nil
}

// Value implements driver.Valuer; the text form is cast to jsonb by the server.
func (v jsonValue) Value() (driver.Value, error) {
	if len(v) == 0 {
		return "null", nil
	}
	return string(v), nil
}

// settingRow is a row of the settings table. The id column keeps its database default.
type settingRow struct {
	WorkspaceID string    `gorm:"column:workspace_id"`
	UserID      string    `gorm:"column:user_id"`
	Key         string    `gorm:"column:key"`
	Value       jsonValue `gorm:"column:value;type:jsonb"`
}

func (settingRow) TableName() string { return "settings" }

// Store implements the settings port of the app package.
type Store struct {
	db *gorm.DB
}

// NewStore builds a Store on a GORM handle.
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

var _ interface {
	List(context.Context, string) (app.Bag, error)
	Upsert(context.Context, string, string, app.Bag) error
} = (*Store)(nil)

// List returns every setting of the workspace as key to raw JSON.
func (s *Store) List(ctx context.Context, workspaceID string) (app.Bag, error) {
	var rows []settingRow
	if err := s.db.WithContext(ctx).Where("workspace_id = ?", workspaceID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("select settings: %w", err)
	}
	bag := make(app.Bag, len(rows))
	for _, r := range rows {
		if len(r.Value) == 0 { // GORM leaves the field empty for a SQL NULL
			bag[r.Key] = json.RawMessage("null")
			continue
		}
		bag[r.Key] = json.RawMessage(r.Value)
	}
	return bag, nil
}

// Upsert writes all entries in one INSERT … ON CONFLICT statement, so either every key lands
// or none does.
func (s *Store) Upsert(ctx context.Context, workspaceID, userID string, entries app.Bag) error {
	if len(entries) == 0 {
		return nil
	}
	rows := make([]settingRow, 0, len(entries))
	for key, value := range entries {
		rows = append(rows, settingRow{WorkspaceID: workspaceID, UserID: userID, Key: key, Value: jsonValue(value)})
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "workspace_id"}, {Name: "user_id"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	return nil
}

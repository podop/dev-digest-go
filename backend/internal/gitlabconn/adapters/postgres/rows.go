package postgres

import (
	"errors"
	"time"

	"dev-digest-go/backend/internal/gitlabconn/domain"
)

// uniqueViolation is the SQLState of a duplicate key.
const uniqueViolation = "23505"

// Table names, for the builder calls of the store.
const (
	connectionsTable = "gitlab_connections"
	reposTable       = "repos"
)

// connectionColumns lists the columns of gitlab_connections as connectionRow reads them.
const connectionColumns = `id::text AS id, name, base_url, host, created_at, updated_at`

// connectionRow is a row of gitlab_connections.
type connectionRow struct {
	ID        string    `gorm:"column:id"`
	Name      string    `gorm:"column:name"`
	BaseURL   string    `gorm:"column:base_url"`
	Host      string    `gorm:"column:host"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (r connectionRow) toDomain() domain.Connection {
	return domain.Connection{
		ID: r.ID, Name: r.Name, BaseURL: r.BaseURL, Host: r.Host, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// hostCountRow is one group of the repository count per host key.
type hostCountRow struct {
	Host  string `gorm:"column:host"`
	Count int    `gorm:"column:n"`
}

// isUniqueViolation reports whether err is a duplicate-key error (SQLState 23505); the driver's
// error type exposes it as a method.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}

// nullTime maps the zero time to SQL NULL, which the statements turn into now().
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// nullIfEmpty maps "" to SQL NULL, which the statements turn into the column default.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

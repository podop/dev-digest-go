// Package postgres persists installed plugins with GORM and implements the plugins context's
// ports ExportReader, Names, Installer and Plugins. A plugin owns agents, skills, eval cases and
// learnings that belong to other contexts, so this adapter writes those tables itself, in one
// transaction per install and per removal, mirroring what the agents and skills contexts write on
// create and on delete (the plan's rule: the plugin context's adapter owns the cross-table
// transaction; the contexts' stores stay untouched).
package postgres

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/plugins/app"
)

// uniqueViolation is the SQLSTATE of a unique constraint violation.
const uniqueViolation = "23505"

// batchSize is the most rows of one multi-row INSERT: the widest table (agents, 14 columns) stays
// far below the 65,535 bind parameters of one statement.
const batchSize = 500

// Store is the GORM-backed plugin store.
type Store struct {
	db *gorm.DB
	tx *pg.Tx
}

// The ports the store implements. LastExports and WorkspaceName are adapters of cmd/server.
var (
	_ app.ExportReader = (*Store)(nil)
	_ app.Names        = (*Store)(nil)
	_ app.Installer    = (*Store)(nil)
	_ app.Plugins      = (*Store)(nil)
)

// NewStore builds a Store on a GORM handle. Install and Remove open their own transaction on it.
func NewStore(db *gorm.DB) *Store { return &Store{db: db, tx: pg.NewTx(db)} }

// isUniqueViolation reports whether err is a PostgreSQL unique violation, without importing the
// driver.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == uniqueViolation
}

// isUUID reports whether s has the canonical 8-4-4-4-12 hex form; any other id cannot name a row,
// and a query with it would fail (SQLSTATE 22P02) instead of finding nothing.
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
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// newUUID returns a random (version 4) UUID. Install generates its ids itself so a batch insert
// needs no RETURNING to learn which row got which id.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// clean makes a string storable in a Postgres text column: U+0000 and invalid UTF-8 (both
// rejected by the server, SQLSTATE 22021) become U+FFFD. The domain already refuses U+0000; this
// is the last line of defence.
func clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
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

// optional maps an empty string to NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	c := clean(s)
	return &c
}

// jsonText makes raw JSON storable in a jsonb column: nil, empty and the literal null are SQL
// NULL. encoding/json writes U+0000 as the escape \u0000, which jsonb rejects (SQLSTATE 22P05),
// so that escape becomes U+FFFD (an escaped backslash followed by the text u0000 is left alone),
// and invalid UTF-8 is replaced.
func jsonText(raw []byte) *string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	if bytes.Contains(t, []byte(`\u0000`)) {
		out := make([]byte, 0, len(t))
		for i := 0; i < len(t); {
			if t[i] != '\\' || i+1 >= len(t) {
				out = append(out, t[i])
				i++
				continue
			}
			if t[i+1] == 'u' && i+6 <= len(t) && string(t[i+2:i+6]) == "0000" {
				out = append(out, "�"...)
				i += 6
				continue
			}
			out = append(out, t[i], t[i+1])
			i += 2
		}
		t = out
	}
	s := strings.ToValidUTF8(string(t), "�")
	return &s
}

// stamps hands out strictly increasing created_at values, one microsecond apart, so rows inserted
// by one install keep the bundle's order when read back by (created_at, id): a transaction's
// now() would give them all the same instant.
type stamps struct {
	base time.Time
	n    int
}

func (s *stamps) next() time.Time {
	s.n++
	return s.base.Add(time.Duration(s.n) * time.Microsecond)
}

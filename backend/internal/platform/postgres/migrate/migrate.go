// Package migrate applies versioned SQL migrations to Postgres.
//
// Each version is a pair of files, NNNN_name.up.sql and NNNN_name.down.sql. A version is
// applied or reverted in one transaction that also takes a transaction-scoped advisory lock
// and writes the schema_migrations bookkeeping row (the table itself is created inside that
// same transaction), so a failing version leaves the database exactly as it was.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// lockKey is the advisory-lock key serialising concurrent migrators (ASCII "devdgmig").
const lockKey int64 = 0x6465766467_6d6967

const createTable = `CREATE TABLE IF NOT EXISTS public.schema_migrations (
	version    bigint      PRIMARY KEY,
	name       text        NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

var fileName = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_]+)\.(up|down)\.sql$`)

// Migration is one version with its SQL.
type Migration struct {
	Version int64
	Name    string
	Up      string
	Down    string
}

// Status describes one known version.
type Status struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time // zero unless Applied
}

// Migrator applies migrations to one database.
type Migrator struct {
	db         *sql.DB
	migrations []Migration
	log        *slog.Logger
	now        func() time.Time
}

// Load reads and validates the migration files in dir "." of fsys: every version has an up
// and a down file, versions are unique and names are well formed.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	byVersion := map[int64]*Migration{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileName.FindStringSubmatch(e.Name())
		if m == nil {
			if strings.HasSuffix(e.Name(), ".sql") {
				return nil, fmt.Errorf("migration file %q: want NNNN_name.up.sql or NNNN_name.down.sql", e.Name())
			}
			continue
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("migration file %q: invalid version", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		cur := byVersion[version]
		if cur == nil {
			cur = &Migration{Version: version, Name: m[2]}
			byVersion[version] = cur
		} else if cur.Name != m[2] {
			return nil, fmt.Errorf("migration version %d has two names: %q and %q", version, cur.Name, m[2])
		}
		kind := m[3]
		if (kind == "up" && cur.Up != "") || (kind == "down" && cur.Down != "") {
			return nil, fmt.Errorf("migration version %d has two %s files", version, kind)
		}
		if kind == "up" {
			cur.Up = string(body)
		} else {
			cur.Down = string(body)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.Up == "" {
			return nil, fmt.Errorf("migration version %d (%s) has no up file", m.Version, m.Name)
		}
		if m.Down == "" {
			return nil, fmt.Errorf("migration version %d (%s) has no down file", m.Version, m.Name)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// New builds a Migrator over the migrations in fsys. It fails when the files are invalid.
func New(db *sql.DB, fsys fs.FS, log *slog.Logger) (*Migrator, error) {
	migrations, err := Load(fsys)
	if err != nil {
		return nil, err
	}
	return &Migrator{db: db, migrations: migrations, log: log, now: time.Now}, nil
}

// Up applies every pending version in order and returns how many it applied. Versions that
// are already recorded are skipped.
func (m *Migrator) Up(ctx context.Context) (int, error) {
	applied := 0
	for _, mig := range m.migrations {
		ok, err := m.applyUp(ctx, mig)
		if err != nil {
			return applied, fmt.Errorf("migrate up version %d (%s): %w", mig.Version, mig.Name, err)
		}
		if ok {
			applied++
		}
	}
	return applied, nil
}

// Down reverts the latest applied version and returns it. It returns ok=false and no error
// when nothing is applied.
func (m *Migrator) Down(ctx context.Context) (version int64, ok bool, err error) {
	known := map[int64]Migration{}
	for _, mig := range m.migrations {
		known[mig.Version] = mig
	}
	var reverted Migration
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, createTable); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
		var latest int64
		err := tx.QueryRowContext(ctx, `SELECT version FROM public.schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&latest)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read latest version: %w", err)
		}
		mig, found := known[latest]
		if !found {
			return fmt.Errorf("applied version %d has no migration files", latest)
		}
		start := m.now()
		if _, err := tx.ExecContext(ctx, mig.Down); err != nil {
			return fmt.Errorf("run down sql: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM public.schema_migrations WHERE version = $1`, latest); err != nil {
			return fmt.Errorf("delete version row: %w", err)
		}
		reverted = mig
		m.log.InfoContext(ctx, "migration reverted", "version", mig.Version, "name", mig.Name, "duration_ms", m.now().Sub(start).Milliseconds())
		return nil
	})
	if err != nil {
		if reverted.Version != 0 {
			return 0, false, fmt.Errorf("migrate down version %d (%s): %w", reverted.Version, reverted.Name, err)
		}
		return 0, false, fmt.Errorf("migrate down: %w", err)
	}
	return reverted.Version, reverted.Version != 0, nil
}

// Status lists every known version and whether it is applied. It never creates the
// bookkeeping table.
func (m *Migrator) Status(ctx context.Context) ([]Status, error) {
	var exists bool
	if err := m.db.QueryRowContext(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("migrate status: %w", err)
	}
	appliedAt := map[int64]time.Time{}
	if exists {
		rows, err := m.db.QueryContext(ctx, `SELECT version, applied_at FROM public.schema_migrations`)
		if err != nil {
			return nil, fmt.Errorf("migrate status: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v int64
			var at time.Time
			if err := rows.Scan(&v, &at); err != nil {
				return nil, fmt.Errorf("migrate status: %w", err)
			}
			appliedAt[v] = at
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("migrate status: %w", err)
		}
	}
	out := make([]Status, 0, len(m.migrations))
	for _, mig := range m.migrations {
		at, ok := appliedAt[mig.Version]
		out = append(out, Status{Version: mig.Version, Name: mig.Name, Applied: ok, AppliedAt: at})
	}
	return out, nil
}

// applyUp applies one version unless it is already recorded.
func (m *Migrator) applyUp(ctx context.Context, mig Migration) (bool, error) {
	applied := false
	err := m.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, createTable); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
		var done bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = $1)`, mig.Version).Scan(&done); err != nil {
			return fmt.Errorf("check version row: %w", err)
		}
		if done {
			return nil
		}
		start := m.now()
		if _, err := tx.ExecContext(ctx, mig.Up); err != nil {
			return fmt.Errorf("run up sql: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public.schema_migrations (version, name) VALUES ($1, $2)`, mig.Version, mig.Name); err != nil {
			return fmt.Errorf("insert version row: %w", err)
		}
		applied = true
		m.log.InfoContext(ctx, "migration applied", "version", mig.Version, "name", mig.Name, "duration_ms", m.now().Sub(start).Milliseconds())
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// inTx runs fn in a transaction holding the migration advisory lock; it commits when fn
// returns nil and rolls back otherwise.
func (m *Migrator) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("advisory lock: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

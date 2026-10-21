// Package pgtest gives tests a real Postgres 16 with pgvector.
//
// One container is started lazily per test binary (a sync.Once); every test gets its own
// freshly created database in it, dropped on cleanup. Tests that call Empty or Migrated
// are skipped under -short. Call Main from TestMain to stop the container when the binary
// ends; without it the testcontainers reaper removes the container after the process exits.
//
// This package is for tests only: production code must not import it.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/migrations"
)

const (
	image        = "pgvector/pgvector:pg16"
	templateName = "devdigest_migrated_template"
)

// Database is one isolated database inside the shared test container.
type Database struct {
	// Name is the database name.
	Name string
	// DSN connects to this database; use it for code that opens its own pool.
	DSN string
	// DB is a GORM handle on this database, closed on cleanup.
	DB *gorm.DB
}

// Drop removes the database, terminating open connections (including DB's pool). It is safe
// to call more than once; tests use it to simulate a database that disappears.
func (d *Database) Drop(t testing.TB) {
	t.Helper()
	if sqlDB, err := d.DB.DB(); err == nil {
		_ = sqlDB.Close()
	}
	s := shared(t)
	if err := s.admin.Exec(`DROP DATABASE IF EXISTS ` + quote(d.Name) + ` WITH (FORCE)`).Error; err != nil {
		t.Errorf("drop database %s: %v", d.Name, err)
	}
}

// Empty returns a new database with no objects at all.
func Empty(t testing.TB) *Database {
	t.Helper()
	skipShort(t)
	s := shared(t)
	return s.create(t, "")
}

// Migrated returns a new database with every embedded migration applied (no seed data).
func Migrated(t testing.TB) *Database {
	t.Helper()
	skipShort(t)
	s := shared(t)
	s.templateOnce.Do(func() { s.templateErr = s.buildTemplate() })
	if s.templateErr != nil {
		t.Fatalf("pgtest: build migrated template: %v", s.templateErr)
	}
	return s.create(t, templateName)
}

// Main runs the tests of a package and then stops the shared container. Use it as
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }
func Main(m *testing.M) int {
	code := m.Run()
	closeShared()
	return code
}

func skipShort(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: needs Docker (skipped with -short)")
	}
}

type sharedState struct {
	ctr     *postgres.PostgresContainer
	baseURL *url.URL
	admin   *gorm.DB

	templateOnce sync.Once
	templateErr  error
}

var (
	sharedMu    sync.Mutex
	sharedOnce  sync.Once
	sharedValue *sharedState
	sharedErr   error
)

func shared(t testing.TB) *sharedState {
	t.Helper()
	sharedOnce.Do(func() {
		sharedMu.Lock()
		defer sharedMu.Unlock()
		sharedValue, sharedErr = startContainer()
	})
	if sharedErr != nil {
		t.Fatalf("pgtest: start postgres container: %v", sharedErr)
	}
	return sharedValue
}

func startContainer() (*sharedState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := postgres.Run(ctx, image,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", image, err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = ctr.Terminate(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("connection string: %w", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		_ = ctr.Terminate(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("parse connection string: %w", err)
	}
	admin, err := pg.Open(ctx, dsn, discardLogger())
	if err != nil {
		_ = ctr.Terminate(context.WithoutCancel(ctx))
		return nil, err
	}
	return &sharedState{ctr: ctr, baseURL: u, admin: admin}, nil
}

func closeShared() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	s := sharedValue
	if s == nil {
		return
	}
	if sqlDB, err := s.admin.DB(); err == nil {
		_ = sqlDB.Close()
	}
	_ = s.ctr.Terminate(context.Background())
	sharedValue = nil
}

// dsnFor returns the connection string of database name in the shared container.
func (s *sharedState) dsnFor(name string) string {
	u := *s.baseURL
	u.Path = "/" + name
	return u.String()
}

// create makes a uniquely named database, optionally from a template, and opens it.
func (s *sharedState) create(t testing.TB, template string) *Database {
	t.Helper()
	name := "t_" + randomHex(t)
	stmt := `CREATE DATABASE ` + quote(name)
	if template != "" {
		stmt += ` TEMPLATE ` + quote(template)
	}
	if err := s.admin.Exec(stmt).Error; err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	d := &Database{Name: name, DSN: s.dsnFor(name)}
	db, err := pg.Open(t.Context(), d.DSN, discardLogger())
	if err != nil {
		t.Fatalf("pgtest: open database %s: %v", name, err)
	}
	d.DB = db
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		if err := s.admin.Exec(`DROP DATABASE IF EXISTS ` + quote(name) + ` WITH (FORCE)`).Error; err != nil {
			t.Errorf("pgtest: drop database %s: %v", name, err)
		}
	})
	return d
}

// buildTemplate creates the migrated template database once. Cloning it is much faster than
// running the migrations for every test.
func (s *sharedState) buildTemplate() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.admin.WithContext(ctx).Exec(`CREATE DATABASE ` + quote(templateName)).Error; err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	db, err := pg.Open(ctx, s.dsnFor(templateName), discardLogger())
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("template pool: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	return migrateUp(ctx, sqlDB)
}

func migrateUp(ctx context.Context, sqlDB *sql.DB) error {
	m, err := migrate.New(sqlDB, migrations.FS, discardLogger())
	if err != nil {
		return err
	}
	if _, err := m.Up(ctx); err != nil {
		return err
	}
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func quote(ident string) string { return `"` + ident + `"` }

func randomHex(t testing.TB) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("pgtest: random name: %v", err)
	}
	return hex.EncodeToString(b)
}

// Package postgres opens the shared GORM connection to Postgres.
package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	// DefaultMaxOpenConns is the pool cap when WithMaxOpenConns is not given. Postgres allows
	// 100 connections per server (max_connections); the cap keeps one backend, a second
	// instance, the migrate CLI and psql well below it.
	DefaultMaxOpenConns = 20
	// maxIdleConns bounds the connections kept warm; the rest are closed as soon as they are idle.
	maxIdleConns = 10
	// connMaxIdleTime closes a connection that nobody used for this long, so a quiet backend
	// gives its slots back to Postgres.
	connMaxIdleTime = 5 * time.Minute
)

// Option tunes the connection pool of Open.
type Option func(*poolConfig)

type poolConfig struct{ maxOpen int }

// WithMaxOpenConns caps the connections the pool opens at once (database/sql SetMaxOpenConns).
// A request beyond the cap waits for a free connection until its context ends instead of
// opening one more; n < 1 keeps DefaultMaxOpenConns.
func WithMaxOpenConns(n int) Option {
	return func(c *poolConfig) {
		if n >= 1 {
			c.maxOpen = n
		}
	}
}

// Open connects GORM to Postgres and verifies the database answers. The schema is owned by
// SQL migrations; GORM never auto-migrates.
//
// The pool is bounded (DefaultMaxOpenConns unless WithMaxOpenConns says otherwise): an
// unbounded pool let a burst of work open connections until Postgres refused with "too many
// clients". Code that holds a connection (a transaction) must not wait for a second one from
// the same pool: stores take the transaction's handle through Conn.
func Open(ctx context.Context, dsn string, log *slog.Logger, opts ...Option) (*gorm.DB, error) {
	pool := poolConfig{maxOpen: DefaultMaxOpenConns}
	for _, opt := range opts {
		opt(&pool)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.NewSlogLogger(log, logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			// Failed and slow queries are logged; with values inlined, they would write skill
			// bodies, context files and scan errors into the log (NFR202). Placeholders only.
			ParameterizedQueries: true,
		}),
		DisableAutomaticPing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(pool.maxOpen)
	sqlDB.SetMaxIdleConns(min(maxIdleConns, pool.maxOpen))
	sqlDB.SetConnMaxIdleTime(connMaxIdleTime)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

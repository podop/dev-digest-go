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

// Open connects GORM to Postgres and verifies the database answers. The schema is owned by
// SQL migrations; GORM never auto-migrates.
func Open(ctx context.Context, dsn string, log *slog.Logger) (*gorm.DB, error) {
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
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

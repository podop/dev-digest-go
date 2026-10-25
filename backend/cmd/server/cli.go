package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"dev-digest-go/backend/internal/config"
	"dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/migrate"
	"dev-digest-go/backend/internal/seed"
	seedpg "dev-digest-go/backend/internal/seed/adapters/postgres"
	"dev-digest-go/backend/migrations"
)

const usage = "usage: server [healthcheck | migrate up|down|status | seed]"

// runMigrate implements `server migrate up|down|status` (FR17).
func runMigrate(ctx context.Context, cfg config.Config, args []string, stdout io.Writer, log *slog.Logger) error {
	if len(args) != 1 {
		return errors.New("migrate: expected up, down or status; " + usage)
	}
	db, err := postgres.Open(ctx, cfg.DatabaseURL, log, postgres.WithMaxOpenConns(cfg.DBMaxOpenConns))
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	m, err := migrate.New(sqlDB, migrations.FS, log)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	switch args[0] {
	case "up":
		n, err := m.Up(ctx)
		if err != nil {
			return err
		}
		// The cost ledger backfill summary is logged once, by whichever start sees it first (FR15).
		if err := reportLedgerBackfill(ctx, db, log); err != nil {
			log.ErrorContext(ctx, "cost ledger backfill summary not logged", "error", err)
		}
		_, err = fmt.Fprintf(stdout, "migrate up: %d version(s) applied\n", n)
		return err
	case "down":
		v, ok, err := m.Down(ctx)
		if err != nil {
			return err
		}
		if !ok {
			_, err = fmt.Fprintln(stdout, "migrate down: nothing to revert")
			return err
		}
		_, err = fmt.Fprintf(stdout, "migrate down: reverted version %d\n", v)
		return err
	case "status":
		list, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range list {
			state := "pending"
			if s.Applied {
				state = "applied " + s.AppliedAt.UTC().Format(time.RFC3339)
			}
			if _, err := fmt.Fprintf(stdout, "%04d %-24s %s\n", s.Version, s.Name, state); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("migrate: unknown subcommand %q; %s", args[0], usage)
	}
}

// runSeed implements `server seed`.
func runSeed(ctx context.Context, cfg config.Config, stdout io.Writer, log *slog.Logger) error {
	db, err := postgres.Open(ctx, cfg.DatabaseURL, log, postgres.WithMaxOpenConns(cfg.DBMaxOpenConns))
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("postgres pool: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	res, err := seed.New(seedpg.NewStore(db), log).Run(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "seed: workspace %s, %d row(s) inserted\n", res.WorkspaceID, insertedRows(res))
	return err
}

func insertedRows(r seed.Result) int {
	return r.Workspaces + r.Users + r.Members + r.Settings + r.Repos + r.PullRequests + r.Files + r.Commits + r.Agents + r.Skills + r.AgentSkills
}

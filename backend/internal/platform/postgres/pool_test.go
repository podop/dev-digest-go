package postgres_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

// openCapped opens a second pool with the given cap on a fresh database that has the items table.
func openCapped(t *testing.T, maxOpen int) *gorm.DB {
	t.Helper()
	d := pgtest.Empty(t)
	if err := d.DB.Exec(`CREATE TABLE items (id int PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	db, err := pg.Open(t.Context(), d.DSN, slog.New(slog.DiscardHandler), pg.WithMaxOpenConns(maxOpen))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestOpen_PoolIsBoundedByDefault(t *testing.T) {
	d := pgtest.Empty(t)
	sqlDB, err := d.DB.DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != pg.DefaultMaxOpenConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, pg.DefaultMaxOpenConns)
	}
}

// Far more concurrent work than connections: transactions that hold a connection while they work
// and plain queries all finish without an error, and Postgres never sees more connections than
// the cap (before the cap a burst failed with SQLSTATE 53300, too many clients).
func TestPool_MoreConcurrentWorkThanTheCapNeedsNoError(t *testing.T) {
	const maxOpen, txWorkers, readWorkers = 4, 40, 40
	db := openCapped(t, maxOpen)
	tx := pg.NewTx(db)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, txWorkers+readWorkers)
	var peak atomic.Int64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				if n := int64(sqlDB.Stats().OpenConnections); n > peak.Load() {
					peak.Store(n)
				}
			}
		}
	}()

	for i := range txWorkers {
		wg.Go(func() {
			errs <- tx.InTx(t.Context(), func(ctx context.Context) error {
				if err := pg.Conn(ctx, db).Exec(`INSERT INTO items (id) VALUES (?)`, i).Error; err != nil {
					return err
				}
				// Hold the connection a moment, as a transaction doing real work does.
				if err := pg.Conn(ctx, db).Exec(`SELECT pg_sleep(0.02)`).Error; err != nil {
					return err
				}
				// A nested InTx joins the open transaction instead of asking for a second connection.
				return tx.InTx(ctx, func(ctx context.Context) error {
					return pg.Conn(ctx, db).Exec(`UPDATE items SET id = id WHERE id = ?`, i).Error
				})
			})
		})
	}
	for range readWorkers {
		wg.Go(func() {
			var n int64
			errs <- db.WithContext(t.Context()).Raw(`SELECT count(*) FROM items`).Scan(&n).Error
		})
	}
	wg.Wait()
	close(stop)
	<-sampled
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent work error = %v, want nil", err)
		}
	}
	if got := peak.Load(); got > maxOpen {
		t.Errorf("peak open connections = %d, want <= %d", got, maxOpen)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM items`).Scan(&rows).Error; err != nil || rows != txWorkers {
		t.Errorf("items after the burst = %d (err %v), want %d", rows, err, txWorkers)
	}
}

// A saturated pool makes new work wait, and the wait ends with the caller's context: it never
// hangs, and the connection comes back once the holders finish.
func TestPool_SaturatedPoolWaitsAndHonoursTheContext(t *testing.T) {
	db := openCapped(t, 2)
	tx := pg.NewTx(db)

	release := make(chan struct{})
	held := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_ = tx.InTx(t.Context(), func(ctx context.Context) error {
				if err := pg.Conn(ctx, db).Exec(`SELECT 1`).Error; err != nil {
					return err
				}
				held <- struct{}{}
				<-release
				return nil
			})
		})
	}
	<-held
	<-held

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var n int64
	err := db.WithContext(ctx).Raw(`SELECT 1`).Scan(&n).Error
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("query on a saturated pool error = %v, want context.DeadlineExceeded", err)
	}

	close(release)
	wg.Wait()
	if err := db.WithContext(t.Context()).Raw(`SELECT 1`).Scan(&n).Error; err != nil {
		t.Errorf("query after the holders finished error = %v, want nil", err)
	}
}

// With ONE connection a transaction that reaches every store through Conn still works: the
// invariant that keeps a saturated pool deadlock-free.
func TestPool_TransactionNeedsOnlyItsOwnConnection(t *testing.T) {
	db := openCapped(t, 1)
	tx := pg.NewTx(db)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := tx.InTx(ctx, func(ctx context.Context) error {
		if err := pg.Conn(ctx, db).Exec(`INSERT INTO items (id) VALUES (1)`).Error; err != nil {
			return err
		}
		return tx.Savepoint(ctx, func(ctx context.Context) error {
			return pg.Conn(ctx, db).Exec(`INSERT INTO items (id) VALUES (2)`).Error
		})
	})
	if err != nil {
		t.Fatalf("InTx() on a one-connection pool error = %v, want nil", err)
	}
	if got := rowCount(t, db); got != 2 {
		t.Errorf("rows = %d, want 2", got)
	}
}

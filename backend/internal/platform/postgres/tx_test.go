package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"gorm.io/gorm"

	pg "dev-digest-go/backend/internal/platform/postgres"
	"dev-digest-go/backend/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// newTable returns a database with a one-column table, a Tx on it and its raw handle.
func newTable(t *testing.T) (*gorm.DB, *pg.Tx) {
	t.Helper()
	d := pgtest.Empty(t)
	if err := d.DB.Exec(`CREATE TABLE items (id int PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	return d.DB, pg.NewTx(d.DB)
}

func insert(t *testing.T, ctx context.Context, db *gorm.DB, id int) error {
	t.Helper()
	return pg.Conn(ctx, db).Exec(`INSERT INTO items (id) VALUES (?)`, id).Error
}

func rowCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM items`).Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestInTx_CommitsOnNil(t *testing.T) {
	db, tx := newTable(t)
	err := tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := insert(t, ctx, db, 1); err != nil {
			return err
		}
		return insert(t, ctx, db, 2)
	})
	if err != nil {
		t.Fatalf("InTx() error = %v, want nil", err)
	}
	if got := rowCount(t, db); got != 2 {
		t.Errorf("rows after commit = %d, want 2", got)
	}
}

func TestInTx_RollsBackOnError(t *testing.T) {
	db, tx := newTable(t)
	boom := errors.New("boom")
	err := tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := insert(t, ctx, db, 1); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx() error = %v, want %v", err, boom)
	}
	if got := rowCount(t, db); got != 0 {
		t.Errorf("rows after rollback = %d, want 0", got)
	}
}

func TestInTx_RollsBackOnPanic(t *testing.T) {
	db, tx := newTable(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("InTx did not propagate the panic")
			}
		}()
		_ = tx.InTx(t.Context(), func(ctx context.Context) error {
			if err := insert(t, ctx, db, 1); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if got := rowCount(t, db); got != 0 {
		t.Errorf("rows after panic = %d, want 0", got)
	}
}

func TestInTx_WritesAreInvisibleOutsideUntilCommit(t *testing.T) {
	db, tx := newTable(t)
	err := tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := insert(t, ctx, db, 1); err != nil {
			return err
		}
		if got := rowCount(t, db); got != 0 {
			t.Errorf("rows seen outside the open tx = %d, want 0", got)
		}
		var inside int64
		if err := pg.Conn(ctx, db).Raw(`SELECT count(*) FROM items`).Scan(&inside).Error; err != nil {
			return err
		}
		if inside != 1 {
			t.Errorf("rows seen through Conn(ctx) = %d, want 1", inside)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestInTx_NestedJoinsOuter(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		innerErr  error
		outerErr  error
		wantRows  int64
		wantError error
	}{
		{name: "both succeed", wantRows: 2},
		{name: "inner error returned by outer rolls everything back", innerErr: boom, wantRows: 0, wantError: boom},
		{name: "outer error after inner success rolls the inner back", outerErr: boom, wantRows: 0, wantError: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, tx := newTable(t)
			err := tx.InTx(t.Context(), func(ctx context.Context) error {
				if err := insert(t, ctx, db, 1); err != nil {
					return err
				}
				inner := tx.InTx(ctx, func(ctx context.Context) error {
					if err := insert(t, ctx, db, 2); err != nil {
						return err
					}
					return tc.innerErr
				})
				if inner != nil {
					return inner
				}
				return tc.outerErr
			})
			if !errors.Is(err, tc.wantError) {
				t.Errorf("InTx() error = %v, want %v", err, tc.wantError)
			}
			if got := rowCount(t, db); got != tc.wantRows {
				t.Errorf("rows = %d, want %d", got, tc.wantRows)
			}
		})
	}
}

func TestInTx_SwallowedInnerErrorStillCommits(t *testing.T) {
	// A nested call has no savepoint: if the outer function ignores the inner error, the outer
	// transaction commits whatever it wrote. The failing statement itself must not poison it
	// (here the inner error is a plain Go error, not a SQL one).
	db, tx := newTable(t)
	err := tx.InTx(t.Context(), func(ctx context.Context) error {
		if err := insert(t, ctx, db, 1); err != nil {
			return err
		}
		_ = tx.InTx(ctx, func(context.Context) error { return errors.New("ignored") })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowCount(t, db); got != 1 {
		t.Errorf("rows = %d, want 1", got)
	}
}

func TestConn_OutsideTxUsesDB(t *testing.T) {
	db, _ := newTable(t)
	if err := insert(t, t.Context(), db, 1); err != nil {
		t.Fatal(err)
	}
	if got := rowCount(t, db); got != 1 {
		t.Errorf("rows = %d, want 1 (autocommit)", got)
	}
}

func TestConn_IgnoresTxOfAnotherDatabase(t *testing.T) {
	dbA, txA := newTable(t)
	dbB, _ := newTable(t)
	err := txA.InTx(t.Context(), func(ctx context.Context) error {
		// dbB is another connection: the write must land there directly, not in A's tx.
		return insert(t, ctx, dbB, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowCount(t, dbB); got != 1 {
		t.Errorf("rows in B = %d, want 1", got)
	}
	if got := rowCount(t, dbA); got != 0 {
		t.Errorf("rows in A = %d, want 0", got)
	}
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"gorm.io/gorm"
)

// ErrNoTransaction is returned by Savepoint when ctx carries no open transaction of this
// connection: a savepoint only exists inside one.
var ErrNoTransaction = errors.New("postgres: no open transaction in context")

// Tx runs a function in one database transaction that every store taking part reaches through
// the context. The use case decides where the transaction starts and ends; stores stay unaware
// of it and obtain their handle with Conn.
type Tx struct {
	db *gorm.DB
	// savepoints numbers the savepoints this runner creates, so nested ones never share a name.
	savepoints atomic.Uint64
}

// NewTx returns the transaction runner of a connection.
func NewTx(db *gorm.DB) *Tx { return &Tx{db: db} }

// Savepoint runs fn inside a savepoint of the transaction ctx carries (from InTx on this
// connection). When fn returns nil the savepoint is released and its writes stay part of the
// transaction; when it returns an error or panics (the panic propagates) the transaction is
// rolled back to the savepoint, so a failed statement in fn does not abort the surrounding
// transaction and the caller may carry on, for instance with the next unit of work. The error of
// fn is returned as is (joined with the rollback error, if the rollback itself failed). ctx is
// passed to fn unchanged: stores keep reaching the transaction through Conn. Without an open
// transaction it returns ErrNoTransaction and does not call fn.
func (t *Tx) Savepoint(ctx context.Context, fn func(context.Context) error) (err error) {
	v, ok := ctx.Value(txKey{}).(txValue)
	if !ok || v.pool != t.db.ConnPool {
		return ErrNoTransaction
	}
	name := fmt.Sprintf("sp_%d", t.savepoints.Add(1))
	conn := v.tx.WithContext(ctx)
	if err := conn.SavePoint(name).Error; err != nil {
		return fmt.Errorf("create savepoint: %w", err)
	}

	finished := false
	defer func() {
		if finished {
			return
		}
		// fn panicked: undo its writes, then let the panic go on.
		_ = rollbackTo(ctx, v.tx, name)
	}()
	fnErr := fn(ctx)
	finished = true
	if fnErr != nil {
		if rbErr := rollbackTo(ctx, v.tx, name); rbErr != nil {
			return errors.Join(fnErr, rbErr)
		}
		return fnErr
	}
	// RELEASE has no builder in GORM; it is transaction control, like SAVEPOINT itself.
	if err := conn.Exec("RELEASE SAVEPOINT " + name).Error; err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// rollbackTo undoes everything after the savepoint and releases it. It does not follow a
// cancelled ctx: the undo must still reach the database for the transaction to stay usable.
func rollbackTo(ctx context.Context, tx *gorm.DB, name string) error {
	conn := tx.WithContext(context.WithoutCancel(ctx))
	if err := conn.RollbackTo(name).Error; err != nil {
		return fmt.Errorf("rollback to savepoint: %w", err)
	}
	if err := conn.Exec("RELEASE SAVEPOINT " + name).Error; err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// txKey is the context key of the active transaction.
type txKey struct{}

// txValue is what InTx stores in the context: the open transaction and the pool it belongs to,
// so a handle on another database never picks it up.
type txValue struct {
	pool gorm.ConnPool
	tx   *gorm.DB
}

// InTx runs fn in a transaction: it commits when fn returns nil and rolls back on an error or a
// panic (the panic propagates). The context passed to fn carries the transaction; stores called
// with it, directly or through other use cases, join it via Conn. When ctx already carries a
// transaction of this connection, fn simply joins it: nothing is committed or rolled back until
// the outermost InTx returns, and an error returned by a nested fn only rolls back if it is
// returned by the outer one as well.
func (t *Tx) InTx(ctx context.Context, fn func(context.Context) error) error {
	if v, ok := ctx.Value(txKey{}).(txValue); ok && v.pool == t.db.ConnPool {
		return fn(ctx)
	}
	return t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, txValue{pool: t.db.ConnPool, tx: tx}))
	})
}

// Conn returns the handle a store must use for ctx: the open transaction when ctx came from
// InTx on the same connection, else db itself, bound to ctx.
func Conn(ctx context.Context, db *gorm.DB) *gorm.DB {
	if v, ok := ctx.Value(txKey{}).(txValue); ok && v.pool == db.ConnPool {
		return v.tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

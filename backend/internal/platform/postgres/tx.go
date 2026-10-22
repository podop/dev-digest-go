package postgres

import (
	"context"

	"gorm.io/gorm"
)

// Tx runs a function in one database transaction that every store taking part reaches through
// the context. The use case decides where the transaction starts and ends; stores stay unaware
// of it and obtain their handle with Conn.
type Tx struct {
	db *gorm.DB
}

// NewTx returns the transaction runner of a connection.
func NewTx(db *gorm.DB) *Tx { return &Tx{db: db} }

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

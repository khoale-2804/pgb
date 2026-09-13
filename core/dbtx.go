package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the execution surface every statement runs through. *pgxpool.Pool,
// *pgx.Conn and pgx.Tx all satisfy it, so builders are transaction-compatible
// by construction: a tx is passed exactly where a pool would be.
type DBTX interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Sentinels. All wrap with %w so errors.Is / errors.As always traverse to the
// original pgx error — no sentinel is hidden behind a custom error type.
var (
	// ErrNotFound is returned by single-row lookups. It wraps
	// pgx.ErrNoRows, so errors.Is(err, pgx.ErrNoRows) matches too.
	ErrNotFound = fmt.Errorf("pgb: row not found: %w", pgx.ErrNoRows)

	// ErrNoWhere is returned by Update and Delete whose WHERE clause is
	// empty — the guard against accidental full-table mutations. It fires
	// before any SQL is sent.
	ErrNoWhere = errors.New("pgb: statement requires a WHERE clause")

	// ErrCursorMismatch is returned when a cursor token does not match the
	// sort key in play: different column, direction, table, or a token
	// issued before a cursor-format version bump. Fail closed.
	ErrCursorMismatch = errors.New("pgb: cursor does not match the requested sort key")
)

// beginner is the capability WithTx needs. Pools and connections have Begin;
// a plain pgx.Tx does not (it is already inside a transaction).
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn inside a transaction: Begin, fn(tx), Commit on nil error,
// Rollback on error or panic (panics are re-raised after rollback). db must
// be able to begin a transaction — *pgxpool.Pool and *pgx.Conn can, a bare
// pgx.Tx cannot and is rejected with an error.
func WithTx[T any](ctx context.Context, db DBTX, fn func(DBTX) (T, error)) (T, error) {
	var zero T
	b, ok := db.(beginner)
	if !ok {
		return zero, errors.New("pgb: WithTx requires a connection or pool that can begin a transaction")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()
	v, err := fn(tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return v, nil
}

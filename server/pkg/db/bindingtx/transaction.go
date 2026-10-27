package bindingtx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// One database-wide prefix also covers batches spanning several workspaces.
// Acquire it before row locks, and retain it through the original transaction.
const LockKey int64 = 0x4d554c5442494e44

type Starter interface {
	Begin(context.Context) (pgx.Tx, error)
}

type WaitMode bool

const TryLock WaitMode = true

var ErrBusy = errors.New("binding transaction is busy")

func Begin(ctx context.Context, starter Starter, mode ...WaitMode) (pgx.Tx, error) {
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if len(mode) > 0 && mode[0] == TryLock {
		var acquired bool
		err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", LockKey).Scan(&acquired)
		if err == nil && !acquired {
			err = ErrBusy
		}
	} else {
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", LockKey)
	}
	if err != nil {
		_ = tx.Rollback(context.Background())
		return nil, fmt.Errorf("lock binding transaction: %w", err)
	}

	return tx, nil
}

// BeginReadOnly cannot participate in a row-lock cycle: PostgreSQL rejects
// writes and locking reads for the entire transaction.
func BeginReadOnly(ctx context.Context, starter Starter) (pgx.Tx, error) {
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

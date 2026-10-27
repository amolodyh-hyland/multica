package bindingtx

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Starter interface {
	Begin(context.Context) (pgx.Tx, error)
}

type WaitMode bool

const TryLock WaitMode = true

var ErrBusy = errors.New("binding transaction is busy")

// Resolve on this connection, acquire the complete sorted key set before rows,
// then revalidate owner scopes. Scope changes only retry before caller work.
func Begin(ctx context.Context, starter Starter, scope Scope, mode ...WaitMode) (pgx.Tx, error) {
	scope.tryLock = len(mode) > 0 && mode[0] == TryLock
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := starter.Begin(ctx)
		if err != nil {
			return nil, err
		}
		fail := func(err error) (pgx.Tx, error) {
			_ = tx.Rollback(context.Background())
			return nil, fmt.Errorf("lock binding transaction: %w", err)
		}
		if scope.prepare != nil {
			if err = scope.prepare(ctx, preparationTx{Tx: tx}); err != nil {
				return fail(err)
			}
		}
		if scope.all || scope.creating {
			if err = acquireCatalog(ctx, tx, scope.creating, scope.tryLock); err != nil {
				return fail(err)
			}
		}

		before, err := scope.resolve(ctx, tx)
		missing := errors.Is(err, pgx.ErrNoRows) && scope.missingAll
		if missing {
			// Preserve zero-row/idempotent operations on deleted owners.
			// Broaden only BEFORE any workspace key, never during revalidation.
			if err = acquireCatalog(ctx, tx, false, scope.tryLock); err != nil {
				return fail(err)
			}
			before, err = AllWorkspaces().resolve(ctx, tx)
		}
		if err != nil {
			return fail(err)
		}
		keys := make([]int32, 0, len(before))
		for _, id := range before {
			keys = append(keys, WorkspaceKey(id))
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		for _, key := range keys {
			if scope.tryLock {
				var acquired bool
				err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1,$2)", Namespace, key).Scan(&acquired)
				if err == nil && !acquired {
					err = ErrBusy
				}
			} else {
				_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1,$2)", Namespace, key)
			}
			if err != nil {
				return fail(err)
			}
		}
		after, err := scope.resolve(ctx, tx)
		if errors.Is(err, pgx.ErrNoRows) && scope.missingAll {
			if missing {
				return tx, nil
			}
			_ = tx.Rollback(context.Background())
			continue
		}
		if err != nil {
			return fail(err)
		}
		if !missing && slices.Equal(before, after) {
			return tx, nil
		}
		_ = tx.Rollback(context.Background())
	}
	return nil, errors.New("binding workspace scope changed")
}

func acquireCatalog(ctx context.Context, tx pgx.Tx, exclusive, try bool) error {
	name := "pg_advisory_xact_lock_shared"
	if exclusive {
		name = "pg_advisory_xact_lock"
	}
	if try {
		name = strings.Replace(name, "pg_", "pg_try_", 1)
		var acquired bool
		if err := tx.QueryRow(ctx, "SELECT "+name+"($1,$2)", catalogNamespace, int32(0)).Scan(&acquired); err != nil {
			return err
		}
		if !acquired {
			return ErrBusy
		}
		return nil
	}
	_, err := tx.Exec(ctx, "SELECT "+name+"($1,$2)", catalogNamespace, int32(0))
	return err
}

type ReadIsolation string

const ReadCommitted ReadIsolation = "READ COMMITTED"

// BeginReadOnly sets database-enforced read-only mode without writer keys.
func BeginReadOnly(ctx context.Context, starter Starter, isolation ...ReadIsolation) (pgx.Tx, error) {
	tx, err := starter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	level := "REPEATABLE READ"
	if len(isolation) > 0 && isolation[0] == ReadCommitted {
		level = "READ COMMITTED"
	}
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL "+level+" READ ONLY"); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

// Preparation is deliberately unable to mutate or take row locks. Slot locks
// are advisory and precede workspace keys; the caller cannot commit this phase.
type preparationTx struct{ pgx.Tx }

var preparationWrite = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|truncate|lock_task_owner_rows|issue_runtime_for_run|for\s+(?:key\s+)?share)\b`)

func preparationAllowed(sql string) error {
	clean := sqlStrings.ReplaceAllString(sqlComments.ReplaceAllString(sql, ""), "''")
	if preparationWrite.MatchString(clean) {
		return errors.New("binding preparation cannot write or lock rows")
	}
	return nil
}
func (t preparationTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := preparationAllowed(sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return t.Tx.Exec(ctx, sql, args...)
}
func (t preparationTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := preparationAllowed(sql); err != nil {
		return nil, err
	}
	return t.Tx.Query(ctx, sql, args...)
}
func (t preparationTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := preparationAllowed(sql); err != nil {
		return errorRow{err}
	}
	return t.Tx.QueryRow(ctx, sql, args...)
}
func (preparationTx) Commit(context.Context) error {
	return errors.New("binding preparation cannot commit")
}
func (preparationTx) Rollback(context.Context) error {
	return errors.New("binding preparation cannot rollback")
}
func (preparationTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("binding preparation cannot begin a nested transaction")
}

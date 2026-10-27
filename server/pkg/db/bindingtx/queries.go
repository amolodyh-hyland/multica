package bindingtx

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type queries struct{ DBTX }

func Wrap(db DBTX) DBTX {
	if _, ok := db.(*queries); ok {
		return db
	}
	return &queries{DBTX: db}
}

var sqlComments = regexp.MustCompile(`(?m)--[^\n]*|(?s:/\*.*?\*/)`)
var sqlStrings = regexp.MustCompile(`'(?:''|[^'])*'`)
var lockingSQL = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|truncate|pg_(?:try_)?advisory_\w+|lock_task_owner_rows|issue_runtime_for_run|for\s+(?:key\s+)?share)\b`)

func (q *queries) transaction(ctx context.Context, sql string) (pgx.Tx, bool, error) {
	if !lockingSQL.MatchString(sqlStrings.ReplaceAllString(sqlComments.ReplaceAllString(sql, ""), "''")) {
		return nil, false, nil
	}
	if tx, ok := q.DBTX.(pgx.Tx); ok {
		_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", LockKey)
		return tx, false, err
	}
	starter, ok := q.DBTX.(Starter)
	if !ok {
		return nil, false, errors.New("binding write requires a transaction starter")
	}
	tx, err := Begin(ctx, starter)
	return tx, true, err
}

func (q *queries) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, own, err := q.transaction(ctx, sql)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	if tx == nil {
		return q.DBTX.Exec(ctx, sql, args...)
	}
	if own {
		defer tx.Rollback(context.Background())
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err == nil && own {
		err = tx.Commit(ctx)
	}
	return tag, err
}

func (q *queries) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx, own, err := q.transaction(ctx, sql)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return q.DBTX.Query(ctx, sql, args...)
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		if own {
			_ = tx.Rollback(context.Background())
		}
		return nil, err
	}
	if !own {
		return rows, nil
	}
	return &transactionRows{Rows: rows, tx: tx, ctx: ctx}, nil
}

func (q *queries) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx, own, err := q.transaction(ctx, sql)
	if err != nil {
		return errorRow{err}
	}
	if tx == nil {
		return q.DBTX.QueryRow(ctx, sql, args...)
	}
	row := tx.QueryRow(ctx, sql, args...)
	if !own {
		return row
	}
	return transactionRow{Row: row, tx: tx, ctx: ctx}
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }

type transactionRow struct {
	pgx.Row
	tx  pgx.Tx
	ctx context.Context
}

func (r transactionRow) Scan(dest ...any) error {
	defer r.tx.Rollback(context.Background())
	scanErr := r.Row.Scan(dest...)
	if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
		return scanErr
	}
	if err := r.tx.Commit(r.ctx); err != nil {
		return err
	}
	return scanErr
}

type transactionRows struct {
	pgx.Rows
	tx   pgx.Tx
	ctx  context.Context
	done bool
	err  error
}

func (r *transactionRows) Next() bool {
	if r.done {
		return false
	}
	if r.Rows.Next() {
		return true
	}
	r.done = true
	r.err = r.Rows.Err()
	if r.err == nil {
		r.err = r.tx.Commit(r.ctx)
	}
	if r.err != nil {
		_ = r.tx.Rollback(context.Background())
	}
	return false
}
func (r *transactionRows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.Rows.Err()
}
func (r *transactionRows) Close() {
	r.Rows.Close()
	if !r.done {
		r.done = true
		_ = r.tx.Rollback(context.Background())
	}
}

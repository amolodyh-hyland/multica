package bindingtx

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type testTx struct {
	pgx.Tx
	events                        *[]string
	prefixErr, scanErr, commitErr error
	tryAcquired                   bool
}

func (t *testTx) Begin(context.Context) (pgx.Tx, error) {
	*t.events = append(*t.events, "begin")
	return t, nil
}
func (t *testTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if sql == "SELECT pg_advisory_xact_lock($1)" {
		*t.events = append(*t.events, "prefix")
		if len(args) != 1 || args[0] != LockKey {
			return pgconn.CommandTag{}, errors.New("wrong prefix key")
		}
		return pgconn.CommandTag{}, t.prefixErr
	}
	*t.events = append(*t.events, "write")
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (t *testTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if sql == "SELECT pg_try_advisory_xact_lock($1)" {
		*t.events = append(*t.events, "try_prefix")
		return boolRow{value: t.tryAcquired}
	}
	*t.events = append(*t.events, "query")
	return errorRow{t.scanErr}
}
func (t *testTx) Commit(context.Context) error {
	*t.events = append(*t.events, "commit")
	return t.commitErr
}
func (t *testTx) Rollback(context.Context) error {
	*t.events = append(*t.events, "rollback")
	return nil
}

type testDB struct {
	DBTX
	tx *testTx
}

func (d testDB) Begin(ctx context.Context) (pgx.Tx, error) { return d.tx.Begin(ctx) }

func TestBeginAcquiresPrefixBeforeCallerWork(t *testing.T) {
	events := []string{}
	tx := &testTx{events: &events}
	got, err := Begin(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = got.Exec(context.Background(), "UPDATE agent SET status='idle'")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"begin", "prefix", "write"}) {
		t.Fatal(events)
	}
}

func TestStandaloneWriteTransactionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name               string
		scanErr, commitErr error
		want               []string
	}{
		{name: "success", want: []string{"begin", "prefix", "query", "commit", "rollback"}},
		{name: "no_rows_commits_statement", scanErr: pgx.ErrNoRows, want: []string{"begin", "prefix", "query", "commit", "rollback"}},
		{name: "scan_error_rolls_back", scanErr: errors.New("scan failed"), want: []string{"begin", "prefix", "query", "rollback"}},
		{name: "commit_error_returns", commitErr: errors.New("commit failed"), want: []string{"begin", "prefix", "query", "commit", "rollback"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []string{}
			tx := &testTx{events: &events, scanErr: tc.scanErr, commitErr: tc.commitErr}
			err := Wrap(testDB{tx: tx}).QueryRow(context.Background(), "UPDATE agent SET status='idle' RETURNING id").Scan()
			wantErr := tc.scanErr
			if tc.commitErr != nil {
				wantErr = tc.commitErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("error=%v want %v", err, wantErr)
			}
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatal(events)
			}
		})
	}
}

func TestPrefixFailureRollsBackWithoutWriting(t *testing.T) {
	events := []string{}
	injected := errors.New("prefix failed")
	tx := &testTx{events: &events, prefixErr: injected}
	_, err := Wrap(testDB{tx: tx}).Exec(context.Background(), "DELETE FROM agent_runtime")
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"begin", "prefix", "rollback"}) {
		t.Fatal(events)
	}
}

func TestCallerTransactionKeepsOriginalCommitBoundary(t *testing.T) {
	events := []string{}
	tx := &testTx{events: &events}
	if _, err := Wrap(tx).Exec(context.Background(), "UPDATE agent SET status='idle'"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"prefix", "write"}) {
		t.Fatal(events)
	}
}

type testRows struct {
	pgx.Rows
	remaining int
	err       error
}

func (r *testRows) Next() bool {
	if r.remaining == 0 {
		return false
	}
	r.remaining--
	return true
}
func (r *testRows) Close()     {}
func (r *testRows) Err() error { return r.err }

func TestRowsRetainTransactionUntilConsumed(t *testing.T) {
	for _, mode := range []string{"consumed", "closed_early", "query_error", "commit_error"} {
		t.Run(mode, func(t *testing.T) {
			events := []string{}
			tx := &testTx{events: &events}
			rows := &testRows{remaining: 1}
			if mode == "query_error" {
				rows.err = errors.New("query failed")
			}
			if mode == "commit_error" {
				tx.commitErr = errors.New("commit failed")
			}
			result := &transactionRows{Rows: rows, tx: tx, ctx: context.Background()}
			if !result.Next() {
				t.Fatal("first row missing")
			}
			if len(events) != 0 {
				t.Fatalf("transaction completed before rows were consumed: %v", events)
			}
			if mode == "closed_early" {
				result.Close()
				if !reflect.DeepEqual(events, []string{"rollback"}) {
					t.Fatal(events)
				}
				return
			}
			if result.Next() {
				t.Fatal("unexpected second row")
			}
			result.Close()
			if mode == "query_error" {
				if !errors.Is(result.Err(), rows.err) || !reflect.DeepEqual(events, []string{"rollback"}) {
					t.Fatalf("error=%v events=%v", result.Err(), events)
				}
			} else {
				if !errors.Is(result.Err(), tx.commitErr) || events[0] != "commit" {
					t.Fatalf("error=%v events=%v", result.Err(), events)
				}
			}
		})
	}
}

type boolRow struct{ value bool }

func (r boolRow) Scan(dest ...any) error { *dest[0].(*bool) = r.value; return nil }
func TestBeginNonblockingUsesSamePrefix(t *testing.T) {
	for _, acquired := range []bool{true, false} {
		events := []string{}
		tx := &testTx{events: &events, tryAcquired: acquired}
		_, err := Begin(context.Background(), tx, TryLock)
		if acquired {
			if err != nil || !reflect.DeepEqual(events, []string{"begin", "try_prefix"}) {
				t.Fatalf("error=%v events=%v", err, events)
			}
		} else {
			if !errors.Is(err, ErrBusy) || !reflect.DeepEqual(events, []string{"begin", "try_prefix", "rollback"}) {
				t.Fatalf("error=%v events=%v", err, events)
			}
		}
	}
}

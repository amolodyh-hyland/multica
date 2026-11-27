package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type wakeupDocBindingTx struct {
	pgx.Tx
	t         *testing.T
	wakeup    db.ChatWakeup
	docID     pgtype.UUID
	events    []string
	prefixErr error
	lookupErr error
	locked    bool
}

func (f *wakeupDocBindingTx) Begin(context.Context) (pgx.Tx, error) {
	f.events = append(f.events, "begin")
	return f, nil
}
func (f *wakeupDocBindingTx) Commit(context.Context) error {
	f.events = append(f.events, "commit")
	return nil
}
func (f *wakeupDocBindingTx) Rollback(context.Context) error {
	f.events = append(f.events, "rollback")
	return nil
}
func (f *wakeupDocBindingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if sql != "SELECT pg_advisory_xact_lock($1,$2)" || !reflect.DeepEqual(args, []any{bindingtx.Namespace, bindingtx.WorkspaceKey(f.wakeup.WorkspaceID)}) {
		f.t.Fatalf("unexpected scope SQL %q args %v", sql, args)
	}
	f.events = append(f.events, "prefix")
	f.locked = f.prefixErr == nil
	return pgconn.CommandTag{}, f.prefixErr
}
func (f *wakeupDocBindingTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "-- name: GetChatWakeupInSession"):
		f.events = append(f.events, "lookup")
		if !reflect.DeepEqual(args, []any{f.wakeup.ID, f.wakeup.ChatSessionID}) {
			f.t.Fatalf("wakeup lookup escaped its session: %v", args)
		}
		if f.lookupErr != nil {
			return errRow{f.lookupErr}
		}
	case strings.Contains(sql, "-- name: LockChatWakeupInSession"):
		f.events = append(f.events, "lock")
		if !f.locked || !reflect.DeepEqual(args, []any{f.wakeup.ID, f.wakeup.ChatSessionID}) {
			f.t.Fatal("wakeup row locked without its resolved workspace prefix and session")
		}
	case strings.Contains(sql, "-- name: UpdateChatWakeupConfig"):
		f.events = append(f.events, "update")
		if !f.locked || len(args) != 9 || args[8] != f.wakeup.ID {
			f.t.Fatal("wakeup update escaped its locked row")
		}
	default:
		f.t.Fatalf("unexpected row SQL: %s", sql)
	}
	return wakeupBindingRow{f.wakeup}
}
func (f *wakeupDocBindingTx) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.HasPrefix(sql, "SELECT DISTINCT workspace_id FROM (") {
		if !strings.Contains(sql, "FROM doc WHERE") || !reflect.DeepEqual(args, []any{[]pgtype.UUID{f.docID}}) {
			f.t.Fatalf("doc owner resolution escaped its doc: %v", args)
		}
		f.events = append(f.events, "owner")
		return &sliceRows{rows: [][]any{{f.wakeup.WorkspaceID}}}, nil
	}
	if !f.locked || !strings.Contains(sql, "-- name: CancelAgentTasksByDoc") || args[0] != f.docID {
		f.t.Fatalf("doc cancellation before prefix or outside owner: %s %v", sql, args)
	}
	if strings.Contains(sql, "-- name: CancelAgentTasksByDocThread") && !reflect.DeepEqual(args, []any{f.docID, f.wakeup.ID}) {
		f.t.Fatalf("doc cancellation escaped its discussion: %v", args)
	}
	f.events = append(f.events, "cancel")
	return &sliceRows{}, nil
}

type wakeupBindingRow struct{ value db.ChatWakeup }

func (r wakeupBindingRow) Scan(dest ...any) error {
	v := reflect.ValueOf(r.value)
	if len(dest) != v.NumField() {
		return errors.New("wakeup fixture column count mismatch")
	}
	for i, d := range dest {
		reflect.ValueOf(d).Elem().Set(v.Field(i))
	}
	return nil
}

func newWakeupDocBindingTx(t *testing.T) *wakeupDocBindingTx {
	return &wakeupDocBindingTx{t: t, docID: pgtype.UUID{Bytes: [16]byte{4}, Valid: true}, wakeup: db.ChatWakeup{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		ChatSessionID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, Status: "active", Kind: "at",
	}}
}

func TestChatWakeupRescheduleBindingScopeAndRefusals(t *testing.T) {
	injected := errors.New("prefix unavailable")
	for _, name := range []string{"success", "not_found", "ended", "prefix_failure"} {
		t.Run(name, func(t *testing.T) {
			f := newWakeupDocBindingTx(t)
			want := []string{"lookup", "begin", "prefix", "lock", "update", "commit", "rollback"}
			var wantErr error
			switch name {
			case "not_found":
				f.lookupErr, wantErr = pgx.ErrNoRows, ErrChatWakeupNotFound
				want = []string{"lookup"}
			case "ended":
				f.wakeup.Status, wantErr = "finished", ErrChatWakeupEnded
				want = []string{"lookup", "begin", "prefix", "lock", "rollback"}
			case "prefix_failure":
				f.prefixErr, wantErr = injected, injected
				want = []string{"lookup", "begin", "prefix", "rollback"}
			}
			s := ChatWakeupService{Tasks: &TaskService{Queries: db.New(f), TxStarter: f}}
			later := time.Now().Add(time.Hour)
			_, err := s.Reschedule(context.Background(), f.wakeup.ChatSessionID, f.wakeup.ID, ChatWakeupTiming{At: &later})
			if !errors.Is(err, wantErr) || !reflect.DeepEqual(f.events, want) {
				t.Fatalf("error=%v events=%v; want error=%v events=%v", err, f.events, wantErr, want)
			}
		})
	}
}

func TestDocCancellationBindingScope(t *testing.T) {
	for _, thread := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "doc", true: "thread"}[thread], map[bool]string{false: "success", true: "prefix_failure"}[fail]}, "/"), func(t *testing.T) {
				f := newWakeupDocBindingTx(t)
				want := []string{"begin", "owner", "prefix", "owner", "cancel", "commit", "rollback"}
				if fail {
					f.prefixErr = errors.New("prefix unavailable")
					want = []string{"begin", "owner", "prefix", "rollback"}
				}
				s := TaskService{Queries: db.New(f), TxStarter: f}
				var err error
				if thread {
					err = s.CancelTasksForDocThread(context.Background(), f.docID, f.wakeup.ID)
				} else {
					err = s.CancelTasksForDoc(context.Background(), f.docID)
				}
				if !errors.Is(err, f.prefixErr) || !reflect.DeepEqual(f.events, want) {
					t.Fatalf("error=%v events=%v; want error=%v events=%v", err, f.events, f.prefixErr, want)
				}
			})
		}
	}
}

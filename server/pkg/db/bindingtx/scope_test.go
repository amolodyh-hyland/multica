package bindingtx

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type scopeScript struct {
	results  [][]pgtype.UUID
	events   []string
	keys     []int32
	begins   int
	acquired bool
}
type scopeScriptTx struct {
	pgx.Tx
	script *scopeScript
}

func (s *scopeScript) Begin(context.Context) (pgx.Tx, error) {
	s.begins++
	s.events = append(s.events, "begin")
	return &scopeScriptTx{script: s}, nil
}
func (t *scopeScriptTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if len(args) == 2 && args[0] == Namespace {
		t.script.events = append(t.script.events, "workspace")
		t.script.keys = append(t.script.keys, args[1].(int32))
	} else if len(args) == 2 && args[0] == catalogNamespace {
		t.script.events = append(t.script.events, "catalog")
	} else {
		t.script.events = append(t.script.events, sql)
	}
	return pgconn.NewCommandTag("SELECT 1"), nil
}
func (t *scopeScriptTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	t.script.events = append(t.script.events, "metadata")
	if len(t.script.results) == 0 {
		return nil, errors.New("unexpected metadata query")
	}
	ids := t.script.results[0]
	t.script.results = t.script.results[1:]
	return &scopeUUIDRows{ids: ids}, nil
}
func (t *scopeScriptTx) QueryRow(context.Context, string, ...any) pgx.Row {
	t.script.events = append(t.script.events, "try")
	return boolRow{value: t.script.acquired}
}
func (t *scopeScriptTx) Rollback(context.Context) error {
	t.script.events = append(t.script.events, "rollback")
	return nil
}
func (t *scopeScriptTx) Commit(context.Context) error {
	t.script.events = append(t.script.events, "commit")
	return nil
}

type scopeUUIDRows struct {
	pgx.Rows
	ids     []pgtype.UUID
	current pgtype.UUID
}

func (r *scopeUUIDRows) Next() bool {
	if len(r.ids) == 0 {
		return false
	}
	r.current = r.ids[0]
	r.ids = r.ids[1:]
	return true
}
func (r *scopeUUIDRows) Scan(dest ...any) error { *dest[0].(*pgtype.UUID) = r.current; return nil }
func (r *scopeUUIDRows) Close()                 {}
func (r *scopeUUIDRows) Err() error             { return nil }

func TestWorkspaceScopeSortsAndDeduplicatesActualKeys(t *testing.T) {
	other := testUUID
	other.Bytes[0]++
	s := &scopeScript{}
	tx, err := Begin(context.Background(), s, Workspaces(other, testUUID, other, testUUID.String()))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	want := []int32{WorkspaceKey(other), WorkspaceKey(testUUID)}
	slices.Sort(want)
	if !reflect.DeepEqual(s.keys, want) {
		t.Fatalf("keys %v, want sorted unique %v", s.keys, want)
	}
}

func TestScopeChangeRetriesBeforeCallerWork(t *testing.T) {
	other := testUUID
	other.Bytes[0]++
	s := &scopeScript{results: [][]pgtype.UUID{{testUUID}, {other}, {other}, {other}}}
	tx, err := Begin(context.Background(), s, Lookup("SELECT workspace_id FROM metadata"))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if s.begins != 2 || !reflect.DeepEqual(s.keys, []int32{WorkspaceKey(testUUID), WorkspaceKey(other)}) {
		t.Fatalf("scope retry: begins=%d keys=%v events=%v", s.begins, s.keys, s.events)
	}
	if _, err := tx.Exec(context.Background(), "caller"); err != nil {
		t.Fatal(err)
	}
	if s.events[len(s.events)-1] != "caller" {
		t.Fatal(s.events)
	}
}

func TestDisappearedOwnerRestartsBeforeCatalogAcquisition(t *testing.T) {
	s := &scopeScript{results: [][]pgtype.UUID{{testUUID}, {}, {}, {testUUID}, {}}}
	tx, err := Begin(context.Background(), s, Owners(testUUID))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	want := []string{"begin", "metadata", "workspace", "metadata", "rollback", "begin", "metadata", "catalog", "metadata", "workspace", "metadata"}
	if !reflect.DeepEqual(s.events, want) {
		t.Fatalf("late catalog acquisition or lost idempotent boundary: %v", s.events)
	}
}

func TestPreparationCannotWriteLockRowsOrCommit(t *testing.T) {
	for _, sql := range []string{"UPDATE agent SET status='idle'", "SELECT * FROM issue FOR UPDATE", "SELECT * FROM agent FOR SHARE", "SELECT issue_runtime_for_run($1,$2)"} {
		s := &scopeScript{}
		_, err := Begin(context.Background(), s, Workspaces(testUUID).WithPreparation(func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, sql); return err }))
		if err == nil || !reflect.DeepEqual(s.events, []string{"begin", "rollback"}) {
			t.Fatalf("preparation %q: error=%v events=%v", sql, err, s.events)
		}
	}
	s := &scopeScript{}
	_, err := Begin(context.Background(), s, Workspaces(testUUID).WithPreparation(func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }))
	if err == nil || !reflect.DeepEqual(s.events, []string{"begin", "rollback"}) {
		t.Fatalf("preparation committed: %v %v", err, s.events)
	}
}

func TestScopeLookupCannotLockRowsBeforePrefix(t *testing.T) {
	s := &scopeScript{}
	_, err := Begin(context.Background(), s, Lookup("SELECT id FROM workspace FOR SHARE"))
	if err == nil || !reflect.DeepEqual(s.events, []string{"begin", "rollback"}) {
		t.Fatalf("locking lookup: %v %v", err, s.events)
	}
}

func TestTryCatalogAdmissionIsNonblocking(t *testing.T) {
	s := &scopeScript{}
	_, err := Begin(context.Background(), s, AllWorkspaces(), TryLock)
	if !errors.Is(err, ErrBusy) || !reflect.DeepEqual(s.events, []string{"begin", "try", "rollback"}) {
		t.Fatalf("busy catalog: %v %v", err, s.events)
	}
}

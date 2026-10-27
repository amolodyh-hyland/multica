package handler

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func resourceTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg := testPool.Config()
	cfg.MaxConns = 2
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type resourceGateStarter struct {
	txStarter
	held    chan pgx.Tx
	release <-chan struct{}
	once    sync.Once
}

func (s *resourceGateStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &resourceGateTx{Tx: tx, gate: s}, nil
}

type resourceGateTx struct {
	pgx.Tx
	gate *resourceGateStarter
}

func (t *resourceGateTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := t.Tx.Exec(ctx, sql, args...)
	if err == nil && sql == "SELECT pg_advisory_xact_lock($1,$2)" && len(args) == 2 && args[0] == bindingtx.Namespace {
		t.gate.once.Do(func() {
			t.gate.held <- t.Tx
			select {
			case <-t.gate.release:
			case <-ctx.Done():
			}
		})
	}
	return tag, err
}

// Exactly two connections: A owns the prefix, B occupies the other connection
// waiting on A. Real validators must finish using A's transaction connection.
func saturatedBindingRequest(t *testing.T, endpoint func(*Handler) http.HandlerFunc, req *http.Request) *testutil.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), 8*time.Second)
	defer cancel()
	pool := resourceTestPool(t)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	gate := &resourceGateStarter{txStarter: pool, held: make(chan pgx.Tx, 1), release: release}
	h := *testHandler
	h.Queries = db.NewBinding(pool)
	h.TxStarter = gate
	response := make(chan *testutil.Response, 1)
	go func() { response <- testutil.Call(t, endpoint(&h), req.WithContext(ctx)) }()
	var holder pgx.Tx
	select {
	case holder = <-gate.held:
	case res := <-response:
		t.Fatalf("handler exited before prefix: status=%d body=%s", res.Code, res.Body.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { unblock(); cancel(); conn.Release() }()
	done := make(chan error, 1)
	go func() {
		tx, err := bindingtx.Begin(ctx, conn, bindingtx.Workspaces(testWorkspaceID))
		if err == nil {
			err = tx.Commit(ctx)
		}
		done <- err
	}()
	if err := waitHandlerBindingPrefix(ctx, holder, conn.Conn().PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	if pool.Stat().AcquiredConns() != 2 {
		t.Fatal("pool was not saturated")
	}
	unblock()
	var result *testutil.Response
	select {
	case result = <-response:
	case <-ctx.Done():
		t.Fatal("validation escaped to the saturated pool")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBindingWorkflowValidationWithSaturatedPool(t *testing.T) {
	runtime := dbfx.Runtime(t, "workflow resources")
	agent := dbfx.Agent(t, "workflow resource handler", runtime)
	withFeatureFlag(t, testHandler, featureflags.ProjectWorkflowsV1, true)
	resetDefaultWorkflow(t)
	steps := statusSteps("backlog", "in_progress", "blocked")
	steps[1]["handler"] = map[string]any{"type": "agent", "id": agent}
	req := workflowRequest("POST", "/api/issue-workflows", map[string]any{"name": "Pool-safe workflow " + workflowTestSuffix(), "steps": steps})
	var created IssueWorkflowResponse
	saturatedBindingRequest(t, func(h *Handler) http.HandlerFunc { return h.CreateIssueWorkflow }, req).Want(http.StatusCreated).JSON(&created)
	dbfx.Cleanup(t, "DELETE FROM issue_workflow WHERE id=$1", created.ID)
	if n := dbfx.Count(t, "SELECT count(*) FROM issue_workflow WHERE id=$1", created.ID); n != 1 {
		t.Fatal("workflow did not commit")
	}
}

func TestBindingActorPropertyValidationWithSaturatedPool(t *testing.T) {
	property := createTestProperty(t, map[string]any{"name": "Pool actor " + workflowTestSuffix(), "type": "actor"})
	issue := dbfx.Issue(t, "pool-safe actor property")
	value := "member:" + testUserID
	req := withIssuePropertyParams(newRequest("PUT", "/api/issues/"+issue+"/properties/"+property.ID, map[string]any{"value": value}), issue, property.ID)
	saturatedBindingRequest(t, func(h *Handler) http.HandlerFunc { return h.SetIssueProperty }, req).Want(http.StatusOK)
	if n := dbfx.Count(t, "SELECT count(*) FROM issue WHERE id=$1 AND properties->>$2=$3", issue, property.ID, value); n != 1 {
		t.Fatal("actor value did not commit")
	}
}

func TestBindingSearchDoesNotBlockWorkspaceWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := resourceTestPool(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	search := make(chan error, 1)
	go func() {
		search <- runSearchQuery(ctx, pool, "SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only')", nil, func(rows pgx.Rows) error {
			if !rows.Next() {
				return fmt.Errorf("missing transaction settings: %v", rows.Err())
			}
			var isolation, ro string
			if err := rows.Scan(&isolation, &ro); err != nil {
				return err
			}
			rows.Close()
			if isolation != "read committed" || ro != "on" {
				return fmt.Errorf("search transaction settings %q %q", isolation, ro)
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case err := <-search:
		t.Fatalf("search failed before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	writerCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	tx, err := bindingtx.Begin(writerCtx, pool, bindingtx.Workspaces(testWorkspaceID))
	if err != nil {
		t.Fatalf("search blocked writer: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(writerCtx, "SELECT id FROM workspace WHERE id=$1 FOR UPDATE NOWAIT", testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(writerCtx); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-search; err != nil {
		t.Fatal(err)
	}
}

func TestBindingWorkspaceIndependenceAndSortedBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	other := dbfx.Workspace(t, "independent prefix workspace", "prefix-"+workflowTestSuffix())
	holder, err := bindingtx.Begin(ctx, testPool, bindingtx.Workspaces(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// A distinct workspace progresses while A's key is held.
	short, stop := context.WithTimeout(ctx, time.Second)
	tx, err := bindingtx.Begin(short, conn, bindingtx.Workspaces(other))
	if err != nil {
		stop()
		t.Fatalf("unrelated workspace serialized: %v", err)
	}
	if _, err = tx.Exec(short, "UPDATE workspace SET description='independent write' WHERE id=$1", other); err == nil {
		err = tx.Commit(short)
	}
	stop()
	if err != nil {
		t.Fatal(err)
	}
	// A batch that includes A must wait before it acquires any row locks.
	done := make(chan error, 1)
	go func() {
		tx, err := bindingtx.Begin(ctx, conn, bindingtx.Workspaces(other, testWorkspaceID, other))
		if err == nil {
			_, err = tx.Exec(ctx, "SELECT id FROM workspace WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE", []string{other, testWorkspaceID})
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(context.Background())
			}
		}
		done <- err
	}()
	if err := waitHandlerBindingPrefix(ctx, holder, conn.Conn().PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Pause after the first key. Reverse-order callers must wait on that
	// same key, rather than own each other's second key and form a cycle.
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	gate := &resourceGateStarter{txStarter: testPool, held: make(chan pgx.Tx, 1), release: release}
	firstDone := make(chan error, 1)
	go func() {
		tx, err := bindingtx.Begin(ctx, gate, bindingtx.Workspaces(testWorkspaceID, other))
		if err == nil {
			err = tx.Commit(ctx)
		}
		firstDone <- err
	}()
	var partial pgx.Tx
	select {
	case partial = <-gate.held:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		tx, err := bindingtx.Begin(ctx, conn, bindingtx.Workspaces(other, testWorkspaceID))
		if err == nil {
			err = tx.Commit(ctx)
		}
		done <- err
	}()
	if err := waitHandlerBindingPrefix(ctx, partial, conn.Conn().PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

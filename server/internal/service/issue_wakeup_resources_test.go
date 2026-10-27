package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type stalledWakeupOverlay struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *stalledWakeupOverlay) BuildTaskOverlay(ctx context.Context, _ pgtype.UUID, _ db.Agent) (runtimeapps.MCPOverlayResult, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return runtimeapps.MCPOverlayResult{MCPOverlay: json.RawMessage(`{"mcpServers":{}}`)}, nil
	case <-ctx.Done():
		return runtimeapps.MCPOverlayResult{}, ctx.Err()
	}
}

func TestSystemWakeupOverlayPreparedOutsideBindingTransaction(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "stable_configuration"
		if changed {
			name = "changed_allowlist"
		}
		t.Run(name, func(t *testing.T) {
			f, s, issue, agent := conditionFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			ws := parseTestUUID(t, f.WorkspaceID)
			f.Exec(t, "UPDATE issue SET status='in_progress',assignee_type='agent',assignee_id=$2 WHERE id=$1", issue, agent)
			f.Issue(t, "closed child", testutil.Cols{"parent_issue_id": issue, "status": "done"})
			f.Cleanup(t, "DELETE FROM issue_child_event WHERE parent_id=$1", issue)
			rule, err := f.q.CreateSystemWakeup(ctx, db.CreateSystemWakeupParams{ID: dbid.NewV7(), WorkspaceID: ws, IssueID: issue,
				EventTypes: []string{"issue.status_changed"}, Condition: childDoneCondition, Enabled: true, SystemRule: systemRuleText(SystemRuleChildDone)})
			if err != nil {
				t.Fatal(err)
			}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			builder := &stalledWakeupOverlay{entered: make(chan struct{}), release: release}
			s.Tasks.Composio = builder
			s.Tasks.FeatureFlags = composioMCPAppsTestFlags(true)
			done := make(chan error, 1)
			go func() { done <- s.dispatchSystem(ctx, rule) }()
			select {
			case <-builder.entered:
			case err := <-done:
				t.Fatalf("dispatch exited before overlay: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// A paused external call must not exclude any writer in this workspace.
			writerCtx, stop := context.WithTimeout(ctx, time.Second)
			tx, err := bindingtx.Begin(writerCtx, f.Pool, bindingtx.Workspaces(ws))
			if err != nil {
				stop()
				t.Fatalf("overlay holds workspace prefix: %v", err)
			}
			if changed {
				_, err = tx.Exec(writerCtx, "UPDATE agent SET composio_toolkit_allowlist=ARRAY['linear']::text[] WHERE id=$1", agent)
			} else {
				_, err = tx.Exec(writerCtx, "UPDATE issue SET description='concurrent writer completed' WHERE id=$1", issue)
			}
			if err == nil {
				err = tx.Commit(writerCtx)
			} else {
				_ = tx.Rollback(context.Background())
			}
			stop()
			if err != nil {
				t.Fatal(err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if changed {
				if n := wakeRuns(t, f, rule.ID); n != 0 {
					t.Fatal("stale overlay configuration enqueued a run")
				}
				if n := f.Count(t, "SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1 AND processed_at IS NULL", rule.ID); n == 0 {
					t.Fatal("configuration change lost pending inputs")
				}
				if err := s.dispatchSystem(ctx, rule); err != nil {
					t.Fatal(err)
				}
			}
			if n := wakeRuns(t, f, rule.ID); n != 1 {
				t.Fatalf("system wakeup tasks=%d, want one durable run", n)
			}
		})
	}
}

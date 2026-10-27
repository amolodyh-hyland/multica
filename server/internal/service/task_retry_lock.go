package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errIssueRetryOwnersChanged = errors.New("issue retry owners changed")

// Lock retry owners before the parent task. Runtime teardown uses runtime ->
// profile -> task too. Only a changed snapshot, detected before writes, is retried.
func (s *TaskService) runTaskRetryTx(ctx context.Context, taskID pgtype.UUID, needsRetry bool, fn func(*db.Queries) error) error {
	if !needsRetry || s.TxStarter == nil {
		return s.runInTx(ctx, bindingtx.Owners(taskID), fn)
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := s.runInTx(ctx, bindingtx.Owners(taskID), func(q *db.Queries) error {
			before, err := q.GetIssueRetryOwners(ctx, taskID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fn(q)
			}
			if err != nil {
				return fmt.Errorf("read issue retry owners: %w", err)
			}
			if _, err := q.LockWorkspaceForChatSessionCreate(ctx, before.WorkspaceID); err != nil {
				return err
			}
			ids := make([]pgtype.UUID, 0, 3)
			for _, id := range []pgtype.UUID{before.ParentRuntimeID, before.DefaultRuntimeID, before.OverrideRuntimeID} {
				if id.Valid {
					ids = append(ids, id)
				}
			}
			if _, err := q.LockIssueRetryRuntimes(ctx, ids); err != nil {
				return fmt.Errorf("lock issue retry runtimes: %w", err)
			}
			if _, err := q.GetAgentForUpdate(ctx, before.AgentID); err != nil {
				return fmt.Errorf("lock issue retry profile: %w", err)
			}
			after, err := q.GetIssueRetryOwners(ctx, taskID)
			if err != nil {
				return err
			}
			if before != after {
				return errIssueRetryOwnersChanged
			}
			if _, err := q.LockIssueForTaskRetry(ctx, db.LockIssueForTaskRetryParams{IssueID: after.IssueID, WorkspaceID: after.WorkspaceID}); err != nil {
				return fmt.Errorf("lock retry issue: %w", err)
			}
			return fn(q)
		})
		if !errors.Is(err, errIssueRetryOwnersChanged) {
			return err
		}
	}
	return errIssueRetryOwnersChanged
}

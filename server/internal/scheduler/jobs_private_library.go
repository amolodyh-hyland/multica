package scheduler

import (
	"context"
	"fmt"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const JobNamePrunePrivateLibraryImports = "prune_private_library_imports"

// PrivateLibraryImportPruneJob covers legacy orphans and task cascades outside
// explicit issue deletion. Terminal tasks remain owners of retained snapshots.
func PrivateLibraryImportPruneJob(queries *db.Queries) JobSpec {
	return JobSpec{
		Name:              JobNamePrunePrivateLibraryImports,
		Cadence:           time.Hour,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        2 * time.Minute,
		StaleTimeout:      5 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff:      []time.Duration{time.Minute, 5 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			var total int64
			for range 20 {
				deleted, err := queries.PruneUnreferencedPrivateLibraryImports(ctx, 500)
				if err != nil {
					return HandlerResult{RowsAffected: total}, fmt.Errorf("prune unreferenced private imports: %w", err)
				}
				total += deleted
				if deleted < 500 {
					break
				}
				if in.Heartbeat != nil {
					if err := in.Heartbeat(ctx); err != nil {
						return HandlerResult{RowsAffected: total}, err
					}
				}
			}
			return HandlerResult{RowsAffected: total}, nil
		},
	}
}

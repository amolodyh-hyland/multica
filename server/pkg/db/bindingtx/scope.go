package bindingtx

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"reflect"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const Namespace int32 = 0x4d424e44
const catalogNamespace int32 = Namespace - 1

type Scope struct {
	workspaces   []pgtype.UUID
	owners       []pgtype.UUID
	all          bool
	creating     bool
	lookup       string
	args         []any
	required     bool
	allowMissing bool
	missingAll   bool
	tryLock      bool
	prepare      func(context.Context, pgx.Tx) error
}

func Workspaces(ids ...any) Scope { return Scope{workspaces: uuidArguments(ids), required: true} }
func Owners(ids ...any) Scope {
	return Scope{owners: uuidArguments(ids), required: true, missingAll: true}
}
func AllWorkspaces() Scope { return Scope{all: true} }

// NewWorkspace protects discovery by database-wide batches. The new workspace
// and its dependent rows are invisible until their creator commits.
func NewWorkspace() Scope                  { return Scope{creating: true} }
func Lookup(sql string, args ...any) Scope { return Scope{lookup: sql, args: args, required: true} }
func unscoped() Scope                      { return Scope{} }

// WithPreparation runs before any workspace prefix or row lock. It may read
// metadata and hold a routing-slot advisory lock while proving credentials.
// Database work must use the supplied connection; row locks and writes are
// rejected until the workspace key set has been acquired and revalidated.
func (s Scope) WithPreparation(fn func(context.Context, pgx.Tx) error) Scope {
	s.prepare = fn
	return s
}

// ChannelSlot includes a reclaimable former owner's workspace, even if its
// workspace row has already been deleted. Re-resolving after acquisition also
// fences a concurrent routing-slot change before caller row work.
func ChannelSlot(workspace pgtype.UUID, channelType, appID string) Scope {
	return Lookup(`SELECT $1::uuid AS workspace_id
 UNION SELECT workspace_id FROM channel_installation
 WHERE channel_type=$2 AND config->>'app_id'=$3 ORDER BY workspace_id`, workspace, channelType, appID)
}

func uuidArguments(args []any) []pgtype.UUID {
	var ids []pgtype.UUID
	var add func(any)
	add = func(value any) {
		switch v := value.(type) {
		case pgtype.UUID:
			if v.Valid {
				ids = append(ids, v)
			}
			return
		case string:
			if len(v) == 36 {
				var id pgtype.UUID
				if id.Scan(v) == nil && id.Valid {
					ids = append(ids, id)
				}
			}
			return
		}
		v := reflect.ValueOf(value)
		if v.IsValid() && v.Kind() == reflect.Array && v.Len() == 16 && v.Type().Elem().Kind() == reflect.Uint8 {
			var id pgtype.UUID
			for i := 0; i < 16; i++ {
				id.Bytes[i] = byte(v.Index(i).Uint())
			}
			id.Valid = true
			ids = append(ids, id)
			return
		}
		if v.IsValid() && (v.Kind() == reflect.Slice || v.Kind() == reflect.Array) && v.Type().Elem().Kind() != reflect.Uint8 {
			for i := 0; i < v.Len(); i++ {
				add(v.Index(i).Interface())
			}
		}
	}
	for _, arg := range args {
		add(arg)
	}
	return ids
}

func WorkspaceKey(id pgtype.UUID) int32 {
	sum := sha256.Sum256(id.Bytes[:])
	return int32(binary.BigEndian.Uint32(sum[:4]))
}

func (s Scope) resolve(ctx context.Context, tx pgx.Tx) ([]pgtype.UUID, error) {
	ids := append([]pgtype.UUID(nil), s.workspaces...)
	if s.lookup != "" {
		if err := preparationAllowed(s.lookup); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, s.lookup, s.args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if s.all {
		rows, err := tx.Query(ctx, "SELECT id FROM workspace ORDER BY id")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}
	if len(s.owners) > 0 {
		rows, err := tx.Query(ctx, ownerWorkspacesSQL, s.owners)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if len(ids) == 0 && s.required && !s.allowMissing {
		return nil, pgx.ErrNoRows
	}
	slices.SortFunc(ids, func(a, b pgtype.UUID) int { return slices.Compare(a.Bytes[:], b.Bytes[:]) })
	return slices.Compact(ids), nil
}

const ownerWorkspacesSQL = `SELECT DISTINCT workspace_id FROM (
SELECT id AS workspace_id FROM workspace WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM agent WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM agent_runtime WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM issue WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM comment WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM chat_session WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM issue_wakeup WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM issue_child_event WHERE parent_id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM runtime_profile WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM project WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM member WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM squad WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM skill WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM plugin_installation WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM plugin_package WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM workspace_invitation WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM workspace_share_link WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM issue_workflow WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM channel_installation WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM issue_source_context WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM attachment WHERE id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM issue_status WHERE id=ANY($1::uuid[])
UNION ALL
SELECT a.workspace_id FROM agent_task_queue t JOIN agent a ON a.id=t.agent_id WHERE t.id=ANY($1::uuid[])
UNION ALL
SELECT i.workspace_id FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id WHERE t.id=ANY($1::uuid[])
UNION ALL
SELECT r.workspace_id FROM agent_task_queue t JOIN agent_runtime r ON r.id=t.runtime_id WHERE t.id=ANY($1::uuid[])
UNION ALL
SELECT s.workspace_id FROM chat_message m JOIN chat_session s ON s.id=m.chat_session_id WHERE m.id=ANY($1::uuid[])
UNION ALL
SELECT a.workspace_id FROM autopilot a WHERE a.id=ANY($1::uuid[])
UNION ALL
SELECT a.workspace_id FROM autopilot_run r JOIN autopilot a ON a.id=r.autopilot_id WHERE r.id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM agent_runtime WHERE profile_id=ANY($1::uuid[])
UNION ALL
SELECT workspace_id FROM github_pull_request WHERE id=ANY($1::uuid[])
) owners WHERE workspace_id IS NOT NULL ORDER BY workspace_id`

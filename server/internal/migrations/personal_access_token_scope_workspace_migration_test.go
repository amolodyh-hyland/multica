package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPersonalAccessTokenScopeWorkspaceMigrationHasReversibleDDL(t *testing.T) {
	up := readMigrationFile(t, "586_personal_access_token_scope_workspace.up.sql")
	down := readMigrationFile(t, "586_personal_access_token_scope_workspace.down.sql")
	if !strings.Contains(up, "ADD COLUMN scope_workspace_id UUID") {
		t.Errorf("up migration must add a nullable workspace binding")
	}
	if !strings.Contains(down, "DROP COLUMN scope_workspace_id") {
		t.Errorf("down migration must drop the workspace binding")
	}
}

func TestPersonalAccessTokenScopeWorkspaceMigrationUpDownOnPostgres(t *testing.T) {
	dbURL := os.Getenv("MULTICA_MIGRATION_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("MULTICA_MIGRATION_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to dedicated migration test Postgres: %v", err)
	}
	defer conn.Close(ctx)

	schema := "public_api_pat_workspace_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	if _, err := conn.Exec(ctx, "SELECT set_config('search_path', $1, false)", schema); err != nil {
		t.Fatalf("set isolated migration search path: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE personal_access_token (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("create pre-migration table: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO personal_access_token (id) VALUES (1)`); err != nil {
		t.Fatalf("seed pre-migration PAT: %v", err)
	}

	applyMigrationFile(t, ctx, conn, "586_personal_access_token_scope_workspace.up.sql")
	var workspaceID pgtype.Text
	if err := conn.QueryRow(ctx, `SELECT scope_workspace_id::text FROM personal_access_token WHERE id = 1`).Scan(&workspaceID); err != nil {
		t.Fatalf("read pre-existing PAT binding: %v", err)
	}
	if workspaceID.Valid {
		t.Fatalf("pre-existing PAT binding = %q, want NULL", workspaceID.String)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO personal_access_token (id, scope_workspace_id) VALUES (2, '00000000-0000-0000-0000-000000000001')`); err != nil {
		t.Fatalf("insert workspace-bound PAT: %v", err)
	}

	applyMigrationFile(t, ctx, conn, "586_personal_access_token_scope_workspace.down.sql")
	var columnCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'personal_access_token' AND column_name = 'scope_workspace_id'
	`, schema).Scan(&columnCount); err != nil {
		t.Fatalf("inspect binding column after down migration: %v", err)
	}
	if columnCount != 0 {
		t.Fatalf("workspace binding column count after down migration = %d, want 0", columnCount)
	}
}

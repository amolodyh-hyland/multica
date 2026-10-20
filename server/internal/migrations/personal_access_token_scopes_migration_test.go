package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPersonalAccessTokenScopesMigrationHasReversibleDDL(t *testing.T) {
	up := readMigrationFile(t, "585_personal_access_token_scopes.up.sql")
	down := readMigrationFile(t, "585_personal_access_token_scopes.down.sql")
	for _, fragment := range []string{
		"ALTER TABLE personal_access_token",
		"ADD COLUMN scopes TEXT[] NOT NULL DEFAULT '{}'::text[]",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("up migration is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"ALTER TABLE personal_access_token",
		"DROP COLUMN scopes",
	} {
		if !strings.Contains(down, fragment) {
			t.Errorf("down migration is missing %q", fragment)
		}
	}
}

// This exercises the migration against a synthetic schema only when a
// dedicated disposable Postgres URL is provided. It never falls back to the
// application's DATABASE_URL.
func TestPersonalAccessTokenScopesMigrationUpDownOnPostgres(t *testing.T) {
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

	schema := "public_api_pat_scopes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

	applyMigrationFile(t, ctx, conn, "585_personal_access_token_scopes.up.sql")
	var scopes string
	if err := conn.QueryRow(ctx, `SELECT scopes::text FROM personal_access_token WHERE id = 1`).Scan(&scopes); err != nil {
		t.Fatalf("read backfilled PAT scopes: %v", err)
	}
	if scopes != "{}" {
		t.Fatalf("pre-existing PAT scopes = %q, want empty array", scopes)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO personal_access_token (id, scopes) VALUES (2, ARRAY['issues:read']::text[])`); err != nil {
		t.Fatalf("insert scoped PAT: %v", err)
	}

	applyMigrationFile(t, ctx, conn, "585_personal_access_token_scopes.down.sql")
	var remainingRows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM personal_access_token`).Scan(&remainingRows); err != nil {
		t.Fatalf("count PAT rows after down migration: %v", err)
	}
	if remainingRows != 2 {
		t.Fatalf("PAT rows after down migration = %d, want 2", remainingRows)
	}
	var scopesColumnCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'personal_access_token' AND column_name = 'scopes'
	`, schema).Scan(&scopesColumnCount); err != nil {
		t.Fatalf("inspect scopes column after down migration: %v", err)
	}
	if scopesColumnCount != 0 {
		t.Fatalf("scopes column count after down migration = %d, want 0", scopesColumnCount)
	}
}

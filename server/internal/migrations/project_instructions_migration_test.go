package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestProjectInstructionsMigrationDDL(t *testing.T) {
	up := readMigrationFile(t, "590_project_instructions.up.sql")
	down := readMigrationFile(t, "590_project_instructions.down.sql")
	for _, want := range []string{"ALTER TABLE project ADD COLUMN instructions TEXT", "octet_length(instructions) <= 32768"} {
		if !strings.Contains(up, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	if strings.Contains(up, "DEFAULT") || strings.Contains(up, "UPDATE project") || strings.Contains(up, "CREATE INDEX") {
		t.Fatal("instructions migration must be additive without backfill or indexes")
	}
	if !strings.Contains(down, "DROP COLUMN IF EXISTS instructions") {
		t.Fatal("migration must be reversible")
	}
}

func TestProjectInstructionsMigrationUpDownOnPostgres(t *testing.T) {
	dbURL := os.Getenv("MULTICA_MIGRATION_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("MULTICA_MIGRATION_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	schema := "project_instructions_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	if _, err := conn.Exec(ctx, "SELECT set_config('search_path', $1, false)", schema); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE project (id integer PRIMARY KEY, description text); INSERT INTO project VALUES (1, 'Existing summary')`); err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, ctx, conn, "590_project_instructions.up.sql")
	var description string
	var instructions *string
	if err := conn.QueryRow(ctx, `SELECT description, instructions FROM project WHERE id = 1`).Scan(&description, &instructions); err != nil {
		t.Fatal(err)
	}
	if description != "Existing summary" || instructions != nil {
		t.Fatalf("existing row changed: %q, %v", description, instructions)
	}
	for _, value := range []string{"", strings.Repeat("x", 32768), strings.Repeat("界", 10922)} {
		if _, err := conn.Exec(ctx, `UPDATE project SET instructions = $1 WHERE id = 1`, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{strings.Repeat("x", 32769), strings.Repeat("界", 10923)} {
		if _, err := conn.Exec(ctx, `UPDATE project SET instructions = $1 WHERE id = 1`, value); err == nil {
			t.Fatal("oversized instructions accepted")
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO project (id, description) VALUES (2, 'Older client')`); err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, ctx, conn, "590_project_instructions.down.sql")
	if err := conn.QueryRow(ctx, `SELECT description FROM project WHERE id = 1`).Scan(&description); err != nil || description != "Existing summary" {
		t.Fatalf("rollback changed description: %q, %v", description, err)
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'project' AND column_name = 'instructions'`, schema).Scan(&count); err != nil || count != 0 {
		t.Fatalf("instructions after rollback: %d, %v", count, err)
	}
}

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
	for _, want := range []string{"ADD COLUMN instructions TEXT", "ADD CONSTRAINT project_instructions_size CHECK (octet_length(instructions) <= 32768) NOT VALID"} {
		if !strings.Contains(up, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	if strings.Contains(up, "DEFAULT") || strings.Contains(up, "UPDATE project") || strings.Contains(up, "CREATE INDEX") {
		t.Fatal("instructions migration must be additive without backfill or indexes")
	}
	if strings.Contains(up, "VALIDATE CONSTRAINT") {
		t.Fatal("590 must defer validation to a separate migration")
	}
	validateUp := readMigrationFile(t, "591_project_instructions_validate.up.sql")
	if strings.TrimSpace(validateUp) != "ALTER TABLE project VALIDATE CONSTRAINT project_instructions_size;" {
		t.Fatal("591 must only validate the constraint under its weaker lock")
	}
	validateDown := readMigrationFile(t, "591_project_instructions_validate.down.sql")
	for _, want := range []string{"DROP CONSTRAINT IF EXISTS project_instructions_size", "ADD CONSTRAINT project_instructions_size CHECK (octet_length(instructions) <= 32768) NOT VALID"} {
		if !strings.Contains(validateDown, want) {
			t.Fatalf("validation rollback missing %q", want)
		}
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
	assertValidated := func(want bool) {
		t.Helper()
		var validated bool
		if err := conn.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid = 'project'::regclass AND conname = 'project_instructions_size'`).Scan(&validated); err != nil {
			t.Fatal(err)
		}
		if validated != want {
			t.Fatalf("constraint validated = %v, want %v", validated, want)
		}
	}
	assertValidated(false)
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
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	applyMigrationFile(t, ctx, tx, "591_project_instructions_validate.up.sql")
	var validationLock, exclusiveLock bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND relation = 'project'::regclass AND mode = 'ShareUpdateExclusiveLock' AND granted),
		EXISTS(SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND relation = 'project'::regclass AND mode = 'AccessExclusiveLock' AND granted)
	`).Scan(&validationLock, &exclusiveLock); err != nil {
		t.Fatal(err)
	}
	if !validationLock || exclusiveLock {
		t.Fatalf("validation locks: share update exclusive=%v, access exclusive=%v", validationLock, exclusiveLock)
	}
	probe, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close(ctx)
	if _, err := probe.Exec(ctx, "SET lock_timeout = '1s'"); err != nil {
		t.Fatal(err)
	}
	if err := probe.QueryRow(ctx, "SELECT description FROM "+quoted+".project WHERE id = 1").Scan(&description); err != nil || description != "Existing summary" {
		t.Fatalf("read during validation: %q, %v", description, err)
	}
	if _, err := probe.Exec(ctx, "UPDATE "+quoted+".project SET description = description WHERE id = 1"); err != nil {
		t.Fatalf("write blocked by validation: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertValidated(true)
	applyMigrationFile(t, ctx, conn, "591_project_instructions_validate.down.sql")
	assertValidated(false)
	if _, err := conn.Exec(ctx, `UPDATE project SET instructions = $1 WHERE id = 1`, strings.Repeat("x", 32769)); err == nil {
		t.Fatal("validation rollback stopped enforcing the size limit")
	}
	applyMigrationFile(t, ctx, conn, "591_project_instructions_validate.up.sql")
	assertValidated(true)
	applyMigrationFile(t, ctx, conn, "591_project_instructions_validate.down.sql")
	applyMigrationFile(t, ctx, conn, "590_project_instructions.down.sql")
	if err := conn.QueryRow(ctx, `SELECT description FROM project WHERE id = 1`).Scan(&description); err != nil || description != "Existing summary" {
		t.Fatalf("rollback changed description: %q, %v", description, err)
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'project' AND column_name = 'instructions'`, schema).Scan(&count); err != nil || count != 0 {
		t.Fatalf("instructions after rollback: %d, %v", count, err)
	}
}

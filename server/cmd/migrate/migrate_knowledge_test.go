package main

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

func TestKnowledgeMigrationsUpDownUp(t *testing.T) {
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, base, "knowledge_")
	pool := openTestPoolWithSearchPath(t, schema)
	if _, err := pool.Exec(ctx, `CREATE TABLE agent_task_queue (id UUID, agent_id UUID, session_id TEXT, created_at TIMESTAMPTZ DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"up", "down", "up"} {
		versions := []string{"592_task_knowledge_manifest", "593_task_knowledge_session_index"}
		if direction == "down" {
			slices.Reverse(versions)
		}
		if err := runMigrations(ctx, pool, runOptions{Direction: direction, Files: realMigrationFiles(t, versions, direction), SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Hooks: hooksForDirection(direction), Conditions: conditionsForDirection(direction)}); err != nil {
			t.Fatalf("migrate %s: %v", direction, err)
		}
		var column bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='agent_task_queue' AND column_name='knowledge_manifest')`, schema).Scan(&column)
		if err != nil || column != (direction == "up") {
			t.Fatalf("after %s, manifest column=%v: %v", direction, column, err)
		}
		var index bool
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='idx_task_knowledge_session' AND i.indisvalid)`, schema).Scan(&index)
		if err != nil || index != (direction == "up") {
			t.Fatalf("after %s, session index=%v: %v", direction, index, err)
		}
	}
}

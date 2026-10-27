package bindingtx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendTransactionPrefixGuard(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, dir := range []string{"internal", "cmd/server"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasSuffix(path, "selfhosttelemetry/collector.go") {
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "Begin" || sel.Sel.Name == "BeginTx" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "bindingtx" {
						return true
					}
					// This Begin registers a remote integration, not a database transaction.
					if owner, ok := sel.X.(*ast.SelectorExpr); ok && owner.Sel.Name == "client" && strings.HasSuffix(path, "lark/registration_service.go") {
						return true
					}
					t.Errorf("%s: use bindingtx.Begin before any row lock", fset.Position(call.Pos()))
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "db" && sel.Sel.Name == "New" {
					t.Errorf("%s: standalone queries must use db.NewBinding", fset.Position(call.Pos()))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestStandaloneLockingSQL(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"-- name: GetAgent :one\nSELECT * FROM agent", false},
		{"-- A read unlike FOR UPDATE.\nSELECT * FROM agent", false},
		{"SELECT 'update', 'delete', 'for share'", false},
		{"SELECT * FROM issue FOR SHARE", true},
		{"SELECT * FROM workspace FOR KEY SHARE", true},
		{"SELECT * FROM agent FOR UPDATE SKIP LOCKED", true},
		{"SELECT * FROM issue FOR NO KEY UPDATE", true},
		{"WITH candidates AS (SELECT 1) UPDATE agent_task_queue SET status='queued'", true},
		{"WITH bumped AS (UPDATE issue SET updated_at=now() RETURNING id) INSERT INTO comment SELECT * FROM bumped", true},
		{"SELECT lock_task_owner_rows($1,$2,$3)", true},
		{"SELECT issue_runtime_for_run($1,$2)", true},
		{"SELECT pg_advisory_xact_lock(4246)", true},
		{"SELECT pg_try_advisory_xact_lock(4246)", true},
	}
	for _, tc := range cases {
		got := lockingSQL.MatchString(sqlStrings.ReplaceAllString(sqlComments.ReplaceAllString(tc.sql, ""), "''"))
		if got != tc.want {
			t.Errorf("%q: locking=%v want %v", tc.sql, got, tc.want)
		}
	}
}

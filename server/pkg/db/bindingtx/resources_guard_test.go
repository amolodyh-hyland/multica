package bindingtx

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// This is a source regression guard, not a Go effect-system proof. It follows
// same-receiver helpers and checks explicit Begin..Commit regions and callback
// transactions. WithTx/withQueries are the explicit connection-bound escape.
// Transaction-bearing helper calls require separate inventory review.
type resourceMethod struct {
	key, path, receiver string
	node                *ast.FuncDecl
	sink                string
	calls               []string
}

func resourceExpr(n ast.Expr) string {
	switch x := n.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return resourceExpr(x.X) + "." + x.Sel.Name
	}
	return ""
}
func resourceSink(name string) string {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "qtx.") {
		return ""
	}
	for _, field := range []string{".Queries.", ".q.", ".pool.", ".Pool.", ".DB."} {
		if strings.Contains(name, field) && !strings.HasSuffix(name, ".WithTx") {
			return "pooled database access"
		}
	}
	for _, method := range []string{".BuildTaskOverlay", ".buildRuntimeMCPOverlay", ".Probe", ".Do", ".SendRequest", ".SendMessage", ".BuildRuntimeMCPOverlayForMerge"} {
		if strings.HasSuffix(name, method) {
			return "external client call"
		}
	}
	return ""
}
func resourceCallSink(call *ast.CallExpr) string {
	if sink := resourceSink(resourceExpr(call.Fun)); sink != "" {
		return sink
	}
	for _, arg := range call.Args {
		name := resourceExpr(arg)
		for _, field := range []string{".Queries", ".q", ".Pool", ".pool", ".DB"} {
			if name != "" && !strings.HasPrefix(name, "qtx.") && strings.HasSuffix(name, field) {
				return "pooled database handle passed to helper"
			}
		}
	}
	return ""
}
func resourceMethods(fset *token.FileSet, path string, file *ast.File) map[string]*resourceMethod {
	methods := map[string]*resourceMethod{}
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Body == nil {
			continue
		}
		receiver, receiverType := "", ""
		if d.Recv != nil {
			r := d.Recv.List[0]
			if len(r.Names) == 0 {
				continue
			}
			typ := r.Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			id, ok := typ.(*ast.Ident)
			if !ok {
				continue
			}
			receiver, receiverType = r.Names[0].Name, id.Name
		}
		key := filepath.Dir(path) + "/" + receiverType + "." + d.Name.Name
		m := &resourceMethod{key: key, path: path, receiver: receiver, node: d}

		methods[key] = m
		var committed token.Pos
		ast.Inspect(d.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && strings.HasSuffix(resourceExpr(c.Fun), ".Commit") && (committed == 0 || c.Pos() < committed) {
				committed = c.Pos()
			}
			return true
		})
		ast.Inspect(d.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := resourceExpr(c.Fun)
			if committed > 0 && c.Pos() > committed {
				return true
			}
			if sink := resourceCallSink(c); sink != "" {
				m.sink = sink
			}
			if m.receiver != "" && strings.HasPrefix(name, m.receiver+".") && strings.Count(name, ".") == 1 {
				m.calls = append(m.calls, filepath.Dir(path)+"/"+receiverType+name[strings.Index(name, "."):])
			}
			return true
		})
	}
	return methods
}
func resourceViolations(fset *token.FileSet, files map[string]*ast.File) []string {
	methods := map[string]*resourceMethod{}
	for path, file := range files {
		for key, m := range resourceMethods(fset, path, file) {
			methods[key] = m
		}
	}
	var reaches func(string, map[string]bool) string
	reaches = func(key string, seen map[string]bool) string {
		if seen[key] {
			return ""
		}
		seen[key] = true
		m := methods[key]
		if m == nil {
			return ""
		}
		if m.sink != "" {
			return m.sink + " via " + key
		}
		for _, callee := range m.calls {
			if sink := reaches(callee, seen); sink != "" {
				return sink
			}
		}
		return ""
	}
	var violations []string
	for _, m := range methods {
		var first, last token.Pos
		var callbacks []*ast.FuncLit
		ast.Inspect(m.node.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := resourceExpr(c.Fun)
			if (name == "bindingtx.Begin" || strings.HasSuffix(name, ".beginWakeupWrite")) && (first == 0 || c.Pos() < first) {
				first = c.End()
			}
			if strings.HasSuffix(name, ".Commit") && c.Pos() > last {
				last = c.Pos()
			}
			for _, wrapper := range []string{"runInTx", "runTaskRetryTx", "wakeupWrite", "withLiveCommentLock", "withPropertyLock", "updateIssueAtomically", "runWithIssueStatusGuard", "withAttachmentOwnerLock", "attachmentOwnerLockAttempt", "terminateTasksInTx"} {
				if name == wrapper || strings.HasSuffix(name, "."+wrapper) {
					for _, arg := range c.Args {
						if cb, ok := arg.(*ast.FuncLit); ok {
							callbacks = append(callbacks, cb)
						}
					}
				}
			}
			return true
		})
		check := func(c *ast.CallExpr) {
			name := resourceExpr(c.Fun)
			sink := resourceCallSink(c)
			if m.receiver != "" && strings.HasPrefix(name, m.receiver+".") && strings.Count(name, ".") == 1 {
				key := m.key[:strings.LastIndex(m.key, ".")] + name[strings.Index(name, "."):]
				if hit := reaches(key, map[string]bool{}); hit != "" {
					sink = hit
				}
			}
			if sink != "" {
				violations = append(violations, fmt.Sprintf("%s: %s (%s)", fset.Position(c.Pos()), sink, name))
			}
		}
		// Inspect the body in source order. Branches after an explicit rollback
		// use the pool only after releasing the prefix; deferred rollback doesn't.
		var walk func(*ast.BlockStmt, bool) bool
		walk = func(block *ast.BlockStmt, active bool) bool {
			if block == nil {
				return active
			}
			previousBegin := false
			for _, stmt := range block.List {
				begins := false
				ast.Inspect(stmt, func(n ast.Node) bool {
					if _, ok := n.(*ast.FuncLit); ok {
						return false
					}
					if c, ok := n.(*ast.CallExpr); ok && (resourceExpr(c.Fun) == "bindingtx.Begin" || strings.HasSuffix(resourceExpr(c.Fun), ".beginWakeupWrite")) {
						begins = true
					}
					return true
				})
				beginFailed := previousBegin
				previousBegin = begins
				switch s := stmt.(type) {
				case *ast.IfStmt:
					branch := active
					// Begin failure cannot hold a prefix. Its compensation may use a pool.
					if beginFailed {
						branch = false
					}
					walk(s.Body, branch)
					if b, ok := s.Else.(*ast.BlockStmt); ok {
						walk(b, active)
					}
					continue
				case *ast.DeferStmt:
					continue
				}
				ast.Inspect(stmt, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.FuncLit:
						return false
					case *ast.BlockStmt:
						if v != block {
							walk(v, active)
							return false
						}
					case *ast.CallExpr:
						name := resourceExpr(v.Fun)
						if strings.HasSuffix(name, ".Rollback") || strings.HasSuffix(name, ".Commit") {
							active = false
						}
						if active && v.Pos() > first && v.Pos() < last {
							check(v)
						}
						if name == "bindingtx.Begin" || strings.HasSuffix(name, ".beginWakeupWrite") {
							active = true
						}
					}
					return true
				})
			}
			return active
		}
		if first > 0 && last > first {
			walk(m.node.Body, false)
		}
		for _, cb := range callbacks {
			ast.Inspect(cb.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					check(c)
				}
				return true
			})
		}
	}
	return violations
}

func TestPrefixedTransactionResourceGuard(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, dir := range []string{"internal", "cmd/server"} {
		err := filepath.WalkDir(filepath.Join("..", "..", "..", dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err == nil {
				files[path] = f
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range resourceViolations(fset, files) {
		t.Error(v)
	}
}

func TestResourceGuardDetectsPoolAndExternalEscapes(t *testing.T) {
	for _, body := range []string{
		`h.Queries.GetAgent(ctx,id)`, `h.validate(ctx)`, `h.Composio.BuildTaskOverlay(ctx,u,a)`, `h.buildOverlay(ctx)`, `h.Tasks.buildRuntimeMCPOverlay(ctx,u,a)`, `validate(ctx,h.Queries)`,
	} {
		source := `package handler
func(h *Handler)validate(ctx C){h.Queries.GetAgent(ctx,id)}
func(h *Handler)buildOverlay(ctx C){h.Composio.BuildTaskOverlay(ctx,u,a)}
func(h *Handler)write(ctx C){tx,_:=bindingtx.Begin(ctx,pool,scope);defer tx.Rollback(ctx);` + body + `;tx.Commit(ctx)}`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "handler.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(resourceViolations(fset, map[string]*ast.File{"handler.go": f})) == 0 {
			t.Fatalf("guard missed %s", body)
		}
	}
}

func TestResourceGuardDetectsCallbackPoolEscapes(t *testing.T) {
	for _, wrapper := range []string{"wakeupWrite", "h.withPropertyLock", "h.runWithIssueStatusGuard", "h.withLiveCommentLock", "s.runInTx", "s.runTaskRetryTx", "s.terminateTasksInTx"} {
		source := `package handler
func write(){` + wrapper + `(ctx,func(q *db.Queries)error{h.Queries.GetAgent(ctx,id);return nil})}`
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "handler.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(resourceViolations(fset, map[string]*ast.File{"handler.go": f})) == 0 {
			t.Fatalf("guard missed %s callback", wrapper)
		}
	}
}

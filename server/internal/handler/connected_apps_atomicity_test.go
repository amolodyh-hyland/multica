package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Uses only the package's disposable database fixture and fake Composio SDK.
// A failure in the second half must roll back the first half of either API.
func TestConnectedApps_AllowlistAndAppsRollbackTogether(t *testing.T) {
	for _, api := range []string{"apps", "legacy"} {
		t.Run(api, func(t *testing.T) {
			f := newAppsFixture(t)
			ctx := context.Background()
			if w := f.putAgentApps(t, testUserID, []map[string]string{{"toolkit_slug": "gmail", "account_scope": "personal", "level": "read"}}); w.Code != http.StatusOK {
				t.Fatalf("seed apps: %d %s", w.Code, w.Body.String())
			}
			name := fmt.Sprintf("apps_atomic_%d", testSeq.Add(1))
			ident := pgx.Identifier{name}.Sanitize()
			table, event, condition := "agent", "UPDATE OF composio_toolkit_allowlist", "NEW.id"
			if api == "legacy" {
				table, event, condition = "agent_app", "INSERT OR UPDATE", "NEW.agent_id"
			}
			body := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF %s = '%s'::uuid THEN RAISE EXCEPTION 'owned atomicity fixture'; END IF; RETURN NEW; END $$`, ident, condition, f.agentID)
			if _, err := testPool.Exec(ctx, body); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				testPool.Exec(ctx, "DROP TRIGGER IF EXISTS "+ident+" ON "+table)
				testPool.Exec(ctx, "DROP FUNCTION IF EXISTS "+ident+"()")
			})
			if _, err := testPool.Exec(ctx, "CREATE TRIGGER "+ident+" BEFORE "+event+" ON "+table+" FOR EACH ROW EXECUTE FUNCTION "+ident+"()"); err != nil {
				t.Fatal(err)
			}
			var w *httptest.ResponseRecorder
			if api == "apps" {
				w = f.putAgentApps(t, testUserID, []map[string]string{{"toolkit_slug": "gmail", "account_scope": "personal", "level": "write"}})
			} else {
				w = httptest.NewRecorder()
				f.h.UpdateAgent(w, withURLParam(appsReqAs(t, testUserID, http.MethodPatch, "/api/agents/"+f.agentID, map[string]any{"composio_toolkit_allowlist": []string{"gmail", "notion"}}), "id", f.agentID))
			}
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("injected %s failure: %d %s", api, w.Code, w.Body.String())
			}
			agent, err := f.h.Queries.GetAgent(ctx, parseUUID(f.agentID))
			if err != nil {
				t.Fatal(err)
			}
			if len(agent.ComposioToolkitAllowlist) != 1 || agent.ComposioToolkitAllowlist[0] != "gmail" {
				t.Fatalf("partial allowlist commit: %v", agent.ComposioToolkitAllowlist)
			}
			apps, err := f.h.Queries.ListAgentApps(ctx, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(apps) != 1 || apps[0].ToolkitSlug != "gmail" || apps[0].Level != "read" {
				t.Fatalf("partial apps commit: %+v", apps)
			}
		})
	}
}

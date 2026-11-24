package connectedapps

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

type scopeRow []any

func (r scopeRow) Scan(dest ...any) error {
	if len(r) != len(dest) {
		return errors.New("fixture column count mismatch")
	}
	for i, value := range r {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func scopeStructRow(value any) scopeRow {
	v := reflect.ValueOf(value)
	row := make(scopeRow, v.NumField())
	for i := range row {
		row[i] = v.Field(i).Interface()
	}
	return row
}

type scopeRows struct {
	pgx.Rows
	rows []scopeRow
	next int
}

func (r *scopeRows) Next() bool             { r.next++; return r.next <= len(r.rows) }
func (r *scopeRows) Scan(dest ...any) error { return r.rows[r.next-1].Scan(dest...) }
func (r *scopeRows) Close()                 {}
func (r *scopeRows) Err() error             { return nil }

type scopeCall struct {
	sql  string
	args []any
}

type scopeDB struct {
	pgx.Tx
	t          *testing.T
	workspaces []pgtype.UUID
	row        func(string, []any) pgx.Row
	calls      []scopeCall
	committed  bool
}

func (d *scopeDB) Begin(context.Context) (pgx.Tx, error) {
	d.calls = append(d.calls, scopeCall{sql: "BEGIN"})
	return d, nil
}
func (d *scopeDB) Commit(context.Context) error   { d.committed = true; return nil }
func (d *scopeDB) Rollback(context.Context) error { return nil }
func (d *scopeDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	d.calls = append(d.calls, scopeCall{query, args})
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func (d *scopeDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	d.calls = append(d.calls, scopeCall{query, args})
	return d.row(query, args)
}
func (d *scopeDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	d.calls = append(d.calls, scopeCall{query, args})
	switch {
	case query == "SELECT id FROM workspace ORDER BY id":
		rows := &scopeRows{}
		for _, id := range d.workspaces {
			rows.rows = append(rows.rows, scopeRow{id})
		}
		return rows, nil
	case strings.Contains(query, "-- name: ListAgentApps"):
		return &scopeRows{}, nil
	default:
		d.t.Fatalf("unexpected fixture query: %s", query)
		return nil, errors.New("unexpected query")
	}
}

type scopeSealer struct{}

func (scopeSealer) Seal(value []byte) ([]byte, error) {
	return append([]byte("sealed:"), value...), nil
}
func (scopeSealer) Open(value []byte) ([]byte, error) {
	if !strings.HasPrefix(string(value), "sealed:") {
		return nil, errors.New("fixture ciphertext required")
	}
	return value[len("sealed:"):], nil
}

type scopeUpstream struct {
	RemoteUpstream
	refresh func(string, string, string, remotemcp.OAuthClientRegistration) (remotemcp.OAuthTokenResponse, error)
}

func (u scopeUpstream) Refresh(_ context.Context, endpoint, resource, token string, client remotemcp.OAuthClientRegistration) (remotemcp.OAuthTokenResponse, error) {
	return u.refresh(endpoint, resource, token, client)
}

func scopeUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRemoteRefreshBindingPreservesLegacyAccountsAndLimitsWrites(t *testing.T) {
	ws1 := scopeUUID(t, "00000000-0000-0000-0000-000000000001")
	ws2 := scopeUUID(t, "00000000-0000-0000-0000-000000000002")
	connectionID := scopeUUID(t, "00000000-0000-0000-0000-000000000003")
	clientID := scopeUUID(t, "00000000-0000-0000-0000-000000000004")
	for _, legacy := range []bool{true, false} {
		for _, status := range []string{"active", "expired", "revoked"} {
			t.Run(strings.Join([]string{map[bool]string{true: "legacy-personal", false: "workspace-team"}[legacy], status}, "/"), func(t *testing.T) {
				connection := db.RemoteAppConnection{ID: connectionID, OauthClientID: clientID, AppSlug: "fixture", Scope: ScopeTeam, OwnerID: ws1, WorkspaceID: ws1, Status: status, RefreshToken: []byte("sealed:refresh-fixture")}
				if legacy {
					connection.Scope = ScopePersonal
					connection.WorkspaceID = pgtype.UUID{}
				}
				client := db.RemoteAppOauthClient{ID: clientID, TokenEndpoint: "https://fixture.invalid/token", ClientID: "public-fixture", ClientSecret: []byte("sealed:client-fixture")}
				store := &scopeDB{t: t, workspaces: []pgtype.UUID{ws1, ws2}}
				store.row = func(query string, args []any) pgx.Row {
					switch {
					case strings.Contains(query, "-- name: LockRemoteAppConnection"):
						if args[0] != connectionID {
							t.Fatal("looked up unrelated connection")
						}
						return scopeStructRow(connection)
					case strings.Contains(query, "-- name: GetRemoteAppOAuthClientByID"):
						if args[0] != clientID {
							t.Fatal("looked up unrelated OAuth client")
						}
						return scopeStructRow(client)
					default:
						t.Fatalf("unexpected row query: %s", query)
						return nil
					}
				}
				refreshes := 0
				upstream := scopeUpstream{refresh: func(endpoint, resource, token string, registration remotemcp.OAuthClientRegistration) (remotemcp.OAuthTokenResponse, error) {
					refreshes++
					if endpoint != client.TokenEndpoint || resource != "https://fixture.invalid/mcp" || token != "refresh-fixture" || registration.ClientSecret != "client-fixture" {
						t.Fatal("incorrect synthetic refresh inputs")
					}
					return remotemcp.OAuthTokenResponse{AccessToken: "access-next", RefreshToken: "refresh-next", ExpiresIn: 3600}, nil
				}}
				s := &Service{q: db.New(store), now: time.Now, remote: newRemoteSupport(Config{DB: store, SecretBox: scopeSealer{}, RemoteApps: []RemoteApp{{Slug: "fixture", Endpoint: "https://fixture.invalid/mcp"}}, RemoteUpstream: upstream}, "https://fixture.invalid")}
				token, err := s.refreshRemote(context.Background(), connectionID, "")
				if status != "active" {
					if !errors.Is(err, ErrAccountMissing) || token != "" || refreshes != 0 || store.committed {
						t.Fatal("inactive account gained refresh access")
					}
					return
				}
				if err != nil || token != "access-next" || refreshes != 1 || !store.committed {
					t.Fatalf("refresh failed: %v", err)
				}
				var keys []int32
				writes := 0
				for _, call := range store.calls {
					if call.sql == "SELECT pg_advisory_xact_lock($1,$2)" {
						keys = append(keys, call.args[1].(int32))
					}
					if strings.HasPrefix(call.sql, "-- name: LockRemoteAppConnection") && len(keys) != 2 {
						t.Fatal("account row locked before workspace prefixes")
					}
					if strings.Contains(call.sql, "-- name: UpdateRemoteAppConnectionTokens") {
						writes++
						if call.args[0] != connectionID || string(call.args[1].([]byte)) != "sealed:access-next" || string(call.args[2].([]byte)) != "sealed:refresh-next" {
							t.Fatal("token update escaped its account or sealing boundary")
						}
					} else if strings.Contains(call.sql, "-- name:") && !strings.Contains(call.sql, "-- name: LockRemoteAppConnection") && !strings.Contains(call.sql, "-- name: GetRemoteAppOAuthClientByID") {
						t.Fatalf("unexpected mutation/read: %s", call.sql)
					}
				}
				want := []int32{bindingtx.WorkspaceKey(ws1), bindingtx.WorkspaceKey(ws2)}
				slices.Sort(want)
				if !slices.Equal(keys, want) || writes != 1 {
					t.Fatalf("prefixes=%v writes=%d", keys, writes)
				}
			})
		}
	}
}

func TestPersonalAppAttachmentUsesResolvedAgentWorkspace(t *testing.T) {
	ws := scopeUUID(t, "00000000-0000-0000-0000-000000000011")
	other := scopeUUID(t, "00000000-0000-0000-0000-000000000012")
	agent := db.Agent{ID: scopeUUID(t, "00000000-0000-0000-0000-000000000013"), WorkspaceID: ws}
	actor := scopeUUID(t, "00000000-0000-0000-0000-000000000014")
	store := &scopeDB{t: t}
	store.row = func(query string, args []any) pgx.Row {
		switch {
		case strings.Contains(query, "-- name: GetAgent"):
			if args[0] != agent.ID {
				t.Fatal("looked up unrelated agent")
			}
			return scopeStructRow(agent)
		case strings.Contains(query, "-- name: UpsertAgentApp"):
			if args[0] != ws || args[1] != agent.ID {
				t.Fatal("attachment escaped resolved agent workspace")
			}
			return scopeStructRow(db.AgentApp{})
		default:
			t.Fatalf("unexpected row query: %s", query)
			return nil
		}
	}
	s := &Service{q: db.New(store), tx: store}
	err := s.addPersonalApp(context.Background(), db.AppActionRequest{AgentID: agent.ID, WorkspaceID: other, ToolkitSlug: "fixture"}, actor)
	if err != nil || !store.committed {
		t.Fatalf("attachment failed: %v", err)
	}
	var keys []int32
	for _, call := range store.calls {
		if call.sql == "SELECT pg_advisory_xact_lock($1,$2)" {
			keys = append(keys, call.args[1].(int32))
		}
		if strings.Contains(call.sql, "-- name: SetAgentComposioToolkitAllowlist") && call.args[0] != agent.ID {
			t.Fatal("changed unrelated agent")
		}
		if call.sql == "SELECT id FROM workspace ORDER BY id" {
			t.Fatal("ordinary attachment broadened to all workspaces")
		}
	}
	if !slices.Equal(keys, []int32{bindingtx.WorkspaceKey(ws)}) {
		t.Fatalf("attachment prefixes=%v", keys)
	}
}

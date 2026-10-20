package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

type publicAPIWorkspaceQueriesStub struct {
	workspace db.Workspace
	members   []db.Member
}

func (s publicAPIWorkspaceQueriesStub) GetWorkspaceBySlug(context.Context, string) (db.Workspace, error) {
	return s.workspace, nil
}

func (s publicAPIWorkspaceQueriesStub) GetMemberByUserAndWorkspace(_ context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error) {
	for _, member := range s.members {
		if member.UserID == arg.UserID && member.WorkspaceID == arg.WorkspaceID {
			return member, nil
		}
	}
	return db.Member{}, pgx.ErrNoRows
}

func TestRequirePublicAPIWorkspaceMemberAllowsMemberWorkspace(t *testing.T) {
	userID, err := util.ParseUUID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := util.ParseUUID("00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	member := db.Member{UserID: userID, WorkspaceID: workspaceID, Role: "member"}
	gate := requirePublicAPIWorkspaceMember(publicAPIWorkspaceQueriesStub{
		workspace: db.Workspace{ID: workspaceID},
		members:   []db.Member{member},
	})
	called := false
	ctx := WithPublicAPIUserCredential(context.Background(), PublicAPIUserCredential{
		UserID:      "00000000-0000-0000-0000-000000000001",
		WorkspaceID: util.UUIDToString(workspaceID),
		Scopes:      []string{"issues:read"},
		Credential:  publicapiv1.CredentialPersonalAccess,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/MUL-1", nil).WithContext(ctx)
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
	req.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000002")
	response := httptest.NewRecorder()
	gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if WorkspaceIDFromContext(r.Context()) != "00000000-0000-0000-0000-000000000002" {
			t.Errorf("workspace context = %q", WorkspaceIDFromContext(r.Context()))
		}
		if got, ok := MemberFromContext(r.Context()); !ok || got.UserID != userID {
			t.Errorf("member context = %+v, present=%v", got, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("status=%d called=%v body=%s", response.Code, called, response.Body.String())
	}
}

func TestRequirePublicAPIWorkspaceMemberHidesOtherWorkspace(t *testing.T) {
	userID, err := util.ParseUUID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	memberWorkspaceID, err := util.ParseUUID("00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	requestedWorkspaceID, err := util.ParseUUID("00000000-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	gate := requirePublicAPIWorkspaceMember(publicAPIWorkspaceQueriesStub{
		workspace: db.Workspace{ID: requestedWorkspaceID},
		members: []db.Member{{
			UserID: userID, WorkspaceID: memberWorkspaceID, Role: "member",
		}},
	})
	ctx := WithPublicAPIUserCredential(context.Background(), PublicAPIUserCredential{
		UserID:      "00000000-0000-0000-0000-000000000001",
		WorkspaceID: util.UUIDToString(memberWorkspaceID),
		Scopes:      []string{"issues:read"},
		Credential:  publicapiv1.CredentialPersonalAccess,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/MUL-1", nil).WithContext(ctx)
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
	req.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000003")
	response := httptest.NewRecorder()
	called := false
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, req)
	if called {
		t.Fatal("request for a workspace without membership reached the handler")
	}
	assertPublicAPIProblem(t, response, http.StatusNotFound, "not_found")
}

func TestRequirePublicAPIWorkspaceMemberEnforcesTokenWorkspaceBinding(t *testing.T) {
	userID, err := util.ParseUUID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	boundWorkspaceID, err := util.ParseUUID("00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	requestedWorkspaceID, err := util.ParseUUID("00000000-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	gate := requirePublicAPIWorkspaceMember(publicAPIWorkspaceQueriesStub{
		workspace: db.Workspace{ID: requestedWorkspaceID},
		members:   []db.Member{{UserID: userID, WorkspaceID: requestedWorkspaceID, Role: "member"}},
	})
	ctx := WithPublicAPIUserCredential(context.Background(), PublicAPIUserCredential{
		UserID:      "00000000-0000-0000-0000-000000000001",
		WorkspaceID: util.UUIDToString(boundWorkspaceID),
		Scopes:      []string{"issues:read"},
		Credential:  publicapiv1.CredentialPersonalAccess,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/MUL-1", nil).WithContext(ctx)
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
	req.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000003")
	response := httptest.NewRecorder()
	called := false
	gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, req)
	if called {
		t.Fatal("PAT reached a workspace outside its stored grant")
	}
	assertPublicAPIProblem(t, response, http.StatusNotFound, "not_found")
}

package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/foldersnapshot"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func folderSnapshotData(t *testing.T, name string, mode os.FileMode) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	h := &zip.FileHeader{Name: name}
	if mode != 0 {
		h.SetMode(mode)
	}
	f, err := w.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("contents")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func uploadFolderRequest(t *testing.T, data []byte, workspaceID string, fields ...map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	f, err := w.CreateFormFile("file", "project.multica-folder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, group := range fields {
		for key, value := range group {
			if err := w.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload-file", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
}

type cleanupFolderStorage struct {
	mockStorage
	uploadedKeys []string
	deletedKeys  []string
}

func (s *cleanupFolderStorage) Upload(ctx context.Context, key string, data []byte, contentType, filename string) (string, error) {
	s.uploadedKeys = append(s.uploadedKeys, key)
	return s.mockStorage.Upload(ctx, key, data, contentType, filename)
}

func (s *cleanupFolderStorage) DeleteKeys(ctx context.Context, keys []string) {
	s.deletedKeys = append(s.deletedKeys, keys...)
	for _, key := range keys {
		s.mockStorage.Delete(ctx, key)
	}
}

func TestUploadFolderSnapshotInsertFailureCleansStoredObject(t *testing.T) {
	agentID := dbfx.Agent(t, "Folder insert failure", "")
	sessionID := dbfx.ChatSession(t, agentID, testutil.Cols{"explicitly_created_at": testutil.Raw("now()")})
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "folder_insert_failure_fn_" + suffix
	triggerName := "folder_insert_failure_" + suffix
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON attachment", triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
	})
	dbfx.Exec(t, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'forced folder attachment insert failure';
END;
$$;`, functionName))
	dbfx.Exec(t, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON attachment
FOR EACH ROW WHEN (NEW.workspace_id = '%s'::uuid AND NEW.chat_session_id = '%s'::uuid)
EXECUTE FUNCTION %s();`, triggerName, testWorkspaceID, sessionID, functionName))
	original := testHandler.Storage
	store := &cleanupFolderStorage{}
	testHandler.Storage = store
	t.Cleanup(func() { testHandler.Storage = original })
	req := uploadFolderRequest(t, folderSnapshotData(t, "project/a", 0), testWorkspaceID, map[string]string{"chat_session_id": sessionID})
	result := testutil.Call(t, testHandler.UploadFile, req).Want(http.StatusInternalServerError).Map()
	if result["id"] != nil {
		t.Fatalf("failed insert returned a bindable attachment: %v", result)
	}
	if len(store.uploadedKeys) != 1 || len(store.deletedKeys) != 1 || store.uploadedKeys[0] != store.deletedKeys[0] || len(store.files) != 0 {
		t.Fatalf("stored object was not rolled back: uploaded=%v deleted=%v remaining=%d", store.uploadedKeys, store.deletedKeys, len(store.files))
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM attachment WHERE chat_session_id=$1", sessionID).Scan(&count)
	if count != 0 {
		t.Fatalf("failed insert left %d attachment rows", count)
	}
}

func TestUploadFolderSnapshotWorkspaceAndSessionAuthorization(t *testing.T) {
	agentID := dbfx.Agent(t, "Folder authorization", "")
	visible := testutil.Cols{"explicitly_created_at": testutil.Raw("now()")}
	ownedSession := dbfx.ChatSession(t, agentID, visible)
	otherUser := dbfx.User(t, "Folder other member", uuid.NewString()+"@folder.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	otherSession := dbfx.ChatSession(t, agentID, testutil.Cols{
		"creator_id": otherUser, "explicitly_created_at": testutil.Raw("now()"),
	})
	foreignWorkspace := dbfx.Workspace(t, "Folder foreign workspace", "folder-"+uuid.NewString())
	foreignAgent := dbfx.Agent(t, "Folder foreign agent", "", testutil.Cols{"workspace_id": foreignWorkspace})
	foreignSession := dbfx.ChatSession(t, foreignAgent, testutil.Cols{
		"workspace_id": foreignWorkspace, "explicitly_created_at": testutil.Raw("now()"),
	})
	privateAgent := dbfx.Agent(t, "Folder private agent", "", testutil.Cols{"owner_id": otherUser})
	restrictedUser := dbfx.User(t, "Folder restricted member", uuid.NewString()+"@folder.test")
	dbfx.Member(t, testWorkspaceID, restrictedUser, "member")
	privateSession := dbfx.ChatSession(t, privateAgent, testutil.Cols{
		"creator_id": restrictedUser, "explicitly_created_at": testutil.Raw("now()"),
	})
	hiddenSession := dbfx.ChatSession(t, agentID)
	for _, tc := range []struct {
		name, workspaceID, sessionID, userID string
		status                               int
	}{
		{"unauthenticated", testWorkspaceID, "", "", http.StatusUnauthorized},
		{"missing workspace", "", "", testUserID, http.StatusBadRequest},
		{"workspace nonmember", foreignWorkspace, "", testUserID, http.StatusForbidden},
		{"other creator", testWorkspaceID, otherSession, testUserID, http.StatusForbidden},
		{"foreign workspace session", testWorkspaceID, foreignSession, testUserID, http.StatusNotFound},
		{"private agent", testWorkspaceID, privateSession, restrictedUser, http.StatusForbidden},
		{"hidden session", testWorkspaceID, hiddenSession, testUserID, http.StatusNotFound},
		{"owned session", testWorkspaceID, ownedSession, testUserID, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := testHandler.Storage
			store := &mockStorage{}
			testHandler.Storage = store
			t.Cleanup(func() { testHandler.Storage = original })
			fields := map[string]string{}
			if tc.sessionID != "" {
				fields["chat_session_id"] = tc.sessionID
			}
			req := uploadFolderRequest(t, folderSnapshotData(t, "project/a", 0), tc.workspaceID, fields)
			req.Header.Set("X-User-ID", tc.userID)
			result := testutil.Call(t, testHandler.UploadFile, req).Want(tc.status)
			if tc.status != http.StatusOK {
				if len(store.files) != 0 {
					t.Fatal("unauthorized snapshot reached storage")
				}
				return
			}
			var attachment AttachmentResponse
			result.JSON(&attachment)
			if attachment.ChatSessionID == nil || *attachment.ChatSessionID != ownedSession {
				t.Fatalf("wrong attachment session: %+v", attachment)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), "DELETE FROM attachment WHERE id=$1", attachment.ID)
			})
		})
	}
}

type folderZeroReader struct{}

func (folderZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func sizedFolderSnapshotData(t *testing.T, count int, totalBytes int64) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for i := 0; i < count; i++ {
		f, err := w.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("project/file-%03d", i), Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := io.CopyN(f, folderZeroReader{}, totalBytes); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestUploadFolderSnapshotMultipartLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		bytes  int64
		status int
	}{
		{"exact file count", 500, 0, http.StatusOK},
		{"one file over", 501, 0, http.StatusBadRequest},
		{"exact byte size", 1, 100 << 20, http.StatusOK},
		{"one byte over", 1, (100 << 20) + 1, http.StatusBadRequest},
		{"both exact limits", 500, 100 << 20, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := testHandler.Storage
			store := &mockStorage{}
			testHandler.Storage = store
			t.Cleanup(func() { testHandler.Storage = original })
			data := sizedFolderSnapshotData(t, tc.count, tc.bytes)
			req := uploadFolderRequest(t, data, testWorkspaceID)
			if req.ContentLength <= int64(len(data)) {
				t.Fatal("multipart request must include envelope overhead")
			}
			result := testutil.Call(t, testHandler.UploadFile, req).Want(tc.status)
			if tc.status != http.StatusOK {
				if len(store.files) != 0 {
					t.Fatal("over-limit snapshot reached storage")
				}
				return
			}
			var attachment AttachmentResponse
			result.JSON(&attachment)
			if attachment.ID == "" || len(store.files) != 1 {
				t.Fatalf("expected one folder attachment: %+v", attachment)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), "DELETE FROM attachment WHERE id=$1", attachment.ID)
			})
		})
	}
}

func TestUploadFolderSnapshotRejectsUnsafeContentBeforeStorage(t *testing.T) {
	original := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	t.Cleanup(func() { testHandler.Storage = original })
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{
		{"project/../outside", 0}, {"/absolute/file", 0}, {"project/link", os.ModeSymlink | 0o777},
	} {
		w := httptest.NewRecorder()
		testHandler.UploadFile(w, uploadFolderRequest(t, folderSnapshotData(t, tc.name, tc.mode), testWorkspaceID))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	if len(store.files) != 0 {
		t.Fatal("invalid snapshot reached storage")
	}
}

func TestUploadFolderSnapshotCreatesOneAttachment(t *testing.T) {
	original := testHandler.Storage
	store := &mockStorage{}
	testHandler.Storage = store
	t.Cleanup(func() { testHandler.Storage = original })
	w := httptest.NewRecorder()
	testHandler.UploadFile(w, uploadFolderRequest(t, folderSnapshotData(t, "project/src/a.txt", 0), testWorkspaceID))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var result AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || result.ContentType != foldersnapshot.ContentType || len(store.files) != 1 {
		t.Fatalf("invalid folder attachment: %+v", result)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), "DELETE FROM attachment WHERE id=$1", result.ID) })
}

type failedFolderStorage struct{ mockStorage }

func (s *failedFolderStorage) Upload(context.Context, string, []byte, string, string) (string, error) {
	return "", errors.New("storage unavailable")
}

func TestUploadFolderSnapshotFailureReturnsNoAttachment(t *testing.T) {
	original := testHandler.Storage
	testHandler.Storage = &failedFolderStorage{}
	t.Cleanup(func() { testHandler.Storage = original })
	w := httptest.NewRecorder()
	testHandler.UploadFile(w, uploadFolderRequest(t, folderSnapshotData(t, "project/a", 0), testWorkspaceID))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["id"] != nil {
		t.Fatalf("failed snapshot returned bindable attachment: %v", result)
	}
}

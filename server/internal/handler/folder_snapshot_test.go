package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/foldersnapshot"
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

func uploadFolderRequest(t *testing.T, data []byte, workspaceID string) *http.Request {
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
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload-file", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
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

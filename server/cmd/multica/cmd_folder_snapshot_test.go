package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/foldersnapshot"
)

func TestRunAttachmentDownloadReconstructsFolderSnapshot(t *testing.T) {
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for _, name := range []string{"project/readme.txt", "project/src/readme.txt"} {
		f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/attachments/folder":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "folder", "filename": "project.multica-folder", "content_type": foldersnapshot.ContentType,
				"download_url": "/snapshot",
			})
		case "/snapshot":
			_, _ = w.Write(data.Bytes())
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	output := t.TempDir()
	cmd := newAttachmentDownloadTestCmd()
	_ = cmd.Flags().Set("output-dir", output)
	out, err := captureStdout(t, func() error { return runAttachmentDownload(cmd, []string{"folder"}) })
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Path string }
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"readme.txt", "src/readme.txt"} {
		got, err := os.ReadFile(filepath.Join(result.Path, name))
		if err != nil || string(got) != "project/"+name {
			t.Fatalf("%s: %q, %v", name, got, err)
		}
	}
}

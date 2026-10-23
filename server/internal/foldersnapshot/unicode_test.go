package foldersnapshot

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const coreAttachments = "../../../packages/core/attachments/"

func TestSharedFilenameVectors(t *testing.T) {
	data, err := os.ReadFile(coreAttachments + "folder-path-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name  string   `json:"name"`
		Paths []string `json:"paths"`
		Error string   `json:"error"`
		Keys  []string `json:"keys"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			entries := make([]entry, len(vector.Paths))
			for i, name := range vector.Paths {
				entries[i] = entry{name: name, body: "test"}
			}
			_, err := Parse(archive(t, entries...))
			if vector.Error != "" {
				if err == nil {
					t.Fatal("expected rejection")
				}
				if unsupported := strings.Contains(err.Error(), "Unicode 15"); unsupported != (vector.Error == "unsupported_name") {
					t.Fatalf("wrong rejection category: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, name := range vector.Paths {
				if got := pathKey(name); got != vector.Keys[i] {
					t.Errorf("key %q: got %q, want %q", name, got, vector.Keys[i])
				}
			}
		})
	}
}

// The browser consumes the canonical JSON directly. Verify that its generated
// Go counterpart cannot drift when either table changes.
func TestPinnedUnicodeDataMatchesClient(t *testing.T) {
	data, err := os.ReadFile(coreAttachments + "unicode15-data.json")
	if err != nil {
		t.Fatal(err)
	}
	var client struct {
		Version  string          `json:"version"`
		Assigned [][2]rune       `json:"assigned"`
		Marks    [][2]rune       `json:"marks"`
		Folds    map[rune]string `json:"folds"`
	}
	if err := json.Unmarshal(data, &client); err != nil {
		t.Fatal(err)
	}
	if client.Version != "15.0.0" || !reflect.DeepEqual(client.Assigned, assigned15) || !reflect.DeepEqual(client.Marks, marks15) || !reflect.DeepEqual(client.Folds, folds15) {
		t.Fatal("Unicode tables differ; regenerate both with scripts/generate-folder-unicode.mjs")
	}
}

package foldersnapshot

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, body string
	mode       os.FileMode
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestExtractRelativeStructureAndNoOverwrite(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(out, "project")); err != nil {
		t.Fatal(err)
	}
	data := archive(t, entry{name: "project/a.txt", body: "first"}, entry{name: "project/src/a.txt", body: "nested"})
	dest, err := Extract(data, out)
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"a.txt": "first", "src/a.txt": "nested"} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v", rel, got, err)
		}
	}
	if !strings.HasPrefix(dest, out+string(os.PathSeparator)) {
		t.Fatalf("escaped work directory: %s", dest)
	}
	files, err := os.ReadDir(outside)
	if err != nil || len(files) != 0 {
		t.Fatalf("followed existing symlink: %v", files)
	}
	second, err := Extract(data, out)
	if err != nil || second == dest {
		t.Fatalf("repeated download must be isolated: %s %v", second, err)
	}
}

func TestParseRejectsUnsafePathsAndLinks(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "C:/absolute", "root/../outside", "root/./a", "root//a", "root/a\\b", "root/NUL.txt", "root/a.", "root/a\x00b"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(archive(t, entry{name: name})); err == nil {
				t.Fatalf("accepted %q", name)
			}
		})
	}
	for _, mode := range []os.FileMode{os.ModeSymlink | 0o777, os.ModeNamedPipe | 0o600, os.ModeDir | 0o755} {
		if _, err := Parse(archive(t, entry{name: "root/link", body: "../../outside", mode: mode})); err == nil {
			t.Fatalf("accepted special mode %v", mode)
		}
	}
}

func TestParseRejectsConflictsAndMultipleRoots(t *testing.T) {
	for _, names := range [][]string{{"root/a", "root/A"}, {"root/a", "root/a/b"}, {"root/a", "other/b"}} {
		if _, err := Parse(archive(t, entry{name: names[0]}, entry{name: names[1]})); err == nil {
			t.Fatalf("accepted conflicting paths %v", names)
		}
	}
}

func TestFolderLimits(t *testing.T) {
	entries := make([]entry, MaxFiles)
	for i := range entries {
		entries[i].name = "root/" + strings.Repeat("a", i+1)
	}
	if _, err := Parse(archive(t, entries...)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(archive(t, append(entries, entry{name: "root/extra"})...)); err == nil {
		t.Fatal("accepted more than 500 files")
	}
	if _, err := Parse(archive(t)); err == nil {
		t.Fatal("accepted empty folder")
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	_, err := w.CreateRaw(&zip.FileHeader{Name: "root/large", UncompressedSize64: MaxBytes + 1, Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "100 MiB") {
		t.Fatalf("accepted oversized content: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestFolderExactByteLimit(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("root/large")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(f, zeroReader{}, MaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(buf.Bytes()); err != nil {
		t.Fatalf("rejected exact 100 MiB content: %v", err)
	}
}

func TestCorruptSnapshotLeavesNoPartialFolder(t *testing.T) {
	data := archive(t, entry{name: "root/first", body: "first"}, entry{name: "root/second", body: "unique contents"})
	i := bytes.Index(data, []byte("unique contents"))
	data[i] ^= 0xff
	out := t.TempDir()
	if _, err := Extract(data, out); err == nil {
		t.Fatal("accepted corrupt snapshot")
	}
	files, err := os.ReadDir(out)
	if err != nil || len(files) != 0 {
		t.Fatalf("left partial extraction: %v %v", files, err)
	}
}

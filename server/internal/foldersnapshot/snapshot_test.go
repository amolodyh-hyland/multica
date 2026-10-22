package foldersnapshot

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
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

func archive(t testing.TB, entries ...entry) []byte {
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

func FuzzParse(f *testing.F) {
	f.Add(archive(f, entry{name: "root/a", body: "contents"}))
	f.Add([]byte{})
	f.Add([]byte("PK\x05\x06"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Parse(data)
	})
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
	for _, names := range [][]string{
		{"root/a", "root/A"}, {"root/a", "root/a/b"}, {"root/a", "other/b"},
		{"root/é.txt", "root/e\u0301.txt"}, {"root/Foo/a", "root/foo/b"},
		{"root/é/a", "root/e\u0301/b"}, {"root/Foo", "root/foo/a"},
		{"root/straße.txt", "root/STRASSE.txt"}, {"root/ς/a", "root/σ/b"},
	} {
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
	f, err := w.CreateHeader(&zip.FileHeader{Name: "root/large", Method: zip.Store})
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

func TestParseAllowsOneConsistentDirectorySpelling(t *testing.T) {
	if _, err := Parse(archive(t, entry{name: "root/é/a"}, entry{name: "root/é/b"})); err != nil {
		t.Fatal(err)
	}
}

func TestParseBoundsActualCentralDirectoryCountBeforeAllocation(t *testing.T) {
	const records = 1_000_000
	base := archive(t, entry{name: "root/"})
	end := len(base) - 22
	offset := int(binary.LittleEndian.Uint32(base[end+16:]))
	header := base[offset:end]
	data := make([]byte, offset+records*len(header)+22)
	copy(data, base[:offset])
	for i := 0; i < records; i++ {
		copy(data[offset+i*len(header):], header)
	}
	footer := data[len(data)-22:]
	copy(footer, base[end:])
	binary.LittleEndian.PutUint32(footer[12:], uint32(records*len(header)))
	allocs := testing.AllocsPerRun(1, func() {
		if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "500 files") {
			t.Fatalf("accepted hidden central-directory records: %v", err)
		}
	})
	if allocs > 20 {
		t.Fatalf("central directory materialized before its count was bounded: %.0f allocations", allocs)
	}
}

func TestParseChecksZeroCRCWithoutDataDescriptor(t *testing.T) {
	for _, body := range []string{"wrong contents", ""} {
		var data bytes.Buffer
		w := zip.NewWriter(&data)
		f, err := w.CreateRaw(&zip.FileHeader{
			Name: "root/a", Method: zip.Store, CRC32: 0,
			CompressedSize64: uint64(len(body)), UncompressedSize64: uint64(len(body)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = Parse(data.Bytes())
		if body == "" && err != nil {
			t.Fatalf("valid zero CRC: %v", err)
		}
		if body != "" && err == nil {
			t.Fatal("accepted nonzero content with zero central CRC and no descriptor")
		}
	}
}

func TestParseRejectsCompressionMethods(t *testing.T) {
	for _, method := range []uint16{zip.Deflate, 99} {
		data := archive(t, entry{name: "root/a", body: "content"})
		end := len(data) - 22
		central := int(binary.LittleEndian.Uint32(data[end+16:]))
		binary.LittleEndian.PutUint16(data[central+10:], method)
		if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "STORE") {
			t.Fatalf("method %d: %v", method, err)
		}
	}
	data := archive(t, entry{name: "root/a", body: "content"})
	binary.LittleEndian.PutUint16(data[8:], zip.Deflate)
	if _, err := Parse(data); err == nil {
		t.Fatal("accepted mismatched local compression method")
	}
}

func TestParseRejectsInvalidCentralDirectoryBounds(t *testing.T) {
	base := archive(t, entry{name: "root/a", body: "contents"})
	for _, mutate := range []func([]byte){
		func(data []byte) { binary.LittleEndian.PutUint32(data[len(data)-22+12:], 0xffffffff) },
		func(data []byte) { binary.LittleEndian.PutUint32(data[len(data)-22+16:], 0xffffffff) },
		func(data []byte) { binary.LittleEndian.PutUint16(data[len(data)-22+4:], 1) },
		func(data []byte) { binary.LittleEndian.PutUint16(data[len(data)-22+8:], 2) },
		func(data []byte) { binary.LittleEndian.PutUint16(data[len(data)-22+10:], 0xffff) },
		func(data []byte) { binary.LittleEndian.PutUint16(data[len(data)-22+20:], 1) },
		func(data []byte) {
			central := int(binary.LittleEndian.Uint32(data[len(data)-22+16:]))
			binary.LittleEndian.PutUint16(data[central+28:], 0xffff)
		},
	} {
		data := bytes.Clone(base)
		mutate(data)
		if _, err := Parse(data); err == nil {
			t.Fatal("accepted invalid central directory")
		}
	}
}

func TestParseValidatesEveryCRC(t *testing.T) {
	data := archive(t, entry{name: "root/a", body: "contents"})
	central := int(binary.LittleEndian.Uint32(data[len(data)-22+16:]))
	binary.LittleEndian.PutUint32(data[central+16:], crc32.ChecksumIEEE([]byte("different contents")))
	if _, err := Parse(data); err == nil {
		t.Fatal("accepted mismatched nonzero CRC")
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

package foldersnapshot

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func validateCentralDirectory(data []byte) error {
	if len(data) < 22 {
		return zip.ErrFormat
	}
	end := len(data) - 22
	footer := data[end:]
	if binary.LittleEndian.Uint32(footer) != 0x06054b50 || binary.LittleEndian.Uint16(footer[20:]) != 0 {
		return errors.New("folder snapshot requires a ZIP without an archive comment")
	}
	declared := binary.LittleEndian.Uint16(footer[10:])
	if binary.LittleEndian.Uint16(footer[4:]) != 0 || binary.LittleEndian.Uint16(footer[6:]) != 0 || binary.LittleEndian.Uint16(footer[8:]) != declared {
		return errors.New("folder snapshot requires a single-disk ZIP")
	}
	if declared == 0 || declared > MaxFiles {
		return errors.New("folder snapshot must contain 1 to 500 files")
	}
	offset := uint64(binary.LittleEndian.Uint32(footer[16:]))
	size := uint64(binary.LittleEndian.Uint32(footer[12:]))
	if offset+size != uint64(end) {
		return zip.ErrFormat
	}
	count := 0
	for pos := int(offset); pos < end; {
		if count == MaxFiles {
			return errors.New("folder snapshot must contain 1 to 500 files")
		}
		if end-pos < 46 || binary.LittleEndian.Uint32(data[pos:]) != 0x02014b50 {
			return zip.ErrFormat
		}
		header := data[pos : pos+46]
		if binary.LittleEndian.Uint16(header[10:]) != zip.Store {
			return errors.New("folder snapshot supports only ZIP STORE entries")
		}
		nameSize := int(binary.LittleEndian.Uint16(header[28:]))
		recordSize := 46 + nameSize + int(binary.LittleEndian.Uint16(header[30:])) + int(binary.LittleEndian.Uint16(header[32:]))
		if nameSize > MaxPathBytes || recordSize > end-pos || binary.LittleEndian.Uint16(header[34:]) != 0 {
			return zip.ErrFormat
		}
		local := uint64(binary.LittleEndian.Uint32(header[42:]))
		if local+30 > offset || binary.LittleEndian.Uint32(data[int(local):]) != 0x04034b50 {
			return zip.ErrFormat
		}
		if binary.LittleEndian.Uint16(data[int(local)+8:]) != zip.Store {
			return errors.New("folder snapshot supports only ZIP STORE entries")
		}
		count++
		pos += recordSize
	}
	if count != int(declared) {
		return zip.ErrFormat
	}
	return nil
}

func pathKey(name string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(name)))
}

const (
	Extension       = ".multica-folder"
	ContentType     = "application/vnd.multica.folder+zip"
	MaxFiles        = 500
	MaxBytes        = 100 << 20
	MaxArchiveBytes = MaxBytes + (2 << 20)
	MaxPathBytes    = 1024
)

func ValidPath(name string) bool {
	if name == "" || len(name) > MaxPathBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, "<>\"|?*") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
		for _, c := range part {
			if c < 32 || c == 127 {
				return false
			}
		}
	}
	return true
}

func Parse(data []byte) (*zip.Reader, error) {
	if len(data) > MaxArchiveBytes {
		return nil, errors.New("folder snapshot exceeds 100 MiB limit")
	}
	if err := validateCentralDirectory(data); err != nil {
		return nil, fmt.Errorf("invalid folder snapshot: %w", err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid folder snapshot: %w", err)
	}
	if len(r.File) == 0 || len(r.File) > MaxFiles {
		return nil, errors.New("folder snapshot must contain 1 to 500 files")
	}
	seen := make(map[string]bool)
	directories := make(map[string]string)
	root := ""
	var total uint64
	for _, f := range r.File {
		parts := strings.SplitN(f.Name, "/", 2)
		if !ValidPath(f.Name) || len(parts) != 2 || !f.Mode().IsRegular() {
			return nil, fmt.Errorf("unsafe folder entry: %q", f.Name)
		}
		if root == "" {
			root = parts[0]
		}
		if root != parts[0] {
			return nil, errors.New("folder snapshot must have one root directory")
		}
		key := pathKey(f.Name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate folder entry: %q", f.Name)
		}
		for parent, parentKey := path.Dir(f.Name), path.Dir(key); parent != "."; parent, parentKey = path.Dir(parent), path.Dir(parentKey) {
			if original, exists := directories[parentKey]; exists && original != parent {
				return nil, fmt.Errorf("ambiguous folder directory: %q and %q", original, parent)
			}
			directories[parentKey] = parent
		}
		seen[key] = true
		if f.UncompressedSize64 > MaxBytes || total > MaxBytes-f.UncompressedSize64 {
			return nil, errors.New("folder snapshot exceeds 100 MiB limit")
		}
		total += f.UncompressedSize64
	}
	for name := range directories {
		if seen[name] {
			return nil, errors.New("folder entry conflicts with a directory")
		}
	}
	for _, f := range r.File {
		src, err := f.Open()
		if err != nil {
			return nil, err
		}
		checksum := crc32.NewIEEE()
		n, err := io.Copy(checksum, io.LimitReader(src, int64(f.UncompressedSize64)+1))
		closeErr := src.Close()
		if err != nil || closeErr != nil || n != int64(f.UncompressedSize64) || checksum.Sum32() != f.CRC32 {
			return nil, fmt.Errorf("corrupt folder entry: %q", f.Name)
		}
	}
	return r, nil
}

func Extract(data []byte, outputDir string) (string, error) {
	r, err := Parse(data)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	staging := "folder-snapshot-" + rand.Text()
	if err := root.Mkdir(staging, 0o700); err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = root.RemoveAll(staging)
		}
	}()
	dir, err := root.OpenRoot(staging)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	for _, f := range r.File {
		if err := dir.MkdirAll(filepath.FromSlash(path.Dir(f.Name)), 0o755); err != nil {
			return "", err
		}
		dest, err := dir.OpenFile(filepath.FromSlash(f.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return "", err
		}
		src, err := f.Open()
		if err != nil {
			_ = dest.Close()
			return "", err
		}
		_, copyErr := io.Copy(dest, io.LimitReader(src, int64(f.UncompressedSize64)+1))
		readErr, writeErr := src.Close(), dest.Close()
		if err := errors.Join(copyErr, readErr, writeErr); err != nil {
			return "", err
		}
	}
	if info, err := root.Lstat(staging); err != nil || info.Mode()&fs.ModeSymlink != 0 {
		return "", errors.New("folder snapshot directory changed during download")
	}
	complete = true
	return filepath.Abs(filepath.Join(outputDir, staging, strings.SplitN(r.File[0].Name, "/", 2)[0]))
}

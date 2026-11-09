//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeControlTokenPrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalControlRejectsPublicPermissionsAndSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLocalControlToken(path); err == nil {
		t.Fatal("accepted public token")
	}
	makeControlTokenPrivate(t, path)
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLocalControlToken(link); err == nil {
		t.Fatal("accepted symlink token")
	}
}

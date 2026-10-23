//go:build !windows

package knowledge

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestKnowledgeFIFOIsRejectedWithoutBlocking(t *testing.T) {
	dir := testDirectory(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	store := LocalStore{Dir: testDirectory(t)}
	if _, err := store.Register(context.Background(), testScope(), Folder, dir, true, true); err == nil {
		t.Fatal("FIFO accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO read blocked")
	}
}

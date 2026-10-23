//go:build !windows

package knowledge

import (
	"fmt"
	"os"
	"syscall"
)

func readFlags() int                          { return os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW }
func openLocal(path string) (*os.File, error) { return os.OpenFile(path, readFlags(), 0) }
func fileIdentity(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

func lockRegistration(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

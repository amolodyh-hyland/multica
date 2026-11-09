//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

func validateLocalControlPrivacy(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("cannot inspect daemon control token permissions")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon control token must be owned by this user with no group or other access")
	}
	return nil
}

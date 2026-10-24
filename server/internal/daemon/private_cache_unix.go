//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func privateCacheReadFlags() int { return os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }

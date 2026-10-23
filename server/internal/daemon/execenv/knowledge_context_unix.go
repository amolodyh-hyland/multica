//go:build !windows

package execenv

import (
	"os"
	"syscall"
)

func knowledgeContextWriteFlags() int { return os.O_WRONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }

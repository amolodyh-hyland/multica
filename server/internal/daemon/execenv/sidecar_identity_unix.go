//go:build !windows

package execenv

import (
	"fmt"
	"os"
	"reflect"
	"syscall"
)

func sidecarFileIdentity(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", os.ErrInvalid
	}
	identity := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	if info.Mode().IsRegular() {
		// Change time is not restorable with user-set modification timestamps.
		fields := reflect.ValueOf(stat).Elem()
		for _, name := range []string{"Ctim", "Ctimespec"} {
			value := fields.FieldByName(name)
			if value.IsValid() {
				return identity + ":" + fmt.Sprint(value.Interface()), nil
			}
		}
		return "", os.ErrInvalid
	}
	return identity, nil
}

func sidecarReadFlags() int { return os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }

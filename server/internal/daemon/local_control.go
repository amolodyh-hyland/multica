package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var localControlTokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func loadLocalControlToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("daemon control token file must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 64 || info.Size() > 66 {
		return "", fmt.Errorf("daemon control token requires a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot open daemon control token file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("daemon control token file changed")
	}
	if err := validateLocalControlPrivacy(f); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, 67))
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if err != nil || !localControlTokenPattern.MatchString(token) {
		return "", fmt.Errorf("daemon control token must contain 64 lowercase hexadecimal characters")
	}
	return token, nil
}

func isolatedDaemonIdentity(value, profile, id string, restricted bool) (bool, error) {
	if value == "" {
		return false, nil
	}
	isolated, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("MULTICA_DAEMON_ISOLATED must be boolean")
	}
	if isolated {
		parsed, err := uuid.Parse(id)
		if restricted || profile == "" || err != nil || parsed == uuid.Nil || parsed.String() != id {
			return false, fmt.Errorf("isolated standard daemon requires a named standard profile and explicit canonical nonzero UUID")
		}
	}
	return isolated, nil
}

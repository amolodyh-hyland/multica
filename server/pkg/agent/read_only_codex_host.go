package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Codex rust-v0.144.6 config/src/loader/{mod,layer_io,macos}.rs.
// Unix loaders have no environment-selected alternate managed file paths;
// LoaderOverrides are internal test/embedding hooks, not stock CLI settings.
func readOnlyCodexManagedPaths(home string) []string {
	return []string{
		"/etc/codex/config.toml", "/etc/codex/managed_config.toml", "/etc/codex/requirements.toml",
		filepath.Join(home, "managed_config.toml"), filepath.Join(home, "requirements.toml"),
	}
}

// Query the same CFPreferencesCopyAppValue API/domain/keys as Codex, including
// effective MDM preferences. Never decode or print preference contents.
const readOnlyCodexPreferencesProbe = `import ctypes
cf = ctypes.CDLL('/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation')
ptr = ctypes.c_void_p
cf.CFStringCreateWithCString.argtypes = [ptr, ctypes.c_char_p, ctypes.c_uint32]
cf.CFStringCreateWithCString.restype = ptr
cf.CFPreferencesCopyAppValue.argtypes = [ptr, ptr]
cf.CFPreferencesCopyAppValue.restype = ptr
cf.CFRelease.argtypes = [ptr]
cf.CFRelease.restype = None
domain = cf.CFStringCreateWithCString(None, b'com.openai.codex', 0x08000100)
if not domain:
    raise RuntimeError('preference domain unavailable')
present = False
for name in [b'config_toml_base64', b'requirements_toml_base64']:
    key = cf.CFStringCreateWithCString(None, name, 0x08000100)
    if not key:
        raise RuntimeError('preference key unavailable')
    value = cf.CFPreferencesCopyAppValue(key, domain)
    if value:
        present = True
        cf.CFRelease(value)
    cf.CFRelease(key)
cf.CFRelease(domain)
print('present' if present else 'absent')
`

func probeReadOnlyCodexPreferences(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", readOnlyCodexPreferencesProbe).Output()
}

func auditReadOnlyCodexHost(ctx context.Context, home, cwd string) error {
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolve isolated Codex home: %w", err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return fmt.Errorf("resolve isolated Codex working directory: %w", err)
	}
	if err := auditReadOnlyCodexLocalManaged(ctx, home, runtime.GOOS, os.Lstat, probeReadOnlyCodexPreferences); err != nil {
		return err
	}
	return auditReadOnlyCodexConfig(home, cwd, os.ReadFile)
}

func auditReadOnlyCodexLocalManaged(ctx context.Context, home, goos string, stat func(string) (os.FileInfo, error), preferences func(context.Context) ([]byte, error)) error {
	for _, path := range readOnlyCodexManagedPaths(home) {
		_, err := stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot rule out local managed Codex settings at %s; refusing native startup", path)
		}
		return fmt.Errorf("local managed Codex settings present at %s; refusing native startup without overriding policy", path)
	}
	if goos == "darwin" {
		result, err := preferences(ctx)
		if err != nil || strings.TrimSpace(string(result)) != "absent" {
			return fmt.Errorf("local managed Codex preferences or MDM settings are present or cannot be ruled out; refusing native startup without overriding policy")
		}
	}
	return nil
}

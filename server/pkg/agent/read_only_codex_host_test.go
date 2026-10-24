package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyCodexLocalManagedPresenceBeforeLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stock Codex bundle execution is unsupported on Windows")
	}
	home, cwd := t.TempDir(), t.TempDir()
	fixture := filepath.Join(t.TempDir(), "managed.toml")
	marker := filepath.Join(t.TempDir(), "started")
	fake := writeFakeCodexAppServer(t, "touch '"+marker+"'\nexit 1\n")
	cases := append(readOnlyCodexManagedPaths(home), "mdm")
	for _, source := range cases {
		for _, contents := range []string{"", "[features]\nplugins=false\n", "invalid='SECRET"} {
			t.Run(source+"/"+contents, func(t *testing.T) {
				if err := os.WriteFile(fixture, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
				b := &codexBackend{cfg: Config{provider: "codex", NativeReadOnly: true, CLIVersion: MinReadOnlyCodexVersion, ExecutablePath: fake, Env: map[string]string{"CODEX_HOME": home}, Logger: slog.Default()}}
				b.readOnlyHostAudit = func(ctx context.Context, home, _ string) error {
					return auditReadOnlyCodexLocalManaged(ctx, home, "darwin", func(path string) (os.FileInfo, error) {
						if path == source {
							return os.Lstat(fixture)
						}
						return nil, os.ErrNotExist
					}, func(context.Context) ([]byte, error) {
						if source != "mdm" {
							t.Fatal("read preferences after a managed file was already present")
						}
						return []byte("present\n"), nil
					})
				}
				if _, err := b.Execute(context.Background(), "fixture", ExecOptions{Cwd: cwd}); err == nil || !strings.Contains(err.Error(), "local managed Codex") || strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("managed launch outcome: %v", err)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("stock executable started before local-managed refusal")
				}
				if data, err := os.ReadFile(fixture); err != nil || string(data) != contents {
					t.Fatal("changed existing managed policy")
				}
			})
		}
	}
}

func TestReadOnlyCodexLocalManagedProbeFailures(t *testing.T) {
	absent := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	for _, response := range []struct {
		value string
		err   error
	}{{"present", nil}, {"", nil}, {"unknown SECRET", nil}, {"absent", errors.New("SECRET")}} {
		err := auditReadOnlyCodexLocalManaged(context.Background(), t.TempDir(), "darwin", absent, func(context.Context) ([]byte, error) { return []byte(response.value), response.err })
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("probe refusal outcome: %v", err)
		}
	}
	for _, platform := range []string{"darwin", "linux"} {
		if err := auditReadOnlyCodexLocalManaged(context.Background(), t.TempDir(), platform, absent, func(context.Context) ([]byte, error) {
			if platform != "darwin" {
				t.Fatal("queried macOS preferences on Linux")
			}
			return []byte("absent\n"), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := auditReadOnlyCodexLocalManaged(context.Background(), t.TempDir(), "darwin", func(string) (os.FileInfo, error) { return nil, os.ErrPermission }, nil); err == nil {
		t.Fatal("ignored unreadable system policy")
	}
	if runtime.GOOS == "windows" {
		return // Symlink creation may require privileges on Windows.
	}
	link := filepath.Join(t.TempDir(), "managed-link")
	if err := os.Symlink("missing", link); err != nil {
		t.Fatal(err)
	}
	if err := auditReadOnlyCodexLocalManaged(context.Background(), t.TempDir(), "darwin", func(string) (os.FileInfo, error) { return os.Lstat(link) }, nil); err == nil {
		t.Fatal("ignored present dangling managed symlink")
	}
}

func TestReadOnlyCodexPreferencesAPIAbsentFixture(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("CoreFoundation is macOS-only")
	}
	// Exercise the real API binding against a unique, nonexistent fixture domain.
	// Never query this computer's Codex preferences or any account settings.
	domain := "org.example.multica.fixture." + filepath.Base(filepath.Dir(t.TempDir()))
	script := strings.ReplaceAll(readOnlyCodexPreferencesProbe, "com.openai.codex", domain)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", script).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "absent" {
		t.Fatalf("CoreFoundation fixture probe: %v %q", err, output)
	}
}

package execenv

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestReadOnlyNativeHomeIgnoresInheritedBypass(t *testing.T) {
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "config.toml"), []byte("sandbox_mode=\"danger-full-access\"\n[mcp_servers.bad]\ncommand=\"sh\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "config.json"), []byte(`{"approval_policy":"auto"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "native-home")
	if err := prepareReadOnlyCodexHome(home, shared, "darwin"); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := toml.Unmarshal(bytes, &config); err != nil {
		t.Fatal(err)
	}
	if config["sandbox_mode"] != "read-only" || config["approval_policy"] != "never" || strings.Contains(string(bytes), "danger-full-access") {
		t.Fatalf("unsafe config: %s", bytes)
	}
	for _, file := range []string{"config.json", "instructions.md", "skills", "plugins"} {
		if _, err := os.Lstat(filepath.Join(home, file)); !os.IsNotExist(err) {
			t.Fatalf("inherited %s", file)
		}
	}
	if err := hydrateReadOnlyAwareCodexSkills(home, true, nil, nil, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if err := prepareReadOnlyCodexHome(home, shared, "darwin"); err == nil {
		t.Fatal("reused previous native home")
	}
	if err := prepareReadOnlyCodexHome(filepath.Join(t.TempDir(), "windows"), shared, "windows"); err == nil {
		t.Fatal("unsupported Windows sandbox accepted")
	}
	before, err := os.ReadFile(filepath.Join(shared, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "danger-full-access") {
		t.Fatal("modified shared settings")
	}
}

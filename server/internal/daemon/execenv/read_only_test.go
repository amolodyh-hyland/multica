package execenv

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
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
	features := config["features"].(map[string]any)
	for _, feature := range []string{"shell_tool", "unified_exec", "apps", "plugins", "remote_plugin", "plugin_sharing", "skill_mcp_dependency_install", "tool_suggest", "in_app_browser", "browser_use", "browser_use_full_cdp_access", "browser_use_external", "computer_use", "image_generation", "hooks", "multi_agent", "code_mode_host", "shell_snapshot", "mentions_v2", "guardian_approval", "goals", "tool_call_mcp_elicitation", "auth_elicitation", "workspace_dependencies", "remote_compaction_v2"} {
		if features[feature] != false {
			t.Fatalf("native home leaves startup feature enabled/defaulted: %s", feature)
		}
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

func TestReadOnlyCodexDisablesWarmupsBeforeFirstRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture; native Windows is unsupported")
	}
	home := filepath.Join(t.TempDir(), "native")
	if err := prepareReadOnlyCodexHome(home, t.TempDir(), runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "startup-order")
	fake := filepath.Join(t.TempDir(), "codex")
	// This native fixture evaluates startup defaults before reading any RPC.
	script := `#!/bin/sh
for feature in shell_tool unified_exec apps plugins remote_plugin plugin_sharing skill_mcp_dependency_install tool_suggest in_app_browser browser_use browser_use_full_cdp_access browser_use_external computer_use image_generation hooks multi_agent code_mode_host shell_snapshot mentions_v2 guardian_approval goals tool_call_mcp_elicitation auth_elicitation workspace_dependencies remote_compaction_v2 remote_control realtime_conversation; do
  found=false
  for arg in "$@"; do
    if [ "$arg" = "features.$feature=false" ]; then found=true; fi
  done
  if [ "$found" != true ]; then printf 'warmup-before-rpc\n' > "$STARTUP_MARKER"; exit 1; fi
  found=false
  while IFS= read -r line; do
    if [ "$line" = "$feature = false" ]; then found=true; fi
  done < "$CODEX_HOME/config.toml"
  if [ "$found" != true ]; then printf 'warmup-before-rpc\n' > "$STARTUP_MARKER"; exit 1; fi
done
printf 'disabled-before-rpc\n' > "$STARTUP_MARKER"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$STARTUP_MARKER"
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' '{"id":1,"result":{}}' ;;
    *'"method":"config/read"'*) printf '%s\n' '{"id":2,"result":{"config":{"mcp_servers":{},"sandbox_mode":"read-only","approval_policy":"never","web_search":"disabled","features":{"shell_tool":false,"unified_exec":false}},"layers":[{"name":{"type":"user"},"version":"fixture","config":{}}]}}' ;;
  esac
done
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// Execute only the synthetic native fixture with the actual emitted argv.
	// This verifies local startup flags; cloud policy can still re-enable warmups.
	cmd := exec.Command(fake, agent.ReadOnlyCodexArgs()...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home, "STARTUP_MARKER="+marker)
	cmd.Stdin = strings.NewReader(`{"id":1,"method":"initialize"}` + "\n" + `{"method":"initialized"}` + "\n" + `{"id":2,"method":"config/read"}` + "\n")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("startup fixture: %v %s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 || lines[0] != "disabled-before-rpc" || !strings.Contains(lines[1], `"method":"initialize"`) || !strings.Contains(lines[2], `"method":"initialized"`) || !strings.Contains(lines[3], `"method":"config/read"`) {
		t.Fatalf("unsafe startup ordering: %s", data)
	}
}

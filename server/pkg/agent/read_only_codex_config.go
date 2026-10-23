package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// An empty TOML overlay does not delete inherited MCP entries. Audit every
// file layer conservatively (even untrusted project layers and inactive
// profiles), rather than trying to override managed policy or reproduce
// Codex's trust/recursive merge semantics. No harness process is started here.
func auditReadOnlyCodexConfig(home, cwd string, readFile func(string) ([]byte, error)) error {
	if !filepath.IsAbs(home) || !filepath.IsAbs(cwd) {
		return fmt.Errorf("read-only Codex requires absolute isolated CODEX_HOME and working directory")
	}
	paths := []string{
		"/etc/codex/config.toml", "/etc/codex/managed_config.toml", "/etc/codex/requirements.toml",
		filepath.Join(home, "config.toml"), filepath.Join(home, "managed_config.toml"),
		filepath.Join(cwd, "config.toml"),
	}
	// Include all ancestors regardless of git markers or trust. A system
	// project_root_markers override must not hide a higher project layer.
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		paths = append(paths, filepath.Join(dir, ".codex", "config.toml"))
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for _, path := range paths {
		data, err := readFile(path)
		if errors.Is(err, os.ErrNotExist) {
			// The isolated user config is required; never fall back to a real home.
			if path == filepath.Join(home, "config.toml") {
				return fmt.Errorf("read-only Codex isolated config is missing")
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot audit read-only Codex config layer %s: %w", path, err)
		}
		var config map[string]any
		if err := toml.Unmarshal(data, &config); err != nil {
			// Do not include TOML diagnostics: configuration may contain secrets.
			return fmt.Errorf("cannot audit invalid read-only Codex config layer %s", path)
		}
		if codexConfigHasMCP(config) {
			return fmt.Errorf("read-only Codex refuses inherited MCP or plugin configuration in %s; managed settings will not be overridden", path)
		}
	}
	return nil
}

func codexConfigHasMCP(config map[string]any) bool {
	for key, value := range config {
		// A selected external profile introduces another configuration file.
		// The bundle never selects profiles; refuse inherited selection.
		if key == "profile" && value != "" {
			return true
		}
		if key == "mcp_servers" || key == "plugins" || key == "apps" {
			table, ok := value.(map[string]any)
			if !ok || len(table) != 0 {
				return true
			}
		}
		if table, ok := value.(map[string]any); ok && codexConfigHasMCP(table) {
			return true
		}
	}
	return false
}

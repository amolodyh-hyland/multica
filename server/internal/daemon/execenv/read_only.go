package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const readOnlyCodexConfig = `sandbox_mode = "read-only"
approval_policy = "never"
web_search = "disabled"

[features]
shell_tool = false
unified_exec = false

[mcp_servers]
`

func prepareReadOnlyCodexHome(home, sharedHome, goos string) error {
	if resolveGOOS(goos) == "windows" {
		return fmt.Errorf("restricted bundle does not support the unverified Windows sandbox")
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		return fmt.Errorf("restricted native home must be fresh: %w", err)
	}
	if err := ensureSymlink(filepath.Join(sharedHome, "auth.json"), filepath.Join(home, "auth.json")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(readOnlyCodexConfig), 0o600)
}

func hydrateReadOnlyAwareCodexSkills(home string, readOnly bool, skills []SkillContextForEnv, disabled []RuntimeSkillRefForEnv, logger *slog.Logger) error {
	if readOnly {
		return nil
	}
	return hydrateCodexSkills(home, skills, disabled, logger)
}

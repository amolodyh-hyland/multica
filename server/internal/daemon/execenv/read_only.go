package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func readOnlyCodexHomeConfig() string {
	var config strings.Builder
	config.WriteString("sandbox_mode = \"read-only\"\napproval_policy = \"never\"\nweb_search = \"disabled\"\n\n[features]\n")
	for _, feature := range agent.ReadOnlyCodexDisabledFeatures() {
		fmt.Fprintf(&config, "%s = false\n", feature)
	}
	config.WriteString("\n[mcp_servers]\n")
	return config.String()
}

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
	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(readOnlyCodexHomeConfig()), 0o600)
}

func hydrateReadOnlyAwareCodexSkills(home string, readOnly bool, skills []SkillContextForEnv, disabled []RuntimeSkillRefForEnv, logger *slog.Logger) error {
	if readOnly {
		return nil
	}
	return hydrateCodexSkills(home, skills, disabled, logger)
}

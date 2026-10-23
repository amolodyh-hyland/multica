package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const MinReadOnlyClaudeVersion = "2.1.259"
const MinReadOnlyCodexVersion = "0.144.6"

func validateReadOnlyExecution(cfg Config, opts ExecOptions) error {
	if len(cfg.LaunchPrefix) != 0 || len(opts.ExtraArgs) != 0 || len(opts.CustomArgs) != 0 || opts.ResumeSessionID != "" || opts.ResumeExpected {
		return fmt.Errorf("read-only bundle does not support launch overrides or conversation continuation")
	}
	if cfg.provider == "codex" {
		home, err := filepath.EvalSymlinks(cfg.Env["CODEX_HOME"])
		if err != nil {
			return fmt.Errorf("resolve isolated Codex home: %w", err)
		}
		cwd, err := filepath.EvalSymlinks(opts.Cwd)
		if err != nil {
			return fmt.Errorf("resolve isolated Codex working directory: %w", err)
		}
		if err := auditReadOnlyCodexConfig(home, cwd, os.ReadFile); err != nil {
			return err
		}
	}
	if !SupportsNativeReadOnly(cfg.provider, cfg.CLIVersion, runtime.GOOS) {
		return fmt.Errorf("unsupported read-only native harness/version/platform: %s %s %s", cfg.provider, cfg.CLIVersion, runtime.GOOS)
	}

	return nil
}

func readOnlyCodexConfig() map[string]any {
	return map[string]any{
		"sandbox_mode": "read-only", "approval_policy": "never", "web_search": "disabled",
		"mcp_servers": map[string]any{}, "features.shell_tool": false, "features.unified_exec": false,
	}
}

func readOnlyCodexArgs() []string {
	return []string{"app-server", "--listen", "stdio://", "-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`, "-c", "mcp_servers={}", "-c", "features.shell_tool=false", "-c", "features.unified_exec=false"}
}

func applyReadOnlyCodexTurn(params map[string]any, opts ExecOptions) {
	if !opts.NativeReadOnly {
		return
	}
	params["approvalPolicy"] = "never"
	params["sandboxPolicy"] = map[string]any{"type": "readOnly", "networkAccess": false, "access": map[string]any{
		"type": "restricted", "includePlatformDefaults": true, "readableRoots": []string{opts.Cwd},
	}}
}

func SupportsNativeReadOnly(provider, version, goos string) bool {
	switch provider {
	case "claude":
		return codexVersionAtLeast(version, MinReadOnlyClaudeVersion)
	case "codex":
		return (goos == "linux" || goos == "darwin") && codexVersionAtLeast(version, MinReadOnlyCodexVersion)
	default:
		return false
	}
}

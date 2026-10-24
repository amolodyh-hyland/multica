package agent

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
)

const MinReadOnlyClaudeVersion = "2.1.259"
const MinReadOnlyCodexVersion = "0.144.6"

var readOnlyStockCodexVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

const ReadOnlyCodexCloudRiskWarning = "Stock Codex: cloud-delivered managed configuration for a company (enterprise or business) account cannot be detected before launch; plugin warmups might start before effective-config attestation refuses. This bundle accepts that residual risk. No thread or task starts until attestation passes."

// Audited against Codex rust-v0.144.6. Keep startup, thread config and
// attestation aligned: config/read does not materialize native defaults.
var readOnlyCodexDisabledFeatures = [...]string{
	"shell_tool", "unified_exec", "apps", "plugins", "remote_plugin",
	"plugin_sharing", "skill_mcp_dependency_install", "tool_suggest",
	"in_app_browser", "browser_use", "browser_use_full_cdp_access", "browser_use_external",
	"computer_use", "image_generation", "hooks", "multi_agent", "code_mode_host",
	"shell_snapshot", "mentions_v2", "guardian_approval", "goals",
	"tool_call_mcp_elicitation", "auth_elicitation", "workspace_dependencies", "remote_compaction_v2", "remote_control", "realtime_conversation",
}

// ReadOnlyCodexDisabledFeatures returns the startup policy for isolated homes.
func ReadOnlyCodexDisabledFeatures() []string {
	return append([]string(nil), readOnlyCodexDisabledFeatures[:]...)
}

func validateReadOnlyExecution(cfg Config, opts ExecOptions) error {
	if len(cfg.LaunchPrefix) != 0 || len(opts.ExtraArgs) != 0 || len(opts.CustomArgs) != 0 || opts.ResumeSessionID != "" || opts.ResumeExpected {
		return fmt.Errorf("read-only bundle does not support launch overrides or conversation continuation")
	}
	if !SupportsNativeReadOnly(cfg.provider, cfg.CLIVersion, runtime.GOOS) {
		return fmt.Errorf("unsupported read-only native harness/version/platform: %s %s %s", cfg.provider, cfg.CLIVersion, runtime.GOOS)
	}
	return nil
}

func readOnlyCodexConfig() map[string]any {
	config := map[string]any{
		"sandbox_mode": "read-only", "approval_policy": "never", "web_search": "disabled",
		"mcp_servers": map[string]any{},
	}
	for _, feature := range readOnlyCodexDisabledFeatures {
		config["features."+feature] = false
	}
	return config
}

// ReadOnlyCodexArgs disables tool features before stock app-server startup.
func ReadOnlyCodexArgs() []string {
	args := []string{"app-server", "--strict-config", "--listen", "stdio://", "-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`, "-c", "mcp_servers={}"}
	for _, feature := range readOnlyCodexDisabledFeatures {
		args = append(args, "-c", "features."+feature+"=false")
	}
	return args
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
		version = strings.TrimPrefix(strings.TrimSpace(version), "codex-cli ")
		return (goos == "darwin" || goos == "linux") && readOnlyStockCodexVersion.MatchString(version) &&
			codexVersionAtLeast(version, MinReadOnlyCodexVersion)
	default:
		return false
	}
}

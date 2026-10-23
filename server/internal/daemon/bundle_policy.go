package daemon

import (
	"encoding/json"
	"fmt"
)

func bundleProviderSupported(provider string) bool {
	return provider == "claude" || provider == "codex"
}

func (d *Daemon) validateBundleTask(task Task, provider string) error {
	deny := func(reason string) error { return fmt.Errorf("read-only bundle: %s", reason) }
	if !bundleProviderSupported(provider) {
		return deny("unsupported native harness")
	}
	if _, custom := d.customProfileLaunchForRuntime(task.RuntimeID); custom {
		return deny("custom runtime commands are unsupported")
	}
	if task.WorkspaceID != d.cfg.BundleWorkspaceID || task.Agent == nil || task.DelegationID == "" {
		return deny("requires one approved workspace and a direct delegation profile")
	}
	if task.IssueID != "" || task.ChatSessionID != "" || task.IsLeaderTask || task.AutopilotID != "" || task.PriorSessionID != "" || task.PriorWorkDir != "" {
		return deny("only fresh brief-only delegations are supported")
	}
	if len(task.Repos) != 0 || len(task.ProjectResources) != 0 || len(task.RemoteMCPConnections) != 0 || len(task.PluginHookTools) != 0 {
		return deny("repository bindings and remote tools are unsupported")
	}
	a := task.Agent
	if len(a.CustomArgs) != 0 || len(a.CustomEnv) != 0 || len(defaultArgsForProvider(d.cfg, provider)) != 0 || len(a.Skills) != 0 || len(a.SkillRefs) != 0 {
		return deny("launch overrides, environment overrides and skills are unsupported")
	}
	if len(a.McpConfig) != 0 && string(a.McpConfig) != "null" {
		var mcp struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(a.McpConfig, &mcp) != nil || json.Unmarshal(a.McpConfig, &fields) != nil || len(mcp.Servers) != 0 || len(fields) > 1 || (len(fields) == 1 && fields["mcpServers"] == nil) {
			return deny("MCP tools are unsupported")
		}
	}
	var config struct {
		LocalDirectoryMode string `json:"local_directory_mode"`
		PermissionMode     string `json:"permission_mode"`
	}
	if err := json.Unmarshal(a.RuntimeConfig, &config); err != nil {
		return deny("invalid per-profile runtime configuration")
	}
	if config.LocalDirectoryMode != "none" || config.PermissionMode != "read_only" {
		return deny("profile must select local_directory_mode=none and permission_mode=read_only")
	}
	return nil
}

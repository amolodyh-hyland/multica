package agent

import (
	"context"
	"encoding/json"
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
		if key == "profile" && value != nil && value != "" {
			return true
		}
		if key == "mcp_servers" || key == "plugins" || key == "apps" || key == "remote_plugin" {
			if key != "mcp_servers" && (value == nil || value == false) {
				continue
			}
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

// config/read uses the app-server's config manager: its response includes cloud
// fragments, macOS managed preferences and exact managed requirements. Inspect
// it before thread/start, which is the boundary that starts MCP connections.
func (c *codexClient) attestReadOnlyCodexConfig(ctx context.Context, cwd string) error {
	result, err := c.request(ctx, "config/read", map[string]any{"includeLayers": true, "cwd": cwd})
	if err != nil {
		// RPC errors can contain configuration values. Persist only the refusal.
		return fmt.Errorf("read-only Codex effective configuration unavailable; refusing thread startup")
	}
	if err := validateReadOnlyCodexEffectiveConfig(result); err != nil {
		return err
	}
	// config/read omits normalized feature requirements; attest them separately.
	result, err = c.request(ctx, "configRequirements/read", map[string]any{})
	if err != nil {
		return fmt.Errorf("read-only Codex managed requirements unavailable; refusing thread startup")
	}
	return validateReadOnlyCodexRequirements(result)
}

func validateReadOnlyCodexEffectiveConfig(result json.RawMessage) error {
	refuse := func() error {
		return fmt.Errorf("read-only Codex effective configuration is unsafe or incomplete; refusing thread startup without overriding managed settings")
	}
	var response struct {
		Config map[string]any `json:"config"`
		Layers []struct {
			Name struct {
				Type string `json:"type"`
			} `json:"name"`
			Version string         `json:"version"`
			Config  map[string]any `json:"config"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.Config == nil || len(response.Layers) == 0 {
		return refuse()
	}
	config := response.Config
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok || len(servers) != 0 || codexConfigHasMCP(config) {
		return refuse()
	}
	features, ok := config["features"].(map[string]any)
	if !ok ||
		config["sandbox_mode"] != "read-only" || config["approval_policy"] != "never" || config["web_search"] != "disabled" {
		return refuse()
	}
	for _, feature := range readOnlyCodexDisabledFeatures {
		if features[feature] != false {
			return refuse()
		}
	}
	for _, layer := range response.Layers {
		if layer.Config == nil || layer.Version == "" || codexConfigHasMCP(layer.Config) {
			return refuse()
		}
		switch layer.Name.Type {
		case "mdm", "system", "enterpriseManaged", "user", "project", "sessionFlags", "legacyManagedConfigTomlFromFile", "legacyManagedConfigTomlFromMdm", "packagedDefaults":
		default:
			return refuse()
		}
	}
	return nil
}

func validateReadOnlyCodexRequirements(result json.RawMessage) error {
	refuse := func() error {
		return fmt.Errorf("read-only Codex managed requirements are unsafe or incomplete; refusing thread startup")
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(result, &response) != nil || response["requirements"] == nil {
		return refuse()
	}
	if string(response["requirements"]) == "null" {
		return nil
	}
	var requirements struct {
		Features         map[string]bool `json:"featureRequirements"`
		ApprovalPolicies []string        `json:"allowedApprovalPolicies"`
		SandboxModes     []string        `json:"allowedSandboxModes"`
		WebSearchModes   []string        `json:"allowedWebSearchModes"`
	}
	if json.Unmarshal(response["requirements"], &requirements) != nil {
		return refuse()
	}
	for _, feature := range readOnlyCodexDisabledFeatures {
		if requirements.Features[feature] {
			return refuse()
		}
	}
	allows := func(values []string, required string) bool {
		if values == nil {
			return true
		}
		for _, value := range values {
			if value == required {
				return true
			}
		}
		return false
	}
	if !allows(requirements.ApprovalPolicies, "never") || !allows(requirements.SandboxModes, "read-only") || !allows(requirements.WebSearchModes, "disabled") {
		return refuse()
	}
	return nil
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	zcodePersonalConfigEnv   = "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"
	zcodeProvidersTempPrefix = "multica-zcode-providers-"
)

type zcodeDesktopProvider struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Enabled *bool             `json:"enabled"`
	Headers map[string]string `json:"headers"`
	Options struct {
		APIKey  string            `json:"apiKey"`
		BaseURL string            `json:"baseURL"`
		Headers map[string]string `json:"headers"`
	} `json:"options"`
	Models map[string]struct {
		Limit struct {
			Context int64 `json:"context"`
		} `json:"limit"`
	} `json:"models"`
}

func zcodeEnvValue(env []string, key string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			return strings.TrimPrefix(entry, key+"=")
		}
	}
	return ""
}

func readZcodeConfig(path string, into any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("ZCode provider config exceeds 2 MiB")
	}
	return json.Unmarshal(data, into)
}

// prepareZcodeRuntimeEnv adapts Desktop's enabled API-key connections into a
// private, process-owned native provider repository snapshot under the daemon's
// profile directory (or the process temp dir for callers without a profile).
// It reads Desktop's enabled connections to build that snapshot, but never
// edits Desktop's repositories or converts
// Start Plan identity tokens into API keys. Explicit native repositories remain
// authoritative. A lease protects the snapshot until session cleanup.
func prepareZcodeRuntimeEnv(ctx context.Context, executable string, env []string) ([]string, func(), error) {
	noop := func() {}
	if zcodeEnvValue(env, zcodePersonalConfigEnv) != "" {
		return zcodeRuntimeEnv(executable, env), noop, nil
	}
	builtin := filepath.Join(filepath.Dir(executable), "..", "config", "provider", "zcode-builtin.json")
	if !isNodeScriptPath(executable) {
		return env, noop, nil
	}
	if info, err := os.Stat(builtin); err != nil || !info.Mode().IsRegular() {
		return env, noop, nil
	}
	base := zcodeEnvValue(env, "ZCODE_DATA_BASE_DIR")
	if base == "" {
		var err error
		base, err = os.UserHomeDir()
		if err != nil {
			return nil, noop, err
		}
	}
	root := filepath.Join(base, ".zcode", "v2")
	var desktop struct {
		Providers map[string]zcodeDesktopProvider `json:"provider"`
	}
	err := readZcodeConfig(filepath.Join(root, "config.json"), &desktop)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, noop, fmt.Errorf("read ZCode Desktop connections: %w", err)
	}
	native := map[string]any{"schemaVersion": 1, "config": map[string]any{}}
	err = readZcodeConfig(filepath.Join(root, "provider_config.json"), &native)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, noop, fmt.Errorf("read ZCode native provider repository: %w", err)
	}
	config, ok := native["config"].(map[string]any)
	if !ok {
		return nil, noop, fmt.Errorf("ZCode native provider repository has no config object")
	}
	group := func(key string) map[string]any {
		if value, ok := config[key].(map[string]any); ok {
			return value
		}
		value := map[string]any{}
		config[key] = value
		return value
	}
	providers, models := group("providerConfigRules"), group("modelConfigRules")
	providerRules, _ := providers["providerRules"].([]any)
	modelRules, _ := models["providerModelRules"].([]any)
	order, _ := config["providerOrder"].([]any)
	if providerRules == nil {
		providerRules = []any{}
	}
	if modelRules == nil {
		modelRules = []any{}
	}
	if order == nil {
		order = []any{}
	}
	sourceIDs := make([]string, 0, len(desktop.Providers))
	for id := range desktop.Providers {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	for _, sourceID := range sourceIDs {
		provider := desktop.Providers[sourceID]
		if provider.Enabled == nil || !*provider.Enabled || strings.TrimSpace(provider.Options.APIKey) == "" || len(provider.Models) == 0 {
			continue
		}
		if strings.HasPrefix(sourceID, "builtin:") && strings.Contains(sourceID, "start-plan") {
			continue
		}
		providerID := "multica-desktop:" + sourceID
		ids := make([]string, 0, len(provider.Models))
		for id := range provider.Models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		apiType := "openai-chat-completions"
		switch provider.Kind {
		case "anthropic":
			apiType = "anthropic-messages"
		case "openai":
			apiType = "openai-responses"
		}
		api := map[string]any{"type": apiType}
		if provider.Options.BaseURL != "" {
			api["baseUrl"] = provider.Options.BaseURL
		}
		headers := map[string]string{}
		for key, value := range provider.Headers {
			headers[key] = value
		}
		for key, value := range provider.Options.Headers {
			headers[key] = value
		}
		if len(headers) > 0 {
			api["headers"] = headers
		}
		rule := map[string]any{"providerId": providerID, "config": map[string]any{
			"group": "standard-personal", "access": map[string]any{"type": "api-key", "apiKey": provider.Options.APIKey},
			"api": api, "personalModelIds": ids, "modelOrder": ids,
		}}
		if provider.Name != "" {
			rule["providerName"] = provider.Name
		}
		providerRules = append(providerRules, rule)
		order = append(order, providerID)
		for _, id := range ids {
			modelConfig := map[string]any{"enabled": true}
			if contextWindow := provider.Models[id].Limit.Context; contextWindow > 0 {
				modelConfig["properties"] = map[string]any{"contextWindow": contextWindow}
			}
			modelRules = append(modelRules, map[string]any{"providerId": providerID, "modelId": id, "config": modelConfig})
		}
	}
	providers["providerRules"], models["providerModelRules"], config["providerOrder"] = providerRules, modelRules, order
	if _, present := models["manualProviderModelRules"]; !present {
		models["manualProviderModelRules"] = []any{}
	}
	data, err := json.Marshal(native)
	if err != nil {
		return nil, noop, fmt.Errorf("encode process-owned ZCode provider repository: %w", err)
	}
	file, cleanup, err := writeZcodeProviderSnapshot(ctx, data)
	if err != nil {
		return nil, noop, err
	}
	env = zcodeRuntimeEnv(executable, env)
	return append(env, zcodePersonalConfigEnv+"="+file), cleanup, nil
}

type zcodeSnapshotProfileKey struct{}

// WithZcodeProviderSnapshotProfile scopes snapshots and crash cleanup to one daemon profile.
func WithZcodeProviderSnapshotProfile(ctx context.Context, profileDir string) context.Context {
	return context.WithValue(ctx, zcodeSnapshotProfileKey{}, profileDir)
}

func zcodeProviderSnapshotRoot(profileDir string) string {
	return filepath.Join(profileDir, "zcode-provider-snapshots")
}

func writeZcodeProviderSnapshot(ctx context.Context, data []byte) (string, func(), error) {
	noop := func() {}
	root := os.TempDir()
	if profileDir, _ := ctx.Value(zcodeSnapshotProfileKey{}).(string); profileDir != "" {
		root = zcodeProviderSnapshotRoot(profileDir)
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", noop, err
		}
	}
	temp, err := os.MkdirTemp(root, ".preparing-")
	if err != nil {
		return "", noop, err
	}
	leasePath := filepath.Join(temp, "lease")
	if err := os.WriteFile(leasePath, nil, 0o600); err != nil {
		_ = os.RemoveAll(temp)
		return "", noop, err
	}
	lease, locked, err := tryLockPiSessionFile(leasePath)
	if err != nil || !locked {
		_ = os.RemoveAll(temp)
		if err == nil {
			err = errors.New("ZCode snapshot lease is already held")
		}
		return "", noop, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = os.RemoveAll(temp)
			releasePiSessionFileLock(lease)
		})
	}
	// Publish only after acquiring the lifetime lease; startup ignores staging directories.
	published := filepath.Join(root, zcodeProvidersTempPrefix+strings.TrimPrefix(filepath.Base(temp), ".preparing-"))
	if err := os.Rename(temp, published); err != nil {
		cleanup()
		return "", noop, err
	}
	temp = published
	file := filepath.Join(temp, "providers.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		cleanup()
		return "", noop, err
	}
	return file, cleanup, nil
}

// CleanupStaleZcodeProviderSnapshots removes only unlocked snapshots in this profile.
// Unknown legacy snapshots in the shared temp directory are deliberately left alone.
func CleanupStaleZcodeProviderSnapshots(profileDir string) int {
	if profileDir == "" {
		return 0
	}
	root := zcodeProviderSnapshotRoot(profileDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), zcodeProvidersTempPrefix) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		leasePath := filepath.Join(dir, "lease")
		if info, err := os.Lstat(leasePath); err != nil || !info.Mode().IsRegular() {
			continue
		}
		lease, locked, err := tryLockPiSessionFile(leasePath)
		if err != nil || !locked {
			continue
		}
		if err := os.RemoveAll(dir); err == nil {
			removed++
		}
		releasePiSessionFileLock(lease)
	}
	return removed
}

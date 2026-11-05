package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZcodeDesktopProviderRepositoryIsPrivateAndDoesNotModifyUserConfig(t *testing.T) {
	base := t.TempDir()
	executable := filepath.Join(base, "ZCode.app", "Contents", "Resources", "glm", "zcode.cjs")
	builtin := filepath.Join(filepath.Dir(executable), "..", "config", "provider", "zcode-builtin.json")
	if err := os.MkdirAll(filepath.Dir(builtin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(builtin, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(base, ".zcode", "v2")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := []byte(`{"provider":{
		"builtin:bigmodel-coding-plan":{"enabled":true,"kind":"anthropic","options":{"apiKey":"test-secret","baseURL":"https://example.invalid"},"models":{"m":{"limit":{"context":12000}}}},
		"builtin:bigmodel-start-plan":{"enabled":true,"options":{"apiKey":"identity-token"},"models":{"m":{}}},
		"disabled":{"enabled":false,"options":{"apiKey":"disabled-secret"},"models":{"m":{}}}
	}}`)
	file := filepath.Join(configRoot, "config.json")
	if err := os.WriteFile(file, source, 0o600); err != nil {
		t.Fatal(err)
	}
	native := []byte(`{"schemaVersion":1,"config":{"providerConfigRules":{"providerRules":[{"providerId":"existing"}]},"modelConfigRules":{"providerModelRules":[]}}}`)
	nativePath := filepath.Join(configRoot, "provider_config.json")
	if err := os.WriteFile(nativePath, native, 0o600); err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(base, "profile-a")
	ctx := WithZcodeProviderSnapshotProfile(context.Background(), profileDir)
	env, cleanup, err := prepareZcodeRuntimeEnv(ctx, executable, []string{"ZCODE_DATA_BASE_DIR=" + base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	snapshot := zcodeEnvValue(env, zcodePersonalConfigEnv)
	if filepath.Dir(filepath.Dir(snapshot)) != zcodeProviderSnapshotRoot(profileDir) {
		t.Fatalf("snapshot is outside its daemon profile: %s", snapshot)
	}
	if snapshot == "" || snapshot == nativePath {
		t.Fatal("expected a separate process-owned snapshot")
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("provider snapshot mode = %v", info.Mode())
	}
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Config struct {
			Providers struct {
				Rules []json.RawMessage `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Config.Providers.Rules) != 2 || !strings.Contains(string(data), "test-secret") || strings.Contains(string(data), "identity-token") || strings.Contains(string(data), "disabled-secret") {
		t.Fatal("snapshot must preserve native providers and import only enabled API connections")
	}
	if after, _ := os.ReadFile(file); string(after) != string(source) {
		t.Fatal("Desktop source was modified")
	}
	if after, _ := os.ReadFile(nativePath); string(after) != string(native) {
		t.Fatal("native repository was modified")
	}
	cleanup()
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatal("provider snapshot was not removed")
	}
}

func TestZcodeExplicitProviderRepositoryRemainsAuthoritative(t *testing.T) {
	env := []string{zcodePersonalConfigEnv + "=/explicit/providers.json", "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE=/explicit/builtin.json"}
	got, cleanup, err := prepareZcodeRuntimeEnv(context.Background(), "/app/zcode.cjs", env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Join(got, "\n") != strings.Join(env, "\n") {
		t.Fatal("explicit repository was replaced")
	}
}

func TestCleanupStaleZcodeProviderSnapshotsPreservesLiveProfiles(t *testing.T) {
	profileA, profileB := t.TempDir(), t.TempDir()
	data := []byte(`{"apiKey":"fixture-secret"}`)
	live := make([]string, 0, 2)
	for _, profile := range []string{profileA, profileB} {
		ctx := WithZcodeProviderSnapshotProfile(context.Background(), profile)
		file, cleanup, err := writeZcodeProviderSnapshot(ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		live = append(live, file)
	}
	orphan := func(root, prefix string) string {
		t.Helper()
		dir, err := os.MkdirTemp(root, prefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lease"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "providers.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	abandonedA := orphan(zcodeProviderSnapshotRoot(profileA), zcodeProvidersTempPrefix)
	abandonedB := orphan(zcodeProviderSnapshotRoot(profileB), zcodeProvidersTempPrefix)
	staging := orphan(zcodeProviderSnapshotRoot(profileA), ".preparing-")
	legacy := orphan(t.TempDir(), zcodeProvidersTempPrefix)
	if removed := CleanupStaleZcodeProviderSnapshots(profileA); removed != 1 {
		t.Fatalf("profile A removed %d snapshots, want only its abandoned snapshot", removed)
	}
	if _, err := os.Stat(abandonedA); !os.IsNotExist(err) {
		t.Fatalf("abandoned profile A snapshot remains: %v", err)
	}
	for _, file := range append(live, filepath.Join(abandonedB, "providers.json"), filepath.Join(staging, "providers.json"), filepath.Join(legacy, "providers.json")) {
		if got, err := os.ReadFile(file); err != nil || string(got) != string(data) {
			t.Fatalf("cleanup changed another live/profile/staging snapshot %s: %v", file, err)
		}
	}
	if removed := CleanupStaleZcodeProviderSnapshots(profileB); removed != 1 {
		t.Fatalf("profile B removed %d snapshots, want only its abandoned snapshot", removed)
	}
	for _, file := range live {
		lease, locked, err := tryLockPiSessionFile(filepath.Join(filepath.Dir(file), "lease"))
		if locked {
			releasePiSessionFileLock(lease)
		}
		if err != nil || locked {
			t.Fatalf("live snapshot lost its lease: locked=%v err=%v", locked, err)
		}
		if got, err := os.ReadFile(file); err != nil || string(got) != string(data) {
			t.Fatalf("live snapshot was removed: %v", err)
		}
	}
	if removed := CleanupStaleZcodeProviderSnapshots(""); removed != 0 {
		t.Fatalf("unscoped cleanup removed %d snapshots", removed)
	}
}

func TestRequireNodeForZcodeScript(t *testing.T) {
	if err := requireNodeForZcodeScript("/usr/local/bin/zcode"); err != nil {
		t.Fatalf("native path should pass: %v", err)
	}
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	t.Setenv("PATH", t.TempDir()) // empty of node
	err := requireNodeForZcodeScript("/Applications/ZCode.app/Contents/Resources/glm/zcode.cjs")
	if err == nil {
		t.Fatal("expected error when node is missing")
	}
	if !strings.Contains(err.Error(), "Node.js") || !strings.Contains(err.Error(), "node was not found") {
		t.Fatalf("error = %q, want clear Node.js message", err)
	}
}

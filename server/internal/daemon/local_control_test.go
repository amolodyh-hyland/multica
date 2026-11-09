package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestLocalControlToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	for _, token := range []string{"", "secret", strings.Repeat("a", 64), strings.Repeat("a", 64) + "\n", strings.Repeat("a", 64) + "\r\n", strings.Repeat("a", 66), " " + strings.Repeat("a", 64)} {
		if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
		makeControlTokenPrivate(t, path)
		got, err := loadLocalControlToken(path)
		valid := token == strings.Repeat("a", 64) || token == strings.Repeat("a", 64)+"\n" || token == strings.Repeat("a", 64)+"\r\n"
		if valid != (err == nil) || valid && got != strings.TrimSpace(token) {
			t.Fatalf("token length %d: accepted=%v", len(token), err == nil)
		}
	}
	if _, err := loadLocalControlToken(""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if _, err := loadLocalControlToken(path); err == nil {
			t.Fatal("accepted invalid control file")
		}
	}
}

func TestIsolatedDaemonIdentity(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		value, profile, id string
		restricted, want   bool
	}{
		{"true", "standard", id, false, true},
		{"false", "", "", false, false},
		{"", "", "", true, false},
	} {
		got, err := isolatedDaemonIdentity(tc.value, tc.profile, tc.id, tc.restricted)
		if err != nil || got != tc.want {
			t.Fatalf("isolation %v %v", got, err)
		}
	}
	for _, tc := range []struct {
		value, profile, id string
		restricted         bool
	}{
		{"invalid", "standard", id, false}, {"true", "", id, false}, {"true", "standard", "", false},
		{"true", "standard", "not-uuid", false}, {"true", "standard", "00000000-0000-0000-0000-000000000000", false},
		{"true", "restricted", id, true},
	} {
		if _, err := isolatedDaemonIdentity(tc.value, tc.profile, tc.id, tc.restricted); err == nil {
			t.Fatal("accepted invalid isolated identity")
		}
	}
}

func TestIsolatedStandardConfigNeverMigratesIdentities(t *testing.T) {
	previousProbe := probeAgentCLIs
	probeAgentCLIs = func() map[string]AgentEntry {
		return map[string]AgentEntry{"claude": {Path: "fixture-native-provider"}}
	}
	t.Cleanup(func() { probeAgentCLIs = previousProbe })
	home := t.TempDir()
	redirectTestHome(t, home)
	t.Setenv("MULTICA_DAEMON_ISOLATED", "true")
	t.Setenv("MULTICA_DAEMON_CONTROL_TOKEN_FILE", "")
	t.Setenv("MULTICA_DAEMON_READ_ONLY", "false")
	const id = "22222222-2222-4222-8222-222222222222"
	root := filepath.Join(home, ".multica")
	if err := os.MkdirAll(filepath.Join(root, "profiles", "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "daemon.id"), filepath.Join(root, "profiles", "old", "daemon.id")} {
		if err := os.WriteFile(path, []byte("11111111-1111-1111-1111-111111111111"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{}, "standard"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(Overrides{Profile: "standard", DaemonID: id, WorkspacesRoot: t.TempDir()})
	if err != nil || cfg.DaemonID != id || len(cfg.LegacyDaemonIDs) != 0 || cfg.BundleReadOnly {
		t.Fatalf("standard identity: id=%s aliases=%v restricted=%v err=%v", cfg.DaemonID, cfg.LegacyDaemonIDs, cfg.BundleReadOnly, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "daemon.id")); err != nil || string(data) != "11111111-1111-1111-1111-111111111111" {
		t.Fatal("changed machine identity")
	}
	for _, name := range []string{"daemon.id", "bundle-daemon.id"} {
		if _, err := os.Stat(filepath.Join(root, "profiles", "standard", name)); !os.IsNotExist(err) {
			t.Fatal("created migration identity")
		}
	}
}

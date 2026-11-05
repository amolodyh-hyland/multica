package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func abandonedZcodeSnapshot(t *testing.T, profile string) string {
	t.Helper()
	profileDir, err := cli.ProfileDir(profile)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(profileDir, "zcode-provider-snapshots", "multica-zcode-providers-fixture")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lease"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "providers.json")
	if err := os.WriteFile(file, []byte(`{"apiKey":"fixture-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestZcodeProviderSnapshotStartupOnlyCleansOwnProfile(t *testing.T) {
	profileA, profileB := t.Name()+"-a", t.Name()+"-b"
	own := abandonedZcodeSnapshot(t, profileA)
	other := abandonedZcodeSnapshot(t, profileB)
	d := &Daemon{cfg: Config{Profile: profileA}, logger: slog.Default()}
	if _, err := d.prepareZcodeProviderSnapshots(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatalf("own abandoned snapshot remains: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("starting profile A removed profile B snapshot: %v", err)
	}
}

func TestBundleStartupDoesNotCleanZcodeProviderSnapshots(t *testing.T) {
	profile := t.Name()
	file := abandonedZcodeSnapshot(t, profile)
	shared, err := os.MkdirTemp("", "multica-zcode-providers-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shared) })
	sharedFile := filepath.Join(shared, "providers.json")
	data := []byte(`{"apiKey":"fixture-secret"}`)
	if err := os.WriteFile(sharedFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Profile: profile, BundleReadOnly: true}, logger: slog.Default()}
	ctx := context.Background()
	got, err := d.prepareZcodeProviderSnapshots(ctx)
	if err != nil || got != ctx {
		t.Fatalf("restricted startup changed snapshot context: %v", err)
	}
	for _, path := range []string{file, sharedFile} {
		if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
			t.Fatalf("restricted startup changed credential state %s: %v", path, err)
		}
	}
}

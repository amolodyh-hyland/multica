package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestBundleProfileIsolation(t *testing.T) {
	redirectTestHome(t, t.TempDir())
	const profile = "wi-fac-02-aaaaaaaaaaaa-11111111-1111-1111-1111-111111111111"
	const hosted = "desktop-host"
	hostedRoot := filepath.Join(t.TempDir(), "hosted-root")
	standaloneRoot := filepath.Join(t.TempDir(), "standalone-root")
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{Token: "disposable-hosted-token", WorkspacesRoot: hostedRoot}, hosted); err != nil {
		t.Fatal(err)
	}
	oldPath, err := cli.CLIConfigPathForProfile(hosted)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{Token: "disposable-standalone-token", WorkspacesRoot: standaloneRoot, DisableAutoUpdate: true, DisableAutoReload: true, MaxConcurrentTasks: 1}, profile); err != nil {
		t.Fatal(err)
	}
	standalone, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if standalone.Token != "disposable-standalone-token" || !standalone.DisableAutoUpdate || !standalone.DisableAutoReload || standalone.MaxConcurrentTasks != 1 {
		t.Fatal("standalone configuration was not isolated")
	}
	newPath, err := cli.CLIConfigPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(newPath) == filepath.Dir(oldPath) {
		t.Fatal("standalone reused hosted profile directory")
	}
	for _, candidate := range []string{profile, hosted} {
		got, err := resolveWorkspacesRootForProfile(candidate, "")
		if err != nil {
			t.Fatal(err)
		}
		want := standaloneRoot
		if candidate == hosted {
			want = hostedRoot
		}
		if got != want {
			t.Fatalf("profile %s root = %s, want %s", candidate, got, want)
		}
	}
	if healthPortForProfile(profile) == healthPortForProfile(hosted) || healthPortForProfile(profile) == healthPortForProfile("") {
		t.Fatal("fixture profile reused default/hosted health port")
	}
	after, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("standalone configuration changed hosted profile")
	}
}

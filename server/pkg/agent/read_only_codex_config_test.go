package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyCodexInheritedConfigLayers(t *testing.T) {
	home, cwd := filepath.Join(t.TempDir(), "native"), filepath.Join(t.TempDir(), "scratch")
	fixtureRoot := t.TempDir()
	for _, layer := range []string{"/etc/codex/config.toml", "/etc/codex/managed_config.toml", "/etc/codex/requirements.toml", filepath.Join(home, "config.toml"), filepath.Join(cwd, ".codex", "config.toml"), filepath.Join(filepath.Dir(cwd), ".codex", "config.toml")} {
		t.Run(layer, func(t *testing.T) {
			// Actual system-level fixture, redirected only by the reader used
			// in this test. Production has no configurable system path bypass.
			fixture := filepath.Join(fixtureRoot, "system-config.toml")
			for _, content := range []string{
				"[mcp_servers.inherited]\ncommand='sh'\n",
				"[mcp_servers.inherited]\nenabled=false\ncommand='sh'\n", // strict empty set, even inactive entries refused
				"[profiles.hidden.mcp_servers.inherited]\ncommand='sh'\n",
				"[plugins.inherited]\nenabled=true\n",
				"profile='external'\n",
			} {
				if err := os.WriteFile(fixture, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				read := func(path string) ([]byte, error) {
					if path == layer {
						return os.ReadFile(fixture)
					}
					if path == filepath.Join(home, "config.toml") {
						return []byte("[mcp_servers]\n"), nil
					}
					return nil, os.ErrNotExist
				}
				if err := auditReadOnlyCodexConfig(home, cwd, read); err == nil {
					t.Fatalf("accepted inherited layer %s", layer)
				}
				after, err := os.ReadFile(fixture)
				if err != nil || string(after) != content {
					t.Fatal("modified managed/system layer")
				}
			}
		})
	}
	read := func(path string) ([]byte, error) {
		if path == filepath.Join(home, "config.toml") {
			return []byte("[mcp_servers]\n"), nil
		}
		return nil, os.ErrNotExist
	}
	if err := auditReadOnlyCodexConfig(home, cwd, read); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []struct {
		data []byte
		err  error
	}{{nil, os.ErrPermission}, {[]byte("token='SECRET\n"), nil}} {
		err := auditReadOnlyCodexConfig(home, cwd, func(path string) ([]byte, error) {
			if path == "/etc/codex/config.toml" {
				return failure.data, failure.err
			}
			return read(path)
		})
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("invalid audit outcome: %v", err)
		}
	}
	if err := auditReadOnlyCodexConfig(home, cwd, func(string) ([]byte, error) { return nil, os.ErrNotExist }); err == nil {
		t.Fatal("accepted missing isolated config")
	}
}

func TestReadOnlyCodexRefusesBeforeStartingProcess(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cwd, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".codex", "config.toml"), []byte("[mcp_servers.system]\ncommand='sh'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	executable := filepath.Join(t.TempDir(), "codex-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	b := &codexBackend{cfg: Config{provider: "codex", NativeReadOnly: true, CLIVersion: "0.144.6", ExecutablePath: executable, Env: map[string]string{"CODEX_HOME": home}}}
	if _, err := b.Execute(context.Background(), "fixture", ExecOptions{Cwd: cwd}); err == nil || !strings.Contains(err.Error(), "inherited MCP") {
		t.Fatal("unsafe launch accepted")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("native process started before audit refusal")
	}
}

package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStandardProviderEnvironmentSelectsOnlyNativeClaudeAndCodex(t *testing.T) {
	previousResolver := resolveAgentsViaLoginShell
	resolveAgentsViaLoginShell = func([]string) map[string]string { t.Fatal("explicit paths invoked login-shell fallback"); return nil }
	t.Cleanup(func() { resolveAgentsViaLoginShell = previousResolver })
	resetShellResolveCacheForTest(t)
	redirectTestHome(t, t.TempDir())
	binDir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range defaultAgentCommandNames {
		if err := os.WriteFile(filepath.Join(binDir, name+suffix), []byte("fixture-not-launched"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	missing := filepath.Join(t.TempDir(), "not-installed"+suffix)
	for _, prefix := range []string{
		"ANTIGRAVITY", "CLAUDE", "CODEARTS", "CODEBUDDY", "CODEX", "COPILOT", "CURSOR", "DEVECO", "DEVIN", "DIM", "DSH", "GROK", "HERMES", "KIMI", "KIRO", "MCODE", "MUSE", "OMP", "OPENCLAW", "OPENCODE", "PI", "QODER", "QODERCLICN", "QWEN", "QWENPAW", "REASONIX", "TRAECLI", "ZCODE", "ZEROCLAW",
	} {
		t.Setenv("MULTICA_"+prefix+"_PATH", missing)
	}
	selected := t.TempDir()
	for _, provider := range []struct{ name, env string }{{"claude", "MULTICA_CLAUDE_PATH"}, {"codex", "MULTICA_CODEX_PATH"}} {
		path := filepath.Join(selected, provider.name+suffix)
		if err := os.WriteFile(path, []byte("selected-fixture-not-launched"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(provider.env, path)
	}
	agents := probeAgentCLIs()
	if len(agents) != 2 {
		t.Fatalf("unexpected providers: %v", agents)
	}
	for _, provider := range []string{"claude", "codex"} {
		entry, ok := agents[provider]
		if !ok {
			t.Fatalf("missing selected %s", provider)
		}
		want, err := os.Stat(filepath.Join(selected, provider+suffix))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.Stat(entry.Path)
		if err != nil || !os.SameFile(want, got) {
			t.Fatalf("did not select explicit native %s", provider)
		}
	}
	t.Setenv("MULTICA_CLAUDE_PATH", missing)
	agents = probeAgentCLIs()
	if len(agents) != 1 || agents["codex"].Path == "" {
		t.Fatalf("explicit missing path fell back: %v", agents)
	}
}

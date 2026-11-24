package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReadOnlySuccessfulTurnsNeverEnterWarmPool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executables; native read-only Windows execution is unsupported")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			pool := NewWarmSessionPool(WarmSessionPoolConfig{Logger: logger})
			t.Cleanup(func() { pool.Close() })
			cfg := Config{NativeReadOnly: true, WarmPool: pool, Logger: logger}
			fake := filepath.Join(dir, provider)
			cfg.ExecutablePath = fake
			script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"type":"user"'*)
      printf '%s\n' '{"type":"system","subtype":"init","session_id":"restricted"}'
      printf '%s\n' '{"type":"result","subtype":"success","session_id":"restricted","result":"fixture","is_error":false}'
      ;;
  esac
done
`
			cfg.CLIVersion = MinReadOnlyClaudeVersion
			if provider == "codex" {
				cfg.CLIVersion = MinReadOnlyCodexVersion
				cfg.Env = map[string]string{"CODEX_HOME": dir}
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[mcp_servers]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				response := readOnlyEffectiveConfigFixture(t, "mdm", nil)
				script = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$id" ;;
    *'"method":"config/read"'*) printf '{"id":%s,"result":%s}\n' "$id" '` + string(response) + `' ;;
    *'"method":"configRequirements/read"'*) printf '{"id":%s,"result":{"requirements":null}}\n' "$id" ;;
    *'"method":"thread/start"'*) printf '{"id":%s,"result":{"thread":{"id":"restricted"}}}\n' "$id" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"turn-1"}}}\n' "$id"
      printf '%s\n' '{"method":"turn/completed","params":{"threadId":"restricted","turn":{"id":"turn-1","status":"completed"}}}'
      ;;
  esac
done
`
			}
			writeTestExecutable(t, fake, []byte(script))
			backend, err := New(provider, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if codex, ok := backend.(*codexBackend); ok {
				codex.readOnlyHostAudit = func(context.Context, string, string) error { return nil }
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, err := backend.Execute(ctx, "fixture", ExecOptions{Cwd: dir, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if result.Status != "completed" {
				t.Fatalf("restricted turn failed: %+v", result)
			}
			if n := pool.Len(); n != 0 {
				t.Fatalf("restricted process retained for reuse: %d pooled sessions", n)
			}
		})
	}
}

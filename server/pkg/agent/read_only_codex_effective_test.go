package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readOnlyEffectiveConfigFixture(t *testing.T, source string, mutate func(map[string]any)) json.RawMessage {
	t.Helper()
	response := map[string]any{
		"config": map[string]any{
			"mcp_servers": map[string]any{}, "sandbox_mode": "read-only", "approval_policy": "never", "web_search": "disabled",
			"features": map[string]any{"shell_tool": false, "unified_exec": false, "apps": false, "plugins": false}, "apps": nil, "profile": nil,
		},
		"layers":  []any{map[string]any{"name": map[string]any{"type": source}, "version": "fixture-version", "config": map[string]any{}}},
		"origins": map[string]any{},
	}
	features := response["config"].(map[string]any)["features"].(map[string]any)
	for _, feature := range readOnlyCodexDisabledFeatures {
		features[feature] = false
	}
	if mutate != nil {
		mutate(response)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReadOnlyCodexEffectiveConfigRefusals(t *testing.T) {
	for _, source := range []string{"enterpriseManaged", "mdm", "legacyManagedConfigTomlFromMdm", "user", "system"} {
		t.Run(source, func(t *testing.T) {
			if err := validateReadOnlyCodexEffectiveConfig(readOnlyEffectiveConfigFixture(t, source, nil)); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(map[string]any){
				func(r map[string]any) {
					r["config"].(map[string]any)["mcp_servers"] = map[string]any{"enterprise": map[string]any{"enabled": true, "command": "SECRET"}}
				},
				func(r map[string]any) { r["config"].(map[string]any)["mcp_servers"] = nil },
				func(r map[string]any) { delete(r["config"].(map[string]any), "mcp_servers") },
				func(r map[string]any) { r["config"].(map[string]any)["approval_policy"] = "on-request" },
				func(r map[string]any) { r["config"].(map[string]any)["sandbox_mode"] = "workspace-write" },
				func(r map[string]any) { r["config"].(map[string]any)["features"].(map[string]any)["shell_tool"] = true },
				func(r map[string]any) { r["config"].(map[string]any)["features"].(map[string]any)["apps"] = true },
				func(r map[string]any) {
					r["config"].(map[string]any)["plugins"] = map[string]any{"enterprise": map[string]any{"enabled": true}}
				},
				func(r map[string]any) { r["layers"] = nil },
				func(r map[string]any) { r["layers"].([]any)[0].(map[string]any)["version"] = "" },
				func(r map[string]any) {
					r["layers"].([]any)[0].(map[string]any)["name"].(map[string]any)["type"] = "unknown-managed-source"
				},
				func(r map[string]any) {
					r["layers"].([]any)[0].(map[string]any)["config"] = map[string]any{"mcp_servers": map[string]any{"enterprise": map[string]any{"enabled": true, "command": "SECRET"}}}
				},
			} {
				response := readOnlyEffectiveConfigFixture(t, source, mutate)
				client, _, _ := newTestCodexClient(t)
				client.cfg.NativeReadOnly = true
				stdin := &fakeStdinWithHook{}
				client.stdin = stdin
				stdin.afterWrite = func() {
					var request struct {
						ID     int    `json:"id"`
						Method string `json:"method"`
						Params struct {
							Cwd           string `json:"cwd"`
							IncludeLayers bool   `json:"includeLayers"`
						} `json:"params"`
					}
					lines := stdin.Lines()
					if err := json.Unmarshal([]byte(lines[len(lines)-1]), &request); err != nil {
						t.Fatal(err)
					}
					if request.Method != "config/read" || request.Params.Cwd != "/scratch" || !request.Params.IncludeLayers {
						t.Fatalf("unsafe request: %+v", request)
					}
					client.handleLine(`{"id":` + strconv.Itoa(request.ID) + `,"result":` + string(response) + `}`)
				}
				if _, _, err := client.startOrResumeThread(context.Background(), ExecOptions{Cwd: "/scratch"}, slog.Default()); err == nil || strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("unsafe audit outcome: %v", err)
				}
				if len(stdin.Lines()) != 1 {
					t.Fatal("thread or config mutation followed refused attestation")
				}
			}
		})
	}
	for _, malformed := range []string{`{`, `{}`, `null`, `{"config":{},"layers":[]}`} {
		if err := validateReadOnlyCodexEffectiveConfig(json.RawMessage(malformed)); err == nil {
			t.Fatal("malformed response accepted")
		}
	}
}

func TestReadOnlyCodexEffectiveConfigRPCFailure(t *testing.T) {
	for _, rpcError := range []bool{true, false} {
		client, _, _ := newTestCodexClient(t)
		client.cfg.NativeReadOnly = true
		client.handshakeTimeout = 20 * time.Millisecond
		stdin := &fakeStdinWithHook{}
		client.stdin = stdin
		if rpcError {
			stdin.afterWrite = func() { client.handleLine(`{"id":1,"error":{"code":-32601,"message":"SECRET"}}`) }
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		started := time.Now()
		_, _, err := client.startOrResumeThread(ctx, ExecOptions{Cwd: "/scratch"}, slog.Default())
		cancel()
		if time.Since(started) > 500*time.Millisecond {
			t.Fatal("effective config RPC ignored its startup timeout")
		}
		if err == nil || strings.Contains(err.Error(), "SECRET") || len(stdin.Lines()) != 1 {
			t.Fatalf("RPC failure permitted startup: %v %v", err, stdin.Lines())
		}
	}
}

func TestReadOnlyCodexEffectiveConfigProcessBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture; Windows harness is unsupported")
	}
	for _, source := range []string{"enterpriseManaged", "mdm"} {
		for _, unsafe := range []bool{true, false} {
			t.Run(source+"/unsafe="+strconv.FormatBool(unsafe), func(t *testing.T) {
				home, cwd := t.TempDir(), t.TempDir()
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				response := readOnlyEffectiveConfigFixture(t, source, func(r map[string]any) {
					if unsafe {
						r["config"].(map[string]any)["mcp_servers"] = map[string]any{"enterprise": map[string]any{"enabled": true, "command": "SECRET"}}
					}
				})
				transcript := filepath.Join(t.TempDir(), "requests.jsonl")
				fake := writeFakeCodexAppServer(t, `while IFS= read -r line; do
printf '%s\n' "$line" >> "$TRANSCRIPT"
case "$line" in
  *'"method":"initialize"'*) echo '{"id":1,"result":{}}' ;;
  *'"method":"config/read"'*) printf '%s\n' '`+`{"id":2,"result":`+string(response)+`}`+`' ;;
  *'"method":"thread/start"'*) echo '{"id":3,"result":{"thread":{}}}' ;;
esac
done
`)
				backend, err := New("codex", Config{NativeReadOnly: true, CLIVersion: AuditedReadOnlyCodexVersion, ExecutablePath: fake, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Env: map[string]string{"CODEX_HOME": home, "TRANSCRIPT": transcript}})
				if err != nil {
					t.Fatal(err)
				}
				// Check candidate protocol emission separately from the public unsupported gate.
				session, err := backend.(*codexBackend).executeOnce(context.Background(), "fixture", ExecOptions{NativeReadOnly: true, Cwd: cwd, Timeout: 5 * time.Second, HandshakeTimeout: time.Second}, 1)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					for range session.Messages {
					}
				}()
				result := <-session.Result
				if result.Status != "failed" || strings.Contains(result.Error, "SECRET") {
					t.Fatalf("unexpected result: %+v", result)
				}
				data, err := os.ReadFile(transcript)
				if err != nil {
					t.Fatal(err)
				}
				var methods []string
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var request struct {
						Method string `json:"method"`
					}
					if err := json.Unmarshal([]byte(line), &request); err != nil {
						t.Fatal(err)
					}
					methods = append(methods, request.Method)
				}
				want := "initialize,initialized,config/read"
				if !unsafe {
					want += ",thread/start"
				}
				if strings.Join(methods, ",") != want {
					t.Fatalf("requests=%v want=%s", methods, want)
				}
				if unsafe && !strings.Contains(result.Error, "effective configuration") {
					t.Fatal("failed for a different reason")
				}
				if !unsafe && !strings.Contains(result.Error, "returned no thread ID") {
					t.Fatal("safe attestation never reached thread/start")
				}
			})
		}
	}
}

func TestReadOnlyCodexEffectiveConfigRequiresExplicitStartupFlags(t *testing.T) {
	for _, feature := range ReadOnlyCodexDisabledFeatures() {
		for _, value := range []any{nil, true, "false"} {
			response := readOnlyEffectiveConfigFixture(t, "enterpriseManaged", func(r map[string]any) {
				features := r["config"].(map[string]any)["features"].(map[string]any)
				if value == nil {
					delete(features, feature)
				} else {
					features[feature] = value
				}
			})
			if err := validateReadOnlyCodexEffectiveConfig(response); err == nil {
				t.Fatalf("accepted missing or incompatible native flag: %s=%v", feature, value)
			}
		}
	}
	// Reproduce the previous fixture's absent native defaults explicitly.
	response := readOnlyEffectiveConfigFixture(t, "mdm", func(r map[string]any) {
		r["config"].(map[string]any)["features"] = map[string]any{"shell_tool": false, "unified_exec": false}
	})
	if err := validateReadOnlyCodexEffectiveConfig(response); err == nil {
		t.Fatal("accepted omitted Apps/plugin defaults")
	}
}

func TestReadOnlyCodexUnsupportedBeforeNativeStartup(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"darwin", "linux", "windows"} {
		if SupportsNativeReadOnly("codex", AuditedReadOnlyCodexVersion, platform) {
			t.Fatalf("advertised unsafe managed startup on %s", platform)
		}
	}
	b := &codexBackend{cfg: Config{provider: "codex", NativeReadOnly: true, CLIVersion: AuditedReadOnlyCodexVersion, ExecutablePath: "/missing/native-codex-must-not-be-looked-up", Env: map[string]string{"CODEX_HOME": home}}}
	if _, err := b.Execute(context.Background(), "fixture", ExecOptions{Cwd: cwd}); err == nil || !strings.Contains(err.Error(), "unsupported read-only") {
		t.Fatalf("unsupported harness reached launch: %v", err)
	}
}

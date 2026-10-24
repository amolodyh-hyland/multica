package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

func TestReadOnlyNativeLaunchConfiguration(t *testing.T) {
	opts := ExecOptions{NativeReadOnly: true, ExtraArgs: []string{"--dangerously-skip-permissions"}, CustomArgs: []string{"-c", `sandbox_mode="danger-full-access"`}}
	args := buildClaudeArgs(opts, slog.Default())
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "bypassPermissions") || strings.Contains(joined, "dangerously-skip") {
		t.Fatalf("unsafe launch: %q", args)
	}
	for flag, want := range map[string]string{"--permission-mode": "plan", "--permission-prompts": "none", "--tools": "", "--disallowedTools": "mcp__*"} {
		found := false
		for i, arg := range args {
			if arg == flag && i+1 < len(args) && args[i+1] == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s %q: %q", flag, want, args)
		}
	}
	if !strings.Contains(joined, "--restricted") || !strings.Contains(joined, "--no-session-persistence") {
		t.Fatalf("missing confinement: %q", args)
	}
	codexArgs := strings.Join(buildCodexArgs(opts, slog.Default()), " ")
	if strings.Contains(codexArgs, "danger-full-access") || !strings.Contains(codexArgs, `sandbox_mode="read-only"`) || !strings.Contains(codexArgs, `approval_policy="never"`) {
		t.Fatal(codexArgs)
	}
	if strings.Contains(codexArgs, "--wi-fac") || !strings.Contains(codexArgs, "--strict-config") || !strings.Contains(codexArgs, "mcp_servers={}") || !strings.Contains(codexArgs, "features.shell_tool=false") {
		t.Fatal(codexArgs)
	}
	for _, feature := range []string{"apps", "plugins", "remote_plugin", "plugin_sharing", "skill_mcp_dependency_install", "tool_suggest", "in_app_browser", "browser_use", "browser_use_full_cdp_access", "browser_use_external", "computer_use", "image_generation", "hooks", "multi_agent", "code_mode_host", "shell_snapshot", "mentions_v2", "guardian_approval", "goals", "tool_call_mcp_elicitation", "auth_elicitation", "workspace_dependencies", "remote_compaction_v2"} {
		if !strings.Contains(codexArgs, "features."+feature+"=false") || readOnlyCodexConfig()["features."+feature] != false {
			t.Fatalf("native startup feature not disabled: %s", feature)
		}
	}
}

func TestReadOnlyNativeRejectsOverridesAndUnsupportedPeers(t *testing.T) {
	cfg := Config{NativeReadOnly: true, provider: "claude", CLIVersion: MinReadOnlyClaudeVersion}
	if err := validateReadOnlyExecution(cfg, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []ExecOptions{{ExtraArgs: []string{"--dangerously-skip-permissions"}}, {CustomArgs: []string{"--permission-mode", "bypassPermissions"}}, {ResumeSessionID: "old"}, {ResumeExpected: true}} {
		if err := validateReadOnlyExecution(cfg, opts); err == nil {
			t.Fatalf("accepted override %+v", opts)
		}
	}
	for _, tc := range []struct{ provider, version, goos string }{{"claude", "2.1.258", "darwin"}, {"claude", "unknown", "darwin"}, {"codex", "0.144.6", "windows"}, {"codex", "unknown", "linux"}, {"codex", "0.144.5", "darwin"}, {"codex", "0.144.6-custom", "darwin"}, {"codex", "0.145.0-dirty", "linux"}, {"other", "99.0.0", "darwin"}} {
		if SupportsNativeReadOnly(tc.provider, tc.version, tc.goos) {
			t.Fatalf("accepted unsupported peer: %+v", tc)
		}
	}
	cfg.LaunchPrefix = []string{"wrapper"}
	if err := validateReadOnlyExecution(cfg, ExecOptions{}); err == nil {
		t.Fatal("accepted custom command prefix")
	}
}

func TestReadOnlyClaudeDeniesControlRequests(t *testing.T) {
	b := &claudeBackend{cfg: Config{NativeReadOnly: true}}
	stdin := &fakeStdin{}
	b.handleControlRequest(claudeSDKMessage{RequestID: "request", Request: json.RawMessage(`{"subtype":"tool_use","tool_name":"Bash","input":{"command":"touch outside"}}`)}, stdin)
	var reply struct {
		Response struct {
			Response struct {
				Behavior string `json:"behavior"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(stdin.Lines()[0]), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Response.Response.Behavior != "deny" {
		t.Fatalf("tool request auto-approved: %#v", reply)
	}
}

func TestReadOnlyCodexDeniesServerRequests(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "execCommandApproval", "item/fileChange/requestApproval", "applyPatchApproval", "item/permissions/requestApproval", "mcpServer/elicitation/request", "item/tool/call"} {
		t.Run(method, func(t *testing.T) {
			client, stdin, _ := newTestCodexClient(t)
			client.cfg.NativeReadOnly = true
			client.handleLine(`{"id":1,"method":` + strconv.Quote(method) + `,"params":{"permissions":{"network":{"enabled":true},"fileSystem":{"write":["/outside"]}}}}`)
			lines := stdin.Lines()
			if len(lines) != 1 {
				t.Fatalf("missing refusal: %q", lines)
			}
			var reply map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &reply); err != nil {
				t.Fatal(err)
			}
			result, _ := reply["result"].(map[string]any)
			switch method {
			case "item/permissions/requestApproval":
				permissions, _ := result["permissions"].(map[string]any)
				if len(permissions) != 0 {
					t.Fatalf("permission escalation granted: %#v", reply)
				}
			case "mcpServer/elicitation/request":
				if result["action"] != "decline" {
					t.Fatalf("elicitation accepted: %#v", reply)
				}
			case "item/tool/call":
				if reply["error"] == nil {
					t.Fatal("unknown request accepted")
				}
			default:
				if result["decision"] != "decline" {
					t.Fatalf("request accepted: %#v", reply)
				}
			}
		})
	}
}

func TestReadOnlyCodexThreadAndTurn(t *testing.T) {
	client, _, _ := newTestCodexClient(t)
	client.cfg.NativeReadOnly = true
	stdin := &fakeStdinWithHook{}
	client.stdin = stdin
	var captured map[string]any
	stdin.afterWrite = func() {
		lines := stdin.Lines()
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &captured); err != nil {
			t.Fatal(err)
		}
		id := int(captured["id"].(float64))
		result := json.RawMessage(`{"thread":{"id":"fresh"}}`)
		if captured["method"] == "configRequirements/read" {
			result = json.RawMessage(`{"requirements":null}`)
		}
		if captured["method"] == "config/read" {
			result = readOnlyEffectiveConfigFixture(t, "mdm", nil)
		}
		reply, err := json.Marshal(map[string]any{"id": id, "result": result})
		if err != nil {
			t.Fatal(err)
		}
		client.handleLine(string(reply))
	}
	opts := ExecOptions{NativeReadOnly: true, Cwd: "/scratch"}
	id, resumed, err := client.startOrResumeThread(context.Background(), opts, slog.Default())
	if err != nil || id != "fresh" || resumed {
		t.Fatalf("thread=%s resumed=%t err=%v", id, resumed, err)
	}
	params := captured["params"].(map[string]any)
	if params["approvalPolicy"] != "never" || params["sandbox"] != "read-only" {
		t.Fatalf("unsafe thread: %#v", params)
	}
	turn := map[string]any{}
	applyReadOnlyCodexTurn(turn, opts)
	policy := turn["sandboxPolicy"].(map[string]any)
	access := policy["access"].(map[string]any)
	if policy["type"] != "readOnly" || access["type"] != "restricted" || turn["approvalPolicy"] != "never" {
		t.Fatalf("unsafe turn: %#v", turn)
	}
	opts.ResumeSessionID = "old"
	if _, _, err := client.startOrResumeThread(context.Background(), opts, slog.Default()); err == nil {
		t.Fatal("resumed under old permissions")
	}
	if len(stdin.Lines()) != 3 {
		t.Fatal("resume reached transport")
	}
}

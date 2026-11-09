package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon"
)

func TestDaemonControlledPolicyPersistsWithoutCallerEnvironment(t *testing.T) {
	t.Setenv("MULTICA_DAEMON_CONTROL_TOKEN_FILE", "")
	const profile = "durable-control-test"
	mkProfiles(t, profile)
	if err := persistDaemonControl(profile, daemon.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(daemonControlPath(profile)); !os.IsNotExist(err) {
		t.Fatal("ordinary profile acquired control policy")
	}
	cfg := daemon.Config{LocalControlToken: strings.Repeat("a", 64), DaemonID: "22222222-2222-4222-8222-222222222222", CLICommit: strings.Repeat("b", 40)}
	if err := persistDaemonControl(profile, cfg); err != nil {
		t.Fatal(err)
	}
	record, present, err := readDaemonControlRecord(profile)
	if err != nil || !present || record.PID != os.Getpid() || record.Profile != profile || record.DaemonID != cfg.DaemonID || record.CLICommit != cfg.CLICommit || record.StartedAt == "" {
		t.Fatalf("missing durable start identity: %+v %v", record, err)
	}
	data, err := os.ReadFile(daemonControlPath(profile))
	if err != nil || strings.Contains(string(data), cfg.LocalControlToken) {
		t.Fatal("invalid or secret-bearing control record")
	}
	if err := requireLegacyDaemonControl(profile, nil); err == nil {
		t.Fatal("control policy depended on caller env/health")
	}
	if err := persistDaemonControl(profile, daemon.Config{}); err == nil {
		t.Fatal("silently downgraded controlled profile")
	}
	cfg.DaemonID = "33333333-3333-4333-8333-333333333333"
	if err := persistDaemonControl(profile, cfg); err == nil {
		t.Fatal("silently changed controlled identity")
	}
}

func TestDaemonControlledOutageRefusesForceAndRestart(t *testing.T) {
	clearDaemonTaskEnv(t)
	t.Setenv("MULTICA_DAEMON_CONTROL_TOKEN_FILE", "")
	const profile = "controlled-outage-test"
	mkProfiles(t, profile)
	child, release := startStopLockHolder(t, profile, true)
	port := healthPortForProfile(profile)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("fixture port unavailable: %v", err)
	}
	var healthRequests, shutdownRequests atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/shutdown") {
			shutdownRequests.Add(1)
		} else {
			healthRequests.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: child.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	health := checkDaemonHealthOnPort(ctx, port)
	cancel()
	if daemonAlive(health) {
		t.Fatal("fixture health was unexpectedly usable")
	}
	base := daemonControlRecord{Version: 1, PID: child.Process.Pid, Profile: profile,
		DaemonID: "22222222-2222-4222-8222-222222222222", CLICommit: strings.Repeat("b", 40), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, evidence := range []string{"valid", "stale-pid", "foreign-profile", "invalid-start", "malformed", "directory"} {
		t.Run(evidence, func(t *testing.T) {
			record := base
			switch evidence {
			case "stale-pid":
				record.PID += 100000
			case "foreign-profile":
				record.Profile = "other-profile"
			case "invalid-start":
				record.StartedAt = "unknown"
			}
			data, _ := json.Marshal(record)
			if evidence == "malformed" {
				data = []byte("{")
			}
			if evidence == "directory" {
				if err := os.Remove(daemonControlPath(profile)); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(daemonControlPath(profile), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(daemonControlPath(profile), data, 0o600); err != nil {
				t.Fatal(err)
			}
			stop := daemonStatusCmdFor(t, profile, "")
			stop.Flags().Bool("force", true, "")
			for _, run := range []struct {
				name string
				call func() error
			}{
				{"stop --force", func() error { return runDaemonStop(stop, nil) }},
				{"restart", func() error { return runDaemonRestart(newRestartTestCmd(t, profile), nil) }},
				{"force fallback", func() error { return stopDaemonAndWait(profile, port, nil, 0, 0) }},
			} {
				if err := run.call(); err == nil || !strings.Contains(err.Error(), "/shutdown/verified") {
					t.Fatalf("%s did not refuse durable %s evidence: %v", run.name, evidence, err)
				}
			}
		})
	}
	if healthRequests.Load() == 0 || shutdownRequests.Load() != 0 {
		t.Fatalf("unexpected HTTP control: health=%d shutdown=%d", healthRequests.Load(), shutdownRequests.Load())
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("controlled daemon was killed instead of exiting normally: %v", err)
	}
}

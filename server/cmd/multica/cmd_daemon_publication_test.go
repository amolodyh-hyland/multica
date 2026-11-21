package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/spf13/cobra"
)

func publicationTestConfig() daemon.Config {
	return daemon.Config{HealthPort: healthPortForProfile("background-publication"), LocalControlToken: strings.Repeat("a", 64), DaemonID: "22222222-2222-4222-8222-222222222222", CLICommit: strings.Repeat("b", 40)}
}

func requireNoPublication(t *testing.T, profile string) {
	t.Helper()
	if _, err := os.Stat(daemonPIDPathForProfile(profile)); !os.IsNotExist(err) {
		t.Fatalf("PID published before policy, or not cleaned: %v", err)
	}
	if _, ok := readDaemonPortRecord(profile); ok {
		t.Fatal("port published before policy, or not cleaned")
	}
}

func TestDaemonPublicationPolicyOrderingAndFailure(t *testing.T) {
	const profile = "publication-order"
	for _, outcome := range []string{"success", "bind-failure", "policy-refusal"} {
		t.Run(outcome, func(t *testing.T) {
			mkProfiles(t, profile)
			cfg := publicationTestConfig()
			if outcome == "policy-refusal" {
				if err := persistDaemonControl(profile, cfg); err != nil {
					t.Fatal(err)
				}
				cfg.LocalControlToken = ""
			}
			called := false
			ln, _, cleanup, err := prepareDaemonPublication(profile, cfg, func(network, address string) (net.Listener, error) {
				called = true
				record, present, err := readDaemonControlRecord(profile)
				if err != nil || !present || record.PID != os.Getpid() {
					t.Fatalf("listener bound without sticky policy: %+v %v", record, err)
				}
				requireNoPublication(t, profile)
				if outcome == "bind-failure" {
					return nil, errors.New("fixture bind failure")
				}
				return net.Listen(network, address)
			})
			if outcome == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if ln == nil {
					t.Fatal("missing listener")
				}
				if data, err := os.ReadFile(daemonPIDPathForProfile(profile)); err != nil || string(data) != strconv.Itoa(os.Getpid()) {
					t.Fatalf("PID: %q %v", data, err)
				}
				cleanup()
			} else if err == nil {
				t.Fatal("failed startup succeeded")
			}
			if called != (outcome != "policy-refusal") {
				t.Fatalf("listener attempted on %s", outcome)
			}
			requireNoPublication(t, profile)
			if err := requireLegacyDaemonControl(profile, nil); err == nil {
				t.Fatal("startup failure/cleanup reopened legacy control")
			}
		})
	}
}

func waitPublicationFixture(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture did not reach %s", filepath.Base(path))
}

// Executed only as an owned test subprocess. It calls the same publication
// helper as runDaemonForeground; no daemon, provider, account or server starts.
func TestDaemonPublicationChildProcess(t *testing.T) {
	dir := os.Getenv("MULTICA_TEST_PUBLICATION_FIXTURE")
	if dir == "" {
		return
	}
	redirectTestHome(t, os.Getenv("MULTICA_TEST_PUBLICATION_HOME"))
	profile := "background-publication"
	if err := os.WriteFile(filepath.Join(dir, "paused"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitPublicationFixture(t, filepath.Join(dir, "begin"))
	defer os.WriteFile(filepath.Join(dir, "done"), nil, 0o600)
	if os.Getenv("MULTICA_TEST_PUBLICATION_OUTCOME") == "early-exit" {
		return
	}
	ln, _, cleanup, err := prepareDaemonPublication(profile, publicationTestConfig(), net.Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		pid := os.Getpid()
		if os.Getenv("MULTICA_TEST_PUBLICATION_OUTCOME") == "wrong-health" {
			pid++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "profile": profile, "pid": pid})
	})}
	go server.Serve(ln)
	defer server.Close()
	if os.Getenv("MULTICA_TEST_PUBLICATION_OUTCOME") == "wrong-health" {
		// Let the parent's real matching-child poll reject this response.
		time.Sleep(1200 * time.Millisecond)
		return
	}
	waitPublicationFixture(t, filepath.Join(dir, "release"))
}

func TestDaemonBackgroundChildOwnsPublication(t *testing.T) {
	for _, outcome := range []string{"running", "early-exit", "wrong-health"} {
		t.Run(outcome, func(t *testing.T) {
			clearDaemonTaskEnv(t)
			const profile = "background-publication"
			home := mkProfiles(t, profile)
			if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{Token: "mul_fixture"}, profile); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			originalCommand, originalExecutable, originalSync := daemonChildCommand, daemonExecutable, syncDaemonAutostart
			t.Cleanup(func() {
				daemonChildCommand, daemonExecutable, syncDaemonAutostart = originalCommand, originalExecutable, originalSync
			})
			daemonExecutable = func() (string, error) { return os.Args[0], nil }
			syncDaemonAutostart = func(string, bool) {}
			daemonChildCommand = func(exe string, args ...string) *exec.Cmd {
				child := exec.Command(exe, append([]string{"-test.run=^TestDaemonPublicationChildProcess$", "--"}, args...)...)
				child.Env = append(os.Environ(), "MULTICA_TEST_PUBLICATION_FIXTURE="+dir, "MULTICA_TEST_PUBLICATION_OUTCOME="+outcome, "MULTICA_TEST_PUBLICATION_HOME="+home)
				return child
			}
			cmd := &cobra.Command{Use: "start"}
			cmd.Flags().Bool("foreground", false, "")
			cmd.Flags().String("profile", profile, "")
			cmd.Flags().String("server-url", "", "")
			result := make(chan error, 1)
			go func() { result <- runDaemonStart(cmd, nil) }()
			// Always release owned children normally, including after a failed assertion.
			t.Cleanup(func() {
				_ = os.WriteFile(filepath.Join(dir, "begin"), nil, 0o600)
				_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600)
				waitPublicationFixture(t, filepath.Join(dir, "done"))
			})
			waitPublicationFixture(t, filepath.Join(dir, "paused"))
			for until := time.Now().Add(250 * time.Millisecond); time.Now().Before(until); {
				requireNoPublication(t, profile)
				time.Sleep(10 * time.Millisecond)
			}
			if _, present, _ := readDaemonControlRecord(profile); present {
				t.Fatal("child was not paused before policy")
			}
			if err := os.WriteFile(filepath.Join(dir, "begin"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if outcome == "running" {
					if err != nil {
						t.Fatal(err)
					}
					if _, present, err := readDaemonControlRecord(profile); err != nil || !present {
						t.Fatalf("ready without policy: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "daemon exited during startup") {
					t.Fatalf("%s startup: %v", outcome, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("parent did not finish bounded fixture startup")
			}
			if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			waitPublicationFixture(t, filepath.Join(dir, "done"))
			requireNoPublication(t, profile)
			if outcome != "early-exit" && requireLegacyDaemonControl(profile, nil) == nil {
				t.Fatal("cleanup downgraded controlled identity")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "owned fixture completed:", outcome)
		})
	}
}

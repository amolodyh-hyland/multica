package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/multica-ai/multica/server/internal/daemon"
)

// Only the foreground child publishes its identity. The sticky control policy
// must survive even an unsuccessful start, before any listener, port or PID is
// observable, so later callers cannot fall back to unauthenticated control.
func prepareDaemonPublication(profile string, cfg daemon.Config, listen func(string, string) (net.Listener, error)) (net.Listener, int, func(), error) {
	if err := persistDaemonControl(profile, cfg); err != nil {
		return nil, 0, nil, fmt.Errorf("persist daemon control policy: %w", err)
	}
	if err := writeDaemonShutdownResult(profile, daemonShutdownResult{PID: os.Getpid()}); err != nil {
		return nil, 0, nil, fmt.Errorf("initialize daemon shutdown result: %w", err)
	}
	ln, port, err := bindDaemonHealthPort(cfg.HealthPort, listen)
	if err != nil {
		return nil, 0, nil, err
	}
	cleanup := func() {
		_ = ln.Close()
		removeDaemonPortIfOwned(profile, os.Getpid())
		_ = os.Remove(daemonPIDPathForProfile(profile))
	}
	if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: os.Getpid()}); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("publish daemon health port: %w", err)
	}
	if err := os.WriteFile(daemonPIDPathForProfile(profile), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("publish daemon PID: %w", err)
	}
	return ln, port, cleanup, nil
}

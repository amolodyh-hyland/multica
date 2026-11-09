package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemon"
)

type daemonControlRecord struct {
	Version   int    `json:"version"`
	PID       int    `json:"pid"`
	Profile   string `json:"profile"`
	DaemonID  string `json:"daemon_id"`
	CLICommit string `json:"cli_commit"`
	StartedAt string `json:"started_at"`
}

func daemonControlPath(profile string) string {
	return filepath.Join(daemonDirForProfile(profile), "daemon.control.json")
}

func validDaemonControlRecord(record daemonControlRecord, profile string) bool {
	id, err := uuid.Parse(record.DaemonID)
	startedAt, timeErr := time.Parse(time.RFC3339Nano, record.StartedAt)
	return record.Version == 1 && record.PID > 1 && record.Profile == profile && err == nil && id != uuid.Nil &&
		id.String() == record.DaemonID && regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(record.CLICommit) && timeErr == nil && !startedAt.IsZero()
}

func readDaemonControlRecord(profile string) (daemonControlRecord, bool, error) {
	var record daemonControlRecord
	info, err := os.Lstat(daemonControlPath(profile))
	if os.IsNotExist(err) {
		return record, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return record, true, fmt.Errorf("cannot verify durable daemon control record; refusing legacy control, use /shutdown/verified")
	}
	data, err := os.ReadFile(daemonControlPath(profile))
	if err != nil || json.Unmarshal(data, &record) != nil || !validDaemonControlRecord(record, profile) {
		return record, true, fmt.Errorf("invalid durable daemon control identity; refusing legacy control, use /shutdown/verified")
	}
	return record, true, nil
}

func persistDaemonControl(profile string, cfg daemon.Config) error {
	previous, present, err := readDaemonControlRecord(profile)
	if err != nil {
		return err
	}
	if cfg.LocalControlToken == "" {
		if present {
			return fmt.Errorf("profile requires authenticated /shutdown/verified control; refusing tokenless startup")
		}
		return nil
	}
	if present && previous.DaemonID != cfg.DaemonID {
		return fmt.Errorf("durable daemon control identity differs from the requested daemon")
	}
	record := daemonControlRecord{Version: 1, PID: os.Getpid(), Profile: profile, DaemonID: cfg.DaemonID,
		CLICommit: cfg.CLICommit, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if !validDaemonControlRecord(record, profile) {
		return fmt.Errorf("cannot persist invalid daemon control start identity")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(daemonDirForProfile(profile), ".daemon.control-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, daemonControlPath(profile))
}

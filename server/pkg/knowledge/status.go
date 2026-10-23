package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const MaxStatusBindings = 8

type StatusRequest struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	OwnerID     string    `json:"owner_id"`
	DaemonID    string    `json:"daemon_id"`
	Bindings    []Binding `json:"bindings"`
	Deadline    time.Time `json:"deadline"`
}

type SourceStatus struct {
	ResourceID    string `json:"resource_id"`
	ReferenceHash string `json:"reference_hash"`
	Status        string `json:"status"`
	Exists        *bool  `json:"exists,omitempty"`
	Type          string `json:"type,omitempty"`
	SizeBytes     *int   `json:"size_bytes,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
	Reason        string `json:"reason"`
}

func (r StatusRequest) Validate(scope Scope) error {
	if !ValidID(r.ID) || len(r.Bindings) == 0 || len(r.Bindings) > MaxStatusBindings || r.WorkspaceID != scope.WorkspaceID || r.OwnerID != scope.OwnerID || r.DaemonID != scope.DaemonID {
		return errors.New("invalid knowledge status scope")
	}
	seen := map[string]bool{}
	for _, b := range r.Bindings {
		if !ValidID(b.ID) || seen[b.ID] || b.Reference.Validate(b.Kind) != nil || b.Reference.OwnerID != scope.OwnerID || b.Reference.DaemonID != scope.DaemonID {
			return errors.New("invalid knowledge status binding")
		}
		seen[b.ID] = true
	}
	return nil
}

func UnverifiedStatuses(bindings []Binding, reason string) []SourceStatus {
	out := make([]SourceStatus, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, SourceStatus{ResourceID: b.ID, ReferenceHash: b.Reference.ContentHash, Status: "unverified", Reason: reason})
	}
	return out
}

func (s LocalStore) CheckStatus(ctx context.Context, scope Scope, request StatusRequest) ([]SourceStatus, error) {
	if err := request.Validate(scope); err != nil {
		return nil, err
	}
	deadline := minTime(request.Deadline, time.Now().Add(30*time.Second))
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	results := UnverifiedStatuses(request.Bindings, "unreadable")
	remainingBytes, remainingFiles := MaxBytes, 1024
	for i, binding := range request.Bindings {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		result := &results[i]
		raw, err := boundedLocalRead(filepath.Join(s.Dir, "sources", binding.Reference.RootRef+".json"), 16<<10)
		if err != nil {
			continue
		}
		var record registration
		if json.Unmarshal(raw, &record) != nil || record.Scope != scope || record.Kind != binding.Kind {
			continue
		}
		current, pinned := record.Reference, binding.Reference
		if current.Validate(binding.Kind) != nil {
			continue
		}
		if current.RootRef != pinned.RootRef || current.SourceID != pinned.SourceID || current.EntryID != pinned.EntryID || current.AuthorityID != pinned.AuthorityID || current.OwnerID != pinned.OwnerID || current.DaemonID != pinned.DaemonID || current.ResolverID != pinned.ResolverID {
			continue
		}
		info, err := os.Lstat(record.Path)
		if errors.Is(err, os.ErrNotExist) {
			exists := false
			result.Exists = &exists
			result.Status = "missing"
			result.Reason = "missing"
			continue
		}
		if err != nil {
			continue
		}
		exists := true
		result.Exists = &exists
		result.Type = "file"
		if info.IsDir() {
			result.Type = "folder"
			if binding.Kind == Skill {
				result.Type = "skill"
			}
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			result.Status = "stale"
			result.Reason = "changed"
			continue
		}
		if (binding.Kind == File) != info.Mode().IsRegular() {
			result.Status = "stale"
			result.Reason = "changed"
			continue
		}
		if remainingBytes <= 0 || remainingFiles <= 0 {
			result.Reason = "limit"
			continue
		}
		files, _, err := readSourceBounded(ctx, record, remainingBytes, min(MaxFiles, remainingFiles))
		if err != nil {
			remainingBytes = 0
			if strings.Contains(err.Error(), "limit") {
				result.Reason = "limit"
			}
			if strings.Contains(err.Error(), "root replaced") || strings.Contains(err.Error(), "root changed") || strings.Contains(err.Error(), "source is empty") {
				result.Status = "stale"
				result.Reason = "changed"
			}
			continue
		}
		size := 0
		for _, file := range files {
			size += file.SizeBytes
		}
		remainingBytes -= size
		remainingFiles -= len(files)
		result.SizeBytes = &size
		kind := binding.Kind
		if kind == Skill {
			kind = "skill"
		}
		hash, err := ContentHash(kind, pinned.Format, pinned.EntryPoint, files)
		if err != nil {
			result.Status = "stale"
			result.Reason = "changed"
			continue
		}
		result.ContentHash = hash
		result.Status = "available"
		result.Reason = "checked"
		if hash != pinned.ContentHash || current.RootGeneration != pinned.RootGeneration || current.SourceRevision != pinned.SourceRevision {
			result.Status = "stale"
			result.Reason = "changed"
		}
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return results, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func StatusFallback(bindings []Binding, online, supported bool) []SourceStatus {
	if online && supported {
		return nil
	}
	results := UnverifiedStatuses(bindings, "owner_offline")
	if online {
		for i := range results {
			results[i].Status = "incompatible"
			results[i].Reason = "needs_newer_runner"
		}
	}
	return results
}

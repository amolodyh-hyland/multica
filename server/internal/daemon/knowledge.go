package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/knowledge"
)

const (
	knowledgeResolutionTimeout = 30 * time.Second
	maxKnowledgeResolutions    = 2
)

type knowledgeSnapshotStore interface {
	Resolve(context.Context, knowledge.Scope, knowledge.Selection) (knowledge.Snapshot, error)
}

type knowledgeResolutionLimiter struct {
	once  sync.Once
	slots chan struct{}
}

func (r *knowledgeResolutionLimiter) resolve(ctx context.Context, store knowledgeSnapshotStore, server, daemonID string, task Task) (*knowledge.Snapshot, error) {
	if task.KnowledgeManifest == nil {
		return nil, nil
	}
	return runKnowledgeResolution(r, ctx, func(ctx context.Context) (*knowledge.Snapshot, error) {
		return resolveTaskKnowledge(ctx, store, server, daemonID, task)
	})
}

func runKnowledgeResolution[T any](r *knowledgeResolutionLimiter, ctx context.Context, work func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, knowledgeResolutionTimeout)
	defer cancel()
	r.once.Do(func() { r.slots = make(chan struct{}, maxKnowledgeResolutions) })
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return zero, context.Cause(ctx)
	}
	if ctx.Err() != nil {
		<-r.slots
		return zero, context.Cause(ctx)
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		// A stalled filesystem retains its shared permit until the syscall returns.
		defer func() { <-r.slots }()
		value, err := work(ctx)
		done <- result{value, err}
	}()
	select {
	case <-ctx.Done():
		return zero, context.Cause(ctx)
	case resolved := <-done:
		if ctx.Err() != nil {
			return zero, context.Cause(ctx)
		}
		return resolved.value, resolved.err
	}
}

func (d *Daemon) resolveTaskKnowledge(ctx context.Context, task Task) (*knowledge.Snapshot, error) {
	if task.KnowledgeManifest == nil {
		return nil, nil
	}
	if task.KnowledgeManifest.WorkspaceID != task.WorkspaceID || task.KnowledgeManifest.DaemonID != d.cfg.DaemonID {
		return nil, errors.New("knowledge reference is unavailable on this machine")
	}
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return nil, err
	}
	return d.knowledgeResolutions.resolve(ctx, knowledge.LocalStore{Dir: filepath.Join(dir, "knowledge-v1")}, d.cfg.ServerBaseURL, d.cfg.DaemonID, task)
}

func resolveTaskKnowledge(ctx context.Context, store knowledgeSnapshotStore, server, daemonID string, task Task) (*knowledge.Snapshot, error) {
	if task.KnowledgeManifest == nil {
		return nil, nil
	}
	selection := *task.KnowledgeManifest
	if selection.DaemonID != daemonID || selection.WorkspaceID != task.WorkspaceID {
		return nil, errors.New("knowledge reference is unavailable on this machine")
	}
	snapshot, err := store.Resolve(ctx, knowledge.Scope{Server: server, WorkspaceID: task.WorkspaceID, OwnerID: selection.OwnerID, DaemonID: daemonID}, selection)
	if err != nil {
		var pathError *os.PathError
		var linkError *os.LinkError
		if errors.As(err, &pathError) || errors.As(err, &linkError) {
			return nil, errors.New("knowledge snapshot storage is unavailable")
		}
		return nil, err
	}
	total := len(task.ProjectInstructions) + len(task.ProjectDescription) + len(task.WorkspaceContext)
	if task.Agent != nil {
		total += len(task.Agent.Instructions)
		for _, skill := range task.Agent.Skills {
			total += len(skill.Content)
			for _, file := range skill.Files {
				total += len(file.Content)
			}
		}
	}
	for _, entry := range snapshot.Entries {
		for _, f := range entry.Files {
			total += f.SizeBytes
		}
	}
	if total > knowledge.MaxBytes {
		return nil, errors.New("knowledge and task context exceed the session byte limit")
	}
	return &snapshot, nil
}

func (d *Daemon) handleKnowledgeStatus(ctx context.Context, runtimeID string, request knowledge.StatusRequest) {
	if d.findRuntime(runtimeID) == nil {
		return
	}
	ctx, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	results := knowledge.UnverifiedStatuses(request.Bindings, "timeout")
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err == nil && request.DaemonID == d.cfg.DaemonID {
		scope := knowledge.Scope{Server: d.cfg.ServerBaseURL, WorkspaceID: request.WorkspaceID, OwnerID: request.OwnerID, DaemonID: d.cfg.DaemonID}
		checked, checkErr := runKnowledgeResolution(&d.knowledgeResolutions, ctx, func(ctx context.Context) ([]knowledge.SourceStatus, error) {
			return (knowledge.LocalStore{Dir: filepath.Join(dir, "knowledge-v1")}).CheckStatus(ctx, scope, request)
		})
		if checkErr == nil {
			results = checked
		}
	}
	reportCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = d.client.ReportKnowledgeStatus(reportCtx, runtimeID, request.ID, results)
}

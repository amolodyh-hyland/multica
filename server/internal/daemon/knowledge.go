package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/knowledge"
)

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
	return resolveTaskKnowledge(ctx, knowledge.LocalStore{Dir: filepath.Join(dir, "knowledge-v1")}, d.cfg.ServerBaseURL, d.cfg.DaemonID, task)
}

func resolveTaskKnowledge(ctx context.Context, store knowledge.LocalStore, server, daemonID string, task Task) (*knowledge.Snapshot, error) {
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

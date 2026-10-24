package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func (h *Handler) pinClaimPrivateLibrary(ctx context.Context, task *db.AgentTaskQueue, runtime db.AgentRuntime, resp *AgentTaskResponse) (*libraryimport.Selection, error) {
	budget, err := h.Queries.PrivateLibraryTaskBudget(ctx, db.PrivateLibraryTaskBudgetParams{WorkspaceID: runtime.WorkspaceID, TaskID: task.ID})
	if err != nil {
		return nil, err
	}

	if budget.TotalBytes > knowledge.MaxBytes || budget.TotalFiles > 1024 || budget.ImportCount > knowledge.MaxBindings || budget.SerializedPayloadBytes > libraryimport.MaxTaskPayloadBytes || budget.SelectionBytesUpperBound > libraryimport.MaxSelectionBytes-libraryimport.MaxSelectionHeaderBytes {
		return nil, errors.New("runtime_limit")
	}
	count := budget.ImportCount
	raw := task.PrivateLibraryManifest
	if len(raw) > libraryimport.MaxSelectionBytes {
		return nil, errors.New("runtime_limit")
	}
	if len(raw) == 0 && resp.PriorSessionID != "" {
		retained, err := h.Queries.GetSessionPrivateLibraryManifest(ctx, db.GetSessionPrivateLibraryManifestParams{AgentID: task.AgentID, SessionID: pgtype.Text{String: resp.PriorSessionID, Valid: true}, MaxSelectionBytes: libraryimport.MaxSelectionBytes})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if len(retained) > 0 {
			var prior libraryimport.Selection
			if err := json.Unmarshal(retained, &prior); err != nil || prior.Validate() != nil {
				return nil, errors.New("private_selection_invalid")
			}
			if len(prior.Imports) > 0 && prior.TaskID != uuidToString(task.ID) {
				return nil, errors.New("private_resume_requires_explicit_task_approval")
			}
			if len(prior.Imports) > 0 {
				raw = retained
			}
		}
	}

	if len(raw) == 0 && count == 0 {
		sourceID := task.RetryOfTaskID
		if !sourceID.Valid {
			sourceID = task.RerunOfTaskID
		}
		if sourceID.Valid {
			source, err := h.Queries.GetAgentTask(ctx, sourceID)
			if err != nil {
				return nil, err
			}
			if len(source.PrivateLibraryManifest) > libraryimport.MaxSelectionBytes {
				return nil, errors.New("runtime_limit")
			}
			if len(source.PrivateLibraryManifest) > 0 {
				var retained libraryimport.Selection
				if json.Unmarshal(source.PrivateLibraryManifest, &retained) != nil || retained.Validate() != nil {
					return nil, errors.New("private_selection_invalid")
				}
				if len(retained.Imports) > 0 {
					return nil, errors.New("private_retry_requires_explicit_task_approval")
				}
			}
		}
	}
	if len(raw) == 0 && count == 0 && (h.cfg.PrivateImportAuthorityID == "" || !runtime.OwnerID.Valid || !runtime.DaemonID.Valid) {
		return nil, nil
	}
	if len(raw) == 0 && count == 0 {
		destination := libraryimport.Destination{ServerID: h.cfg.PrivateImportAuthorityID, Origin: h.cfg.PublicURL, WorkspaceID: uuidToString(runtime.WorkspaceID), OwnerID: uuidToString(runtime.OwnerID), Audience: "owner_and_explicit_task"}
		if destination.Validate() != nil || !knowledge.ValidID(runtime.Provider) {
			return nil, nil
		}
	}
	var candidate libraryimport.Selection
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return nil, errors.New("private_selection_invalid")
		}
	} else {
		var parent knowledge.Selection
		if err := json.Unmarshal(task.KnowledgeManifest, &parent); err != nil || parent.Validate() != nil {
			return nil, errors.New("private_context_unavailable")
		}
		projectDigest, err := libraryimport.ProjectContextDigest(resp.ProjectID, resp.ProjectTitle, resp.ProjectDescription, resp.ProjectInstructions, parent.Digest, resp.ProjectResources)
		if err != nil {
			return nil, errors.New("private_context_unavailable")
		}
		candidate = libraryimport.Selection{SchemaVersion: 1, SnapshotID: uuidToString(task.ID), TaskID: uuidToString(task.ID), Destination: libraryimport.Destination{ServerID: h.cfg.PrivateImportAuthorityID, Origin: h.cfg.PublicURL, WorkspaceID: uuidToString(runtime.WorkspaceID), OwnerID: uuidToString(runtime.OwnerID), Audience: "owner_and_explicit_task"}, Binding: libraryimport.Binding{ProjectID: resp.ProjectID, TaskID: uuidToString(task.ID), RuntimeID: uuidToString(runtime.ID)}, PersonaID: uuidToString(task.AgentID), ContextDigest: parent.Digest, ProjectContextDigest: projectDigest, Provider: runtime.Provider, AdapterVersion: libraryimport.TaskAdapterVersion, Imports: []libraryimport.SelectedImport{}}
		records, err := h.Queries.ListPrivateLibraryImportsForTask(ctx, db.ListPrivateLibraryImportsForTaskParams{WorkspaceID: runtime.WorkspaceID, TaskID: task.ID, OwnerID: runtime.OwnerID})
		if err != nil {
			return nil, err
		}
		if int64(len(records)) != count {
			return nil, errors.New("private_import_owner_unavailable")
		}
		for _, record := range records {
			input, err := libraryimport.DecodeRequest(record.Payload)
			if err != nil {
				return nil, err
			}
			item := libraryimport.SelectedImport{ObjectID: uuidToString(record.ID), PreviewDigest: record.PreviewDigest, TransportHash: record.TransportHash, Entry: input.Preview.Entry}
			object := libraryimport.Object{Receipt: privateImportReceipt(record, h.cfg.PrivateImportAuthorityID), Selection: input, Bundle: (libraryimport.Payload{Entry: input.Preview.Entry, Content: input.Content}).Bundle(item.ObjectID)}
			if err := object.Verify(candidate, item); err != nil || record.ContentHash != item.Entry.ContentHash {
				return nil, errors.New("private_import_integrity_failure")
			}
			candidate.Imports = append(candidate.Imports, item)
		}
		if len(candidate.Imports)+len(parent.Bindings) > knowledge.MaxBindings {
			return nil, errors.New("runtime_limit")
		}
		candidate, err = candidate.Seal()
		if err != nil {
			return nil, err
		}
	}
	if err := candidate.Validate(); err != nil {
		return nil, errors.New("private_selection_invalid")
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	pinned, err := h.Queries.PinTaskPrivateLibraryManifest(ctx, db.PinTaskPrivateLibraryManifestParams{TaskID: task.ID, RuntimeID: task.RuntimeID, Manifest: encoded})
	if err != nil {
		return nil, err
	}
	var selection libraryimport.Selection
	if len(pinned) > libraryimport.MaxSelectionBytes {
		return nil, errors.New("runtime_limit")
	}
	if err := json.Unmarshal(pinned, &selection); err != nil || selection.Validate() != nil {
		return nil, errors.New("private_selection_invalid")
	}
	return &selection, nil
}

func (h *Handler) prepareClaimPrivateLibrary(r *http.Request, task *db.AgentTaskQueue, runtime db.AgentRuntime, resp *AgentTaskResponse) *claimBuildFailure {
	selection, err := h.pinClaimPrivateLibrary(r.Context(), task, runtime, resp)
	if err == nil && selection == nil {
		return nil
	}
	reason := "private_selection_unavailable"
	if err != nil {
		switch err.Error() {
		case "private_selection_invalid", "private_resume_requires_explicit_task_approval", "private_retry_requires_explicit_task_approval", "private_import_owner_unavailable", "private_import_integrity_failure", "runtime_limit", "private_context_unavailable":
			reason = err.Error()
		default:
			return h.rejectClaimSourceLoad(r.Context(), task, err, "private library", uuidToString(task.ID))
		}
	} else {
		var parent knowledge.Selection
		_ = json.Unmarshal(task.KnowledgeManifest, &parent)
		if len(selection.Imports) == 0 {
			return nil
		}
		projectDigest, digestErr := libraryimport.ProjectContextDigest(resp.ProjectID, resp.ProjectTitle, resp.ProjectDescription, resp.ProjectInstructions, parent.Digest, resp.ProjectResources)
		if digestErr != nil || selection.ProjectContextDigest != projectDigest || selection.TaskID != uuidToString(task.ID) || selection.Destination.WorkspaceID != uuidToString(runtime.WorkspaceID) || selection.Destination.OwnerID != uuidToString(runtime.OwnerID) || selection.Destination.ServerID != h.cfg.PrivateImportAuthorityID || selection.Destination.Origin != h.cfg.PublicURL || selection.Binding.RuntimeID != uuidToString(runtime.ID) || selection.PersonaID != uuidToString(task.AgentID) || selection.Binding.ProjectID != resp.ProjectID || selection.ContextDigest != parent.Digest || selection.Provider != runtime.Provider {
			reason = "private_selection_identity_mismatch"
		} else if !requestHasClientCapability(r, protocol.DaemonCapabilityPrivateLibraryV1) {
			reason = "private_import_unsupported_daemon"
		} else {
			records, loadErr := h.Queries.ListPrivateLibraryImportsForTask(r.Context(), db.ListPrivateLibraryImportsForTaskParams{WorkspaceID: runtime.WorkspaceID, TaskID: task.ID, OwnerID: runtime.OwnerID})
			if loadErr != nil || len(records) != len(selection.Imports) {
				reason = "private_import_owner_unavailable"
			} else {
				resp.KnowledgeManifest = &parent
				resp.PrivateLibraryManifest = selection
				return nil
			}
		}
	}
	// Do not expose database or source diagnostics in the claim response.
	return h.failClaimedTaskBeforeLaunch(r.Context(), task, reason, taskfailure.ReasonInvalidTaskIdentity, "private_library_unavailable", http.StatusUnprocessableEntity, "Private library selection is unavailable; explicit task approval or a new runtime may be required.")
}

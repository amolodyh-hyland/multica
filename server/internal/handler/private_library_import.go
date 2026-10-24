package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

func (h *Handler) privateImportMember(w http.ResponseWriter, r *http.Request) (db.Member, bool) {
	credential, authenticated := middleware.AuthCredentialFromContext(r.Context())
	if authenticated && credential.Kind != middleware.AuthCredentialSessionJWT && credential.Kind != middleware.AuthCredentialJWT && credential.Kind != middleware.AuthCredentialPersonalAccess {
		writeError(w, http.StatusForbidden, "private imports require their human owner")
		return db.Member{}, false
	}
	if isMachineCredentialActor(r) || middleware.DaemonIDFromContext(r.Context()) != "" {
		writeError(w, http.StatusForbidden, "private imports require their human owner")
		return db.Member{}, false
	}
	return h.requireWorkspaceMember(w, r, h.resolveWorkspaceID(r), "workspace not found")
}

func (h *Handler) privateImportDestination(r *http.Request, member db.Member) (libraryimport.Destination, bool) {
	d := libraryimport.Destination{ServerID: h.cfg.PrivateImportAuthorityID, Origin: h.cfg.PublicURL, WorkspaceID: uuidToString(member.WorkspaceID), OwnerID: uuidToString(member.UserID), Audience: "owner_and_explicit_task"}
	if d.Validate() != nil || h.DB == nil || h.Queries == nil || h.TxStarter == nil {
		return d, false
	}
	var ready bool
	err := h.DB.QueryRow(r.Context(), `SELECT to_regclass('private_library_import') IS NOT NULL AND
 (SELECT count(*) FROM pg_index WHERE indexrelid IN (
 to_regclass('private_library_import_id_idx'), to_regclass('private_library_import_attempt_idx'),
 to_regclass('private_library_import_task_idx')) AND indrelid = to_regclass('private_library_import') AND indisvalid AND indisready) = 3 AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('agent_task_queue') AND attname='private_library_manifest' AND NOT attisdropped)`).Scan(&ready)
	return d, err == nil && ready
}

func (h *Handler) PrivateLibraryCapabilities(w http.ResponseWriter, r *http.Request) {
	member, ok := h.privateImportMember(w, r)
	if !ok {
		return
	}
	destination, supported := h.privateImportDestination(r, member)
	cap := libraryimport.Capability{Supported: supported, Destination: destination}
	if !supported {
		cap.ReasonCode = "private_import_unsupported"
	}
	writeJSON(w, http.StatusOK, cap)
}

func (h *Handler) ImportPrivateLibrary(w http.ResponseWriter, r *http.Request) {
	member, ok := h.privateImportMember(w, r)
	if !ok {
		return
	}
	destination, supported := h.privateImportDestination(r, member)
	if !supported {
		writeError(w, http.StatusNotImplemented, "private_import_unsupported")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, libraryimport.MaxRequestBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_limit")
		return
	}
	input, err := libraryimport.DecodeRequest(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_private_import")
		return
	}
	if input.Preview.Destination != destination {
		writeError(w, http.StatusConflict, "destination_changed")
		return
	}
	if err := input.VerifyConsent(time.Now()); err != nil {
		writeError(w, http.StatusForbidden, "consent_mismatch_or_expired")
		return
	}
	if err := input.Verify(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_or_unsupported_payload")
		return
	}
	binding := input.Preview.Binding
	taskID, ok := parseUUIDOrBadRequest(w, binding.TaskID, "task_id")
	if !ok {
		return
	}
	runtimeID, ok := parseUUIDOrBadRequest(w, binding.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	projectID, ok := parseUUIDOrBadRequest(w, binding.ProjectID, "project_id")
	if !ok {
		return
	}
	var personaID pgtype.UUID
	if binding.PersonaRef != nil {
		personaID, ok = parseUUIDOrBadRequest(w, binding.PersonaRef.PersonaID, "persona_id")
		if !ok {
			return
		}
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err := q.LockWorkspaceForChatSessionCreate(ctx, member.WorkspaceID); err != nil {
		privateImportDBError(w, err)
		return
	}
	if _, err := q.LockPublicDelegationMember(ctx, db.LockPublicDelegationMemberParams{WorkspaceID: member.WorkspaceID, UserID: member.UserID}); err != nil {
		privateImportDBError(w, err)
		return
	}
	objectID := dbid.NewV7()
	_, transport := (libraryimport.Payload{Entry: input.Preview.Entry, Content: input.Content}).Transport(uuidToString(objectID))
	record, err := q.CreatePrivateLibraryImport(ctx, db.CreatePrivateLibraryImportParams{ID: objectID, WorkspaceID: member.WorkspaceID, OwnerID: member.UserID, AttemptID: input.Preview.AttemptID, TaskID: taskID, RuntimeID: runtimeID, ProjectID: projectID, PreviewDigest: input.Consent.PreviewDigest, ContentHash: input.Preview.Entry.ContentHash, TransportHash: transport.Hash, Payload: raw})
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = q.GetPrivateLibraryImportByAttempt(ctx, db.GetPrivateLibraryImportByAttemptParams{WorkspaceID: member.WorkspaceID, OwnerID: member.UserID, AttemptID: input.Preview.AttemptID})
		if err != nil {
			privateImportDBError(w, err)
			return
		}
		if record.PreviewDigest != input.Consent.PreviewDigest {
			writeError(w, http.StatusConflict, "idempotency_conflict")
			return
		}
		writeJSON(w, http.StatusOK, privateImportReceipt(record, destination.ServerID))
		return
	}
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	task, err := q.LockPrivateLibraryImportTask(ctx, db.LockPrivateLibraryImportTaskParams{WorkspaceID: member.WorkspaceID, OwnerID: member.UserID, ProjectID: projectID, TaskID: taskID, RuntimeID: runtimeID})
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	if record.AgentID != task.AgentID || (personaID.Valid && personaID != task.AgentID) {
		writeError(w, http.StatusConflict, "persona_binding_changed")
		return
	}
	budget, err := q.PrivateLibraryTaskBudget(ctx, db.PrivateLibraryTaskBudgetParams{WorkspaceID: member.WorkspaceID, TaskID: taskID})
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	if budget.TotalBytes > knowledge.MaxBytes || budget.TotalFiles > 1024 {
		writeError(w, http.StatusUnprocessableEntity, "runtime_limit")
		return
	}
	count := budget.ImportCount
	resources, err := q.ListProjectResourcesInWorkspace(ctx, db.ListProjectResourcesInWorkspaceParams{ProjectID: projectID, WorkspaceID: member.WorkspaceID})
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	for _, resource := range resources {
		if knowledge.IsType(resource.ResourceType) {
			count++
		}
	}
	if count > knowledge.MaxBindings {
		writeError(w, http.StatusUnprocessableEntity, "binding_limit")
		return
	}
	if err := input.VerifyConsent(time.Now()); err != nil {
		writeError(w, http.StatusForbidden, "consent_mismatch_or_expired")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		privateImportDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, privateImportReceipt(record, destination.ServerID))
}

func privateImportDBError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "private import binding unavailable")
		return
	}
	writeError(w, http.StatusInternalServerError, "private import unavailable")
}

func privateImportReceipt(record db.PrivateLibraryImport, authorityID string) libraryimport.Receipt {
	return libraryimport.Receipt{ObjectID: uuidToString(record.ID), AuthorityID: authorityID, ApprovalRef: "private-import:" + uuidToString(record.ID), PreviewDigest: record.PreviewDigest, ContentHash: record.ContentHash, TransportHash: record.TransportHash, Status: "stored", ReasonCode: "unsupported_provider"}
}

func (h *Handler) GetPrivateLibraryImport(w http.ResponseWriter, r *http.Request) {
	member, ok := h.privateImportMember(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "import_id")
	if !ok {
		return
	}
	record, err := h.Queries.GetPrivateLibraryImportForOwner(r.Context(), db.GetPrivateLibraryImportForOwnerParams{ID: id, WorkspaceID: member.WorkspaceID, OwnerID: member.UserID})
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	h.writePrivateLibraryObject(w, record)
}

func (h *Handler) GetTaskPrivateLibraryImport(w http.ResponseWriter, r *http.Request) {
	credential, authenticated := middleware.AuthCredentialFromContext(r.Context())
	taskRef := chi.URLParam(r, "taskId")
	if !authenticated || credential.Kind != middleware.AuthCredentialTaskToken || r.Header.Get("X-Task-ID") != taskRef {
		writeError(w, http.StatusForbidden, "private task access requires its exact invocation token")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "import_id")
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, taskRef, "task_id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Agent-ID"), "persona_id")
	if !ok {
		return
	}
	ownerID, ok := parseUUIDOrBadRequest(w, requestUserID(r), "owner_id")
	if !ok {
		return
	}
	wsID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	record, err := h.Queries.GetPrivateLibraryImportForTask(r.Context(), db.GetPrivateLibraryImportForTaskParams{ID: id, WorkspaceID: wsID, TaskID: taskID, AgentID: agentID, OwnerID: ownerID})
	if err != nil {
		privateImportDBError(w, err)
		return
	}
	h.writePrivateLibraryObject(w, record)
}

func (h *Handler) writePrivateLibraryObject(w http.ResponseWriter, record db.PrivateLibraryImport) {
	input, err := libraryimport.DecodeRequest(record.Payload)
	if err != nil || input.Preview.Validate() != nil || input.Verify() != nil || input.Preview.Destination.WorkspaceID != uuidToString(record.WorkspaceID) || input.Preview.Destination.OwnerID != uuidToString(record.OwnerID) || input.Preview.Binding.TaskID != uuidToString(record.TaskID) || input.Preview.Binding.RuntimeID != uuidToString(record.RuntimeID) || input.Preview.Binding.ProjectID != uuidToString(record.ProjectID) || input.Consent.ActorID != uuidToString(record.OwnerID) || input.Consent.PreviewDigest != record.PreviewDigest || input.Preview.Entry.ContentHash != record.ContentHash || input.Preview.Destination.ServerID != h.cfg.PrivateImportAuthorityID || input.Preview.Destination.Origin != h.cfg.PublicURL {
		writeError(w, http.StatusConflict, "private_import_integrity_failure")
		return
	}
	digest, err := input.Preview.Digest()
	payload := libraryimport.Payload{Entry: input.Preview.Entry, Content: input.Content}
	bundle := payload.Bundle(uuidToString(record.ID))
	if err != nil || digest != record.PreviewDigest || bundle.Hash != record.TransportHash {
		writeError(w, http.StatusConflict, "private_import_integrity_failure")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Private-Import-Preview-Digest", record.PreviewDigest)
	w.Header().Set("X-Private-Import-Content-Hash", record.ContentHash)
	w.Header().Set("X-Private-Import-Transport-Hash", record.TransportHash)
	writeJSON(w, http.StatusOK, struct {
		Receipt   libraryimport.Receipt `json:"receipt"`
		Selection json.RawMessage       `json:"selection"`
		Bundle    libraryimport.Bundle  `json:"bundle"`
	}{Receipt: privateImportReceipt(record, h.cfg.PrivateImportAuthorityID), Selection: record.Payload, Bundle: bundle})
}

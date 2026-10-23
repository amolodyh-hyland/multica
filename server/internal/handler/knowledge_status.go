package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func knowledgeStatusRuntimeMatches(rt db.AgentRuntime, request knowledge.StatusRequest) bool {
	return uuidToString(rt.WorkspaceID) == request.WorkspaceID && uuidToString(rt.OwnerID) == request.OwnerID && rt.DaemonID.String == request.DaemonID && runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityKnowledgeStatusV1) && runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityKnowledgeV1)
}
func (h *Handler) knowledgeStatusOwnerMember(ctx context.Context, request knowledge.StatusRequest) bool {
	_, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(request.OwnerID), WorkspaceID: parseUUID(request.WorkspaceID)})
	return err == nil
}
func (h *Handler) CheckProjectKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	if isMachineCredentialActor(r) || middleware.DaemonIDFromContext(r.Context()) != "" {
		writeError(w, 403, "knowledge status requires its human owner")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var input struct {
		ResourceIDs []string `json:"resource_ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.ResourceIDs) == 0 || len(input.ResourceIDs) > knowledge.MaxStatusBindings {
		writeError(w, 400, "select between 1 and 8 knowledge references")
		return
	}
	resources, err := h.Queries.ListProjectResourcesInWorkspace(r.Context(), db.ListProjectResourcesInWorkspaceParams{ProjectID: project.ID, WorkspaceID: project.WorkspaceID})
	if err != nil {
		writeError(w, 500, "could not load knowledge references")
		return
	}
	request := knowledge.StatusRequest{ID: randomID(), WorkspaceID: uuidToString(project.WorkspaceID), OwnerID: userID, Deadline: time.Now().Add(30 * time.Second)}
	seen := map[string]bool{}
	for _, id := range input.ResourceIDs {
		if seen[id] {
			writeError(w, 400, "duplicate knowledge reference")
			return
		}
		seen[id] = true
		var resource *db.ProjectResource
		for i := range resources {
			if uuidToString(resources[i].ID) == id {
				resource = &resources[i]
				break
			}
		}
		if resource == nil {
			writeError(w, 404, "knowledge reference not found")
			return
		}
		ref, err := knowledge.DecodeReference(resource.ResourceType, resource.ResourceRef)
		if err != nil {
			writeError(w, 400, "invalid knowledge reference")
			return
		}
		if ref.OwnerID != userID {
			writeError(w, 403, "knowledge reference belongs to another owner")
			return
		}
		if request.DaemonID != "" && request.DaemonID != ref.DaemonID {
			writeError(w, 400, "select references from one owner machine")
			return
		}
		request.DaemonID = ref.DaemonID
		request.Bindings = append(request.Bindings, knowledge.Binding{ID: id, Kind: resource.ResourceType, Reference: ref})
	}
	if !h.knowledgeStatusOwnerMember(r.Context(), request) {
		writeError(w, 403, "knowledge owner is not a workspace member")
		return
	}
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), project.WorkspaceID)
	if err != nil {
		writeError(w, 500, "could not load owner machine")
		return
	}
	var latest *db.AgentRuntime
	for i := range runtimes {
		rt := &runtimes[i]
		if rt.DaemonID.String != request.DaemonID || uuidToString(rt.OwnerID) != userID {
			continue
		}
		if latest == nil || rt.LastSeenAt.Time.After(latest.LastSeenAt.Time) || (rt.LastSeenAt.Time.Equal(latest.LastSeenAt.Time) && rt.Status == "online" && latest.Status != "online") {
			latest = rt
		}
	}
	if results := knowledge.StatusFallback(request.Bindings, latest != nil && latest.Status == "online", latest != nil && knowledgeStatusRuntimeMatches(*latest, request)); results != nil {
		writeJSON(w, 200, map[string]any{"results": results})
		return
	}
	runtimeID := uuidToString(latest.ID)
	created, err := h.KnowledgeStatusStore.create(r.Context(), runtimeID, request)
	if err != nil || !created {
		writeJSON(w, 200, map[string]any{"results": knowledge.UnverifiedStatuses(request.Bindings, "busy")})
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		h.KnowledgeStatusStore.remove(ctx, runtimeID, request.ID)
	}()
	h.requestDaemonPendingWork(runtimeID, "knowledge_status")
	ctx, cancel := context.WithDeadline(r.Context(), request.Deadline)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			writeJSON(w, 200, map[string]any{"results": knowledge.UnverifiedStatuses(request.Bindings, "timeout")})
			return
		case <-ticker.C:
			record, _, err := h.KnowledgeStatusStore.read(ctx, runtimeID)
			if err != nil {
				writeJSON(w, 200, map[string]any{"results": knowledge.UnverifiedStatuses(request.Bindings, "unreadable")})
				return
			}
			if record != nil && record.Request.ID == request.ID && record.State == "completed" {
				if !h.knowledgeStatusOwnerMember(ctx, request) {
					writeError(w, 403, "knowledge owner is not a workspace member")
					return
				}
				writeJSON(w, 200, map[string]any{"results": record.Results})
				return
			}
		}
	}
}

func (h *Handler) takeKnowledgeStatus(ctx context.Context, runtimeID, daemonID, userID string) *knowledge.StatusRequest {
	if h.KnowledgeStatusStore == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	pending, _, err := h.KnowledgeStatusStore.read(ctx, runtimeID)
	if err != nil || pending == nil || pending.State != "pending" {
		return nil
	}
	rt, err := h.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
	if err != nil || !knowledgeStatusActorMatches(rt, daemonID, userID) || !knowledgeStatusRuntimeMatches(rt, pending.Request) || !h.knowledgeStatusOwnerMember(ctx, pending.Request) {
		return nil
	}
	claimed, err := h.KnowledgeStatusStore.take(ctx, runtimeID)
	if err != nil || claimed == nil || claimed.Request.ID != pending.Request.ID || !knowledgeStatusRuntimeMatches(rt, claimed.Request) {
		return nil
	}
	return &claimed.Request
}

func (h *Handler) ReportKnowledgeStatus(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	runtimeID = uuidToString(rt.ID)
	if isMachineCredentialActor(r) || !knowledgeStatusActorMatches(rt, middleware.DaemonIDFromContext(r.Context()), requestUserID(r)) || !requestHasClientCapability(r, protocol.DaemonCapabilityKnowledgeStatusV1) {
		writeError(w, 403, "knowledge status daemon denied")
		return
	}
	record, _, err := h.KnowledgeStatusStore.read(r.Context(), runtimeID)
	if err != nil {
		writeError(w, 500, "could not load status request")
		return
	}
	if record == nil || record.State != "running" || record.Request.ID != chi.URLParam(r, "requestId") {
		writeError(w, 404, "status request not found")
		return
	}
	if !knowledgeStatusRuntimeMatches(rt, record.Request) || !h.knowledgeStatusOwnerMember(r.Context(), record.Request) {
		writeError(w, 403, "knowledge status scope denied")
		return
	}
	var body struct {
		Results []knowledge.SourceStatus `json:"results"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || !validKnowledgeStatusResults(record.Request, body.Results) {
		writeError(w, 400, "invalid knowledge status metadata")
		return
	}
	saved, err := h.KnowledgeStatusStore.finish(r.Context(), runtimeID, record.Request.ID, body.Results)
	if err != nil || !saved {
		writeError(w, 409, "status request no longer active")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func validKnowledgeStatusResults(request knowledge.StatusRequest, results []knowledge.SourceStatus) bool {
	if len(results) != len(request.Bindings) {
		return false
	}
	totalBytes := 0
	for i, result := range results {
		b := request.Bindings[i]
		if result.ResourceID != b.ID || result.ReferenceHash != b.Reference.ContentHash {
			return false
		}
		switch result.Status {
		case "available", "stale", "missing", "unverified", "incompatible":
		default:
			return false
		}
		switch result.Reason {
		case "checked", "changed", "missing", "unreadable", "timeout", "limit", "unsupported_source":
		default:
			return false
		}
		if result.Type != "" && result.Type != "file" && result.Type != "folder" && result.Type != "skill" {
			return false
		}
		if result.SizeBytes != nil && (*result.SizeBytes < 0 || *result.SizeBytes > knowledge.MaxBytes) {
			return false
		}
		if result.SizeBytes != nil {
			totalBytes += *result.SizeBytes
			if totalBytes > knowledge.MaxBytes {
				return false
			}
		}
		if result.Status == "available" && (result.Exists == nil || !*result.Exists || result.SizeBytes == nil || result.Type == "" || result.ContentHash != result.ReferenceHash) {
			return false
		}
		if result.Status == "missing" && (result.Exists == nil || *result.Exists) {
			return false
		}
		if result.Status == "stale" && (result.Exists == nil || !*result.Exists) {
			return false
		}
		if result.ContentHash != "" {
			ref := b.Reference
			ref.ContentHash = result.ContentHash
			if ref.Validate(b.Kind) != nil {
				return false
			}
		}
	}
	return true
}

func knowledgeStatusActorMatches(rt db.AgentRuntime, daemonID, userID string) bool {
	if daemonID != "" {
		return daemonID == rt.DaemonID.String
	}
	return userID != "" && userID == uuidToString(rt.OwnerID)
}

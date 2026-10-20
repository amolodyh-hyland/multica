package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func publicReadMember(w http.ResponseWriter, r *http.Request, scope string) (db.Member, bool) {
	credential, ok := middleware.PublicAPIUserCredentialFromContext(r.Context())
	if !ok || credential.Credential != publicapiv1.CredentialPersonalAccess {
		publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "unauthorized", "a scoped member PAT is required")
		return db.Member{}, false
	}
	if !slices.Contains(credential.Scopes, scope) {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "insufficient_scope", "the credential lacks the "+scope+" scope")
		return db.Member{}, false
	}
	member, ok := middleware.MemberFromContext(r.Context())
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if !ok || workspaceID == "" || workspaceID != credential.WorkspaceID ||
		uuidToString(member.WorkspaceID) != workspaceID || uuidToString(member.UserID) != credential.UserID {
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", "workspace membership not found")
		return db.Member{}, false
	}
	return member, true
}

type publicReadCursor struct {
	User       string `json:"u"`
	Workspace  string `json:"w"`
	Collection string `json:"c"`
	Archived   bool   `json:"a"`
	After      string `json:"p"`
}

func publicReadPage(w http.ResponseWriter, r *http.Request, member db.Member, collection string, archived bool) (int, publicReadCursor, bool) {
	cursor := publicReadCursor{User: uuidToString(member.UserID), Workspace: uuidToString(member.WorkspaceID), Collection: collection, Archived: archived}
	limit := publicapiv1.DefaultPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > publicapiv1.MaxPageSize {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return 0, cursor, false
		}
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 1024 {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return 0, cursor, false
		}
		var decoded publicReadCursor
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &decoded) != nil ||
			decoded.User != cursor.User || decoded.Workspace != cursor.Workspace || decoded.Collection != collection || decoded.Archived != archived {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return 0, cursor, false
		}
		if _, err := util.ParseUUID(decoded.After); err != nil {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return 0, cursor, false
		}
		cursor.After = decoded.After
	}
	return limit, cursor, true
}

func (c publicReadCursor) next(after string) publicapiv1.PageInfo {
	c.After = after
	data, _ := json.Marshal(c)
	return publicapiv1.PageInfo{NextCursor: base64.RawURLEncoding.EncodeToString(data)}
}

func publicProfileProjection(a db.Agent, targets []db.AgentInvocationTarget, member db.Member, runtime db.AgentRuntime, now time.Time) publicapiv1.Agent {
	userID := uuidToString(member.UserID)
	out := publicapiv1.Agent{
		AgentID: uuidToString(a.ID), WorkspaceID: uuidToString(a.WorkspaceID), Name: a.Name,
		RuntimeBound: a.RuntimeID.Valid, PermissionMode: a.PermissionMode,
		InvocationTargets: []publicapiv1.InvocationTarget{}, Archived: a.ArchivedAt.Valid,
		Availability: "unbound",
	}
	if a.PermissionMode == permissionModePublicTo {
		for _, target := range targets {
			out.InvocationTargets = append(out.InvocationTargets, publicapiv1.InvocationTarget{TargetType: target.TargetType, TargetID: uuidToPtr(target.TargetID)})
		}
	}
	out.CanInvoke = !out.Archived && ((userID != "" && uuidToString(a.OwnerID) == userID) ||
		(a.PermissionMode == permissionModePublicTo && memberHitsInvocationTargets(targets, userID)))
	if a.RuntimeID.Valid {
		out.Availability = "unavailable"
		// A visible profile may refer to a hidden runtime, but never one in
		// another workspace. Only its stable reference and coarse health escape.
		if runtime.ID.Valid && runtime.ID == a.RuntimeID && runtime.WorkspaceID == a.WorkspaceID {
			out.RuntimeID = uuidToString(runtime.ID)
			out.Availability = deriveAgentRuntimeAvailability(runtime, now)
		}
	}
	if out.Archived {
		out.Availability = "archived"
	}
	return out
}

func (h *Handler) ListPublicAgents(w http.ResponseWriter, r *http.Request) {
	member, ok := publicReadMember(w, r, "agents:read")
	if !ok {
		return
	}
	includeArchived := false
	if raw := r.URL.Query().Get("include_archived"); raw != "" {
		var err error
		includeArchived, err = strconv.ParseBool(raw)
		if err != nil {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "include_archived must be a boolean")
			return
		}
	}
	limit, cursor, ok := publicReadPage(w, r, member, "agents", includeArchived)
	if !ok {
		return
	}
	agents, err := h.Queries.ListAllAgents(r.Context(), member.WorkspaceID)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	targets, ok := h.loadInvocationTargetsByAgent(r.Context(), agents)
	if !ok {
		publicReadFailure(w, r)
		return
	}
	slices.SortFunc(agents, func(a, b db.Agent) int { return slices.Compare(a.ID.Bytes[:], b.ID.Bytes[:]) })
	page := make([]db.Agent, 0, limit)
	info := publicapiv1.PageInfo{}
	for _, a := range agents {
		id := uuidToString(a.ID)
		if id <= cursor.After || (!includeArchived && a.ArchivedAt.Valid) || !memberAllowedToViewAgent(a, targets[id], uuidToString(member.UserID), member.Role) {
			continue
		}
		if len(page) == limit {
			info = cursor.next(uuidToString(page[len(page)-1].ID))
			break
		}
		page = append(page, a)
	}
	ids := make([]pgtype.UUID, 0, len(page))
	for _, a := range page {
		if a.RuntimeID.Valid {
			ids = append(ids, a.RuntimeID)
		}
	}
	runtimes, err := h.Queries.GetAgentRuntimes(r.Context(), ids)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	byID := make(map[pgtype.UUID]db.AgentRuntime, len(runtimes))
	for _, runtime := range runtimes {
		byID[runtime.ID] = runtime
	}
	out := publicapiv1.AgentListResponse{Agents: make([]publicapiv1.Agent, 0, len(page)), PageInfo: info}
	now := time.Now()
	for _, a := range page {
		out.Agents = append(out.Agents, publicProfileProjection(a, targets[uuidToString(a.ID)], member, byID[a.RuntimeID], now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GetPublicAgent(w http.ResponseWriter, r *http.Request) {
	member, ok := publicReadMember(w, r, "agents:read")
	if !ok {
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "agent_id"))
	if err != nil {
		publicReadNotFound(w, r)
		return
	}
	a, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: member.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		publicReadNotFound(w, r)
		return
	}
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	targets, err := h.Queries.ListAgentInvocationTargets(r.Context(), a.ID)
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if !memberAllowedToViewAgent(a, targets, uuidToString(member.UserID), member.Role) {
		publicReadNotFound(w, r)
		return
	}
	var runtime db.AgentRuntime
	if a.RuntimeID.Valid {
		runtime, err = h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{ID: a.RuntimeID, WorkspaceID: member.WorkspaceID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			publicReadFailure(w, r)
			return
		}
	}
	writeJSON(w, http.StatusOK, publicProfileProjection(a, targets, member, runtime, time.Now()))
}

func publicRuntimeProjection(runtime db.AgentRuntime, now time.Time) publicapiv1.Runtime {
	return publicapiv1.Runtime{RuntimeID: uuidToString(runtime.ID), WorkspaceID: uuidToString(runtime.WorkspaceID), Availability: deriveAgentRuntimeAvailability(runtime, now)}
}

func (h *Handler) ListPublicRuntimes(w http.ResponseWriter, r *http.Request) {
	member, ok := publicReadMember(w, r, "runtimes:read")
	if !ok {
		return
	}
	limit, cursor, ok := publicReadPage(w, r, member, "runtimes", false)
	if !ok {
		return
	}
	runtimes, err := h.Queries.ListVisibleAgentRuntimes(r.Context(), db.ListVisibleAgentRuntimesParams{WorkspaceID: member.WorkspaceID, OwnerID: member.UserID})
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	slices.SortFunc(runtimes, func(a, b db.AgentRuntime) int { return slices.Compare(a.ID.Bytes[:], b.ID.Bytes[:]) })
	out := publicapiv1.RuntimeListResponse{Runtimes: []publicapiv1.Runtime{}}
	now := time.Now()
	for _, runtime := range runtimes {
		if uuidToString(runtime.ID) <= cursor.After || !canUseRuntimeForAgent(member, runtime) {
			continue
		}
		if len(out.Runtimes) == limit {
			out.PageInfo = cursor.next(out.Runtimes[len(out.Runtimes)-1].RuntimeID)
			break
		}
		out.Runtimes = append(out.Runtimes, publicRuntimeProjection(runtime, now))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GetPublicRuntime(w http.ResponseWriter, r *http.Request) {
	member, ok := publicReadMember(w, r, "runtimes:read")
	if !ok {
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "runtime_id"))
	if err != nil {
		publicReadNotFound(w, r)
		return
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{ID: id, WorkspaceID: member.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		publicReadNotFound(w, r)
		return
	}
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	if !canUseRuntimeForAgent(member, runtime) {
		publicReadNotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, publicRuntimeProjection(runtime, time.Now()))
}

func publicReadNotFound(w http.ResponseWriter, r *http.Request) {
	publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", "resource not found")
}

func publicReadFailure(w http.ResponseWriter, r *http.Request) {
	publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to read resource")
}

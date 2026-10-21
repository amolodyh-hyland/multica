package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func (h *Handler) publicDelegationMember(w http.ResponseWriter, r *http.Request, scope string) (db.Member, bool) {
	member, ok := publicReadMember(w, r, scope)
	if !ok {
		return db.Member{}, false
	}
	if !featureflags.PublicDelegationsV1Enabled(r.Context(), h.FeatureFlags) {
		publicReadNotFound(w, r)
		return db.Member{}, false
	}
	return member, true
}

func (h *Handler) ListPublicDelegations(w http.ResponseWriter, r *http.Request) {
	member, ok := h.publicDelegationMember(w, r, "delegations:read")
	if !ok {
		return
	}
	key := r.URL.Query().Get("idempotency_key")
	if _, present := r.URL.Query()["idempotency_key"]; (present && strings.TrimSpace(key) == "") || len(key) > publicapiv1.MaxIdempotencyBytes {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid idempotency_key")
		return
	}
	var id pgtype.UUID
	if raw := r.URL.Query().Get("delegation_id"); raw != "" {
		var err error
		id, err = util.ParseUUID(raw)
		if err != nil {
			publicReadNotFound(w, r)
			return
		}
	}
	filters, _ := json.Marshal([]string{key, uuidToString(id)})
	hash := sha256.Sum256(filters)
	limit, cursor, ok := publicReadPage(w, r, member, "delegations:"+hex.EncodeToString(hash[:]), false)
	if !ok {
		return
	}
	var after pgtype.UUID
	if cursor.After != "" {
		after, _ = util.ParseUUID(cursor.After)
	}
	rows, err := h.Queries.ListPublicDelegationStatuses(r.Context(), db.ListPublicDelegationStatusesParams{
		WorkspaceID: member.WorkspaceID, RequesterID: member.UserID,
		IdempotencyKey: pgtype.Text{String: key, Valid: key != ""}, DelegationID: id,
		AfterID: after, PageLimit: int32(limit + 1),
	})
	if err != nil {
		publicReadFailure(w, r)
		return
	}
	out := publicapiv1.DelegationListResponse{Delegations: []publicapiv1.Delegation{}}
	for _, row := range rows {
		if len(out.Delegations) == limit {
			out.PageInfo = cursor.next(out.Delegations[len(out.Delegations)-1].DelegationID)
			break
		}
		out.Delegations = append(out.Delegations, publicDelegationProjection(row.PublicDelegation, row.Status, row.FailureReason))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) loadPublicDelegation(w http.ResponseWriter, r *http.Request, member db.Member) (db.PublicDelegation, publicapiv1.Delegation, bool) {
	id, err := util.ParseUUID(chi.URLParam(r, "delegation_id"))
	if err != nil {
		publicReadNotFound(w, r)
		return db.PublicDelegation{}, publicapiv1.Delegation{}, false
	}
	row, err := h.Queries.GetPublicDelegationStatusForActor(r.Context(), db.GetPublicDelegationStatusForActorParams{
		ID: id, WorkspaceID: member.WorkspaceID, RequesterID: member.UserID, IsAdmin: member.Role == "admin" || member.Role == "owner",
	})
	if err != nil {
		publicDelegationReadError(w, r, err)
		return db.PublicDelegation{}, publicapiv1.Delegation{}, false
	}
	return row.PublicDelegation, publicDelegationProjection(row.PublicDelegation, row.Status, row.FailureReason), true
}

func writePublicDelegation(w http.ResponseWriter, out publicapiv1.Delegation) {
	w.Header().Set("ETag", "\""+out.Revision+"\"")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GetPublicDelegation(w http.ResponseWriter, r *http.Request) {
	member, ok := h.publicDelegationMember(w, r, "delegations:read")
	if !ok {
		return
	}
	_, out, ok := h.loadPublicDelegation(w, r, member)
	if ok {
		writePublicDelegation(w, out)
	}
}

func (h *Handler) CancelPublicDelegation(w http.ResponseWriter, r *http.Request) {
	member, ok := h.publicDelegationMember(w, r, "delegations:write")
	if !ok {
		return
	}
	d, out, ok := h.loadPublicDelegation(w, r, member)
	if !ok {
		return
	}
	if out.Status == "unavailable" {
		if !publicDelegationPrecondition(w, r, out) {
			return
		}
		writePublicDelegation(w, out)
		return
	}
	if h.TaskService == nil {
		publicReadFailure(w, r)
		return
	}
	result, err := h.TaskService.CancelPublicDelegation(r.Context(), d, member.UserID, r.Header.Get(publicapiv1.HeaderIfMatch))
	switch {
	case errors.Is(err, service.ErrPublicDelegationExecutionUnavailable):
		_, current, ok := h.loadPublicDelegation(w, r, member)
		if ok && publicDelegationPrecondition(w, r, current) {
			writePublicDelegation(w, current)
		}
	case errors.Is(err, service.ErrPublicDelegationNotFound):
		publicReadNotFound(w, r)
	case errors.Is(err, service.ErrPublicDelegationRevision):
		publicapiv1.WriteProblem(w, r, http.StatusConflict, "revision_conflict", "delegation status changed; reconcile before retrying")
	case err != nil:
		publicReadFailure(w, r)
	default:
		writePublicDelegation(w, publicDelegationProjection(d, result.Task.Status, result.Task.FailureReason.String))
	}
}

func publicDelegationPrecondition(w http.ResponseWriter, r *http.Request, out publicapiv1.Delegation) bool {
	expected := r.Header.Get(publicapiv1.HeaderIfMatch)
	if expected != "" && expected != "*" && expected != "\""+out.Revision+"\"" {
		publicapiv1.WriteProblem(w, r, http.StatusConflict, "revision_conflict", "delegation status changed; reconcile before retrying")
		return false
	}
	return true
}

package http

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/domain/scheduling"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
)

// Operator controls on a staff member (ADR-0015 / design/13):
//   - state:    POST  /staff/{staffId}/state
//   - rotation: PATCH /staff/{staffId}/rotation
//   - position: POST  /queues/{queueId}/staff/{staffId}/move
//
// All three are single-shop, transactional and audited with the logged-in user
// as actor. None of them changes the engine's automatic transitions.

func (h *Handler) SetStaffState(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req struct {
		State   string `json:"state"`
		Reason  string `json:"reason"`
		Version int    `json:"version"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if req.Version < 0 {
		badRequest(c, errors.New("version must not be negative"))
		return
	}
	target := scheduling.ExecutorState(strings.TrimSpace(req.State))
	if !scheduling.IsManualTarget(target) {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "state", Reason: "state must be one of available, standby, time_reserved, overtime, leave, off_duty"}))
		return
	}
	if utf8.RuneCountInString(req.Reason) > 200 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "reason", Reason: "reason must be at most 200 characters"}))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.SetStaffState(c.Request.Context(), tx, p.ShopID, postgres.StaffStateInput{
			StaffPub: c.Param("staffId"), Target: target,
			Reason: strings.TrimSpace(req.Reason), Version: req.Version,
			ActorID: p.UserID,
		})
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) SetStaffRotation(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req struct {
		StopRotation *bool `json:"stop_rotation"`
		Version      int   `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.StopRotation == nil {
		badRequest(c, errors.New("stop_rotation is required"))
		return
	}
	if req.Version < 0 {
		badRequest(c, errors.New("version must not be negative"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.SetStaffRotation(c.Request.Context(), tx, p.ShopID,
			c.Param("staffId"), *req.StopRotation, req.Version, p.UserID)
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) MoveQueueStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		To    string `json:"to"`
		Index int    `json:"index"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if !slices.Contains([]string{"head", "tail", "index"}, req.To) {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "to", Reason: "to must be head, tail or index"}))
		return
	}
	if req.To == "index" && req.Index <= 0 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "index", Reason: "index must be a positive integer"}))
		return
	}
	p := Current(c)
	queuePub, staffPub := c.Param("queueId"), c.Param("staffId")
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		position, err := h.Store.MoveQueueStaff(c.Request.Context(), tx, p.ShopID,
			queuePub, staffPub, req.To, req.Index, p.UserID)
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, gin.H{
			"queue_id": queuePub, "staff_id": staffPub, "position": position,
		}, nil), nil
	})
}

// ListStaffSummaries returns the staff wall projection (ADR-0015 §3.1).
func (h *Handler) ListStaffSummaries(c *gin.Context) {
	p := Current(c)
	if !hasPerm(p, "queue.read") && !hasPerm(p, "scheduling.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	v, err := h.Store.ListStaffSummaries(c.Request.Context(), nil, p.ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

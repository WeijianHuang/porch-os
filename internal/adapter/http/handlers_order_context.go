package http

import (
	"errors"
	"strconv"

	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Order context bindings (ADR-0022). Card and room writes reuse order.write;
// all of them are idempotent and audited.

func (h *Handler) BindOrderCard(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		CardPublicID string `json:"card_public_id"`
		Version      int    `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.CardPublicID == "" || req.Version < 0 {
		badRequest(c, errors.New("card_public_id and version are required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.BindOrderCard(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("id"), req.CardPublicID, req.Version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) UnbindOrderCard(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	version, err := strconv.Atoi(c.Query("version"))
	if err != nil || version < 0 {
		badRequest(c, errors.New("version query parameter is required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UnbindOrderCard(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("id"), version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) BindOrderRoom(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		RoomPublicID string `json:"room_public_id"`
		Version      int    `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.RoomPublicID == "" || req.Version < 0 {
		badRequest(c, errors.New("room_public_id and version are required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.BindOrderRoom(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("id"), req.RoomPublicID, req.Version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

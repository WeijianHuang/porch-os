package http

import (
	"errors"
	"strconv"

	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Companion group endpoints (ADR-0022 §5). Groups are order orchestration, so
// they reuse order.read / order.write; no separate permission point exists.

func (h *Handler) CreateCompanionGroup(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		OrderPublicIDs []string `json:"order_public_ids"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateCompanionGroup(c.Request.Context(), tx, p.ShopID, p.UserID, req.OrderPublicIDs)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) ListCompanionGroups(c *gin.Context) {
	if !hasPerm(Current(c), "order.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	limit, t, id, err := parseList(c)
	if err != nil {
		fail(c, err)
		return
	}
	items, page, err := h.Store.ListCompanionGroups(c.Request.Context(), nil, Current(c).ShopID,
		limit, t, id, c.Query("status"))
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var last any
	if len(items) > 0 {
		last = items[len(items)-1]
	}
	ok(c, 200, items, pageResponse(last, page))
}

func (h *Handler) GetCompanionGroup(c *gin.Context) {
	if !hasPerm(Current(c), "order.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	v, err := h.Store.CompanionGroupDetail(c.Request.Context(), nil, Current(c).ShopID, c.Param("groupId"))
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) JoinCompanionGroup(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		OrderPublicIDs []string `json:"order_public_ids"`
		Version        int      `json:"version"`
	}
	if err := decode(c, &req); err != nil || len(req.OrderPublicIDs) == 0 || req.Version < 0 {
		badRequest(c, errors.New("order_public_ids and version are required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.JoinCompanionGroup(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("groupId"), req.OrderPublicIDs, req.Version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) LeaveCompanionGroup(c *gin.Context) {
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
		v, err := h.Store.LeaveCompanionGroup(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("groupId"), c.Param("orderId"), version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) CloseCompanionGroup(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Version int `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CloseCompanionGroup(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("groupId"), req.Version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) CancelCompanionGroup(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Version int `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CancelCompanionGroup(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("groupId"), req.Version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

package http

import (
	"errors"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	rdomain "github.com/WeijianHuang/porch-os/internal/domain/room"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// ---------- rooms ----------

func (h *Handler) ListRooms(c *gin.Context) {
	shopID := Current(c).ShopID
	rooms, err := h.Store.ListRooms(c.Request.Context(), nil, shopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, rooms, nil)
}

func (h *Handler) CreateRoom(c *gin.Context) {
	if !hasPerm(Current(c), "room.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Floor int    `json:"floor"`
		Code  string `json:"code"`
		Name  string `json:"name"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if err := rdomain.Validate(req.Floor, req.Code, req.Name); err != nil {
		badRequest(c, err)
		return
	}
	shopID := Current(c).ShopID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateRoom(c.Request.Context(), tx, shopID, req.Floor, req.Code, req.Name)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) GetRoom(c *gin.Context) {
	p := Current(c)
	v, err := h.Store.RoomByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("roomId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) UpdateRoom(c *gin.Context) {
	if !hasPerm(Current(c), "room.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	existing, err := h.Store.RoomByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("roomId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		Floor   *int    `json:"floor"`
		Code    *string `json:"code"`
		Name    *string `json:"name"`
		Status  *string `json:"status"`
		Version int     `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	if req.Floor == nil && req.Code == nil && req.Name == nil && req.Status == nil {
		badRequest(c, errors.New("at least one field is required"))
		return
	}
	floor, code, name, status := existing.Floor, existing.Code, existing.Name, existing.Status
	if req.Floor != nil {
		floor = *req.Floor
	}
	if req.Code != nil {
		code = *req.Code
	}
	if req.Name != nil {
		name = *req.Name
	}
	if req.Status != nil {
		if !rdomain.ValidStatus(*req.Status) {
			badRequest(c, errors.New("invalid status"))
			return
		}
		status = *req.Status
	}
	if err := rdomain.Validate(floor, code, name); err != nil {
		badRequest(c, err)
		return
	}
	shopID, id := p.ShopID, existing.ID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateRoom(c.Request.Context(), tx, shopID, id, floor, code, name, status, req.Version)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) DeleteRoom(c *gin.Context) {
	if !hasPerm(Current(c), "room.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	existing, err := h.Store.RoomByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("roomId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	shopID, id := p.ShopID, existing.ID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.DeleteRoom(c.Request.Context(), tx, shopID, id); err != nil {
			return 0, nil, pgErr(err)
		}
		return 204, nil, nil
	})
}

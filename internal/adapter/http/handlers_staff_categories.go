package http

import (
	"errors"
	"strings"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// ListStaffCategories returns shop-scoped classification definitions. They are
// display/filter metadata only (ADR-0024), not permissions or qualifications.
func (h *Handler) ListStaffCategories(c *gin.Context) {
	if !hasPerm(Current(c), "queue.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	v, err := h.Store.ListStaffCategories(c.Request.Context(), nil, p.ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) CreateStaffCategory(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	var req struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		SortOrder int    `json:"sort_order"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	req.Code, req.Name = strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if req.Code == "" || req.Name == "" {
		badRequest(c, errors.New("code and name are required"))
		return
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateStaffCategory(c.Request.Context(), tx, p.ShopID,
			req.Code, req.Name, req.SortOrder, p.UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) UpdateStaffCategory(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	var req struct {
		Name      *string `json:"name"`
		SortOrder *int    `json:"sort_order"`
		Status    *string `json:"status"`
		Version   int     `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	if req.Name == nil && req.SortOrder == nil && req.Status == nil {
		badRequest(c, errors.New("at least one field is required"))
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			badRequest(c, errors.New("name must not be blank"))
			return
		}
		req.Name = &trimmed
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateStaffCategory(c.Request.Context(), tx, p.ShopID,
			c.Param("categoryId"), req.Name, req.SortOrder, req.Status, req.Version, p.UserID)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

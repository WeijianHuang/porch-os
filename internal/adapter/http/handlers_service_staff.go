package http

import (
	"errors"
	"strconv"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/domain/servicestaff"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// ADR-0023: qualifications and per-line executor assignment. Both resources
// are shop-scoped; writes are idempotent, versioned where the ADR says so, and
// audited inside the same transaction.

func (h *Handler) ListStaffServiceItems(c *gin.Context) {
	if !hasPerm(Current(c), "queue.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	staff, err := h.Store.StaffByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("staffId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ids, err := h.Store.ServiceItemIDs(c.Request.Context(), nil, p.ShopID, staff.ID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, gin.H{"staff_public_id": staff.PublicID, "service_item_ids": ids}, nil)
}

func (h *Handler) SetStaffServiceItems(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		CatalogItemIDs []string `json:"catalog_item_ids"`
		Version        int      `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("catalog_item_ids and a non-negative version are required"))
		return
	}
	ids := servicestaff.NormalizeItemIDs(req.CatalogItemIDs)
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		staff, err := h.Store.StaffByPublicID(c.Request.Context(), tx, p.ShopID, c.Param("staffId"))
		if err != nil {
			return 0, nil, pgErr(err)
		}
		v, err := h.Store.SetStaffServiceItems(c.Request.Context(), tx, p.ShopID,
			staff.ID, staff.PublicID, req.Version, ids, p.UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) SetOrderLineStaff(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	lineNo, err := strconv.Atoi(c.Param("lineNo"))
	if err != nil || lineNo < 1 {
		badRequest(c, errors.New("line_no must be a positive integer"))
		return
	}
	var req struct {
		StaffPublicID *string `json:"staff_public_id"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.SetOrderLineStaff(c.Request.Context(), tx, p.ShopID,
			c.Param("id"), lineNo, req.StaffPublicID, p.UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

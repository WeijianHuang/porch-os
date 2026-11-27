package http

import (
	"errors"

	"github.com/WeijianHuang/porch-os/internal/domain/shifttemplate"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// ListShiftTemplates returns shop-scoped reusable shift definitions. They are
// display/assignment metadata only (ADR-0025), never a business shift instance.
func (h *Handler) ListShiftTemplates(c *gin.Context) {
	if !hasPerm(Current(c), "queue.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	v, err := h.Store.ListShiftTemplates(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

type shiftTemplateWrite struct {
	Name               *string `json:"name"`
	StartTime          *string `json:"start_time"`
	EndTime            *string `json:"end_time"`
	CheckInLeadMinutes *int    `json:"check_in_lead_minutes"`
	NextDayFlag        *bool   `json:"next_day_flag"`
	SortOrder          *int    `json:"sort_order"`
	Version            int     `json:"version"`
}

func validateShiftTemplateWrite(req shiftTemplateWrite, create bool) error {
	if create && (req.Name == nil || req.StartTime == nil || req.EndTime == nil) {
		return errors.New("name, start_time and end_time are required")
	}
	if req.Name != nil {
		if _, err := shifttemplate.NormalizeName(*req.Name); err != nil {
			return err
		}
	}
	if req.StartTime != nil {
		if err := shifttemplate.ValidateTime(*req.StartTime); err != nil {
			return err
		}
	}
	if req.EndTime != nil {
		if err := shifttemplate.ValidateTime(*req.EndTime); err != nil {
			return err
		}
	}
	if req.CheckInLeadMinutes != nil {
		if err := shifttemplate.ValidateLeadMinutes(*req.CheckInLeadMinutes); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) CreateShiftTemplate(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	var req shiftTemplateWrite
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if err := validateShiftTemplateWrite(req, true); err != nil {
		badRequest(c, err)
		return
	}
	name, _ := shifttemplate.NormalizeName(*req.Name)
	lead, sortOrder, nextDay := 0, 0, false
	if req.CheckInLeadMinutes != nil {
		lead = *req.CheckInLeadMinutes
	}
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}
	if req.NextDayFlag != nil {
		nextDay = *req.NextDayFlag
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateShiftTemplate(c.Request.Context(), tx, p.ShopID,
			name, *req.StartTime, *req.EndTime, lead, sortOrder, nextDay, p.UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) UpdateShiftTemplate(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	var req shiftTemplateWrite
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	if req.Name == nil && req.StartTime == nil && req.EndTime == nil &&
		req.CheckInLeadMinutes == nil && req.NextDayFlag == nil && req.SortOrder == nil {
		badRequest(c, errors.New("at least one field is required"))
		return
	}
	if err := validateShiftTemplateWrite(req, false); err != nil {
		badRequest(c, err)
		return
	}
	if req.Name != nil {
		name, _ := shifttemplate.NormalizeName(*req.Name)
		req.Name = &name
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateShiftTemplate(c.Request.Context(), tx, p.ShopID,
			c.Param("templateId"), req.Name, req.StartTime, req.EndTime,
			req.CheckInLeadMinutes, req.NextDayFlag, req.SortOrder, req.Version, p.UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) DeleteShiftTemplate(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.DeleteShiftTemplate(c.Request.Context(), tx, p.ShopID,
			c.Param("templateId"), p.UserID); err != nil {
			return 0, nil, pgErr(err)
		}
		return 204, nil, nil
	})
}

func (h *Handler) AssignShiftTemplateStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	var req struct {
		StaffPublicIDs []string `json:"staff_public_ids"`
		Version        int      `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.AssignShiftTemplateStaff(c.Request.Context(), tx, p.ShopID,
			c.Param("templateId"), req.StaffPublicIDs, req.Version, p.UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

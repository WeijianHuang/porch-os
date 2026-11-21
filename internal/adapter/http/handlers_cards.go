package http

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
)

// ID card endpoints (ADR-0017 / design/14). Shop-scoped, audited and
// idempotent on every write. admin/manager may write; staff may read.

func requireCard(c *gin.Context, write bool) bool {
	need := "card.read"
	if write {
		need = "card.write"
	}
	if !hasPerm(Current(c), need) {
		fail(c, apierrors.PermissionDenied)
		return false
	}
	return true
}

func (h *Handler) ListCards(c *gin.Context) {
	if !requireCard(c, false) {
		return
	}
	rows, err := h.Store.ListCards(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, rows, nil)
}

func (h *Handler) CreateCard(c *gin.Context) {
	if !requireCard(c, true) {
		return
	}
	var req struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	req.Code, req.Name = strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if req.Code == "" || req.Name == "" {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "code/name", Reason: "code and name are required"}))
		return
	}
	if utf8.RuneCountInString(req.Name) > 200 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "name", Reason: "name must be at most 200 characters"}))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateCard(c.Request.Context(), tx, p.ShopID, postgres.CardInput{
			Code: req.Code, Name: req.Name, Status: req.Status, ActorID: p.UserID,
		})
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) UpdateCard(c *gin.Context) {
	if !requireCard(c, true) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" && req.Status == "" {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "name/status", Reason: "at least one field is required"}))
		return
	}
	if req.Version < 0 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "version", Reason: "version must not be negative"}))
		return
	}
	if utf8.RuneCountInString(req.Name) > 200 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "name", Reason: "name must be at most 200 characters"}))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateCard(c.Request.Context(), tx, p.ShopID, c.Param("cardId"), postgres.CardInput{
			Name: req.Name, Status: req.Status, Version: req.Version, ActorID: p.UserID,
		})
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) HoldCard(c *gin.Context) {
	if !requireCard(c, true) {
		return
	}
	var req struct {
		StaffID string `json:"staff_id"`
		Reason  string `json:"reason"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	req.StaffID = strings.TrimSpace(req.StaffID)
	if req.StaffID == "" {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "staff_id", Reason: "staff_id is required"}))
		return
	}
	if utf8.RuneCountInString(req.Reason) > 200 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "reason", Reason: "reason must be at most 200 characters"}))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.HoldCard(c.Request.Context(), tx, p.ShopID, postgres.HoldInput{
			CardPub: c.Param("cardId"), StaffPub: req.StaffID,
			Reason: strings.TrimSpace(req.Reason), ActorID: p.UserID,
		})
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) ReleaseCard(c *gin.Context) {
	if !requireCard(c, true) {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if utf8.RuneCountInString(req.Reason) > 200 {
		fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
			apierrors.Detail{Field: "reason", Reason: "reason must be at most 200 characters"}))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.ReleaseCard(c.Request.Context(), tx, p.ShopID,
			c.Param("cardId"), strings.TrimSpace(req.Reason), p.UserID)
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

package http

import (
	"errors"
	"strconv"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/domain/money"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// ---------- orders (ADR-0013 / 0014 / 0016 / 0019 / 0020, design/03 §8/§10) ----------

// PreviewOrderPrice is the read-only price quote of ADR-0019: full cart +
// structured adjustments in, per-line allocated amounts out. No database
// write, no order number, no idempotency key (middleware exempts this path).
func (h *Handler) PreviewOrderPrice(c *gin.Context) {
	if !hasPerm(Current(c), "order.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Lines       []postgres.PriceLineInput `json:"lines"`
		Adjustments []postgres.Adjustment     `json:"adjustments"`
	}
	// The idempotency middleware skips this read-only POST, so it does not
	// pre-read the body into Principal; bind directly from the request.
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Lines) == 0 {
		badRequest(c, errors.New("lines are required"))
		return
	}
	amounts := make([]money.Money, 0, len(req.Adjustments))
	for _, a := range req.Adjustments {
		if err := postgres.ValidateAdjustment(a); err != nil {
			fail(c, err)
			return
		}
		m, err := money.FromString(a.Amount)
		if err != nil || m.IsNegative() {
			badRequest(c, errors.New("adjustment amount must be non-negative"))
			return
		}
		amounts = append(amounts, m)
	}
	result, err := h.Store.PricePreview(c.Request.Context(), Current(c).ShopID, req.Lines, amounts)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, result, nil)
}

func (h *Handler) orderID(c *gin.Context, shopID int64) (postgres.Order, bool) {
	v, err := h.Store.OrderByPublic(c.Request.Context(), nil, shopID, c.Param("id"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return v, false
	}
	if err != nil {
		fail(c, pgErr(err))
		return v, false
	}
	return v, true
}

// currentShiftID returns the open shift's internal id, or "" when no shift is
// open. A payment/refund may still be recorded without a shift (shift_id is
// nullable and only an attribution dimension).
func (h *Handler) currentShiftID(c *gin.Context, shopID int64) string {
	s, err := h.Store.CurrentShift(c.Request.Context(), nil, shopID)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(s.ID, 10)
}

func (h *Handler) CreateOrder(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		MemberID     *string `json:"member_id"`
		RoomPublicID string  `json:"room_public_id"`
		CardPublicID string  `json:"card_public_id"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	p := Current(c)
	var memberID *int64
	if req.MemberID != nil && *req.MemberID != "" {
		m, _, err := h.Store.Member(c.Request.Context(), nil, *req.MemberID)
		if postgres.IsNoRows(err) {
			fail(c, apierrors.NotFound)
			return
		}
		if err != nil {
			fail(c, pgErr(err))
			return
		}
		memberID = &m.ID
	}
	shift := h.currentShiftID(c, p.ShopID)
	var shiftID *int64
	if shift != "" {
		n, _ := strconv.ParseInt(shift, 10, 64)
		shiftID = &n
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateOrder(c.Request.Context(), tx, p.ShopID, p.UserID, memberID, shiftID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		// Optional context at create time (ADR-0022); both remain editable
		// until submit. Failures roll the whole order back.
		if req.RoomPublicID != "" {
			v, err = h.Store.BindOrderRoom(c.Request.Context(), tx, p.ShopID, p.UserID,
				v.PublicID, req.RoomPublicID, v.Version)
			if err != nil {
				return 0, nil, pgErr(err)
			}
		}
		if req.CardPublicID != "" {
			v, err = h.Store.BindOrderCard(c.Request.Context(), tx, p.ShopID, p.UserID,
				v.PublicID, req.CardPublicID, v.Version)
			if err != nil {
				return 0, nil, pgErr(err)
			}
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) ListOrders(c *gin.Context) {
	if !hasPerm(Current(c), "order.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	limit, t, id, err := parseList(c)
	if err != nil {
		fail(c, err)
		return
	}
	items, page, err := h.Store.ListOrders(c.Request.Context(), nil, Current(c).ShopID, limit, t, id, c.Query("status"))
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

func (h *Handler) GetOrder(c *gin.Context) {
	if !hasPerm(Current(c), "order.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	v, err := h.Store.OrderDetail(c.Request.Context(), nil, Current(c).ShopID, c.Param("id"))
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

func (h *Handler) AddOrderLine(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		CatalogItemID string `json:"catalog_item_id"`
		Quantity      string `json:"quantity"`
		StaffPublicID string `json:"staff_public_id"` // ADR-0023: service lines only
	}
	if err := decode(c, &req); err != nil || req.CatalogItemID == "" || req.Quantity == "" {
		badRequest(c, errors.New("catalog_item_id and quantity are required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.AddOrderLine(c.Request.Context(), tx, p.ShopID, p.UserID,
			c.Param("id"), req.CatalogItemID, req.Quantity, req.StaffPublicID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) RemoveOrderLine(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	lineNo, err := strconv.Atoi(c.Query("line_no"))
	if err != nil || lineNo < 1 {
		badRequest(c, errors.New("line_no query parameter is required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.RemoveOrderLine(c.Request.Context(), tx, p.ShopID, p.UserID, c.Param("id"), lineNo)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) SubmitOrder(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Version     int                   `json:"version"`
		TotalAmount string                `json:"total_amount"`
		Adjustments []postgres.Adjustment `json:"adjustments"`
	}
	if err := decode(c, &req); err != nil || req.TotalAmount == "" || req.Version < 0 {
		badRequest(c, errors.New("total_amount and version are required"))
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.SubmitOrder(c.Request.Context(), tx, p.ShopID, p.UserID, c.Param("id"), req.Version, req.Adjustments, req.TotalAmount)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) CancelOrder(c *gin.Context) {
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
		v, err := h.Store.CancelOrder(c.Request.Context(), tx, p.ShopID, p.UserID, c.Param("id"), req.Version)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) PayOrder(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Entries []postgres.PayMethod `json:"entries"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	p := Current(c)
	shift := h.currentShiftID(c, p.ShopID)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		payment, order, err := h.Store.PayOrder(c.Request.Context(), tx, p.ShopID, p.UserID, c.Param("id"), p.IdemKey, shift, req.Entries)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, gin.H{"payment": payment, "order": order}, nil), nil
	})
}

// SaleRefund handles POST /orders/{id}/refunds. The original payment is
// identified explicitly by payment_id in the request body.
func (h *Handler) SaleRefund(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		PaymentID     string               `json:"payment_id"`
		Amount        string               `json:"amount"`
		Reason        string               `json:"reason"`
		RefundEntries []postgres.PayMethod `json:"refund_entries"`
	}
	if err := decode(c, &req); err != nil || req.PaymentID == "" {
		badRequest(c, errors.New("payment_id is required"))
		return
	}
	p := Current(c)
	shift := h.currentShiftID(c, p.ShopID)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.SaleRefund(c.Request.Context(), tx, p.ShopID, p.UserID, c.Param("id"), req.PaymentID,
			req.Amount, req.Reason, req.RefundEntries, shift)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

// RefundTopup handles POST /members/{id}/topups/{paymentId}/refunds.
func (h *Handler) RefundTopup(c *gin.Context) {
	if !hasPerm(Current(c), "member.topup.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Amount        string               `json:"amount"`
		Reason        string               `json:"reason"`
		RefundEntries []postgres.PayMethod `json:"refund_entries"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	// memberID must be a member visible globally; the payment's member_id is
	// checked in the store (no cross-shop escape).
	m, _, err := h.Store.Member(c.Request.Context(), nil, c.Param("id"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	p := Current(c)
	shift := h.currentShiftID(c, p.ShopID)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.TopupRefund(c.Request.Context(), tx, p.ShopID, p.UserID, m.ID,
			c.Param("paymentId"), req.Amount, req.Reason, req.RefundEntries, shift)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

// ---------- shifts ----------

func (h *Handler) CurrentShift(c *gin.Context) {
	if !hasPerm(Current(c), "order.read") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	s, err := h.Store.CurrentShift(c.Request.Context(), nil, Current(c).ShopID)
	if postgres.IsNoRows(err) {
		ok(c, 200, nil, nil)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, s, nil)
}

func (h *Handler) OpenShift(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		s, err := h.Store.OpenShift(c.Request.Context(), tx, p.ShopID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, s, nil), nil
	})
}

func (h *Handler) CloseShift(c *gin.Context) {
	if !hasPerm(Current(c), "order.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		s, err := h.Store.CloseShift(c.Request.Context(), tx, p.ShopID, c.Param("id"))
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, s, nil), nil
	})
}

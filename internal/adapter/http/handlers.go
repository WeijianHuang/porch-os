package http

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/domain/money"
	"github.com/WeijianHuang/porch-os/internal/domain/shop"
	"github.com/WeijianHuang/porch-os/internal/platform/config"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/WeijianHuang/porch-os/internal/security"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type Handler struct {
	Store *postgres.Store
	Cfg   config.Config
}

func (h *Handler) envelope(c *gin.Context, data any, page any) gin.H {
	body := gin.H{"data": data, "meta": gin.H{"request_id": Current(c).RequestID}}
	if page != nil {
		body["page"] = page
	}
	return body
}

func decode(c *gin.Context, dst any) error {
	if err := json.Unmarshal(Current(c).Body, dst); err != nil {
		return err
	}
	return nil
}

// writeResult serves either a freshly computed envelope or the cached one from
// idempotency_keys, preserving the original status code and body.
func writeResult(c *gin.Context, status int, payload any) {
	if raw, ok := payload.(json.RawMessage); ok {
		c.Data(status, "application/json", raw)
		return
	}
	c.JSON(status, payload)
}

func (h *Handler) idemScope(p *Principal) string {
	return "user:" + strconv.FormatInt(p.UserID, 10)
}

func (h *Handler) runWrite(c *gin.Context, fn func(tx pgx.Tx) (int, any, error)) {
	p := Current(c)
	st, payload, err := h.Store.IdempotentWrite(
		c.Request.Context(), h.idemScope(p), p.IdemKey,
		postgres.RequestFingerprint(c.Request.Method, c.FullPath(), p.Body), fn)
	if err != nil {
		fail(c, err)
		return
	}
	writeResult(c, st, payload)
}

func pgErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *apierrors.APIError
	if errors.As(err, &apiErr) {
		return err
	}
	return apierrors.FromPG(err)
}

// ---------- auth ----------

func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := decode(c, &req); err != nil || req.Login == "" || req.Password == "" {
		badRequest(c, errors.New("login and password are required"))
		return
	}
	u, err := h.Store.UserByLogin(c.Request.Context(), nil, req.Login)
	if err != nil || u.Status != "active" || !security.CheckPassword(u.PasswordHash, req.Password) {
		fail(c, apierrors.WithDetails(apierrors.Unauthenticated,
			apierrors.Detail{Field: "login", Reason: "invalid_credentials"}))
		return
	}
	visible, err := h.Store.VisibleShops(c.Request.Context(), nil, u.ID)
	if err != nil {
		fail(c, err)
		return
	}
	access, err := security.IssueAccess(h.Cfg.JWTSecret, h.Cfg.AccessTTL, u.ID)
	if err != nil {
		fail(c, err)
		return
	}
	refresh, err := security.NewOpaqueToken()
	if err != nil {
		fail(c, err)
		return
	}
	if err := h.Store.SaveRefreshToken(c.Request.Context(), nil, u.ID,
		security.HashToken(refresh), time.Now().UTC().Add(h.Cfg.RefreshTTL)); err != nil {
		fail(c, pgErr(err))
		return
	}
	view := make([]gin.H, 0, len(visible))
	for _, v := range shop.SortedByGrant(visible) {
		view = append(view, gin.H{"public_id": v.PublicID, "name": v.Name, "status": v.Status})
	}
	ok(c, 200, gin.H{
		"access_token": access, "refresh_token": refresh,
		"token_type": "Bearer", "expires_in": int(h.Cfg.AccessTTL.Seconds()),
		"shops": view, "default_shop_id": shop.DefaultID(visible),
	}, nil)
}

func (h *Handler) Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decode(c, &req); err != nil || req.RefreshToken == "" {
		badRequest(c, errors.New("refresh_token is required"))
		return
	}
	userID, err := h.Store.ConsumeRefreshToken(c.Request.Context(), nil, security.HashToken(req.RefreshToken))
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, apierrors.Unauthenticated)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	access, err := security.IssueAccess(h.Cfg.JWTSecret, h.Cfg.AccessTTL, userID)
	if err != nil {
		fail(c, err)
		return
	}
	refresh, err := security.NewOpaqueToken()
	if err != nil {
		fail(c, err)
		return
	}
	if err := h.Store.SaveRefreshToken(c.Request.Context(), nil, userID,
		security.HashToken(refresh), time.Now().UTC().Add(h.Cfg.RefreshTTL)); err != nil {
		fail(c, pgErr(err))
		return
	}
	visible, err := h.Store.VisibleShops(c.Request.Context(), nil, userID)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, 200, gin.H{
		"access_token": access, "refresh_token": refresh,
		"expires_in": int(h.Cfg.AccessTTL.Seconds()), "default_shop_id": shop.DefaultID(visible),
	}, nil)
}

func (h *Handler) Logout(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decode(c, &req); err != nil || req.RefreshToken == "" {
		badRequest(c, errors.New("refresh_token is required"))
		return
	}
	if err := h.Store.RevokeRefreshToken(c.Request.Context(), nil, security.HashToken(req.RefreshToken)); err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, gin.H{"logged_out": true}, nil)
}

// ---------- me ----------

func (h *Handler) Me(c *gin.Context) {
	p := Current(c)
	ok(c, 200, gin.H{"user_id": p.UserID, "login": p.Login, "roles": p.Roles}, nil)
}

func (h *Handler) MeShops(c *gin.Context) {
	p := Current(c)
	view := make([]gin.H, 0, len(p.Visible))
	for _, v := range shop.SortedByGrant(p.Visible) {
		view = append(view, gin.H{"public_id": v.PublicID, "name": v.Name, "status": v.Status})
	}
	ok(c, 200, gin.H{"shops": view, "default_shop_id": shop.DefaultID(p.Visible)}, nil)
}

func (h *Handler) MePermissions(c *gin.Context) {
	p := Current(c)
	ok(c, 200, gin.H{"roles": p.Roles, "permissions": p.Perms}, nil)
}

// ---------- shops ----------

func (h *Handler) ListShops(c *gin.Context) {
	limit, t, id, err := parseList(c)
	if err != nil {
		fail(c, err)
		return
	}
	items, page, err := h.Store.ListVisibleShops(c.Request.Context(), nil, Current(c).UserID, limit, t, id)
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

func (h *Handler) CreateShop(c *gin.Context) {
	if !hasPerm(Current(c), "shop.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := decode(c, &req); err != nil || req.Code == "" || req.Name == "" {
		badRequest(c, errors.New("code and name are required"))
		return
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateShop(c.Request.Context(), tx, req.Code, req.Name, Current(c).UserID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) GetShop(c *gin.Context) {
	v, _, err := h.Store.ShopByPublicID(c.Request.Context(), nil, Current(c).ShopPID)
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

func (h *Handler) UpdateShop(c *gin.Context) {
	if !hasPerm(Current(c), "shop.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Name == "" || req.Version < 0 {
		badRequest(c, errors.New("name and version are required"))
		return
	}
	if req.Status != "" && req.Status != "active" && req.Status != "inactive" {
		badRequest(c, errors.New("invalid status"))
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	shopID := Current(c).ShopID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateShop(c.Request.Context(), tx, shopID, req.Name, req.Status, req.Version)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.As(apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试"))
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) GetSettings(c *gin.Context) {
	v, err := h.Store.GetSetting(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) UpdateSettings(c *gin.Context) {
	if !hasPerm(Current(c), "shop.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Value map[string]any `json:"value"`
	}
	if err := decode(c, &req); err != nil || req.Value == nil {
		badRequest(c, errors.New("value object is required"))
		return
	}
	shopID := Current(c).ShopID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpsertSetting(c.Request.Context(), tx, shopID, req.Value)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

// ---------- catalog ----------

func (h *Handler) ListCatalogItems(c *gin.Context) {
	limit, t, id, err := parseList(c)
	if err != nil {
		fail(c, err)
		return
	}
	items, page, err := h.Store.ListCatalogItems(c.Request.Context(), nil, limit, t, id)
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

func (h *Handler) CreateCatalogItem(c *gin.Context) {
	if !hasPerm(Current(c), "catalog.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		SKU              string  `json:"sku"`
		Name             string  `json:"name"`
		Type             string  `json:"type"`
		BasePrice        string  `json:"base_price"`
		DurationMinutes  *int    `json:"duration_minutes"`
		CategoryPublicID *string `json:"category_public_id"`
	}
	if err := decode(c, &req); err != nil || req.SKU == "" || req.Name == "" {
		badRequest(c, errors.New("sku and name are required"))
		return
	}
	if req.Type == "" {
		req.Type = "product"
	}
	if req.Type != "product" && req.Type != "service" {
		badRequest(c, errors.New("invalid type"))
		return
	}
	if err := validateItemDuration(req.Type, req.DurationMinutes); err != nil {
		badRequest(c, err)
		return
	}
	if _, err := money.FromString(req.BasePrice); err != nil {
		badRequest(c, err)
		return
	}
	var categoryID *int64
	if req.CategoryPublicID != nil {
		id, err := h.Store.ResolveCatalogCategoryID(c.Request.Context(), nil, *req.CategoryPublicID)
		if err != nil {
			fail(c, pgErr(err))
			return
		}
		categoryID = &id
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateCatalogItem(c.Request.Context(), tx, req.SKU, req.Name, req.Type, req.BasePrice, req.DurationMinutes, categoryID)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

// validateItemDuration enforces the ADR-0018 type/duration invariant at the
// API edge (the CHECK constraint is the DB backstop): products carry no
// duration; services require an integer in 1..1440 minutes.
func validateItemDuration(itemType string, minutes *int) error {
	if itemType == "product" {
		if minutes != nil {
			return errors.New("duration_minutes is only allowed for service items")
		}
		return nil
	}
	if minutes == nil || *minutes < 1 || *minutes > 1440 {
		return errors.New("service items require duration_minutes between 1 and 1440")
	}
	return nil
}

func (h *Handler) GetCatalogItem(c *gin.Context) {
	v, _, err := h.Store.CatalogItem(c.Request.Context(), nil, c.Param("id"))
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

func (h *Handler) UpdateCatalogItem(c *gin.Context) {
	if !hasPerm(Current(c), "catalog.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	existing, itemID, err := h.Store.CatalogItem(c.Request.Context(), nil, c.Param("id"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		Name             string  `json:"name"`
		BasePrice        string  `json:"base_price"`
		Active           *bool   `json:"active"`
		Version          int     `json:"version"`
		DurationMinutes  *int    `json:"duration_minutes"`
		CategoryPublicID *string `json:"category_public_id"`
	}
	if err := decode(c, &req); err != nil || req.Name == "" || req.Version < 0 {
		badRequest(c, errors.New("name and version are required"))
		return
	}
	// `type` is immutable on update (ADR-0018); validate the duration against
	// the persisted type. Omitted duration keeps the stored value.
	if req.DurationMinutes != nil {
		if err := validateItemDuration(existing.Type, req.DurationMinutes); err != nil {
			badRequest(c, err)
			return
		}
	}
	if _, err := money.FromString(req.BasePrice); err != nil {
		badRequest(c, err)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	var categoryID *int64
	if req.CategoryPublicID != nil {
		id, err := h.Store.ResolveCatalogCategoryID(c.Request.Context(), nil, *req.CategoryPublicID)
		if err != nil {
			fail(c, pgErr(err))
			return
		}
		categoryID = &id
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateCatalogItem(c.Request.Context(), tx, itemID, req.Name, req.BasePrice, req.DurationMinutes, categoryID, active, req.Version)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) ListCategories(c *gin.Context) {
	v, err := h.Store.ListCategories(c.Request.Context(), nil)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) PutOverride(c *gin.Context) {
	if !hasPerm(Current(c), "catalog.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	_, itemID, err := h.Store.CatalogItem(c.Request.Context(), nil, c.Param("itemId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		Price     string `json:"price"`
		Available *bool  `json:"available"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if _, err := money.FromString(req.Price); err != nil {
		badRequest(c, err)
		return
	}
	available := true
	if req.Available != nil {
		available = *req.Available
	}
	shopID := Current(c).ShopID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.PutOverride(c.Request.Context(), tx, shopID, itemID, req.Price, available); err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, gin.H{"catalog_item_id": c.Param("itemId"), "price": req.Price, "available": available}, nil), nil
	})
}

// ---------- members ----------

func (h *Handler) ListMembers(c *gin.Context) {
	limit, t, id, err := parseList(c)
	if err != nil {
		fail(c, err)
		return
	}
	items, page, err := h.Store.ListMembers(c.Request.Context(), nil, limit, t, id, c.Query("phone"))
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

func (h *Handler) CreateMember(c *gin.Context) {
	if !hasPerm(Current(c), "member.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Phone       string `json:"phone"`
		DisplayName string `json:"display_name"`
	}
	if err := decode(c, &req); err != nil || req.Phone == "" || req.DisplayName == "" {
		badRequest(c, errors.New("phone and display_name are required"))
		return
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateMember(c.Request.Context(), tx, req.Phone, req.DisplayName)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) GetMember(c *gin.Context) {
	v, _, err := h.Store.Member(c.Request.Context(), nil, c.Param("id"))
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

func (h *Handler) UpdateMember(c *gin.Context) {
	if !hasPerm(Current(c), "member.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	_, memberID, err := h.Store.Member(c.Request.Context(), nil, c.Param("id"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Status      string `json:"status"`
		Version     int    `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.DisplayName == "" || req.Version < 0 {
		badRequest(c, errors.New("display_name and version are required"))
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateMember(c.Request.Context(), tx, memberID, req.DisplayName, req.Status, req.Version)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) memberID(c *gin.Context) (int64, bool) {
	_, id, err := h.Store.Member(c.Request.Context(), nil, c.Param("id"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return 0, false
	}
	if err != nil {
		fail(c, pgErr(err))
		return 0, false
	}
	return id, true
}

func (h *Handler) GetBalance(c *gin.Context) {
	id, fine := h.memberID(c)
	if !fine {
		return
	}
	balance, _, err := h.Store.Balance(c.Request.Context(), nil, Current(c).ShopID, id)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, gin.H{"balance": balance}, nil)
}

func (h *Handler) GetLedger(c *gin.Context) {
	id, fine := h.memberID(c)
	if !fine {
		return
	}
	rows, err := h.Store.Ledger(c.Request.Context(), nil, Current(c).ShopID, id, 100)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, rows, nil)
}

func (h *Handler) Topup(c *gin.Context) {
	if !hasPerm(Current(c), "member.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	id, fine := h.memberID(c)
	if !fine {
		return
	}
	var req struct {
		Amount  string               `json:"amount"`
		Entries []postgres.PayMethod `json:"entries"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	amount, err := money.FromString(req.Amount)
	if err != nil || !amount.IsPositive() {
		badRequest(c, errors.New("amount must be positive"))
		return
	}
	// A topup records the real funding channel so a later topup refund can
	// validate per-channel capacity. Default to cash when the client omits it.
	entries := req.Entries
	if len(entries) == 0 {
		entries = []postgres.PayMethod{{Method: "cash", Amount: amount.String()}}
	}
	entrySum := money.Zero()
	for _, e := range entries {
		if e.Method != "cash" && e.Method != "balance" && e.Method != "card" {
			badRequest(c, errors.New("invalid payment method"))
			return
		}
		m, err := money.FromString(e.Amount)
		if err != nil || !m.IsPositive() {
			badRequest(c, errors.New("invalid entry amount"))
			return
		}
		entrySum = entrySum.Add(m)
	}
	if entrySum.String() != amount.String() {
		badRequest(c, errors.New("entries must sum to amount"))
		return
	}
	shopID := Current(c).ShopID
	p := Current(c)
	shift := h.currentShiftID(c, shopID)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		// ADR-0013 / 0016: a topup is a payment with kind='topup', no order_id,
		// written in the same transaction as the balance credit. That payment
		// is the anchor for the member-scoped topup refund endpoint.
		payment, err := h.Store.TopupPayment(c.Request.Context(), tx, shopID, id, p.UserID,
			amount.String(), p.IdemKey, shift, entries)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		after, _, err := h.Store.Balance(c.Request.Context(), tx, shopID, id)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, gin.H{"balance": after, "payment": payment}, nil), nil
	})
}

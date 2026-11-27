package http

import (
	"errors"
	"strings"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	qdomain "github.com/WeijianHuang/porch-os/internal/domain/queue"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// queueView is the list projection: queue fields plus a derived staff count.
// It stays flat and never embeds a staff entity graph (ADR-0002 / design/10).
type queueView struct {
	postgres.Queue
	StaffCount int `json:"staff_count"`
}

func validStatus(s string) bool { return s == "active" || s == "inactive" }

// ---------- queues ----------

func (h *Handler) ListQueues(c *gin.Context) {
	shopID := Current(c).ShopID
	queues, err := h.Store.ListQueues(c.Request.Context(), nil, shopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	// One flat assignment scan for the shop; counts are computed in-memory.
	assignments, err := h.Store.Assignments(c.Request.Context(), nil, shopID, nil, nil)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	byQueue := qdomain.StaffByQueue(assignments)
	view := make([]queueView, 0, len(queues))
	for _, q := range queues {
		count := len(byQueue[q.PublicID])
		view = append(view, queueView{Queue: q, StaffCount: count})
	}
	ok(c, 200, view, nil)
}

func (h *Handler) CreateQueue(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Seq  int    `json:"seq"`
		Name string `json:"name"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if err := qdomain.ValidateQueue(req.Seq, req.Name); err != nil {
		badRequest(c, err)
		return
	}
	shopID := Current(c).ShopID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateQueue(c.Request.Context(), tx, shopID, req.Seq, req.Name)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, queueView{Queue: v, StaffCount: 0}, nil), nil
	})
}

func (h *Handler) GetQueue(c *gin.Context) {
	p := Current(c)
	v, err := h.Store.QueueByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("queueId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	assignments, err := h.Store.Assignments(c.Request.Context(), nil, p.ShopID, &v.ID, nil)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	staff := qdomain.StaffByQueue(assignments)[v.PublicID]
	if staff == nil {
		staff = []qdomain.StaffRef{}
	}
	ok(c, 200, struct {
		postgres.Queue
		Staff []qdomain.StaffRef `json:"staff"`
	}{Queue: v, Staff: staff}, nil)
}

func (h *Handler) UpdateQueue(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	existing, err := h.Store.QueueByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("queueId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		Seq     *int    `json:"seq"`
		Name    *string `json:"name"`
		Status  *string `json:"status"`
		Version int     `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	seq, name, status := existing.Seq, existing.Name, existing.Status
	if req.Seq != nil {
		seq = *req.Seq
	}
	if req.Name != nil {
		name = *req.Name
	}
	if req.Status != nil {
		if !validStatus(*req.Status) {
			badRequest(c, errors.New("invalid status"))
			return
		}
		status = *req.Status
	}
	if err := qdomain.ValidateQueue(seq, name); err != nil {
		badRequest(c, err)
		return
	}
	shopID := p.ShopID
	id := existing.ID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateQueue(c.Request.Context(), tx, shopID, id, seq, name, status, req.Version)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) DeleteQueue(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	existing, err := h.Store.QueueByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("queueId"))
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
		if err := h.Store.DeleteQueue(c.Request.Context(), tx, shopID, id); err != nil {
			return 0, nil, pgErr(err)
		}
		return 204, nil, nil
	})
}

// ---------- staff ----------

func (h *Handler) CreateStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	var req struct {
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
		Alias       string `json:"alias"`
	}
	if err := decode(c, &req); err != nil {
		badRequest(c, err)
		return
	}
	if err := qdomain.ValidateStaff(req.Code, req.DisplayName); err != nil {
		badRequest(c, err)
		return
	}
	if err := qdomain.ValidateAlias(req.Alias); err != nil {
		badRequest(c, err)
		return
	}
	shopID := Current(c).ShopID
	alias := strings.TrimSpace(req.Alias)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.CreateStaff(c.Request.Context(), tx, shopID, req.Code, req.DisplayName, alias)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		return 201, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) GetStaff(c *gin.Context) {
	p := Current(c)
	v, err := h.Store.StaffByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("staffId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	assignments, err := h.Store.Assignments(c.Request.Context(), nil, p.ShopID, nil, &v.ID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	queues := qdomain.QueuesByStaff(assignments)[v.PublicID]
	if queues == nil {
		queues = []qdomain.Ref{}
	}
	// ADR-0023: the detail view also carries the qualified service items.
	qualified, err := h.Store.ServiceItemIDs(c.Request.Context(), nil, p.ShopID, v.ID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	category, err := h.Store.StaffCategoryForStaff(c.Request.Context(), nil, p.ShopID, v.ID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	v.Category = category
	ok(c, 200, struct {
		postgres.Staff
		Queues         []qdomain.Ref `json:"queues"`
		ServiceItemIDs []string      `json:"service_item_ids"`
	}{Staff: v, Queues: queues, ServiceItemIDs: qualified}, nil)
}

func (h *Handler) UpdateStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	existing, err := h.Store.StaffByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("staffId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		Code             *string `json:"code"`
		Alias            *string `json:"alias"`
		DisplayName      *string `json:"display_name"`
		Status           *string `json:"status"`
		DutyStatus       *string `json:"duty_status"`
		CategoryPublicID *string `json:"category_public_id"`
		Version          int     `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("version is required"))
		return
	}
	if req.Code == nil && req.Alias == nil && req.DisplayName == nil && req.Status == nil &&
		req.DutyStatus == nil && req.CategoryPublicID == nil {
		badRequest(c, errors.New("at least one field is required"))
		return
	}
	// ADR-0026: code stays shop-unique via the DB constraint; alias is a
	// display-only label and normalises whitespace to an empty string.
	code, alias, name, status, duty := existing.Code, existing.Alias, existing.DisplayName, existing.Status, existing.DutyStatus
	if req.Code != nil {
		code = strings.TrimSpace(*req.Code)
	}
	if req.DisplayName != nil {
		name = *req.DisplayName
	}
	if err := qdomain.ValidateStaff(code, name); err != nil {
		badRequest(c, err)
		return
	}
	if req.Alias != nil {
		alias = strings.TrimSpace(*req.Alias)
		if err := qdomain.ValidateAlias(alias); err != nil {
			badRequest(c, err)
			return
		}
	}
	if req.Status != nil {
		if !validStatus(*req.Status) {
			badRequest(c, errors.New("invalid status"))
			return
		}
		status = *req.Status
	}
	if req.DutyStatus != nil {
		duty = *req.DutyStatus
	}
	if err := qdomain.ValidateDuty(status, duty); err != nil {
		badRequest(c, err)
		return
	}
	shopID, id := p.ShopID, existing.ID
	var categoryID *int64
	var newCategoryPublicID, oldCategoryPublicID string
	if req.CategoryPublicID != nil {
		oldCategory, err := h.Store.StaffCategoryForStaff(c.Request.Context(), nil, shopID, id)
		if err != nil {
			fail(c, pgErr(err))
			return
		}
		if oldCategory != nil {
			oldCategoryPublicID = oldCategory.PublicID
		}
		if *req.CategoryPublicID == "" {
			categoryID = nil
		} else {
			category, err := h.Store.AssignableStaffCategory(c.Request.Context(), nil, shopID, *req.CategoryPublicID)
			if err != nil {
				fail(c, pgErr(err))
				return
			}
			resolved, err := h.Store.StaffCategoryID(c.Request.Context(), nil, shopID, category.PublicID)
			if err != nil {
				fail(c, pgErr(err))
				return
			}
			categoryID = &resolved
			newCategoryPublicID = category.PublicID
		}
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.UpdateStaff(c.Request.Context(), tx, shopID, id, code, alias, name, status, duty, categoryID, req.Version)
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		if req.CategoryPublicID != nil {
			if err := h.Store.RecordStaffCategoryChange(c.Request.Context(), tx, shopID, p.UserID,
				existing.PublicID, oldCategoryPublicID, newCategoryPublicID); err != nil {
				return 0, nil, pgErr(err)
			}
		}
		return 200, h.envelope(c, v, nil), nil
	})
}

func (h *Handler) DeleteStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	existing, err := h.Store.StaffByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("staffId"))
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
		if err := h.Store.DeleteStaff(c.Request.Context(), tx, shopID, id); err != nil {
			return 0, nil, pgErr(err)
		}
		return 204, nil, nil
	})
}

// ---------- queue_staff assignment ----------

func (h *Handler) ListQueueStaff(c *gin.Context) {
	p := Current(c)
	queue, err := h.Store.QueueByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("queueId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	assignments, err := h.Store.Assignments(c.Request.Context(), nil, p.ShopID, &queue.ID, nil)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	staff := qdomain.StaffByQueue(assignments)[queue.PublicID]
	if staff == nil {
		staff = []qdomain.StaffRef{}
	}
	ok(c, 200, staff, nil)
}

// resolvePair resolves both public IDs inside the current shop; a miss on
// either end is 404, so cross-shop assignment never reaches the database.
func (h *Handler) resolvePair(c *gin.Context) (shopID, queueID, staffID int64, ok bool) {
	p := Current(c)
	queue, err := h.Store.QueueByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("queueId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return 0, 0, 0, false
	}
	if err != nil {
		fail(c, pgErr(err))
		return 0, 0, 0, false
	}
	staff, err := h.Store.StaffByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("staffId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return 0, 0, 0, false
	}
	if err != nil {
		fail(c, pgErr(err))
		return 0, 0, 0, false
	}
	return p.ShopID, queue.ID, staff.ID, true
}

func (h *Handler) AssignStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	shopID, queueID, staffID, ok := h.resolvePair(c)
	if !ok {
		return
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.Assign(c.Request.Context(), tx, shopID, queueID, staffID); err != nil {
			return 0, nil, pgErr(err)
		}
		return 204, nil, nil
	})
}

func (h *Handler) UnassignStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	shopID, queueID, staffID, ok := h.resolvePair(c)
	if !ok {
		return
	}
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.Unassign(c.Request.Context(), tx, shopID, queueID, staffID); err != nil {
			return 0, nil, pgErr(err)
		}
		return 204, nil, nil
	})
}

// ReorderQueueStaff replaces the queue-local order with a full member list.
// The queue version is the optimistic lock (ADR-0010 §2).
func (h *Handler) ReorderQueueStaff(c *gin.Context) {
	if !hasPerm(Current(c), "queue.write") {
		fail(c, apierrors.PermissionDenied)
		return
	}
	p := Current(c)
	queue, err := h.Store.QueueByPublicID(c.Request.Context(), nil, p.ShopID, c.Param("queueId"))
	if postgres.IsNoRows(err) {
		fail(c, apierrors.NotFound)
		return
	}
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	var req struct {
		StaffIDs []string `json:"staff_ids"`
		Version  int      `json:"version"`
	}
	if err := decode(c, &req); err != nil || req.Version < 0 {
		badRequest(c, errors.New("staff_ids and version are required"))
		return
	}
	shopID, queueID := p.ShopID, queue.ID
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		v, err := h.Store.ReorderQueueStaff(c.Request.Context(), tx, shopID, queueID, req.StaffIDs, req.Version)
		if errors.Is(err, qdomain.ErrInvalidOrder) {
			return 0, nil, apierrors.WithDetails(apierrors.InvalidArgument,
				apierrors.Detail{Field: "staff_ids", Reason: err.Error()})
		}
		if postgres.IsNoRows(err) {
			return 0, nil, apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		}
		if err != nil {
			return 0, nil, pgErr(err)
		}
		assignments, err := h.Store.Assignments(c.Request.Context(), tx, shopID, &v.ID, nil)
		if err != nil {
			return 0, nil, pgErr(err)
		}
		staff := qdomain.StaffByQueue(assignments)[v.PublicID]
		if staff == nil {
			staff = []qdomain.StaffRef{}
		}
		return 200, h.envelope(c, struct {
			postgres.Queue
			Staff []qdomain.StaffRef `json:"staff"`
		}{Queue: v, Staff: staff}, nil), nil
	})
}

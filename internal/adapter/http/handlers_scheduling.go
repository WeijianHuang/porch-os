package http

import (
	"errors"
	"time"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/domain/scheduling"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// mapSchedErr converts the typed engine error into the frozen API error shape
// without leaking internals (ADR-0012 / design/12 §7).
func mapSchedErr(err error) error {
	if err == nil {
		return nil
	}
	var se *postgres.SchedulingError
	if errors.As(err, &se) {
		return apierrors.New(se.Code, se.Msg)
	}
	var stateErr scheduling.StateError
	if errors.As(err, &stateErr) {
		return apierrors.New("INVALID_STATE", "状态流转不被允许")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apierrors.NotFound
	}
	return pgErr(err)
}

func operator(c *gin.Context) string {
	if v := c.GetHeader("X-Operator"); v != "" {
		return v
	}
	return Current(c).Login
}

func requireScheduling(c *gin.Context, write bool) bool {
	need := "scheduling.read"
	if write {
		need = "scheduling.write"
	}
	if !hasPerm(Current(c), need) {
		fail(c, apierrors.PermissionDenied)
		return false
	}
	return true
}

// ---- Reads ------------------------------------------------------------------

func (h *Handler) SchedulingBoard(c *gin.Context) {
	if !requireScheduling(c, false) {
		return
	}
	b, err := h.Store.SchedulingBoard(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, b, nil)
}

func (h *Handler) SchedulingRules(c *gin.Context) {
	if !requireScheduling(c, false) {
		return
	}
	v, err := h.Store.SchedulingRules(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) SchedulingPolicies(c *gin.Context) {
	if !requireScheduling(c, false) {
		return
	}
	v, err := h.Store.SchedulingPolicies(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) SchedulingTaskTypes(c *gin.Context) {
	if !requireScheduling(c, false) {
		return
	}
	v, err := h.Store.SchedulingTaskTypes(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) SchedulingEvents(c *gin.Context) {
	if !requireScheduling(c, false) {
		return
	}
	v, err := h.Store.SchedulingEvents(c.Request.Context(), nil, Current(c).ShopID)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

func (h *Handler) SchedulingAudit(c *gin.Context) {
	if !requireScheduling(c, false) {
		return
	}
	v, err := h.Store.SchedulingAudit(c.Request.Context(), nil, Current(c).ShopID, 0)
	if err != nil {
		fail(c, pgErr(err))
		return
	}
	ok(c, 200, v, nil)
}

// ---- Writes -----------------------------------------------------------------

type dispatchRequest struct {
	EventID     string `json:"event_id"`
	StaffID     string `json:"staff_id"`
	SourceQueue string `json:"source_queue_id"`
	TaskType    string `json:"task_type_id"`
	Method      string `json:"assignment_method"`
}

func (h *Handler) SchedulingDispatch(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req dispatchRequest
	if err := decode(c, &req); err != nil || req.EventID == "" || req.StaffID == "" ||
		req.SourceQueue == "" || req.TaskType == "" {
		badRequest(c, errors.New("event_id, staff_id, source_queue_id and task_type_id are required"))
		return
	}
	method := scheduling.AssignmentMethod(req.Method)
	if method == "" {
		method = scheduling.MethodRotational
	}
	shopID := Current(c).ShopID
	op := operator(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		rec, err := h.Store.Dispatch(c.Request.Context(), tx, shopID, postgres.DispatchInput{
			EventID: req.EventID, StaffPub: req.StaffID, SourceQueue: req.SourceQueue,
			TaskType: req.TaskType, Method: method, Operator: op,
		})
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, rec, nil), nil
	})
}

type pinRequest struct {
	QueueID   string `json:"queue_id"`
	StaffID   string `json:"staff_id"`
	PinType   string `json:"pin_type"`
	ExpiresAt string `json:"expires_at"`
}

func (h *Handler) SchedulingPin(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req pinRequest
	if err := decode(c, &req); err != nil || req.QueueID == "" || req.StaffID == "" || req.PinType == "" {
		badRequest(c, errors.New("queue_id, staff_id and pin_type are required"))
		return
	}
	var exp *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			badRequest(c, err)
			return
		}
		exp = &t
	}
	shopID := Current(c).ShopID
	op := operator(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		err := h.Store.SetPin(c.Request.Context(), tx, shopID, postgres.PinInput{
			QueuePub: req.QueueID, StaffPub: req.StaffID,
			PinType: scheduling.PinType(req.PinType), ExpiresAt: exp, Operator: op,
		})
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, gin.H{"pinned": true}, nil), nil
	})
}

func (h *Handler) SchedulingUnpin(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req struct {
		QueueID string `json:"queue_id"`
		StaffID string `json:"staff_id"`
	}
	if err := decode(c, &req); err != nil || req.QueueID == "" || req.StaffID == "" {
		badRequest(c, errors.New("queue_id and staff_id are required"))
		return
	}
	shopID := Current(c).ShopID
	op := operator(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.Unpin(c.Request.Context(), tx, shopID, req.QueueID, req.StaffID, op); err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, gin.H{"pinned": false}, nil), nil
	})
}

func (h *Handler) SchedulingReleaseExpired(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	shopID := Current(c).ShopID
	op := operator(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		n, err := h.Store.ReleaseExpired(c.Request.Context(), tx, shopID, op)
		if err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, gin.H{"released": n}, nil), nil
	})
}

func (h *Handler) schedulingEventAction(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requireScheduling(c, true) {
			return
		}
		eventID := c.Param("eventId")
		shopID := Current(c).ShopID
		op := operator(c)
		h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
			switch action {
			case "restore":
				if err := h.Store.Restore(c.Request.Context(), tx, shopID, eventID, op); err != nil {
					return 0, nil, mapSchedErr(err)
				}
			case "cancel":
				if err := h.Store.CancelTask(c.Request.Context(), tx, shopID, eventID, op); err != nil {
					return 0, nil, mapSchedErr(err)
				}
			case "complete":
				if err := h.Store.CompleteTask(c.Request.Context(), tx, shopID, eventID, op); err != nil {
					return 0, nil, mapSchedErr(err)
				}
			case "assignment-method":
				var req struct {
					Method string `json:"method"`
				}
				if err := decode(c, &req); err != nil || req.Method == "" {
					return 0, nil, apierrors.InvalidArgument
				}
				changed, err := h.Store.ChangeMethod(c.Request.Context(), tx, shopID, eventID,
					scheduling.AssignmentMethod(req.Method), op)
				if err != nil {
					return 0, nil, mapSchedErr(err)
				}
				return 200, h.envelope(c, gin.H{"changed": changed}, nil), nil
			}
			return 200, h.envelope(c, gin.H{"action": action, "event_id": eventID}, nil), nil
		})
	}
}

func (h *Handler) SchedulingPublishRules(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req struct {
		Rules []postgres.RuleInput `json:"rules"`
	}
	if err := decode(c, &req); err != nil || req.Rules == nil {
		badRequest(c, errors.New("rules array is required"))
		return
	}
	shopID := Current(c).ShopID
	op := operator(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.PublishRules(c.Request.Context(), tx, shopID, req.Rules, op); err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, gin.H{"rules": len(req.Rules)}, nil), nil
	})
}

func (h *Handler) SchedulingPublishPolicies(c *gin.Context) {
	if !requireScheduling(c, true) {
		return
	}
	var req struct {
		Policies []postgres.PolicyInput `json:"policies"`
	}
	if err := decode(c, &req); err != nil || req.Policies == nil {
		badRequest(c, errors.New("policies array is required"))
		return
	}
	shopID := Current(c).ShopID
	op := operator(c)
	h.runWrite(c, func(tx pgx.Tx) (int, any, error) {
		if err := h.Store.PublishPolicies(c.Request.Context(), tx, shopID, req.Policies, op); err != nil {
			return 0, nil, mapSchedErr(err)
		}
		return 200, h.envelope(c, gin.H{"policies": len(req.Policies)}, nil), nil
	})
}

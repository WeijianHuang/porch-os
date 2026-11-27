// Package errors defines the typed API error contract from design/03 §4.
// Handlers never expose database messages, SQL, or stack traces to clients.
package errors

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// Detail is a parameter-level explanation; it never contains internal state.
type Detail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// APIError is the only error type serialized to clients.
type APIError struct {
	Code       string   `json:"code"`
	Status     int      `json:"-"`
	Retryable  bool     `json:"retryable"`
	Message    string   `json:"message"`
	Details    []Detail `json:"details,omitempty"`
	RetryAfter int      `json:"-"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Sentinel constructors keep error codes in one place.
func New(code, message string) *APIError {
	status, retryable := mapCode(code)
	return &APIError{Code: code, Status: status, Retryable: retryable, Message: message}
}

func WithDetails(e *APIError, details ...Detail) *APIError {
	copy := *e
	copy.Details = details
	return &copy
}

func mapCode(code string) (int, bool) {
	switch code {
	case "INVALID_ARGUMENT", "STATE_UNCHANGED", "SHOP_CONTEXT_REQUIRED", "AUDIT_RETENTION_WINDOW_EXCEEDED",
		"CARD_INACTIVE", "MEMBER_UNCHANGED":
		return http.StatusBadRequest, false
	case "UNAUTHENTICATED":
		return http.StatusUnauthorized, false
	case "PERMISSION_DENIED":
		return http.StatusForbidden, false
	case "NOT_FOUND", "SNAPSHOT_MISSING", "EVENT_NOT_FOUND", "TASK_TYPE_NOT_FOUND", "CARD_NOT_FOUND",
		"STAFF_NOT_FOUND", "STAFF_CATEGORY_NOT_FOUND", "SHIFT_TEMPLATE_NOT_FOUND",
		"ROOM_NOT_FOUND", "GROUP_NOT_FOUND", "MEMBER_NOT_IN_GROUP":
		return http.StatusNotFound, false
	case "EXECUTOR_BUSY", "CARD_HELD", "STAFF_ALREADY_HOLDS_CARD", "CARD_IN_USE", "INSUFFICIENT_BALANCE",
		"CARD_NOT_HELD", "CARD_BOUND_TO_ORDER", "ROOM_INACTIVE", "ROOM_IN_USE",
		"GROUP_NOT_OPEN", "GROUP_HAS_UNSETTLED_MEMBERS", "GROUP_HAS_PAID_MEMBERS", "ORDER_SETTLED",
		"STAFF_INACTIVE", "STAFF_CATEGORY_INACTIVE":
		return http.StatusConflict, false
	case "NO_PRIMARY_QUEUE", "INVALID_STATE", "EXECUTOR_NOT_FOUND", "SITE_NOT_FOUND", "QUEUE_NOT_FOUND", "AUDIT_ACTOR_MISSING":
		return http.StatusBadRequest, false
	case "CONFLICT", "IDEMPOTENCY_CONFLICT", "IDEMPOTENCY_IN_PROGRESS", "VERSION_CONFLICT":
		return http.StatusConflict, code == "IDEMPOTENCY_IN_PROGRESS" || code == "VERSION_CONFLICT"
	case "PRECONDITION_FAILED":
		return http.StatusPreconditionFailed, false
	case "UNPROCESSABLE", "ORDER_CARD_REQUIRED", "ORDER_ROOM_REQUIRED",
		"ORDER_LINE_STAFF_REQUIRED", "STAFF_NOT_QUALIFIED":
		return http.StatusUnprocessableEntity, false
	case "RATE_LIMITED":
		return http.StatusTooManyRequests, true
	case "DEPENDENCY_FAILURE":
		return http.StatusBadGateway, true
	case "TIMEOUT":
		return http.StatusGatewayTimeout, true
	default:
		return http.StatusInternalServerError, true
	}
}

var (
	InvalidArgument   = New("INVALID_ARGUMENT", "请求参数无效")
	ShopRequired      = New("SHOP_CONTEXT_REQUIRED", "单店请求必须显式指定 X-Shop-Id")
	Unauthenticated   = New("UNAUTHENTICATED", "需要登录")
	PermissionDenied  = New("PERMISSION_DENIED", "无权访问该资源或门店")
	NotFound          = New("NOT_FOUND", "资源不存在")
	Conflict          = New("CONFLICT", "业务规则冲突")
	IdemConflict      = New("IDEMPOTENCY_CONFLICT", "同一幂等键的请求体不一致")
	IdemInProgress    = New("IDEMPOTENCY_IN_PROGRESS", "首次请求仍在处理，请稍后重试")
	Internal          = New("INTERNAL", "服务内部错误")
	DependencyFailure = New("DEPENDENCY_FAILURE", "依赖服务不可用")

	// ADR-0023: service-line executor assignment.
	StaffNotQualified     = New("STAFF_NOT_QUALIFIED", "该服务员没有这项服务的资质")
	StaffInactive         = New("STAFF_INACTIVE", "服务员已停用，不能执行服务")
	OrderLineStaffMissing = New("ORDER_LINE_STAFF_REQUIRED", "服务明细尚未指定服务员")

	// ADR-0024: staff categories are classification only, not qualification.
	StaffCategoryNotFound = New("STAFF_CATEGORY_NOT_FOUND", "服务人员分类不存在或不属于本店")
	StaffCategoryInactive = New("STAFF_CATEGORY_INACTIVE", "服务人员分类已停用，不能新绑定")

	// ADR-0025: reusable shift templates, one template per staff member.
	ShiftTemplateNotFound = New("SHIFT_TEMPLATE_NOT_FOUND", "班次模板不存在或不属于本店")
)

// FromPG maps PostgreSQL errors to the frozen contract without leaking text.
func FromPG(err error) *APIError {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return Conflict
		case "23503": // foreign_key_violation
			return InvalidArgument
		case "40001", "40P01": // serialization_failure / deadlock_detected
			return New("VERSION_CONFLICT", "并发冲突，请重读后重试")
		case "57014": // query_canceled (statement_timeout)
			return New("TIMEOUT", "查询超时")
		}
	}
	return Internal
}

// As extracts an APIError, wrapping unknown errors as INTERNAL.
func As(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return Internal
}

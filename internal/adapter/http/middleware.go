package http

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/domain/shop"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/WeijianHuang/porch-os/internal/security"
	"github.com/gin-gonic/gin"
)

// Recover must be the outermost middleware: no panic escapes as a raw 500.
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic", "request_id", Current(c).RequestID, "panic", r)
				fail(c, apierrors.Internal)
				c.Abort()
			}
		}()
		c.Next()
	}
}

func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := Current(c)
		p.RequestID = NewRequestID(c.GetHeader("X-Request-Id"))
		c.Header("X-Request-Id", p.RequestID)
		start := time.Now()
		c.Next()
		slog.Info("request", "request_id", p.RequestID, "method", c.Request.Method,
			"path", c.FullPath(), "status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(), "user_id", p.UserID, "shop_id", p.ShopID)
	}
}

// Auth derives identity from the access token and reloads roles/permissions and
// the visible shop set on every request. Nothing authorization-related is
// cached across requests (ADR-0006).
func Auth(store *postgres.Store, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(c, apierrors.Unauthenticated)
			c.Abort()
			return
		}
		userID, err := security.ParseAccess(jwtSecret, strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			fail(c, apierrors.Unauthenticated)
			c.Abort()
			return
		}
		ok, err := store.UserExists(c.Request.Context(), nil, userID)
		if err != nil || !ok {
			fail(c, apierrors.Unauthenticated)
			c.Abort()
			return
		}
		roles, perms, err := store.RolesAndPermissions(c.Request.Context(), nil, userID)
		if err != nil {
			fail(c, err)
			c.Abort()
			return
		}
		visible, err := store.VisibleShops(c.Request.Context(), nil, userID)
		if err != nil {
			fail(c, err)
			c.Abort()
			return
		}
		p := Current(c)
		login, err := store.UserByID(c.Request.Context(), nil, userID)
		if err != nil {
			fail(c, apierrors.Unauthenticated)
			c.Abort()
			return
		}
		p.UserID, p.Login, p.Roles, p.Perms, p.Visible = userID, login, roles, perms, visible
		c.Next()
	}
}

// ShopScope accepts only a shop in the current user's visible set, and rejects
// a mismatch between X-Shop-Id and a path {shopId}. Missing header is a hard
// 400 rather than an implicit default (ADR-0006).
func ShopScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := Current(c)
		header := c.GetHeader("X-Shop-Id")
		pathID := c.Param("shopId")
		if header == "" {
			fail(c, apierrors.ShopRequired)
			c.Abort()
			return
		}
		chosen := header
		if pathID != "" && header != pathID {
			fail(c, apierrors.WithDetails(apierrors.PermissionDenied,
				apierrors.Detail{Field: "shop_id", Reason: "header_path_mismatch"}))
			c.Abort()
			return
		}
		if !shop.Contains(p.Visible, chosen) {
			fail(c, apierrors.WithDetails(apierrors.PermissionDenied,
				apierrors.Detail{Field: "shop_id", Reason: "outside_visible_shops"}))
			c.Abort()
			return
		}
		p.ShopPID = chosen
		id, err := shopIDByPublicID(c, p.UserID, chosen)
		if err != nil {
			fail(c, err)
			c.Abort()
			return
		}
		p.ShopID = id
		c.Next()
	}
}

// shopIDByPublicID uses the store from the router context. Resolving by public
// ID happens only after visibility has passed, preventing ID enumeration.
var shopIDByPublicID func(c *gin.Context, userID int64, publicID string) (int64, error)

// Idempotency enforces the header on all writes and captures the request body
// so a handler can bind it and the fingerprint can be computed once.
func Idempotency() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		p := Current(c)
		key := c.GetHeader("Idempotency-Key")
		if key == "" || len(key) > 200 {
			fail(c, apierrors.WithDetails(apierrors.InvalidArgument,
				apierrors.Detail{Field: "Idempotency-Key", Reason: "required"}))
			c.Abort()
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err != nil {
			fail(c, apierrors.InvalidArgument)
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		p.IdemKey, p.Body = key, body
		c.Next()
	}
}

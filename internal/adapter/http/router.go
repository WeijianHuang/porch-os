package http

import (
	"strconv"

	"github.com/WeijianHuang/porch-os/internal/adapter/postgres"
	"github.com/WeijianHuang/porch-os/internal/platform/config"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/gin-gonic/gin"
)

// NewRouter wires the middleware chain in the frozen order (design/04 §4):
// recover → requestid/log → auth → shopscope → idempotency → handler.
func NewRouter(store *postgres.Store, cfg config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	h := &Handler{Store: store, Cfg: cfg}

	shopIDByPublicID = func(c *gin.Context, _ int64, publicID string) (int64, error) {
		_, id, err := store.ShopByPublicID(c.Request.Context(), nil, publicID)
		if err != nil {
			return 0, apierrors.PermissionDenied
		}
		return id, nil
	}

	r.Use(Recover(), RequestLog())
	r.GET("/health", func(c *gin.Context) { ok(c, 200, gin.H{"status": "ok"}, nil) })
	r.GET("/ready", func(c *gin.Context) {
		var version int64
		err := store.Pool.QueryRow(c.Request.Context(),
			`SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY version_id DESC LIMIT 1`).Scan(&version)
		if err != nil || cfg.ExpectedSchemaVersion == "" ||
			strconv.FormatInt(version, 10) != cfg.ExpectedSchemaVersion {
			fail(c, apierrors.New("DEPENDENCY_FAILURE", "数据库迁移版本不符"))
			return
		}
		ok(c, 200, gin.H{"status": "ready", "schema_version": version}, nil)
	})

	api := r.Group("/api/v1")
	api.Use(Idempotency())
	auth := api.Group("/auth")
	{
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.Refresh)
		auth.POST("/logout", h.Logout)
	}

	protected := api.Group("")
	protected.Use(Auth(store, cfg.JWTSecret))
	{
		protected.GET("/me", h.Me)
		protected.GET("/me/shops", h.MeShops)
		protected.GET("/me/permissions", h.MePermissions)

		protected.GET("/catalog-items", h.ListCatalogItems)
		protected.POST("/catalog-items", h.CreateCatalogItem)
		protected.GET("/catalog-items/:id", h.GetCatalogItem)
		protected.PATCH("/catalog-items/:id", h.UpdateCatalogItem)
		protected.GET("/catalog-categories", h.ListCategories)

		protected.GET("/members", h.ListMembers)
		protected.POST("/members", h.CreateMember)
		protected.GET("/members/:id", h.GetMember)
		protected.PATCH("/members/:id", h.UpdateMember)

		shops := protected.Group("/shops")
		shops.GET("", h.ListShops)
		shops.POST("", h.CreateShop)
		one := shops.Group("/:shopId", ShopScope())
		{
			one.GET("", h.GetShop)
			one.PATCH("", h.UpdateShop)
			one.GET("/settings", h.GetSettings)
			one.PATCH("/settings", h.UpdateSettings)
			one.PUT("/catalog-overrides/:itemId", h.PutOverride)
			one.GET("/members/:id/balance", h.GetBalance)
			one.GET("/members/:id/ledger", h.GetLedger)
			one.POST("/members/:id/topups", h.Topup)

			// Queues and staff (ADR-0009 / design/10): shop-scoped, one queue has
			// seq+name, a staff member serves in many queues of the same shop.
			one.GET("/queues", h.ListQueues)
			one.POST("/queues", h.CreateQueue)
			one.GET("/queues/:queueId", h.GetQueue)
			one.PATCH("/queues/:queueId", h.UpdateQueue)
			one.DELETE("/queues/:queueId", h.DeleteQueue)
			one.GET("/queues/:queueId/staff", h.ListQueueStaff)
			// Static path first: "order" must not be captured as :staffId.
			one.PUT("/queues/:queueId/staff/order", h.ReorderQueueStaff)
			one.PUT("/queues/:queueId/staff/:staffId", h.AssignStaff)
			one.DELETE("/queues/:queueId/staff/:staffId", h.UnassignStaff)
			// Single-member position shortcut (ADR-0015 §3.4).
			one.POST("/queues/:queueId/staff/:staffId/move", h.MoveQueueStaff)

			// GET /staff carries state + memberships for the staff wall.
			one.GET("/staff", h.ListStaffSummaries)
			one.POST("/staff", h.CreateStaff)
			one.GET("/staff/:staffId", h.GetStaff)
			one.PATCH("/staff/:staffId", h.UpdateStaff)
			one.DELETE("/staff/:staffId", h.DeleteStaff)
			// Operator controls on scheduling state / rotation (ADR-0015).
			one.POST("/staff/:staffId/state", h.SetStaffState)
			one.PATCH("/staff/:staffId/rotation", h.SetStaffRotation)

			// Rooms and floors (ADR-0011 / design/11): shop-scoped, one room has
			// a shop-local code and an integer floor grouping dimension.
			one.GET("/rooms", h.ListRooms)
			one.POST("/rooms", h.CreateRoom)
			one.GET("/rooms/:roomId", h.GetRoom)
			one.PATCH("/rooms/:roomId", h.UpdateRoom)
			one.DELETE("/rooms/:roomId", h.DeleteRoom)

			// Scheduling engine (ADR-0012 / design/12): shop-scoped, reuses staff
			// as executors and queue_staff.position as queue-local position.
			one.GET("/scheduling/board", h.SchedulingBoard)
			one.GET("/scheduling/rules", h.SchedulingRules)
			one.GET("/scheduling/policies", h.SchedulingPolicies)
			one.GET("/scheduling/task-types", h.SchedulingTaskTypes)
			one.GET("/scheduling/events", h.SchedulingEvents)
			one.GET("/scheduling/audit", h.SchedulingAudit)
			one.POST("/scheduling/dispatch", h.SchedulingDispatch)
			one.POST("/scheduling/pins", h.SchedulingPin)
			one.DELETE("/scheduling/pins", h.SchedulingUnpin)
			one.POST("/scheduling/pins/release-expired", h.SchedulingReleaseExpired)
			one.PUT("/scheduling/rules", h.SchedulingPublishRules)
			one.PUT("/scheduling/policies", h.SchedulingPublishPolicies)
			one.POST("/scheduling/events/:eventId/restore", h.schedulingEventAction("restore"))
			one.POST("/scheduling/events/:eventId/cancel", h.schedulingEventAction("cancel"))
			one.POST("/scheduling/events/:eventId/complete", h.schedulingEventAction("complete"))
			one.POST("/scheduling/events/:eventId/assignment-method", h.schedulingEventAction("assignment-method"))
		}
	}
	return r
}

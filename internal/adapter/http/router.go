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
		}
	}
	return r
}

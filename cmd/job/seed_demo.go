package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/WeijianHuang/porch-os/internal/platform/config"
	"github.com/jackc/pgx/v5"
)

// Demo service items: generic, shop-independent catalog entries with a
// nominal duration in minutes (ADR-0018). SKUs are globally unique so the
// insert is idempotent by definition.
type demoTool struct {
	sku      string
	name     string
	price    string
	duration int
}

var demoServiceItems = []demoTool{
	{"SVC-001", "标准服务 15 分钟", "39.00", 15},
	{"SVC-002", "标准服务 30 分钟", "68.00", 30},
	{"SVC-003", "进阶服务 45 分钟", "98.00", 45},
	{"SVC-004", "进阶服务 60 分钟", "128.00", 60},
	{"SVC-005", "深度服务 90 分钟", "188.00", 90},
	{"SVC-006", "深度服务 120 分钟", "238.00", 120},
	{"SVC-007", "快速服务 20 分钟", "49.00", 20},
	{"SVC-008", "综合服务 75 分钟", "158.00", 75},
	{"SVC-009", "专项服务 40 分钟", "88.00", 40},
	{"SVC-010", "尊享服务 180 分钟", "338.00", 180},
}

// seedDemo is a repeatable development helper: it writes 10 shop-scoped ID
// cards and 10 service catalog items. Card codes are unique per shop and
// catalog SKUs are unique globally; re-running inserts nothing new. This is
// never called by api/worker and is not part of any migration.
func seedDemo(ctx context.Context, cfg config.Config) error {
	shopID := int64(1)
	if raw := os.Getenv("SEED_SHOP_ID"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return errors.New("SEED_SHOP_ID must be a positive integer")
		}
		shopID = n
	}
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var shopExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shops WHERE id=$1)`, shopID).Scan(&shopExists); err != nil {
		return err
	}
	if !shopExists {
		return fmt.Errorf("shop %d does not exist", shopID)
	}

	cards := 0
	for i := 1; i <= 10; i++ {
		code := fmt.Sprintf("C-%03d", i)
		name := fmt.Sprintf("ID 卡 %02d", i)
		tag, err := tx.Exec(ctx, `
			INSERT INTO id_cards (shop_id, code, name, status)
			VALUES ($1,$2,$3,'active')
			ON CONFLICT (shop_id, code) DO NOTHING`, shopID, code, name)
		if err != nil {
			return err
		}
		cards += int(tag.RowsAffected())
	}

	items := 0
	for _, it := range demoServiceItems {
		tag, err := tx.Exec(ctx, `
			INSERT INTO catalog_items (sku, name, type, base_price, duration_minutes)
			VALUES ($1,$2,'service',$3::numeric,$4)
			ON CONFLICT (sku) DO NOTHING`, it.sku, it.name, it.price, it.duration)
		if err != nil {
			return err
		}
		items += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	slog.Info("seed demo complete", "shop_id", shopID,
		"cards_inserted", cards, "service_items_inserted", items)
	return nil
}

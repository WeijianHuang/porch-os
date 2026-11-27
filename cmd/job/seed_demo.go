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
	category string
}

var demoServiceItems = []demoTool{
	{"SVC-001", "标准服务 15 分钟", "39.00", 15, "CAT-A"},
	{"SVC-002", "标准服务 30 分钟", "68.00", 30, "CAT-A"},
	{"SVC-003", "进阶服务 45 分钟", "98.00", 45, "CAT-B"},
	{"SVC-004", "进阶服务 60 分钟", "128.00", 60, "CAT-B"},
	{"SVC-005", "深度服务 90 分钟", "188.00", 90, "CAT-C"},
	{"SVC-006", "深度服务 120 分钟", "238.00", 120, "CAT-C"},
	{"SVC-007", "快速服务 20 分钟", "49.00", 20, "CAT-D"},
	{"SVC-008", "综合服务 75 分钟", "158.00", 75, "CAT-C"},
	{"SVC-009", "专项服务 40 分钟", "88.00", 40, "CAT-B"},
	{"SVC-010", "尊享服务 180 分钟", "338.00", 180, "CAT-C"},
}

// ADR-0024: generic classification data only. Staff categories are
// shop-scoped; catalog categories are global grouping labels for the picker.
var demoStaffCategories = []struct {
	code, name string
	order      int
}{
	{"SC-A", "常规服务组", 1},
	{"SC-B", "专项服务组", 2},
}

// ADR-0025: generic, repeatable shift templates. These are definitions of
// daily time windows, not the order-domain `shifts` business instances.
var demoShiftTemplates = []struct {
	name, start, end string
	order            int
}{
	{"早班", "04:00", "16:00", 1},
	{"中班", "05:00", "18:00", 2},
	{"午班", "05:00", "17:00", 3},
	{"晚班", "07:00", "19:00", 4},
	{"夜班", "11:00", "20:00", 5},
}

var demoCatalogCategories = []struct {
	code, name string
	order      int
}{
	{"CAT-A", "常规服务", 1},
	{"CAT-B", "专项服务", 2},
	{"CAT-C", "深度服务", 3},
	{"CAT-D", "快捷服务", 4},
}

// The add-product picker groups products under a root category. Its two
// performance columns are read-only catalog metadata, never written to an
// order line (ADR-0023).
var demoProductCategories = []struct {
	code, name string
	order      int
}{
	{"CAT-PRODUCT-ROOT", "商品", 90},
	{"PROD-CONV", "便利商品", 91},
	{"PROD-DRINK", "饮品", 92},
	{"PROD-RETAIL", "零售商品", 93},
	{"PROD-OTHER", "其他商品", 94},
}

var demoProductItems = []struct {
	sku, name, price, category, attributes string
}{
	{"PROD-001", "瓶装饮品 A", "15.00", "PROD-DRINK", `{"performance_primary":"5.00","performance_secondary":"0.00"}`},
	{"PROD-002", "即食食品 A", "10.00", "PROD-CONV", `{"performance_primary":"3.00","performance_secondary":"0.00"}`},
	{"PROD-003", "清洁用品 A", "10.00", "PROD-RETAIL", `{"performance_primary":"3.00","performance_secondary":"0.00"}`},
	{"PROD-004", "日用小件 A", "30.00", "PROD-RETAIL", `{"performance_primary":"8.00","performance_secondary":"0.00"}`},
	{"PROD-005", "卫生用品 A", "78.00", "PROD-RETAIL", `{"performance_primary":"20.00","performance_secondary":"0.00"}`},
	{"PROD-006", "休闲食品 A", "10.00", "PROD-CONV", `{"performance_primary":"3.00","performance_secondary":"0.00"}`},
	{"PROD-007", "一次性用品 A", "38.00", "PROD-OTHER", `{"performance_primary":"10.00","performance_secondary":"0.00"}`},
	{"PROD-008", "罐装饮品 A", "29.00", "PROD-DRINK", `{"performance_primary":"5.00","performance_secondary":"0.00"}`},
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

	// ADR-0024: classification metadata and demo assignments. Re-running the
	// seed is idempotent: inserts are guarded and assignments are overwritten
	// with the same stable values.
	sc := 0
	for _, c := range demoStaffCategories {
		tag, err := tx.Exec(ctx, `
			INSERT INTO staff_categories (shop_id, code, name, sort_order)
			SELECT $1,$2,$3,$4
			WHERE NOT EXISTS (SELECT 1 FROM staff_categories WHERE shop_id=$1 AND code=$2)`,
			shopID, c.code, c.name, c.order)
		if err != nil {
			return err
		}
		sc += int(tag.RowsAffected())
	}
	cc := 0
	for _, c := range demoCatalogCategories {
		tag, err := tx.Exec(ctx, `
			INSERT INTO catalog_categories (code, name, sort_order)
			SELECT $1,$2,$3
			WHERE NOT EXISTS (SELECT 1 FROM catalog_categories WHERE parent_id IS NULL AND code=$1)`,
			c.code, c.name, c.order)
		if err != nil {
			return err
		}
		cc += int(tag.RowsAffected())
	}
	for _, c := range demoProductCategories {
		if c.code == "CAT-PRODUCT-ROOT" {
			tag, err := tx.Exec(ctx, `
				INSERT INTO catalog_categories (code, name, sort_order)
				SELECT $1,$2,$3
				WHERE NOT EXISTS (SELECT 1 FROM catalog_categories WHERE parent_id IS NULL AND code=$1)`,
				c.code, c.name, c.order)
			if err != nil {
				return err
			}
			cc += int(tag.RowsAffected())
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO catalog_categories (code, name, sort_order, parent_id)
			SELECT $1,$2,$3, r.id FROM catalog_categories r
			WHERE r.code='CAT-PRODUCT-ROOT'
			  AND NOT EXISTS (SELECT 1 FROM catalog_categories c WHERE c.code=$1)`,
			c.code, c.name, c.order)
		if err != nil {
			return err
		}
		cc += int(tag.RowsAffected())
	}
	if _, err := tx.Exec(ctx, `
		UPDATE staff st SET category_id = c.id
		FROM staff_categories c
		WHERE st.shop_id=$1 AND c.shop_id=$1
		  AND c.code = CASE WHEN st.id % 2 = 1 THEN 'SC-A' ELSE 'SC-B' END`, shopID); err != nil {
		return err
	}
	// ADR-0025: repeatable templates and a stable one-per-staff assignment.
	shifts := 0
	for i, t := range demoShiftTemplates {
		tag, err := tx.Exec(ctx, `
			INSERT INTO shift_templates (shop_id,name,start_time,end_time,sort_order)
			SELECT $1,$2,$3::time,$4::time,$5
			WHERE NOT EXISTS (SELECT 1 FROM shift_templates WHERE shop_id=$1 AND name=$2)`,
			shopID, t.name, t.start, t.end, t.order)
		if err != nil {
			return err
		}
		shifts += int(tag.RowsAffected())
		if _, err := tx.Exec(ctx, `
			UPDATE staff st SET shift_template_id = t.id
			FROM shift_templates t
			WHERE st.shop_id=$1 AND t.shop_id=$1 AND t.name=$2
			  AND (st.id % 5) = $3`, shopID, t.name, i); err != nil {
			return err
		}
	}
	for _, it := range demoServiceItems {
		if _, err := tx.Exec(ctx, `
			UPDATE catalog_items i SET category_id = c.id
			FROM catalog_categories c
			WHERE i.sku=$1 AND c.parent_id IS NULL AND c.code=$2`, it.sku, it.category); err != nil {
			return err
		}
	}
	products := 0
	for _, it := range demoProductItems {
		tag, err := tx.Exec(ctx, `
			INSERT INTO catalog_items (sku, name, type, base_price, category_id, attributes)
			SELECT $1,$2,'product',$3::numeric,c.id,$4::jsonb
			FROM catalog_categories c WHERE c.code=$5
			ON CONFLICT (sku) DO NOTHING`, it.sku, it.name, it.price, it.attributes, it.category)
		if err != nil {
			return err
		}
		products += int(tag.RowsAffected())
	}
	// The pre-existing generic product predates category grouping; assign it
	// once so the add-product tab bar never shows an uncategorized row.
	if _, err := tx.Exec(ctx, `
		UPDATE catalog_items i SET category_id = c.id
		FROM catalog_categories c
		WHERE i.sku='SMOKE-002' AND c.code='PROD-OTHER' AND i.category_id IS NULL`); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	slog.Info("seed demo complete", "shop_id", shopID,
		"cards_inserted", cards, "service_items_inserted", items,
		"product_items_inserted", products,
		"staff_categories_inserted", sc, "catalog_categories_inserted", cc,
		"shift_templates_inserted", shifts)
	return nil
}

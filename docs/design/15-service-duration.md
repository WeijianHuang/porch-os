# 15 · 服务类目录项时长（ADR-0018）

本册只给 `catalog_items` 增加一个一等字段 `duration_minutes`，不改 ADR-0012 判定链、ADR-0015 状态桶、ADR-0017 持卡与正方形格子。迁移 `00009_service_duration.sql`，schema 版本 **9**。全文只用通用术语。

## 1. 字段与约束

```sql
ALTER TABLE catalog_items
  ADD COLUMN duration_minutes integer,
  ADD CONSTRAINT ck_catalog_items_duration CHECK (
    (type = 'product' AND duration_minutes IS NULL) OR
    (type = 'service' AND duration_minutes BETWEEN 1 AND 1440)
  );
```

| 属性 | 值 |
|---|---|
| 语义 | 服务项目的标称时长（分钟），与订单 / 看板剩余分钟无关 |
| 可空 | 是；仅 `type='service'` 必须非空 |
| 范围 | 1..1440（含），正整数 |
| 隔离类别 | A（全局目录项，无 `shop_id`） |
| 单店差异 | 仍由 `shop_catalog_overrides` 管价格与上下架，**不**覆盖时长 |

CHECK 是最终防线：即使绕过 API 直连数据库写入非法组合也会被拒绝。既有 `product` 行的 `NULL` 满足新约束，无需回补。

## 2. 端点增量

沿用 `design/03` 第 180–182 行的目录端点，不新增路径、不改权限（`catalog.read` / `catalog.write`）。

| 端点 | 变更 |
|---|---|
| `POST /api/v1/catalog-items` | 请求体加 `duration_minutes?: number`；`service` 必填且整数 1..1440，`product` 携带即 400 |
| `GET /api/v1/catalog-items`、`GET /api/v1/catalog-items/{id}` | 响应加 `duration_minutes: number \| null` |
| `PATCH /api/v1/catalog-items/{id}` | 请求体加 `duration_minutes?: number`；只改时长不改 `type`，`version` 乐观锁不变 |

错误码沿用 `INVALID_ARGUMENT`（400）/ `VERSION_CONFLICT`（409），不新增。请求体示例：

```jsonc
// POST /api/v1/catalog-items
{ "sku": "SVC-001", "name": "标准服务 30 分钟", "type": "service",
  "base_price": "88.00", "duration_minutes": 30 }
```

## 3. 开发种子

`cmd/job seed-demo`（`SEED_SHOP_ID`，默认 1）幂等写入：

- **10 张 ID 卡**：`code = C-001..C-010`，`name = 体验卡 01..10` 等通用名，`status='active'`，`(shop_id, code)` 冲突跳过。
- **10 个服务目录项**：`sku = SVC-001..SVC-010`，`type='service'`，`duration_minutes` 取 15 / 30 / 45 / 60 / 90 / 120 等档位，`sku` 冲突跳过。

种子不写迁移、不由常驻进程自动执行；重复运行结果不变。卡片领取 / 释放仍走 ADR-0017 的 `POST .../hold|release`，种子只建档不预占。

## 4. 与剩余分钟的边界

| 字段 | 来源 | 本期 |
|---|---|---|
| `duration_minutes` | 目录建档 | 真实值（服务项） |
| `service_remaining_minutes` | 未来订单域 | 恒 `null`，前端显示「—」 |

看板正方形格子显示的是持卡名称（来自 `id_cards`）+ 剩余分钟占位（ADR-0017），**不**直接显示目录时长；目录时长只在商品目录页展示。

## 5. 验收

1. 迁移可上可下，`EXPECTED_SCHEMA_VERSION=9` 时 `/ready` 200。
2. 非法「类型 + 时长」组合被 CHECK 与 API 双重拒绝。
3. 目录页服务项显示「N 分钟」，商品项显示「—」。
4. `seed-demo` 连跑两次数据量不变；两种看板卡片持卡名称来自种子卡，剩余分钟仍为「—」。

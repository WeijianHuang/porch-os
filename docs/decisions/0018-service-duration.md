# ADR-0018 · 服务类目录项的时长字段

- 状态：Accepted（2026-11-21）
- 关联：[ADR-0004](0004-scope-baseline.md)（单币种 / 东八区 / 不打印 / 不离线）、[ADR-0012](0012-scheduling-engine.md)（调度引擎）、[ADR-0017](0017-id-cards-and-square-slots.md)（方形服务格子与剩余分钟预留位）
- 分册：[design/15-service-duration.md](../design/15-service-duration.md)
- 迁移：`00009_service_duration.sql`；`EXPECTED_SCHEMA_VERSION` 由 8 → **9**

## 背景

`catalog_items.type` 早已区分 `product` / `service`，但目录项只有 `base_price`，无法表达「一次服务本身要多久」。ADR-0017 明确把看板上的 `service_remaining_minutes` 恒置 `null`，因为那是**订单域的剩余时间**，本期不做倒计时。现场仍需要一个**与订单无关的目录属性**：服务项目的标称时长（分钟），用于建档、展示与将来的排程估算。

两者口径不同，不能混为一列：

| 字段 | 归属 | 来源 | 本期值 |
|---|---|---|---|
| `catalog_items.duration_minutes` | 目录项（全局） | 人工建档时录入 | 服务项必填的正整数 |
| `service_remaining_minutes` | 调度看板投影（每店每人） | 未来订单域 | 恒 `null`（ADR-0017 不变） |

因此本 ADR **不修订** ADR-0017 的任何条款：ID 卡两个 partial unique index、方形格子、持卡投影、剩余分钟占位保持不变。

## 决策

### 1. 在 `catalog_items` 上加一个可空的整数列

- `duration_minutes integer`，语义为**服务标称时长，单位分钟**；只有 `type='service'` 的目录项允许有值，取值 **1..1440**（1 分钟到 24 小时）。
- 用 CHECK 把「类型 ↔ 时长」钉死：`product` 必须 `NULL`，`service` 必须非 `NULL` 且在范围内。数据库是最后一道闸，绕过 API 写入也挡得住。
- 全局目录项不带 `shop_id`（A 类，与 `sku` 一致）；单店差异仍走 `shop_catalog_overrides`，本 ADR **不新增**按店时长覆盖。
- 不复用 `attributes jsonb`：时长要参与校验、排序与 API 契约，jsonb 无类型无约束，不适合做一等字段。

### 2. API 在同一组端点上增量扩展，不新增端点

- `POST /api/v1/catalog-items`：请求体新增可选 `duration_minutes`；`type='service'` 时必填且必须是 1..1440 的整数，`type='product'` 时若携带则报 `400 INVALID_ARGUMENT`。
- `PATCH /api/v1/catalog-items/{id}`：请求体新增可选 `duration_minutes`，仅用于把服务项的时长改到另一档；**`type` 仍不可改**，商品要变服务须新建目录项（沿用既有更新契约，避免同一行在两种约束间迁移）。
- `GET` 列表 / 详情：响应新增 `duration_minutes: number | null`。商品项为 `null`，前端显示「—」。
- 所有写入沿用既有 `catalog.write` 权限、`version` 乐观锁与幂等键；错误码沿用 `INVALID_ARGUMENT` / `VERSION_CONFLICT`，不新增错误码。

### 3. 开发种子数据只由 `cmd/job` 生成，不进迁移

- 新增 `cmd/job seed-demo`：幂等地写入**10 张 ID 卡**与**10 个服务类目录项**（带时长），作用域由 `SEED_SHOP_ID` 指定（默认 1），卡以 `(shop_id, code)` 冲突即跳过，目录项以 `sku` 冲突即跳过，重复执行不产生第二份。
- 迁移文件只改结构、不写业务样例（与 `00002_rbac_baseline.sql` 的既有约定一致）；种子是可重复执行的开发工具，任何环境都不自动调用。
- 种子名称只用通用词（服务 / 卡 / 分钟），不写任何门店专属或敏感词。

### 4. 时长不驱动任何自动行为

- 本期不根据时长做倒计时、不推算结束时刻、不自动派单、不校验「订单时长必须等于目录时长」。
- 时长不进入 ADR-0012 的判定链与联动规则；看板上的剩余分钟仍由未来订单域提供。
- 服务项时长上限 1440 分钟是**建档约束**，不是业务承诺；要放开上限需另立 ADR。

## 边界与不做

- **目录时长 ≠ 剩余分钟**：前者是静态标称值，后者是动态订单派生值，互不覆盖。
- **不按店覆盖时长**：`shop_catalog_overrides` 仍只管价格与上下架。
- **不新增品类 / 单位字段**：单位固定分钟，不提供小时 / 天切换，不设 `duration_unit`。
- **不改订单 / 收款 / 退款契约**：ADR-0013 / 0014 / 0016 保持原样。

## 验收标准

1. 迁移可上可下；`EXPECTED_SCHEMA_VERSION = 9` 时 `/ready` 200，设为 8 时 503。
2. 直接对库写入 `type='product'` 且 `duration_minutes>0`、或 `type='service'` 且时长为 0 / 1441 / `NULL`，全部被 CHECK 拒绝。
3. 创建服务项缺时长返回 400；创建商品项带时长返回 400；改时长走 `version` 乐观锁，冲突返回 409。
4. 目录列表 / 详情返回 `duration_minutes`，商品项为 `null`，前端列显示「—」、服务项显示「N 分钟」。
5. `cmd/job seed-demo` 连跑两次后，10 张卡与 10 个服务项数量不变；看板正方形格子的持卡名称来自这 10 张卡，剩余分钟仍显示「—」。

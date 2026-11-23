# ADR-0017 · ID 卡持有与方形服务格子

- 状态：Accepted（2026-11-21）
- 关联：[ADR-0009](0009-queues-and-staff.md)（队列与服务人员）、[ADR-0012](0012-scheduling-engine.md)（调度引擎）、[ADR-0015](0015-staff-state-and-position-controls.md)（状态人工控制）
- 分册：[design/14-id-cards-and-square-slots.md](../design/14-id-cards-and-square-slots.md)
- 迁移：`00008_id_cards.sql`；`EXPECTED_SCHEMA_VERSION` 由 7 → **8**

## 背景

调度看板与队列看板的服务人员格子目前只显示姓名、状态、位次，无法回答两个现场问题：**这个人当前拿着哪张卡**（开单前必须绑定一张卡），以及**这一单还剩几分钟**（服务有固定时长，剩余来自订单）。

目录项 `catalog_items.type = 'service'` 已有服务类型，但当前没有任何时长字段；订单域由 ADR-0013 / 0014 归属、尚未落地。因此「剩余分钟」本期**只做展示预留位**：看板响应里预留 `service_remaining_minutes`，恒为 `null`，前端格子显示占位符；不做倒计时、不从本地时钟推算、不新建时长列。等订单域落地后再补读取口径，另立 ADR。

ID 卡是本期唯一新增的业务实体。

## 决策

### 1. ID 卡是店内资源，可被多人先后持有，同一时刻只被一人持有

- `id_cards`：`shop_id` + 店内唯一的 `code`（卡号）+ `name`（卡名称）+ `status ∈ {active, inactive}`（生效 / 失效）。
- 持有关系独立成 `card_holdings`（一人一卡、一卡一人的历史流水）：
  - `UNIQUE (shop_id, card_id) WHERE released_at IS NULL` — 一张卡同时只有一个在持人；
  - `UNIQUE (shop_id, staff_id) WHERE released_at IS NULL` — 一个服务员同时只持有一张卡；
  - 复合外键 `(shop_id, card_id)`、`(shop_id, staff_id)` 在 DB 层锁同店。
- 领取 / 释放写流水，不改卡档案的 `version`；卡生效失效才改档案 `version`。失效的卡不能领取，释放不受状态限制。
- 不设卡片组、不设卡类型、不设押金 / 挂失 / 补卡。卡与订单的强制绑定校验已由 [ADR-0022](0022-order-card-binding-and-companion-groups.md) 修订并接受（一单一卡、提交前必绑、结算自动释放）。

### 2. 失效与占用的边界

- 置为 `inactive` 时若卡正被持有：`409 CARD_IN_USE`，先释放再失效，不自动踢人（不静默改变现场状态）。
- 领取已失效卡：`400 CARD_INACTIVE`；领取他人正持有的卡：`409 CARD_HELD`；领取已有卡的人：`409 STAFF_ALREADY_HOLDS_CARD`。
- 所有冲突都返回明确错误码，不做「自动换卡」「排队等卡」。

### 3. 看板投影

- `GET /scheduling/board` 的每位成员增加 `id_card: {public_id, code, name} | null`（当前持有）与 `service_remaining_minutes: null`（预留）。
- `GET /staff` 的 `StaffSummary` 同样增加这两个字段，服务人员页与队列页人员视图可复用。
- 不新增独立「当前持卡人」端点：卡片列表 `GET /cards` 直接返回每张卡的 `holder`（`public_id` / `display_name` / `held_from`），一次查询折叠，无 N+1。

### 4. 方形格子只改两处

- 调度看板位次卡（`/board`）、队列看板矩阵卡（`/queues` 矩阵视图）改为**正方形**（`aspect-ratio: 1`），内容四行：姓名 → 状态 pill → 剩余分钟（预留占位）→ ID 卡名称。
- 队列泳道卡与人员视图**不改形状**（横向文本卡片塞成正方形会挤压），但卡片内增加 ID 卡名称一行。
- 状态色沿用 ADR-0015 的 8 桶，不在本 ADR 里重新定义配色。

### 5. 权限与审计

- 新增 `card.read` / `card.write`：admin / manager 全给，staff 只读。
- 建档、改名、生效失效、领取、释放全部写 `audit_logs`，`target_type = 'card'`，actor 为登录用户；写操作带 `Idempotency-Key`。

## 边界与不做

- **卡片 ≠ 订单（本 ADR 原边界，已由 ADR-0022 修订）**：开单必须持卡、订单绑卡、结算 / 取消自动释放持卡关系均由 ADR-0022 定义；本 ADR 仍不自动派单绑卡。
- **剩余分钟 ≠ 倒计时**：本期恒 `null`，前端显示「—」，不做本地计时、不推算结束时刻。
- **失效 ≠ 释放**：两张不同状态位，失效不自动释放、释放不改生效状态。释放来源不再只有人工端点：ADR-0022 增加了订单结算 / 取消的自动释放，通过 `release_reason` 区分。
- 不做卡片搜索 / 分页（首版店内全量，量级与队列一致），不做批量领取，不做跨店转移。

## 验收标准

1. 迁移可上可下；`EXPECTED_SCHEMA_VERSION = 8` 时 `/ready` 200，设为 7 时 503。
2. 同一张卡并发领取只有一人成功，另一人得到 `CARD_HELD`；同一人并发领两张卡只有一张成功。
3. 卡失效后领取返回 `CARD_INACTIVE`；持有的卡失效返回 `CARD_IN_USE`。
4. 看板与 `/staff` 返回持卡信息，`service_remaining_minutes` 为 `null`；前端格子显示占位符而非伪造数字。
5. 两张看板格子为正方形，状态色与 ADR-0015 一致；窄屏不裁切。

# ADR-0022 · 订单绑定 ID 卡与房间、同行组（合并 / 拆分共同结算）

- 状态：Accepted（2026-11-23；同日按业务确认修订：增加「结算后自动释放卡」与「订单同时绑定房间」）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（仅 `shop_id` 一层隔离、复合外键锁同店）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0011](0011-rooms-and-floors.md)（房间实体；本 ADR 修订其「不与单据绑定」边界）、[ADR-0013](0013-payment-and-refund-ownership.md) / [ADR-0014](0014-derived-amounts-and-refund-entries.md) / [ADR-0016](0016-refund-source-and-topup-reversal.md)（支付 / 派生金额 / 退款归属不变）、[ADR-0017](0017-id-cards-and-square-slots.md)（ID 卡与持卡关系；本 ADR 修订其「不校验开单必须持卡」与「人工释放」边界）、[ADR-0019](0019-order-price-preview.md)（试价）、[ADR-0021](0021-rename-order-price-adjustments.md)（订单域表命名惯例）
- 影响分册：修订 `design/01`（§2 聚合 / §4 字段）、`design/02`（§2 表清单 / §3 DDL）、`design/03`（§4 错误码 / §6 幂等 / §8 端点）、`design/06`（新增 F-09 流程）、`design/11`（§1 范围 / §4 契约 / §5 事务）、`design/14`（§6 边界修订）、`design/16`（P1 / P2 / P8 由「待 ADR」改为「已接受」）；不新增分册
- 迁移：新增 `migrations/00011_order_cards_rooms_and_companion_groups.sql`（`orders` 加列、`rooms` 补唯一键、2 张新表）；`EXPECTED_SCHEMA_VERSION` 由 10 → **11**

## 背景

`design/16` 初版把收银原型 R2 的「同行」误读为「多张 ID 卡绑定到同一订单」。业务确认后共三点：

1. **原始关系是「一个订单绑定一张 ID 卡」**，且**一张 ID 卡在同一时刻只对应一个订单**；
2. **「同行」不是一单多卡，而是「多个各自已绑卡的单据」组成一组一起买单结算**，组内**可合并、可拆分**；
3. **订单结算后自动释放卡**（同时结束订单占用与人员持卡关系，卡立即可被下一人领取），且**订单同时绑定房间**（工作台顶部的房间上下文必须落到单据上）。

订单域（schema 10）已落地，但 `orders` 无 `card_id` / `room_id`；ID 卡域（schema 8）只建模「人持卡」（`card_holdings`），不动「单绑卡」；房间域（schema 6）明确「不与单据绑定」。本 ADR 一次性补上「单↔卡」「单↔房间」「单↔组」三层关系，**不改支付 / 退款 / 状态机 / 金额派生口径**。

初版误读若落到代码上会导致：同一张卡被多单引用、拆单时无法判定该退哪张卡的账、组内付款主体不清；而缺少自动释放会让卡结算后仍被占用、现场无法复用。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| "同行是不是多张卡绑到一张单" | **不是**。一单一卡（1:1）；同行是把**多个各自绑卡的单据**组成一组共同结算。合并不是把多张卡塞进一张单 |
| "一卡一单是不是永久绑定" | 不是。指**同一时刻**一张活跃订单只占一张卡；结算后自动释放，卡可被下一张单绑定（可复用店内资源，ADR-0017）。历史订单保留 `card_id` + 释放时间 / 原因供追溯 |
| "结算后自动释放卡吗" | **必须自动释放，且释放两层**：① 订单的卡绑定置 `card_released_at`；② 该卡当前有效的 `card_holdings` 置 `released_at` 且 `release_reason='order_settled'`。两步与支付写同事务，不依赖人工调用释放端点 |
| "什么时候算结算完成" | 订单经 `POST /orders/{id}/payments` 达到 `paid` **且派生 `payable_amount <= 0`**（ADR-0014 口径）。仅部分支付未结清不释放 |
| "取消订单释放吗" | 释放，原因记为 `order_cancelled`；否则卡被废弃单永久占用。`completed` 与退款不重复触发（条件更新 `released_at IS NULL`） |
| "手动解绑 / 换卡也释放持卡吗" | **不释放持卡**，只结束订单侧绑定（原因 `unbound` / `replaced`）；人员仍持卡，可再绑到别的单。人员持卡仍需人工 `release` 或被其它单结算自动释放 |
| "订单绑定房间是什么意思" | `orders.room_id` 落到工作台顶部选定的房间，复合外键锁同店；提交结账前必须有有效房间（修订 ADR-0011「不与单据绑定」） |
| "一间房能同时有几张单" | **多张，且允许同时活跃**。多个客人同处一房时，每位客人各自开单、各绑各的卡；**分开买单** = 各单独立走 `POST /orders/{id}/payments`；**合并买单** = 把这些单放进同一个同行组，在同一屏一起结算，但付款仍逐单落账。房间维度**不设唯一约束、不做互斥** |
| "房间改名 / 停用后历史单怎么显示" | 不落快照：详情按当前 `rooms` 行展示 `floor/code/name`；已绑房间停用不影响结算，只是不可再被新单绑定 |
| "合并 / 拆分改的是什么" | 只改**单据的分组归属**：合并 = 独立单入组（或从 A 组挪到 B 组），拆分 = 成员单出组恢复独立。两者都**不改单据的卡 / 房间绑定、明细、金额** |
| "同行组是不是付款主体" | **不是**。付款仍逐单走 `POST /orders/{id}/payments`（ADR-0013/0014）；组只提供「多单一并结算」的编排与应付合计视图 |
| "一单能同时在两个同行组吗" | **不能**。活跃成员关系 `UNIQUE (shop_id, order_id) WHERE left_at IS NULL` 在 DB 层保证 |
| "组内两张单能用同一张卡吗" | **不能**。活跃订单对卡的部分唯一索引已排除 |
| "要不要新增权限点" | **不要**。绑卡 / 绑房间 / 建组 / 合并 / 拆分都属于订单写操作，复用 `order.read` / `order.write` |
| "开单必须持卡 / 有房间吗" | 草稿可不绑；**提交结账前必须已绑有效 ID 卡与有效房间**，否则分别 `422 ORDER_CARD_REQUIRED` / `ORDER_ROOM_REQUIRED` |
| "能跨店建同行组 / 绑别店卡或房间吗" | **不能**。所有新列 / 表带 `shop_id`，复合外键把组、成员、订单、卡、房间钉在同一店 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 卡绑在哪 | `orders.card_id`（整单一张） | `order_lines.card_id`（每行可不同卡） | 与「一个订单一个绑定一个 ID 卡」直接冲突；行级归属会让拆单 / 退款按行分裂 |
| 卡占用如何标记 | `card_released_at` + `card_release_reason` 留史，部分唯一索引按释放时间判定 | 释放时把 `card_id` 置 NULL | 清空后无法回答「这张单当时用的哪张卡」；审计追溯断裂 |
| 自动释放的触发点 | 应用服务在支付 / 取消的同事务内条件更新，DB 部分唯一索引兜底 | 数据库触发器自动改写 `card_holdings` | 触发器不可见、难单测，且与「业务写入与 outbox 同事务」的既有横切机制割裂 |
| 结算释放范围 | 同时释放订单绑定与人员持有 | 只释放订单绑定，持有仍人工释放 | 现场要的是卡立即可复用；只释放订单层会让卡仍被前一位占用 |
| 房间与订单基数 | 房间 1:N 订单，不做房间互斥 | 一房间同一时刻仅一张活跃单 | 业务只要求「订单带上房间」，未提出互斥；加唯一索引会误拒同一房间的多张单 |
| 房间是否快照 | 只存 `room_id`，展示取当前房间 | 订单冗余 `room_code_snapshot` / `room_name_snapshot` | 增加两列与变更同步规则，业务当前只需要定位到房间；如后续要历史名称另立 ADR |
| 同行组是否落表 | 持久化 `companion_groups` + 成员表 | 只在请求里传一组 `order_ids`，不落库 | 合并 / 拆分要求跨请求保持分组状态，纯请求态无法表达「已属于某组」 |
| 结算主体 | 逐单付款，组只给合计视图 | 组级收款端点，一笔钱按规则分摊到各单 | 分摊规则与新退款口径要重定义，冲击 ADR-0013/0014；业务澄清未要求新付款机制 |
| 拆出历史 | `left_at` 软删除，保留历史成员行 | 物理删除成员行 | 审计需要回答「某单曾在哪个组」 |
| 跨组合入 | 合入时自动从原组移出；原组空则同事务关闭 | 拒绝跨组合入，必须先手动拆出 | 多步操作无法原子完成，中途失败会留下半合并状态 |
| 对外标识 | 请求 / 响应用 `public_id`（UUID），库内 `bigint` | 对外直接用内部 bigint | 违反 `design/03` §1「内部 bigint 不出现在 URL 与响应」 |

## 决策

### 1. 数据模型（迁移 `00011`，schema 10 → 11）

```sql
-- (0) 房间补 (shop_id, id) 唯一键，供订单复合外键引用（ADR-0003 惯例）
ALTER TABLE rooms
  ADD CONSTRAINT uq_rooms_shop_id_id UNIQUE (shop_id, id);

-- (1) 订单绑定一张 ID 卡与一个房间：可空（草稿先建后绑），复合外键锁同店
ALTER TABLE orders
  ADD COLUMN card_id            bigint,
  ADD COLUMN card_released_at   timestamptz,
  ADD COLUMN card_release_reason text,
  ADD COLUMN room_id            bigint,
  ADD CONSTRAINT fk_orders_card FOREIGN KEY (shop_id, card_id)
    REFERENCES id_cards (shop_id, id),
  ADD CONSTRAINT fk_orders_room FOREIGN KEY (shop_id, room_id)
    REFERENCES rooms (shop_id, id),
  ADD CONSTRAINT ck_orders_card_release CHECK (
    (card_id IS NULL AND card_released_at IS NULL AND card_release_reason IS NULL)
    OR (card_id IS NOT NULL AND card_released_at IS NOT NULL AND card_release_reason IS NOT NULL)
    OR (card_id IS NOT NULL AND card_released_at IS NULL AND card_release_reason IS NULL)
  );

-- 一张卡同一时刻只被一张「有效绑定」的订单占用；释放后索引条目移除，卡可复用
CREATE UNIQUE INDEX uq_orders_active_card ON orders (shop_id, card_id)
  WHERE card_id IS NOT NULL AND card_released_at IS NULL;
CREATE INDEX idx_orders_shop_card ON orders (shop_id, card_id);
CREATE INDEX idx_orders_shop_room ON orders (shop_id, room_id);

-- (2) 同行组：B 类业务表，一店一组集合
CREATE TABLE companion_groups (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  status      text        NOT NULL DEFAULT 'open',
  version     integer     NOT NULL DEFAULT 0,
  created_by  bigint      NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_cg_public_id  UNIQUE (public_id),
  CONSTRAINT uq_cg_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT ck_cg_status CHECK (status IN ('open','closed','cancelled'))
);
CREATE INDEX idx_cg_shop_status ON companion_groups (shop_id, status, created_at DESC);

-- (3) 组成员：一单同一时刻只属一个 open 组；left_at 保留拆出历史
CREATE TABLE companion_group_members (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id    bigint      NOT NULL,
  group_id   bigint      NOT NULL,
  order_id   bigint      NOT NULL,
  joined_at  timestamptz NOT NULL DEFAULT now(),
  left_at    timestamptz,
  CONSTRAINT fk_cgm_group FOREIGN KEY (shop_id, group_id)
    REFERENCES companion_groups (shop_id, id),
  CONSTRAINT fk_cgm_order FOREIGN KEY (shop_id, order_id)
    REFERENCES orders (shop_id, id)
);
CREATE UNIQUE INDEX uq_cgm_active_order ON companion_group_members (shop_id, order_id)
  WHERE left_at IS NULL;
CREATE INDEX idx_cgm_shop_group ON companion_group_members (shop_id, group_id)
  WHERE left_at IS NULL;
```

- 新列 / 新表全部带 `shop_id`；三条复合外键 `(shop_id, card_id)` / `(shop_id, room_id)` / `(shop_id, group_id)` / `(shop_id, order_id)` 在 DB 层堵死跨店，延续 ADR-0003。
- **有效卡绑定** := `card_id IS NOT NULL AND card_released_at IS NULL`。`card_release_reason` 取值 `settled` / `cancelled` / `unbound` / `replaced`，与释放时间成对出现；订单详情的 `card` 只返回有效绑定，另返回 `card_binding_history`（最近一次的卡 `public_id/code/name` + `released_at` + `reason`）。
- `companion_groups.status`：`open`（可合入 / 拆出）→ `closed`（结算完毕或成员清空）/ `cancelled`（人工放弃，要求组内无成功支付）。`closed` / `cancelled` 为终态。
- `order_lines` / `order_price_adjustments` / `payments` / `refunds` **均不改动**；房间与组都不产生新的金额真相。

### 2. 卡绑定端点（门店作用域）

| Method | Path | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/shops/{shopId}/orders/{orderId}/card` | `order.write` | 绑定 / 更换：`{card_public_id, version}` |
| DELETE | `/api/v1/shops/{shopId}/orders/{orderId}/card` | `order.write` | 解绑：`?version=`，订单绑定置释放（`reason=unbound`），**不释放持卡** |

- **前置校验**：卡属本店、`status='active'`、当前被本店服务人员持有（`card_holdings.released_at IS NULL`）；订单属本店且处于 `draft` / `open` / `pending_payment` 且无任何 `succeeded` 支付。
- **并发**：对 `id_cards` 行 `SELECT ... FOR UPDATE` 串行化同一张卡的绑定，`uq_orders_active_card` 兜底；冲突与 `version` 不符分别返回 409。
- **提交结账**：`POST /orders/{id}/submit` 新增前置校验 —— 有效卡绑定存在且卡仍 `active` / 未被释放，否则 `422 ORDER_CARD_REQUIRED`。这是对 ADR-0017 边界的显式修订。
- `GET /cards` 响应在 ADR-0017 的 `holder` 之外增加 `bound_order: {public_id, order_no, status} | null`（当前有效占用该卡的订单），一次查询折叠，不逐卡请求。

### 3. 房间绑定端点（门店作用域）

| Method | Path | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/shops/{shopId}/orders` | `order.write` | 开单请求体扩展 `{..., room_public_id?, card_public_id?}`；两者可空，校验同专属端点 |
| POST | `/api/v1/shops/{shopId}/orders/{orderId}/room` | `order.write` | 绑定 / 更换房间：`{room_public_id, version}`；仅未结算单可用 |

- **前置校验**：房间属本店、`status='active'`；订单属本店且未结算（`draft` / `open` / `pending_payment` 且无 succeeded 支付）。已绑定的房间被停用不回滚旧单，只阻止新绑定。
- **同一房间多单（业务确认）**：多个客人同处一房时各自开单，一个房间**允许同时存在多张 `draft` / `open` / `pending_payment` 订单**，不建房间维度的唯一索引、不校验房间占用。各单独立绑卡（仍是一单一卡），分开买单时各自收款；要合并买单则把这些单放进同一同行组（§5），组只做同屏结算编排，付款仍逐单落 `payments`。
- **提交结账**：`POST /orders/{id}/submit` 新增前置校验 —— `room_id IS NOT NULL` 且房间仍属本店，否则 `422 ORDER_ROOM_REQUIRED`。
- **房间详情投影**：订单详情 / 列表的 `room: {public_id, floor, code, name} | null`，从当前 `rooms` 行读取，不落快照。
- **删除房间**：`rooms` 被 `orders.room_id` 引用时外键默认 `RESTRICT`，`DELETE /rooms/{roomId}` 返回 `409 ROOM_IN_USE`；不静默解绑历史单。
- 不提供「清空房间」端点，换房用 `POST .../room` 覆盖；不约束同一房间的活跃订单数；不新增 `GET /orders?room_public_id=` 过滤（确需另立 ADR）。

### 4. 结算 / 取消时自动释放卡（与支付同事务）

`POST /orders/{id}/payments` 使订单达到 `paid` 且派生 `payable_amount <= 0` 时，业务服务在同一事务内依次执行：

1. 更新订单状态 / `version`，同时 `UPDATE orders SET card_released_at = now(), card_release_reason = 'settled' WHERE id = $1 AND shop_id = $2 AND card_released_at IS NULL`；
2. 若订单有有效卡绑定：`UPDATE card_holdings SET released_at = now(), release_reason = 'order_settled' WHERE shop_id = $2 AND card_id = $3 AND released_at IS NULL`；
3. 追加 `audit_logs`（`target_type='order'` 的 `card_released` 与 `target_type='card'` 的 `release`，actor 为操作用户），追加 `outbox_events`（`order.card_released`、`card.hold_released`，载荷带 `reason`）；
4. 以上与支付写入 / 幂等记录同事务提交；任一失败整体回滚。

`POST /orders/{id}/cancel` 走同一序列，`reason='cancelled'` / `release_reason='order_cancelled'`。`completed`、退款、组关闭均不重复触发（第 1/2 步的 `IS NULL` 条件保证幂等）；部分支付未结清不释放。同幂等键重放支付返回首次结果，不重复写审计 / 事件。

释放后：订单侧 `card` 为 null，卡可再次被本店人员领取并绑到新单；历史单仍能通过 `card_binding_history` 追溯。

### 5. 同行组端点（门店作用域）

| Method | Path | 权限 | 说明 |
|---|---|---|---|
| POST | `/api/v1/shops/{shopId}/companion-groups` | `order.write` | 建组：`{order_public_ids?}`；空组允许，`status=open` |
| GET | `/api/v1/shops/{shopId}/companion-groups` | `order.read` | 列表（按 `status` 筛选、keyset 分页） |
| GET | `/api/v1/shops/{shopId}/companion-groups/{groupId}` | `order.read` | 详情：成员单 + 各自派生金额 + 组内合计 |
| POST | `/api/v1/shops/{shopId}/companion-groups/{groupId}/members` | `order.write` | 合入：`{order_public_ids[], version}` |
| DELETE | `/api/v1/shops/{shopId}/companion-groups/{groupId}/members/{orderId}` | `order.write` | 拆出：`?version=` |
| POST | `/api/v1/shops/{shopId}/companion-groups/{groupId}/close` | `order.write` | 关闭：`{version}`，要求全部活跃成员已结算或无活跃成员 |
| POST | `/api/v1/shops/{shopId}/companion-groups/{groupId}/cancel` | `order.write` | 取消：`{version}`，要求成员无成功支付 |

- `{groupId}` / `{orderId}` / `card_public_id` / `room_public_id` 均为 `public_id`（UUID），内部 bigint 不出 URL / 响应（design/03 §1）。
- **合入（合并）**：对目标组 `SELECT ... FOR UPDATE` + `version` 乐观锁；每个入组单必须未结算且属本店。若该单已在另一个 `open` 组，同事务把原成员行置 `left_at = now()`；原组因此无活跃成员时同事务置 `closed`。已与本组关联的活跃单返回 `400 MEMBER_UNCHANGED`。
- **拆出（拆分）**：把成员行置 `left_at = now()`，订单恢复独立；**不改 `orders.card_id` / `room_id`、明细、金额**；组无活跃成员时同事务置 `closed`。
- **并发**：组级写串行化依赖 `FOR UPDATE` + `version`，`uq_cgm_active_order` 兜底「一单只属一组」。
- 所有写端点要求 `Idempotency-Key`（design/03 §6），写 `audit_logs` 与 `outbox_events`，与业务写同事务。

### 6. 金额与结算口径（不新增付款主体）

- 组内**不存金额**，房间也不影响金额。详情端点的派生口径：
  - 每成员单沿用 ADR-0014 派生：`total_amount` / `discount_amount` 不可变，实付 / 已退 / 可退从 succeeded 的 `payments` / `refunds` 聚合；
  - `payable_amount = total_amount − paid_amount`；
  - 组级 `payable_total = Σ 活跃成员 payable_amount`、`paid_total = Σ paid_amount`，**只读派生、不落库**。
- 付款仍由 `POST /orders/{id}/payments` 逐单完成；收银界面用组详情一次性展示多单应付，但每单各自生成 `payments` / `payment_entries`，退款仍按原渠道逐单退（ADR-0013/0014/0016 全不变）。
- 组关闭不触发自动扣款 / 自动退款 / 自动清分（ADR-0007 NQ-16）。

### 7. 错误码（复用 + 新增）

新增：`CARD_NOT_HELD`（409，卡当前无人持有）、`CARD_BOUND_TO_ORDER`（409，卡已被另一活跃订单占用）、`ORDER_CARD_REQUIRED`（422）、`ORDER_ROOM_REQUIRED`（422）、`ROOM_NOT_FOUND`（404）、`ROOM_INACTIVE`（409）、`ROOM_IN_USE`（409，删除被订单引用的房间）、`GROUP_NOT_FOUND`（404）、`GROUP_NOT_OPEN`（409）、`ORDER_SETTLED`（409，已结算单不可改分组 / 卡 / 房间）、`MEMBER_NOT_IN_GROUP`（404）、`MEMBER_UNCHANGED`（400）。
复用：`CARD_NOT_FOUND` / `CARD_INACTIVE`（ADR-0017）、`NOT_FOUND`（ADR-0011 房间不存在时按端点语义映射到 `ROOM_NOT_FOUND`）、`VERSION_CONFLICT`（409）、`SHOP_CONTEXT_REQUIRED`（400）、`PERMISSION_DENIED`（403）。一律不静默改现场状态。

### 8. 审计与事件

- `audit_logs` 追加：`target_type='order'`（绑卡 / 换卡 / 解绑 / 绑房间 / 换房间 / `card_released` / 组操作）、`target_type='card'`（结算 / 取消触发的 `release`，actor 为操作用户，区别于人工释放）。因校验失败被拒的请求不写业务审计。
- `outbox_events`：`order.card_bound` / `order.card_replaced` / `order.card_unbound` / `order.card_released` / `order.room_bound` / `card.hold_released` / `companion_group.created` / `.member_added` / `.member_removed` / `.closed` / `.cancelled`。
- 测试需覆盖：同一幂等键重放不重复写审计 / 事件；并发合入两组只有一个成功；一张卡并发绑两单只有一个成功；结算释放与支付同事务回滚。

## 理由

1. **纠正模型误读**：一单一卡 + 多单成组，与业务澄清逐字一致；把「不能一单多卡」写成 DB 约束，而不是靠前端约定。
2. **现场可用性**：结算后自动释放两层绑定，卡立刻回到可领取池，避免人工漏释放导致卡被占用；取消单同样释放，防止废弃单锁死卡。
3. **房间上下文落单**：工作台顶部选中的房间必须有持久归属，否则事后无法回答「这单在哪个房间」；沿用复合外键锁同店，不引入新的隔离维度。房间只作归属、不作占用：同房多客多单是常态，分开买或合并买由「是否进同一同行组」决定，不由房间约束决定。
4. **不冲击已冻结契约**：付款 / 退款 / 状态机 / 金额派生 / 试价 / 让利结构一行不改，只加列与两张表及一组编排端点，符合 ADR-0008 的受控扩展方式。
5. **可追溯**：卡绑定用 `card_released_at` + `reason` 留史，持卡用 `release_reason` 留史，成员拆出用 `left_at` 留史；事后能回答「这张卡 / 这单当时在哪个组、哪个房间」。

## 后果与限制

- `EXPECTED_SCHEMA_VERSION` 升 11；迁移须验证 `up → down-to 10 → up` 无残留。现有订单 `card_id` / `room_id` 全为 NULL，无需回填；但**旧单在未绑卡 / 未绑房间前无法提交或付款**（校验对存量草稿同样生效），需在发布说明中提示。
- 提交结账新增两个 422 分支：`porch-console` 的无卡 / 无房间草稿必须先补齐；试价端点（ADR-0019）仍不校验，保持只读。
- `GET /cards` 响应新增 `bound_order`（可空）；订单详情新增 `card` / `card_binding_history` / `room`，前端需容忍旧后端缺这些字段。
- 自动释放使 ADR-0017 的「失效不自动释放、释放不改生效状态」仍成立，但**「释放只能人工调用」被修订**：结算 / 取消是新的自动释放来源，释放原因可区分。卡的 `status='active'/'inactive'` 不受自动释放影响。
- ADR-0011「房间不与单据绑定」被修订：房间可新增「被订单引用」关系，删除房间需先确认无引用（409 `ROOM_IN_USE`）；不引入房间占用 / 预订状态机。**同一房间可同时挂多张活跃订单**（多个客人同房），房间列表 / 详情不提供「当前是否占用」的判断；若后续要做房间占用视图，须基于 `orders` 实时派生，不新增占用状态列。
- 组详情 `payable_total` 是只读派生；并发付款时读到的合计可能瞬时滞后，前端须以逐单付款响应为准刷新。
- 仅 `order.write`（店长 + 管理员）可绑卡 / 绑房间 / 建组 / 合并 / 拆分；店员只读。若收银场景要放开店员写权限，须另立 ADR。
- 公开仓库脱钩约束不变：全文只用通用术语（订单 / ID 卡 / 房间 / 同行组 / 结算），不出现内部项目名或行业专属词。

## 验收标准

1. 迁移 `up → down-to 10 → up` 通过；`/ready` 在 `EXPECTED_SCHEMA_VERSION=11` 返回 200，设为 10 返回 503。
2. 同一张卡并发绑定两张活跃订单，仅一张成功，另一张 `409 CARD_BOUND_TO_ORDER`；订单结算后该卡可绑新单。
3. 未绑卡 / 未绑房间的订单提交分别返回 `422 ORDER_CARD_REQUIRED` / `422 ORDER_ROOM_REQUIRED`；补齐后提交成功。
4. **组合支付结清后同事务内**：订单 `card_released_at` 与 `card_holdings.released_at` 均非 NULL、原因分别为 `settled` / `order_settled`；卡可被其他人领取并绑到新单；审计与事件各写一次，幂等重放不重复。
5. **取消订单**按同一规则释放，原因 `cancelled` / `order_cancelled`；部分支付未结清时不释放；退款与 `completed` 不重复释放。
6. 房间跨店绑定返回 403、停用房间新绑定返回 `409 ROOM_INACTIVE`、被订单引用的房间删除返回 `409 ROOM_IN_USE`；历史单在房间停用后仍可结算且详情可读。
   - **同一房间**并发创建两张单均成功（无房间唯一约束）；两单各自绑不同卡、分别支付互不影响；把两单入同一同行组后，组详情 `payable_total` 等于两单应付之和。
7. 建组 → 合入两张各自绑卡的单 → 拆出一张 → 组内只剩一张、被拆出单的卡 / 房间绑定与金额不变。
8. 一张单并发合入两个 open 组，仅一个成功；从 A 组合入 B 组时 A 组被清空则同事务 `closed`。
9. 已支付订单改分组 / 卡 / 房间返回 `409 ORDER_SETTLED`；缺 `X-Shop-Id` 返回 400。
10. 组详情 `payable_total = Σ 活跃成员 payable_amount`，且与逐单派生一致；组表不存金额列。
11. 既有全链路（建单 → 试价 → 提交 → 组合支付 → 退款）金额行为不变，`go test ./...` 全绿。

## 复审条件

- 需要**组级一笔收款并把金额自动分摊到各单** → 必须同时定义分摊顺序、余数落位、逐单退款与渠道上限的映射，另立 ADR。
- 需要**组内共享优惠 / 组成员间转账或自动清分** → 触碰 ADR-0007 NQ-16，另立 ADR。
- 需要**无卡结账或无房间单据**（例如不占用空间的自提场景）→ 本 ADR 强制提交前两者齐备；放开须另立 ADR 并定义凭证与追溯。
- 需要**房间历史名称 / 楼层快照**或**同一房间活跃订单互斥** → 加列 / 加唯一索引，另立 ADR。
- 需要**只释放订单绑定而保留人员持卡**（收银与持卡解耦）→ 调整自动释放范围，另立 ADR。
- 需要**同行组跨店**或按组授权 → 违反 ADR-0003 / 0005，直接否决，不进入复审。
- 需要店员执行绑卡 / 绑房间 / 建组 / 合并 / 拆分 → 调整 `order.write` 权限矩阵，另立 ADR。

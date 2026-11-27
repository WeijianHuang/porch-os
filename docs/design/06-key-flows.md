# 前期系统架构详细设计 · 06 关键流程

> 上游：[01 领域模型](01-domain-model.md)（状态机、不变量）、[02 数据库设计](02-database-design.md)（约束）、[03 接口设计](03-api-design.md)（错误码）、[05 横切机制](05-cross-cutting.md)（幂等、Outbox、报表读模型）、[ADR-0004](../decisions/0004-scope-baseline.md)（范围基线）、[ADR-0005](../decisions/0005-no-shop-groups.md)（跨店范围表达）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（门店切换流程）、[ADR-0007](../decisions/0007-remaining-open-questions.md)（不做跨店清分、暂不读写分离、审计日志 30 天清理）。
> 本册把前五册的约定串成端到端时序，是集成测试与用例评审的清单来源。

## 0. 所有流程的共同前提

| 前提 | 来源 |
|---|---|
| 请求已通过中间件链，context 中有 `request_id`、认证主体、`ShopScope`、超时 deadline | [04 第 4 节](04-application-architecture.md) |
| 所有写操作带 `Idempotency-Key`；业务写入 + 幂等记录 + Outbox 事件在同一事务提交 | [05 第 2、6 节](05-cross-cutting.md) |
| 每条 SQL 显式带 `shop_id`；跨模块只通过用例层与 ID 协作 | [00 第 5 节](00-overview.md) |
| 跨店范围由「可见门店集合 + 显式 `shop_ids`」表达；不存在门店组 / 分组对象，也不按组授权 | [ADR-0005](../decisions/0005-no-shop-groups.md) |
| 一个用户可见多间门店；当前门店由客户端显式携带，切换只改上下文、不改授权、不写服务端 | [ADR-0006](../decisions/0006-user-shops-and-current-shop.md) |
| 状态迁移一律走 `CanTransition` 纯函数；金额计算一律是领域层纯函数 | [01 第 5、6 节](01-domain-model.md) |
| 事务内不做外部 IO；通知、汇总、导出都在事务外由 worker 完成 | [04 第 5 节](04-application-architecture.md) |
| 金额一律 CNY 且不带币种字段；业务日、单号日期按 UTC+8（`Asia/Shanghai`） | [ADR-0004](../decisions/0004-scope-baseline.md) |
| **全程在线**：无本地队列、无离线提交、无补传；网络失败不放行写入 | [05 第 4.1 节](05-cross-cutting.md) |
| 业务数据在线全量保留：流程里没有归档、保留窗口裁剪与打印步骤；**审计日志另有 30 天可配保留窗口，但只由 worker 清理，不参与任何业务流程** | [02 第 7 节](02-database-design.md)、[ADR-0007](../decisions/0007-remaining-open-questions.md) |
| R1 无外部投递目标、无只读副本与读写分离；跨店差额不做自动清分 | [ADR-0007](../decisions/0007-remaining-open-questions.md) |

```
Client ──► API 中间件 ──► Usecase ──► Domain(纯函数) ──► Repo ──► PostgreSQL
                            │                                  │
                            └──────── 同一事务 ─────────────────┘
                                                              │ COMMIT
                                                              ▼
                                                      worker（Outbox / 汇总 / 导出 / 运行期清理）
```

## F-01 开单 → 加项 → 提交结账 → 组合支付（最高风险流程）

这是全系统事务边界最复杂、金额最敏感的流程。**计价与编排必须分离**：计价是纯函数，支付编排是事务。

### 阶段 A｜开单 `draft`

| 步 | 动作 | 失败 |
|---|---|---|
| 1 | 校验命令（门店在 scope 内、班次已开、操作人有效） | `400` / `403` / `409` |
| 2 | 用例层显式调用 `next_order_no(shop_id)` 生成单号（日期取 `business_date`，与日报 / 班次 / 汇总同口径，ADR-0014；序列表序列保证并发唯一）；`order_no` 无字面 DEFAULT，遗漏将触发 `NOT NULL` 错误 | 唯一冲突 → `409` |
| 3 | 事务：`INSERT orders(draft, shop_id, operator_id, order_no)`；写 `order.created` 事件；写幂等记录 `completed` | 唯一冲突 → `409` |
| 4 | 返回 `public_id` | — |

### 阶段 B｜加项 `draft → open`

1. 两次查询取目录项与按店价格覆盖（不嵌实体、不做关联图）。
2. 领域纯函数算 `unit_price`、`amount`，把下单时的名称与价格**快照**进明细。
3. 事务：乐观锁更新 `orders(version)`、`INSERT order_lines`、写幂等记录。
4. 冲突 → `409 VERSION_CONFLICT`（可重试）：客户端重读后复用同一幂等键重试。
5. **ADR-0023 增量**：查询目录项时同时取 `type`；服务行可带 `staff_public_id`，写入前查资质（`staff_service_items`）与任职状态，商品行携带人员 → `400`。服务行暂不带人允许建草稿，必填在阶段 C 判定；落库时一并快照 `staff_name_snapshot`。改派走 `PUT /orders/{id}/lines/{lineNo}/staff`，同事务递增订单 `version`。

> **收银实时试价（ADR-0019）**：收银机增减项时前端调 `POST /shops/{shopId}/orders/price` 刷新应付与优惠分摊。该端点只读、不落库、不占单号、无需幂等键；服务端按 `COALESCE(shop_catalog_overrides.price, catalog_items.base_price)` 取价（覆盖价优先，下架项拒绝），与步骤 2 的加项快照同口径，返回逐行 `unit_price` / `amount` / `allocated_adjustment` / `line_total` 与 `subtotal` / `discount_amount` / `total_amount`。前端据此展示，最终提交仍以本阶段落库的快照价为准。

### 阶段 C｜提交结账 `open → pending_payment`

**ADR-0023 增量前置校验**（与 ADR-0022 的卡 / 房间校验并列，同事务、在 `Quote` 重算之前）：逐条检查 `type='service'` 的明细——无 `staff_id` → `422 ORDER_LINE_STAFF_REQUIRED`（`details.line_no`）；人员 `inactive` → `409 STAFF_INACTIVE`；资质已撤销 → `422 STAFF_NOT_QUALIFIED`。商品行不参与；不静默改派，决定权交还操作员。

| 步 | 动作 |
|---|---|
| 1 | **一致性校验**：后端调用 `Quote` 重算明细合计与客户端提交金额（与试价端点共用同一纯函数，ADR-0019），不一致 → `422 UNPROCESSABLE`（不信任前端金额；两次调用间目录改价时触发，前端须重新试价） |
| 2 | 计价纯函数 `Quote` 内部用 `Allocate`：整单折扣/抹零按明细权重分摊，保证「分摊之和 = 折扣总额」，余数落在 `line_no` 最大的行 |
| 3 | `CanTransition(open → pending_payment)`；非法 → `412` |
| 4 | 事务：写 `order_price_adjustments`、更新 `orders.total_amount` 与状态、锁库存行（`FOR UPDATE`）、写事件、写幂等记录 |

### 阶段 D｜组合支付 `pending_payment → paid`

**单个事务内的顺序**（顺序即为审计链）：

| 序 | 操作 | 约束 |
|---|---|---|
| a | `SELECT ... FOR UPDATE orders WHERE shop_id=$1 AND id=$2 AND version=$3` | 门店 + 乐观锁双条件 |
| b | `CanTransition(pending_payment → paid)` | 非法 → `412` |
| c | 校验支付方式组合与合计；每种方式生成一个 `payment_entries` | 合计必须等于应付 |
| d | `INSERT payments(kind='sale', order_id, shift_id, operator_id, ...)` + `payment_entries` | 复合外键保证同店；`shift_id`/`operator_id` 是本次收款动作的归属（只持 ID） |
| e | 会员余额扣减：`UPDATE ... SET balance = balance - $a WHERE shop_id=$1 AND member_id=$2 AND balance >= $a` | 影响 0 行 → 余额不足 |
| f | 库存扣减：条件更新，不允许负库存 | 影响 0 行 → 库存不足 |
| g | 更新 `orders.status=paid` / `version+1`（**不写 `paid_amount`**：订单只存不可变应付快照，实付从 payments 的 succeeded 行派生，ADR-0014） | 与 a 同一事务 |
| h | 写 `balance_ledger` / `stock_movements`（只追加） | 流水与余额变动一一对应 |
| i | 写 `idempotency_keys(completed, response_body)` | 与业务同事务 |
| j | 写 `outbox_events`：`order.paid`、`payment.succeeded`、`shop.summary_dirty` | 事务外派发 |
| k | `COMMIT` | — |

事务外：worker 领取事件 → 派发订阅者 → 按 `(shop_id, business_date)` 幂等重算 `daily_shop_summary`；`business_date` 由 `created_at` 按 UTC+8 与门店营业日切点推导，不依赖数据库会话时区。

**失败分支表**

| 情况 | 响应 | 客户端行为 |
|---|---|---|
| 余额 / 库存不足 | `409 CONFLICT`（不可重试） | 提示用户，更换支付方式或减少明细 |
| 状态机非法 | `412 PRECONDITION_FAILED` | 刷新单据状态 |
| 乐观锁冲突 / 死锁 | `409 VERSION_CONFLICT`（可重试） | 重读后复用同一幂等键重试，最多 3 次 |
| 幂等键不同指纹 | `409 IDEMPOTENCY_CONFLICT` | 视为调用错误，不重试 |
| 首次请求仍在处理 | `409 IDEMPOTENCY_IN_PROGRESS` | 按 `Retry-After` 轮询 |
| 超时 / 断连 | `504 TIMEOUT` | **先查幂等结果**，确认未生效再重试；盲目重试是重复记账的主要来源 |
| 任意步骤异常 | 事务整体回滚，幂等记录一并消失 | 可用同一 key 安全重试 |

> 关键不变量：任一时刻「已生效的支付」与「幂等记录 `completed`」一一对应；不存在「业务已提交但幂等键没写」的窗口。

## F-02 退款（全额 / 部分）

```
paid / completed ──► refunded（全额）
        └──────────► partially_refunded（部分）──► refunded（累计退完）
```
无 `refunding` 态：退款在单个事务内完成，事务外只观测 `succeeded` / `failed`；失败整体回滚、订单状态不变（ADR-0014）。

1. 校验：原单处于 `paid` / `completed` / `partially_refunded`；原支付 `payments.status = 'succeeded'`；请求体显式携带 `refund_entries`（`method` + `amount`），断言 `Σ refund_entries.amount = amount`、每个 `method` 都在原支付 `payment_entries` 中出现。
2. 事务：对同一 `payment_id` 加锁 → 汇总既有 `succeeded` 退款额并校验累计 ≤ 原支付额 → 对涉及的每个 `(payment_id, method)` 加锁、汇总该渠道累计退款并校验 ≤ 该渠道原支付额（超限 → `409`，由前端 / 客户协商，**不自动改派或按比例分摊**）→ `INSERT refunds(kind='sale', payment_id, order_id, shift_id, operator_id, amount, reason)` 与 `INSERT refund_entries` → `UPDATE orders` 状态与 `version`、按原 `payment_entries` 反向回滚会员余额与库存、写反向流水、写 `refund.succeeded` 与 `shop.summary_dirty` 事件、写幂等记录。
2a. **只退实际支付部分**：券优惠已在下单时进 `order_price_adjustments`、不进 `payment_entries`，可退额天然不含券（ADR-0016）；`refund_entries.method` 不含 `coupon`。**禁止跨店退款**：退款单 `shop_id` 强绑原支付与本店账，A 店单据不能在 B 店退。
3. **退款单的 `shift_id` / `operator_id` 记录退款动作发生时的班次与操作人，不继承原订单**——已支付订单常在另一班次、由另一人退款（ADR-0013）。`shift_id`/`operator_id` 只持 ID、不加外键。
4. **不修改原单金额、不删除明细、不回退到 `open`**（DP-05）：已支付单据是审计事实。实付 / 已退 / 可退均从记录派生，不落库（ADR-0014）。
5. 部分退款后仍可再次退款，直到累计额达到上限；每次都是独立幂等事务，再次退完时 `partially_refunded → refunded`。
6. 更正错误单据 = 退款 + 新建一张正确单据，两张单都能在报表里追溯。

## F-03 会员充值与跨店核销

| 流程 | 步骤 | 关键点 |
|---|---|---|
| 充值 | 事务：`member_balances(shop_id, member_id)` 条件 upsert → 写 `balance_ledger` → 写 `payments(kind='topup', order_id=NULL, member_id=<id>, shift_id, operator_id)` → 事件 + 幂等 | 会员档案全局唯一，**余额按店独立**；充值**不挂订单**，由 `ck_payments_kind_ref` 约束（ADR-0013） |
| 充值退款（ADR-0016） | 前置校验原收款 `kind='topup' AND succeeded` 且属本店 `member_id` → 同事务：对 `payment_id` 加锁并校验累计退款 ≤ 原充值额 → 校验 `refund_entries` 渠道（`Σ`=退款额、单渠道累计 ≤ 原入账额、不含券）→ `INSERT refunds(kind='topup', payment_id, member_id, order_id=NULL, shift_id, operator_id, amount, reason)` + `refund_entries` → 条件扣回 `member_balances(balance ≥ 退款额)` → 写反向 `balance_ledger(ref_type='refund')` → 事件 + 幂等 | **余额不足 → `409 INSUFFICIENT_BALANCE` 整笔拒绝，不做部分退、不写负余额**；只退实际支付部分；**禁止跨店**（本店支付 / 本店余额）；权限 `member.topup.write`；失败整笔回滚 |
| 本店消费 | 与 F-01 阶段 D 的 e/h 相同 | 只在当前 `shop_id` 记账 |
| 跨店核销 | 显式动作：校验两家店都在主体可见集合内 + 单独权限 → 一个事务里在 A 店写一条出账流水、B 店写一条入账流水，用同一个 `transfer_ref` 关联 → 事件 + 幂等 | 不修改会员档案归属、不提权；**不做自动清分**（OQ-07 已由 ADR-0007 关闭），差额只在按店分组的报表呈现，不生成往来账 / 结算单 |

**禁止**：为了核销把 `member_balances` 的 `shop_id` 去掉做成「全局余额」——那等于把门店账合并，单店核算立刻失真。

## F-04 库存扣减与跨店调拨

| 流程 | 规则 |
|---|---|
| 扣减 | 条件更新 `WHERE balance >= $qty`，影响 0 行即失败；每次扣减写一条 `stock_movements`（只追加）；余额恒等于流水累计 |
| 调整 | 入库/盘点走 `inventory/adjustments`，同样条件更新 + 流水，不允许直接 `SET balance = ...` 覆盖 |
| 调拨 | 显式动作：`INSERT stock_transfers` + 出库店一条负流水 + 入库店一条正流水，同一 `transfer_ref`；两店都必须在可见集合内；一个事务、一次幂等 |
| 并发 | `FOR UPDATE` 行锁（按 `shop_id + catalog_item_id` 升序加锁，避免死锁）+ `lock_timeout` |

## F-05 报表查询与流式导出

**查询（L1 / L2）**

1. 请求经 shop scope 校验；`scope=all` 额外校验 `report.cross_shop` 并把 `shop_ids` 与可见集合求交；**不接受组 ID / 分组筛选参数，也不按门店组聚合**（ADR-0005）。
2. 当日 / 短区间 → L1 主库条件聚合（5 s 超时，命中 `(shop_id, created_at)`）。
3. 历史 / 跨店对比 → L2 `daily_shop_summary`，响应带 `recomputed_at` 说明数据截至时间；业务日统一按 UTC+8。
4. 复杂口径走 `derived/*.sql`（CTE / `FILTER`），不建视图；`EXPLAIN` 计划存档。业务数据暂不归档，查询范围不受保留窗口裁剪；**单主库读写，不查只读副本**（ADR-0007）。审计日志查询是独立场景，受 30 天保留窗口约束（F-08）。

**导出（L3）**

```
POST /exports ──► 202 + job_url（校验参数、建 export_jobs、幂等）
                       │
worker 领取 ──► keyset 游标分批读取 ──► StreamWriter 逐行写出
                       │                        │
                       ├── 客户端断开 ─────────┴── context 取消，终止查询与写出
                       ▼
              状态 succeeded / failed（显式原因，绝不返回空文件或 null）
GET /exports/{id}/download ──► 流式下载
```

硬约束：不收集全量结果到 slice、不深 `OFFSET`、不把文件先读进内存再返回；常驻内存与结果集大小无关。导出范围按 `shop_id` 组织，跨店导出需显式授权且结果按店分列，不把不同门店混进同一行。导出只产出文件下载，**不触发打印**（ADR-0004）。

## F-06 授权变更即时生效

1. 管理员修改 `user_shops` / `user_roles`，事务提交。
2. 目标用户的**下一个请求**：`auth` 中间件重新推导权限与可见门店集合，`shopscope` 用新集合校验。
3. 结果立即变化，无需重新登录、无需重启进程、无需清缓存——因为访问令牌里不含权限/门店清单，判定结果也不缓存。
4. 已被撤销的门店：列表接口不再返回其数据；以旧 `X-Shop-Id` 请求直接 `403`；进行中的长连接按心跳周期重校验后断开。

> 这条流程专门验证「权限即时性」质量属性：如果实现里把授权结果缓存、或把门店清单写进 JWT，测试会在这里失败。

## F-07 门店切换（ADR-0006）

一个用户可见多间门店，`porch-console` 的切换器让「当前看哪家店」成为全局上下文。切换是**零写入**的客户端行为，但必须防住串店。

1. 登录 → 后端返回 `shops` + `default_shop_id`；前端用 `/me/shops` 复核后选定当前门店（本地记住的上次选择若已不在集合内则回退）。
2. 之后每个单店请求由 API 客户端统一注入 `X-Shop-Id`；`shopscope` 用 `user_shops` 实时求交，缺头 `400`、越权 `403`。
3. 用户切换：取消所有在途单店请求（或丢弃带旧 `shop_id` 的响应）→ 以 `(user_id, current_shop_id, query_key)` 为键失效并重取所有派生查询与缓存 → 重新拉取 `/me/permissions`，无权限给可读提示 → 跨店视图不变，仍按显式 `shop_ids` + `report.cross_shop`。
4. 当前门店被撤销 / 停用：下一个请求 `403`，前端重新拉 `/me/shops` 并回退到 `default_shop_id`，禁止停留在无权门店。
5. 刷新页面：从 localStorage 恢复当前门店（非令牌、非权限依据），并再次用 `/me/shops` 复核。

> 服务端全程无状态：没有切换端点、没有 `current_shop` 表 / 列，`user_shops` 是唯一授权来源；切换不触发授权重算，授权变更仍在下一个请求生效（F-06）。

## F-08 审计日志定时清理（ADR-0007 / 0008）

`audit_logs` 是基础设施 / 运行期数据，默认保留 **30 天**（`AUDIT_LOG_RETENTION_DAYS` 全局可配、启动时校验）。这是**唯一允许删除审计记录的路径**，且不属于业务数据归档。

1. worker × 2 各自在 UTC+8 03:30 触发，执行键 `job:audit_cleanup:<UTC+8 业务日>`；用 `pg_try_advisory_lock` 抢锁，抢不到记 `skipped` 并跳过，同一时刻只有一个执行者（[07](07-deployment-and-operations.md) §4.2）。
2. cutoff = 当日 00:00（UTC+8）− 30 天（**自然日口径，不是滚动 30×24h**）→ 单批 5000 行 `DELETE ... WHERE created_at < cutoff` → 每批独立提交，失败记 `failed` + 告警；命中 `(created_at)` 索引，不建分区、不导出归档、不 `VACUUM FULL`（[08](08-audit-retention.md) §2/§3）。
3. 在 `audit_log_cleanup_runs` 写一条运行痕迹（cutoff / 删除行数 / 批次数 / 状态），并上报指标与结构化日志；**不写回 `audit_logs`**，否则清理会不断产生新记录形成自我循环（[08](08-audit-retention.md) §5）。
4. 业务侧只读：业务请求无法更新 / 删除审计记录；超出保留期的查询必须返回明确的「超出保留期」语义而非空结果（错误码为 `400 AUDIT_RETENTION_WINDOW_EXCEEDED` + `earliest_queryable_at`，见 [08](08-audit-retention.md) §4）。**业务单据与流水全程不参与任何删除**。

> 边界：审计日志定时删除 ≠ 业务数据归档；运行期清理 ≠ 业务单据删除；业务请求不可删审计 ≠ 清理任务不可删。

## F-09 订单绑定卡 / 房间与同行组合并 / 拆分（ADR-0022）

一单一卡（1:1）+ 一单一房间 + 多单成组共同结算；付款仍逐单走 F-01 阶段 D，组只做编排与派生合计，不新增付款主体。

### 阶段 A｜绑卡 / 换卡

| 步 | 动作 | 失败 |
|---|---|---|
| 1 | 校验卡属本店、`status='active'`、当前被本店人员持有（`card_holdings.released_at IS NULL`）；订单未结算、无 succeeded 支付 | `409 CARD_NOT_HELD` / `CARD_INACTIVE` / `ORDER_SETTLED` |
| 2 | 对 `id_cards` 行 `SELECT ... FOR UPDATE` 串行化同一张卡（沿用 ADR-0017 并发手法） | — |
| 3 | 事务：`UPDATE orders SET card_id=<id>, card_released_at=NULL, card_release_reason=NULL, version=version+1`；写审计（`target_type='order'`）+ 事件 `order.card_bound` / `.card_replaced` + 幂等记录 | 唯一冲突 → `409 CARD_BOUND_TO_ORDER`；版本冲突 → `409 VERSION_CONFLICT` |

**阶段 A2｜绑房间 / 换房间**：校验房间属本店且 `status='active'`、订单未结算（同上表失败语义）；事务内 `UPDATE orders SET room_id=<id>, version=version+1`，写审计 + 事件 `order.room_bound`。不存在的房间 → `404 ROOM_NOT_FOUND`、已停用 → `409 ROOM_INACTIVE`、跨店 → `403`。一房间可关联多张单，不做互斥；房间不落快照。

### 阶段 B｜提交结账（F-01 阶段 C 的增量校验）

`POST /orders/{id}/submit` 在调用 `Quote` 重算之前先断言：① 有效卡绑定（`card_id IS NOT NULL AND card_released_at IS NULL`）且卡仍 `active`；② `room_id IS NOT NULL`。否则 `422 ORDER_CARD_REQUIRED` / `ORDER_ROOM_REQUIRED`。这是 ADR-0022 对 ADR-0017「不校验开单必须持卡」与 ADR-0011「房间不与单据绑定」边界的显式修订；存量缺卡 / 缺房间草稿同样受此约束。

### 阶段 C｜合并（独立单入组 / 跨组挪入）

| 步 | 动作 | 失败 |
|---|---|---|
| 1 | 对目标组 `SELECT ... FOR UPDATE` + 校验 `version`（乐观锁） | `409 GROUP_NOT_OPEN` / `VERSION_CONFLICT` |
| 2 | 每个入组单校验属本店、未结算、无 succeeded 支付 | `409 ORDER_SETTLED` / `403` |
| 3 | 已在别的 `open` 组的单：同事务把原活跃成员行置 `left_at = now()`；原组因此无活跃成员 → 同事务置 `closed` | — |
| 4 | 目标组插成员行（`joined_at = now()`）；写审计 + 事件 `companion_group.member_added` + 幂等记录 | 已属本组 → `400 MEMBER_UNCHANGED`；唯一冲突 → `409` |

### 阶段 D｜拆分（成员出组恢复独立）

1. 校验组 `open`、`version` 匹配、订单是活跃成员 → `409 GROUP_NOT_OPEN` / `404 MEMBER_NOT_IN_GROUP`。
2. 事务：成员行置 `left_at = now()` 留史（**不删行**）；组无活跃成员则同事务置 `closed`；写审计 + 事件 `companion_group.member_removed`。
3. **不改订单的 `card_id`、明细、金额**；出组单恢复为独立单，照常走 F-01 / F-02。

### 阶段 E｜组详情与关闭

- 详情：一次查组 + 一次查活跃成员 + 一次聚合派生金额（沿用 ADR-0014 的 succeeded payments/refunds 口径），纯函数算 `payable_total = Σ payable_amount`、`paid_total = Σ paid_amount`，**不落库**；无 N+1。
- 付款仍逐单 `POST /orders/{id}/payments`；组不产生 `payments` 主体，不做组级扣款 / 退款 / 清分（ADR-0007 NQ-16）。
- `close` 要求全成员已结算或无活跃成员；`cancel` 要求成员无成功支付；两者皆 `version` 乐观锁 + 终态不可再改。

### 阶段 F｜结算 / 取消自动释放卡（与支付同事务）

| 步 | 动作 | 失败 |
|---|---|---|
| 1 | 支付使订单达到 `paid` 且派生 `payable_amount <= 0`（或取消未支付单） | 部分支付未结清 → 不释放 |
| 2 | `UPDATE orders SET card_released_at=now(), card_release_reason='settled'\|'cancelled' WHERE ... AND card_released_at IS NULL` | 已释放 → 跳过（幂等） |
| 3 | 若仍有有效卡绑定：`UPDATE card_holdings SET released_at=now(), release_reason='order_settled'\|'order_cancelled' WHERE shop_id/card_id 匹配 AND released_at IS NULL` | 已释放 → 跳过 |
| 4 | 写审计（`target_type='order'` 的 `card_released`、`target_type='card'` 的 `release`）+ 事件 `order.card_released` / `card.hold_released`（带 `reason`），与支付写入、幂等记录同事务 | 任一步失败 → 整体回滚 |

- `completed`、退款、组关闭不重复触发；手动解绑 / 换卡只结束订单侧绑定（原因 `unbound` / `replaced`），不释放持卡。
- 释放完成后卡可被其他人领取并绑到新单；订单详情的 `card` 变 null，`card_binding_history` 保留追溯。

> 关键不变量：① 一张 ID 卡同一时刻只被一张有效绑定的订单占用，结清 / 取消后自动释放且卡可由他人复用；② 一张订单同一时刻只属一个 `open` 组；③ 合并 / 拆分只动分组归属，不动卡 / 房间绑定、明细与金额；④ 组的金额全是只读派生；⑤ 订单必有本店房间且房间删除受外键保护。

## 测试清单（每个流程必测四类）

| 流程 | 正常 | 边界 | 失败 | 重放 / 并发 |
|---|---|---|---|---|
| F-01 | 完整结账、组合支付 | 抹零分摊余数、多行明细 | 余额不足、库存不足、非法状态、超时 | 同一幂等键重放两次；两个请求并发结账 |
| F-02 | 全额、部分退款 | 退款额恰好等于上限 | 超额退款、对未支付单退款 | 重复退款请求 |
| F-03 | 充值、本店消费、跨店核销 | 余额恰好用尽 | 会员不存在、门店不在可见集合、无跨店权限 | 并发充值同一会员 |
| F-04 | 扣减、调整、调拨 | 库存恰好扣到 0 | 负库存、调拨到无权门店 | 并发扣减同一商品 |
| F-05 | 单店日报、跨店汇总、导出 | 空结果、UTC+8 日界（UTC 16:00）前后、跨营业日切点 | 越权 `scope=all`、导出中途失败 | 重复创建导出任务 |
| F-06 | 授权后立即生效 | 恰好只剩一家可见门店 | 撤销后访问旧门店 | 授权变更与在途请求并发 |
| F-07 | 多店登录、切换后只显示新门店数据 | 可见集合为空、本地上次选择已被撤销 | 缺 `X-Shop-Id`、切到无权门店、旧门店响应后到 | 切换与在途请求 / 授权撤销并发 |
| F-08 | 只删除超期审计记录，指标与日志齐全 | 恰好落在 30 天日界（UTC+8）两侧、保留天数改为其它合法值 | 清理任务中途失败、配置非法、业务请求尝试改删审计记录 | 多副本同时触发清理（advisory lock 只放行一个） |
| F-09 | 绑卡 / 绑房间 → 建组 → 合入两单 → 拆出一单；逐单付款，结清自动释放卡 | 一组仅一成员、跨组合入后原组自动关闭、取消单也释放、房间停用后历史单仍可结算 | 无卡 / 无房间提交、卡已被占用 / 无人持有、房间不存在 / 停用 / 跨店、已结算单改组 / 改卡 / 改房间 | 一卡并发绑两单、一单并发入两组、同幂等键重放（释放事件只写一次） |


**每条写用例强制重放一次幂等键并断言副作用只发生一次**；每条隔离用例用两家店 + 两个用户交叉请求，断言响应中不出现对方门店数据。

## 设计追溯矩阵

| 流程 | 领域模型 | 数据库 | 接口 | 横切 |
|---|---|---|---|---|
| F-01 | `01` §4–6 | `02` §3–4 | `03` §4/§6/§8 | `05` §2/§4/§6 |
| F-02 | `01` §5 | `02` §3 | `03` §4 | `05` §2/§6 |
| F-03 | `01` §3/§7 | `02` §2 | `03` §3/§8 | `05` §6 |
| F-04 | `01` §9 | `02` §3/§5 | `03` §8 | `05` §4 |
| F-05 | `01` §7 | `02` §5/§7 | `03` §5/§9 | `05` §8/§10 |
| F-06 | — | `02` §2 | `03` §3/§7 | `05` §1/§5 |
| F-07 | `01` §6 | `02` §2/§8 | `03` §3/§7/§8 | `05` §1/§5 |
| F-08 | `01` §3 | `02` §2/§5/§7/§8 | `03` §8 | `05` §7/§9/§11 |
| F-09 | `01` §4/§5 | `02` §2/§3（ADR-0022） | `03` §4/§6/§7/§8 | `05` §2/§6 |

## F-10 开单辅助选人（ADR-0024）

1. 开单弹窗点「添加服务员」→ 并行读取 `GET /staff-categories`、`GET /staff`、`GET /catalog-items`、`GET /catalog-categories`，按分类与调度状态在前端筛出服务人员卡片。
2. 单选一位服务员 → 按目录项分类筛服务项表格；未选人或无 ADR-0023 资质时「添加」禁用。
3. 点「添加」→ 向购物车追加一条 `type='service'` 明细，预填 `staffId`；弹窗保持打开可连续添加，同一服务项可形成多条明细。
4. 关闭后购物车仍可逐行改派；`submit` 前仍按 ADR-0023 校验服务明细执行人，缺人 `422 ORDER_LINE_STAFF_REQUIRED`、无资质 `422 STAFF_NOT_QUALIFIED`。
5. 分类 / 状态筛选只影响展示，不写库、不授权、不改变调度状态机。

## F-11 班次模板维护与分配（ADR-0025）

1. 班次管理页 `GET /shift-templates` 列出本店模板；新增 / 编辑校验 `HH:MM` 与 `0..1440` 分钟，带 `version` 乐观锁。
2. 点「分配服务人员」→ `GET /staff` 回显当前成员（按 `shift_template.public_id`）。提交 `PUT /shift-templates/{id}/staff` 为全量替换：先解绑旧成员，再把勾选项写入 `staff.shift_template_id`；已在别的班次的人员自动改派，一人一班始终成立。
3. 删除模板先记录成员，再同事务置空引用并删除，写一条审计；人员档案与历史订单不受影响。
4. 班次模板不触发打卡、提醒、头牌 / 排位、绩效或派单；与营业班次实例流程 F-01 的 `/shifts/open` / `/shifts/:id/close` 无关。

---

返回 [00 总览](00-overview.md) 查看分册地图、已确认决议与已关闭的 OQ / NQ 清单。

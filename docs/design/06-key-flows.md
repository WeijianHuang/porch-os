# 前期系统架构详细设计 · 06 关键流程

> 上游：[01 领域模型](01-domain-model.md)（状态机、不变量）、[02 数据库设计](02-database-design.md)（约束）、[03 接口设计](03-api-design.md)（错误码）、[05 横切机制](05-cross-cutting.md)（幂等、Outbox、报表读模型）、[ADR-0004](../decisions/0004-scope-baseline.md)（范围基线）。
> 本册把前五册的约定串成端到端时序，是集成测试与用例评审的清单来源。

## 0. 所有流程的共同前提

| 前提 | 来源 |
|---|---|
| 请求已通过中间件链，context 中有 `request_id`、认证主体、`ShopScope`、超时 deadline | [04 第 4 节](04-application-architecture.md) |
| 所有写操作带 `Idempotency-Key`；业务写入 + 幂等记录 + Outbox 事件在同一事务提交 | [05 第 2、6 节](05-cross-cutting.md) |
| 每条 SQL 显式带 `shop_id`；跨模块只通过用例层与 ID 协作 | [00 第 5 节](00-overview.md) |
| 状态迁移一律走 `CanTransition` 纯函数；金额计算一律是领域层纯函数 | [01 第 5、6 节](01-domain-model.md) |
| 事务内不做外部 IO；通知、汇总、导出都在事务外由 worker 完成 | [04 第 5 节](04-application-architecture.md) |
| 金额一律 CNY 且不带币种字段；业务日、单号日期按 UTC+8（`Asia/Shanghai`） | [ADR-0004](../decisions/0004-scope-baseline.md) |
| **全程在线**：无本地队列、无离线提交、无补传；网络失败不放行写入 | [05 第 4.1 节](05-cross-cutting.md) |
| 业务数据在线全量保留：流程里没有归档、保留窗口裁剪与打印步骤 | [02 第 7 节](02-database-design.md) |

```
Client ──► API 中间件 ──► Usecase ──► Domain(纯函数) ──► Repo ──► PostgreSQL
                            │                                  │
                            └──────── 同一事务 ─────────────────┘
                                                              │ COMMIT
                                                              ▼
                                                      worker（Outbox / 汇总 / 导出）
```

## F-01 开单 → 加项 → 提交结账 → 组合支付（最高风险流程）

这是全系统事务边界最复杂、金额最敏感的流程。**计价与编排必须分离**：计价是纯函数，支付编排是事务。

### 阶段 A｜开单 `draft`

| 步 | 动作 | 失败 |
|---|---|---|
| 1 | 校验命令（门店在 scope 内、班次已开、操作人有效） | `400` / `403` / `409` |
| 2 | 事务：`INSERT orders(draft, shop_id, operator_id, order_no)`；写 `order.created` 事件；写幂等记录 `completed` | 唯一冲突 → `409` |
| 3 | 返回 `public_id` | — |

### 阶段 B｜加项 `draft → open`

1. 两次查询取目录项与按店价格覆盖（不嵌实体、不做关联图）。
2. 领域纯函数算 `unit_price`、`amount`，把下单时的名称与价格**快照**进明细。
3. 事务：乐观锁更新 `orders(version)`、`INSERT order_lines`、写幂等记录。
4. 冲突 → `409 VERSION_CONFLICT`（可重试）：客户端重读后复用同一幂等键重试。

### 阶段 C｜提交结账 `open → pending_payment`

| 步 | 动作 |
|---|---|
| 1 | **一致性校验**：后端重算明细合计与客户端提交金额，不一致 → `422 UNPROCESSABLE`（不信任前端金额） |
| 2 | 计价纯函数：整单折扣/抹零按明细权重分摊，`Allocate` 保证「分摊之和 = 折扣总额」，余数落在 `line_no` 最大的行 |
| 3 | `CanTransition(open → pending_payment)`；非法 → `412` |
| 4 | 事务：写 `price_adjustments`、更新 `orders.total_amount` 与状态、锁库存行（`FOR UPDATE`）、写事件、写幂等记录 |

### 阶段 D｜组合支付 `pending_payment → paid`

**单个事务内的顺序**（顺序即为审计链）：

| 序 | 操作 | 约束 |
|---|---|---|
| a | `SELECT ... FOR UPDATE orders WHERE shop_id=$1 AND id=$2 AND version=$3` | 门店 + 乐观锁双条件 |
| b | `CanTransition(pending_payment → paid)` | 非法 → `412` |
| c | 校验支付方式组合与合计；每种方式生成一个 `payment_entries` | 合计必须等于应付 |
| d | `INSERT payments` + `payment_entries` | 复合外键保证同店 |
| e | 会员余额扣减：`UPDATE ... SET balance = balance - $a WHERE shop_id=$1 AND member_id=$2 AND balance >= $a` | 影响 0 行 → 余额不足 |
| f | 库存扣减：条件更新，不允许负库存 | 影响 0 行 → 库存不足 |
| g | 更新 `orders.paid_amount` / `status=paid` / `version+1` | 与 a 同一事务 |
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
paid ──► refunding ──► refunded（全额）
                 └──► partially_refunded（部分）
```

1. 校验：原单处于 `paid` / `completed` / `partially_refunded`；累计退款 ≤ 原支付额。
2. 事务：`INSERT refunds`、`UPDATE orders` 状态与 `version`、按原 `payment_entries` 反向回滚会员余额与库存、写反向流水、写 `refund.succeeded` 与 `shop.summary_dirty` 事件、写幂等记录。
3. **不修改原单金额、不删除明细、不回退到 `open`**（DP-05）：已支付单据是审计事实。
4. 更正错误单据 = 退款 + 新建一张正确单据，两张单都能在报表里追溯。
5. 部分退款后仍可再次退款，直到累计额达到上限；每次都是独立幂等事务。

## F-03 会员充值与跨店核销

| 流程 | 步骤 | 关键点 |
|---|---|---|
| 充值 | 事务：`member_balances(shop_id, member_id)` 条件 upsert → 写 `balance_ledger` → 写 `payments` → 事件 + 幂等 | 会员档案全局唯一，**余额按店独立** |
| 本店消费 | 与 F-01 阶段 D 的 e/h 相同 | 只在当前 `shop_id` 记账 |
| 跨店核销 | 显式动作：校验两家店都在主体可见集合内 + 单独权限 → 一个事务里在 A 店写一条出账流水、B 店写一条入账流水，用同一个 `transfer_ref` 关联 → 事件 + 幂等 | 不修改会员档案归属、不提权、不自动清分门店间差额（OQ-07） |

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

1. 请求经 shop scope 校验；`scope=all` 额外校验 `report.cross_shop` 并把 `shop_ids` 与可见集合求交。
2. 当日 / 短区间 → L1 主库条件聚合（5 s 超时，命中 `(shop_id, created_at)`）。
3. 历史 / 跨店对比 → L2 `daily_shop_summary`，响应带 `recomputed_at` 说明数据截至时间；业务日统一按 UTC+8。
4. 复杂口径走 `derived/*.sql`（CTE / `FILTER`），不建视图；`EXPLAIN` 计划存档。业务数据暂不归档，查询范围不受保留窗口裁剪。

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

## 测试清单（每个流程必测四类）

| 流程 | 正常 | 边界 | 失败 | 重放 / 并发 |
|---|---|---|---|---|
| F-01 | 完整结账、组合支付 | 抹零分摊余数、多行明细 | 余额不足、库存不足、非法状态、超时 | 同一幂等键重放两次；两个请求并发结账 |
| F-02 | 全额、部分退款 | 退款额恰好等于上限 | 超额退款、对未支付单退款 | 重复退款请求 |
| F-03 | 充值、本店消费、跨店核销 | 余额恰好用尽 | 会员不存在、门店不在可见集合、无跨店权限 | 并发充值同一会员 |
| F-04 | 扣减、调整、调拨 | 库存恰好扣到 0 | 负库存、调拨到无权门店 | 并发扣减同一商品 |
| F-05 | 单店日报、跨店汇总、导出 | 空结果、UTC+8 日界（UTC 16:00）前后、跨营业日切点 | 越权 `scope=all`、导出中途失败 | 重复创建导出任务 |
| F-06 | 授权后立即生效 | 恰好只剩一家可见门店 | 撤销后访问旧门店 | 授权变更与在途请求并发 |

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

---

返回 [00 总览](00-overview.md) 查看分册地图、已确认决议与剩余开放问题清单。

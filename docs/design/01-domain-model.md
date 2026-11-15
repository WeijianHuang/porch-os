# 前期系统架构详细设计 · 01 领域模型

> 上游：[00 总览](00-overview.md) 第 4–5 节（模块划分、数据归属三分法）、[ADR-0003](../decisions/0003-shop-level-isolation.md)、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种、东八区、暂不归档、不打印、不离线）、[ADR-0005](../decisions/0005-no-shop-groups.md)（无门店组）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（一人多店与当前门店切换）、[ADR-0007](../decisions/0007-remaining-open-questions.md)（不做跨店清分、审计日志保留 30 天）。
> 本文定义「业务上什么必须成立」，不定义表结构（见 [02](02-database-design.md)）与接口形状（见 [03](03-api-design.md)）。

## 1. 建模原则

1. **结构体之间只持有 ID**，不嵌套对方实体；不建实体关联图（ADR-0002 阻断项 ①）。
2. **聚合尽量小**：`Order` 是聚合根，`OrderLine` 属于它；`Payment` 独立聚合，通过 `order_id` 引用，不嵌进 `Order`。
3. **领域层是纯函数与值对象**：计价、状态迁移、一对多折叠都放在 `internal/domain/`，可脱离数据库单测。
4. **跨模块不共享聚合**：`order` 想知道会员余额，通过用例层调用 `member` 的接口，不直接读 `member_balances` 表。
5. **不把业务口径写成数据库视图**：视图会让口径失去唯一来源且脱离版本控制（ADR-0002 阻断项 ③）。

## 2. 聚合与实体清单

| 模块 | 聚合根 | 从属实体 / 值对象 | 归属类别 | 关键不变量 |
|---|---|---|---|---|
| `identity` | `User` | `Role`、`Permission`、`VisibleShops` | A 全局 + C 关系 | 用户至少属于一个角色；**一个用户可关联 N 家门店（N ≥ 0）**，可见门店集合由 `user_shops` 关系推导、不缓存；「当前门店」不是用户字段，也不落库（ADR-0006） |
| `shop` | `Shop` | `ShopSettings`、`BusinessDay` | A 全局 | `Shop.id` 即 `shop_id`；不设 `ShopGroup`，门店之间无分组 / 层级关系（ADR-0005） |
| `catalog` | `CatalogItem` | `Category`、`ShopCatalogOverride` | A 全局 + B 覆盖 | 目录全局唯一；价格/上下架按店覆盖，覆盖记录带 `shop_id` |
| `member` | `Member` | `MemberBalance`、`BalanceLedger` | A 档案 + B 账 | 档案按手机号全局唯一；余额与流水按店独立 |
| `order` | `Order` | `OrderLine`、`PriceAdjustment` | B | 单据与所有明细同属一个 `shop_id`；金额恒等于明细合计减调整 |
| `order` | `Payment` | `PaymentEntry`（组合支付的一笔） | B | 支付总额不超过应付；每个 `PaymentEntry` 带支付方式与金额 |
| `order` | `Refund` | — | B | 退款必须引用已成功的支付；累计退款不超过原支付 |
| `order` | `Shift` | — | B | 一个门店同一时刻最多一个 `open` 班次（数据库部分唯一索引保证） |
| `inventory` | `InventoryBalance` | `StockMovement`、`StockTransfer` | B | 余额 = 流水累计；不允许负库存（除非门店显式开启允许） |
| `reporting` | `DailyShopSummary` | `ExportJob` | B / 派生 | 汇总可重算，重算结果与源流水一致 |
| `platform` | `IdempotencyKey` | `OutboxEvent` | 基础设施 | 幂等键在作用域内唯一；Outbox 事件与业务写入同事务 |

## 3. 全局实体与门店实体的边界

| 全局唯一（不带 `shop_id`） | 为什么全局 | 与之配对的门店数据（必带 `shop_id`） |
|---|---|---|
| `Member` 档案 | 同一个人在不同店应是同一个会员 | `member_balances`、`balance_ledger`、`member_visits` |
| `CatalogItem` 目录 | 商品/服务统一维护，避免各店重复建档 | `shop_catalog_overrides`（价格、上下架）、`inventory_balances`、`stock_movements` |
| `Shop` | 门店本身是维度定义，不是被隔离的数据；门店之间无分组 / 层级关系（ADR-0005） | `shop_settings`、`user_shops`（授权关系） |
| `User` / `Role` | 一套用户体系 | `user_shops`（授权关系）、`audit_logs` |

**判定口诀**：改了它会不会产生「某家店的业务事实」？会 → 带 `shop_id`；不会 → 全局。会员改名是全局事实，会员在 A 店充值 100 元是门店事实。

**用户与门店**（ADR-0006）：从主体视角一个用户可管理 N 家门店，但关系实现是 `user_shops` **多对多**（同一家店也会有多个账号：经营者、店员、管理员），不是 `users.shop_id` 外键，也不在 `shops` 上记 owner。`User` 不「拥有」店里的单据、库存与余额，只持有对门店的授权关系；「当前正在看哪家店」是请求期的 `CurrentShop` 上下文，不是 `User` 的字段，也不产生新的持久化实体。

跨店核销（会员在 A 店的余额于 B 店消费）是一次**显式业务动作**：分别在两家店各写一条流水，并记一条关联 ID，不修改会员档案的归属，也不提升任何查询权限。**门店间差额不做自动清分**（OQ-07 已由 [ADR-0007](../decisions/0007-remaining-open-questions.md) 关闭）：不引入内部往来账、结算单或清分任务，差额只在按店分组的报表里呈现；将来需要清分时另立 ADR。

**平台基础设施数据不计入 A/B/C 归属判定**：`audit_logs` 记录「谁、在哪个店、做了什么、什么结果」，是运行期数据而非业务数据。按 [ADR-0007](../decisions/0007-remaining-open-questions.md)，它**默认保留 30 天、可配置、由 worker 定时删除**；业务请求对其只追加、不可改删，唯一例外是清理任务。这与「业务数据暂不归档」（ADR-0004）不冲突——审计日志定时删除 ≠ 业务数据归档。

## 4. 核心聚合字段（概念层，非 DDL）

**`Order`**

| 字段 | 含义 | 约束 |
|---|---|---|
| `id` / `public_id` | 内部主键 / 对外标识 | 分开；`public_id` 用时间有序 UUID |
| `shop_id` | 归属门店 | 创建后不可变 |
| `order_no` | 人类可读单号 | **店内唯一**，由数据库唯一约束保证 |
| `status` | 状态机取值 | 见第 5 节 |
| `shift_id` / `operator_id` | 班次 / 操作人 | 只持 ID |
| `member_id` | 关联会员（可空） | 只持 ID，不嵌会员对象 |
| `total_amount` / `paid_amount` / `discount_amount` | 金额（一律 CNY） | 定点十进制 `numeric(14,2)`；不带币种字段；`total = Σ(line) − Σ(adjustment)` |
| `version` | 乐观锁版本 | 每次更新 `+1`，更新带 `WHERE version = $n` |
| `created_at` / `updated_at` | 时间 | UTC `timestamptz`；展示按 `Asia/Shanghai`；业务日由 `created_at` 按统一口径推导 |

**`OrderLine`**：`order_id`、`catalog_item_id`（只持 ID）、`quantity`、`unit_price`、`amount`、`line_no`。明细不随目录价格变动而回溯改写——下单时快照价格。

**`Payment` / `PaymentEntry`**：一次收款可组合多种方式（现金、卡、余额、券），每个 `PaymentEntry` 记方式与金额，合计等于本次收款额。禁止把「组合支付」压成一个字符串字段。

## 5. 状态机

### 5.1 单据 `Order`

```
draft ──► open ──► pending_payment ──► paid ──► completed
  │         │            │               │
  │         │            │               └──► refunding ──► refunded
  │         │            │                          └──► partially_refunded
  └─────────┴────────────┴──────► cancelled
```

| 迁移 | 触发 | 允许的副作用 |
|---|---|---|
| `draft → open` | 开单 | 写明细、锁库存 |
| `open → pending_payment` | 提交结账 | 计价纯函数、生成应付 |
| `pending_payment → paid` | 全部支付成功 | 写支付流水、扣库存、改汇总脏标记、发 `order.paid` |
| `paid → completed` | 日结 | 封账并触发当日汇总重算（业务数据不归档，ADR-0004） |
| `paid → refunding → refunded` | 发起退款 | 写退款单、回滚库存与余额 |
| 任意未支付态 → `cancelled` | 取消 | 释放库存 |

**不可变约定（DP-05）**：`paid` 及之后的状态**不允许回退到 `open`**。更正错误单据的方式是「对原单发起退款 + 新建一张正确单据」，而不是反结算改写原单。这样已支付单据始终是审计意义上的事实，报表与对账不需要处理「被改过的历史」。

### 5.2 其他状态机

| 聚合 | 状态流 | 备注 |
|---|---|---|
| `Payment` | `pending → succeeded / failed` | `failed` 是终态；重试生成新的支付尝试 |
| `Refund` | `pending → succeeded / failed` | 累计退款额 ≤ 原支付额 |
| `Shift` | `open → closing → closed` | 一个门店同时最多一个 `open`（部分唯一索引） |
| `ExportJob` | `queued → running → succeeded / failed / expired` | `expired` 由定时任务清理 |

**实现约束**：状态迁移是 `internal/domain/` 下的纯函数 `CanTransition(from, to, event) error`；非法迁移返回类型化错误，不允许在 Handler 里直接 `UPDATE status`。状态迁移与业务写入必须在同一个事务里。

## 6. 值对象

| 值对象 | 定义 | 规则 |
|---|---|---|
| `Money` | `{amount decimal}`，币种为系统级常量 CNY | 禁止 `float64`；算术用定点十进制；`scale = 2`、`ROUND_HALF_UP`；**不带币种字段、不做币种校验与换算**（ADR-0004） |
| `public_id` | 时间有序 UUID（PG 18 内置 `uuidv7()`） | 对外暴露、可安全排序；降级到 PG 17 时由应用层生成（ADR-0002 联动条款） |
| `order_no` | 人类可读单号 | **店内唯一**，日期部分按 UTC+8；序号由数据库序列保证，不靠应用层拼接日期与门店 ID |
| `IdempotencyKey` | `{scope, key, request_hash}` | `scope` 含主体与门店；相同 key + 不同 hash → 冲突错误，不静默复用结果 |
| `ShopScope` | 当前主体可见的门店 ID 集合 | 由认证主体 + `user_shops` 推导（一个用户可有 N 家店、一店也可有多个用户），客户端传参只能做「集合内选择」，不能扩大集合；**是请求期推导结果，不对应任何持久化分组对象**（ADR-0005） |
| `CurrentShop` | 本次请求正在操作的单个 `shop_id` | 客户端经 `X-Shop-Id` **显式给出**，服务端与 `ShopScope` 求交后注入 context；是请求期上下文，**不落库、不进 JWT、不是持久化实体、不是授权来源**（ADR-0006） |
| `TimeRange` | 半开区间 `[start, end)` | 统一 UTC，避免 `BETWEEN` 的边界重复计数 |
| `BusinessDay` | `(occurred_at AT TIME ZONE 'Asia/Shanghai')::date` 再按门店营业日切点偏移 | 全系统唯一口径；日报、班次、汇总、导出共用；基准时区固定 UTC+8，不接受客户端或门店时区参数（ADR-0004） |

> 金额与时间的口径收窄后，领域层不再需要任何汇率、时区表或换算函数；`internal/domain/` 保持零外部依赖。

**计价与分摊**：整单折扣、抹零、优惠都要按明细分摊到行，纯函数 `Allocate(total, weights)` 保证「分摊之和恒等于总额」。余数按固定顺序（明细 `line_no` 升序）落到最后一项，保证相同输入永远得到相同结果，可表驱动测试。

## 7. 领域事件（经 Outbox 投递）

| 事件 | 载荷最小集 | 订阅者用途 |
|---|---|---|
| `order.created` / `order.paid` / `order.cancelled` | `shop_id`、`order_id`、`public_id`、发生时间 | 汇总脏标记、通知、扩展模块 |
| `payment.succeeded` / `refund.succeeded` | `shop_id`、`payment_id`、金额 | 对账、汇总 |
| `member.balance_changed` | `shop_id`、`member_id`、变动额、关联单据 | 会员账、报表 |
| `inventory.adjusted` / `stock.transferred` | `shop_id`、目录项、数量、方向 | 库存报表、预警 |
| `shop.summary_dirty` | `shop_id`、业务日期 | worker 增量重算日报 |
| `export.requested` / `export.completed` | `shop_id`、`export_job_id` | 导出任务状态 |

**规则**：事件与业务写入在同一事务内落到 `outbox_events`，事务外由 worker 派发（DP-03）；事件至少投递一次，订阅方必须幂等；事件不带完整实体快照，只带 ID 与最小上下文，避免载荷版本地狱。

## 8. 通用化边界与扩展点

Porch OS 只提供**通用多店经营底座**，不内置任何行业专属规则。扩展必须走下面三个口子，不得修改核心业务表：

| 扩展点 | 做法 | 例子 |
|---|---|---|
| 目录类型 | `catalog_item.type` + `attributes jsonb` | 区分实物商品与服务项，附加自定义属性 |
| 价格规则 | 领域层 `PricingRule` 接口；内置只有「基础价 / 按店覆盖 / 整单折扣」 | 更复杂的计价由扩展模块实现该接口 |
| 事件订阅 | 订阅第 7 节事件，写**扩展表** | 统计、行业专属口径（打印设备不在范围内，ADR-0004） |

**硬约束**：扩展表同样必带 `shop_id`；扩展模块不得直接读写核心业务表、不得绕过用例层注入的 shop scope、不得新增上层隔离维度（ADR-0003）。行业专属规则进扩展模块，不进 `internal/domain/` 的核心包。

## 9. 不变量清单（评审检查表）

1. 每条 B 类记录有且只有一个 `shop_id`，且与聚合根一致。
2. 单据金额恒等于「明细合计 − 调整合计」，且调整已分摊到行。
3. 已支付单据不可回退到未支付状态；更正走退款 + 新单。
4. 累计退款不超过原支付额；组合支付各笔之和等于收款额。
5. 库存余额等于流水累计；扣减不允许把余额写成负数（除非门店显式允许）。
6. 状态迁移只能通过 `CanTransition` 纯函数，且与写入同事务。
7. 全局实体（会员档案、目录项）不带 `shop_id`，其副作用数据一律带。
8. 领域层不 import 适配器、平台包，也不直接执行 SQL。
9. 金额路径不出现浮点类型；单币种 CNY 下不出现 `currency` 字段、汇率或币种一致性校验（OQ-01 已由 ADR-0004 关闭）。
10. 任何跨店动作都显式声明，且发生在当前主体可见门店集合之内；不存在门店组 / 分组对象，也不按组授权（ADR-0005）。
11. 业务日与单号日期一律按 UTC+8 推导；`AT TIME ZONE` 必须显式写 `'Asia/Shanghai'`，不依赖会话默认时区。
12. 所有写操作在线同步完成；不存在本地队列、离线单据或补传合并路径（ADR-0004）。
13. 一个用户可关联多间门店（`user_shops` 多对多），但当前门店必须显式携带且只是请求期上下文：不落库、不进 JWT、不作为授权来源；不设 `users.shop_id` / `shops.owner_user_id` / 切换端点（ADR-0006）。

---

下一册：[02 数据库设计](02-database-design.md) 把这些不变量落成表、索引与唯一约束。

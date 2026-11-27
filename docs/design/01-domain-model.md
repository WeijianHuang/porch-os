# 前期系统架构详细设计 · 01 领域模型

> 上游：[00 总览](00-overview.md) 第 4–5 节（模块划分、数据归属三分法）、[ADR-0003](../decisions/0003-shop-level-isolation.md)、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种、东八区、暂不归档、不打印、不离线）、[ADR-0005](../decisions/0005-no-shop-groups.md)（无门店组）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（一人多店与当前门店切换）、[ADR-0007](../decisions/0007-remaining-open-questions.md)（不做跨店清分、审计日志保留 30 天）、[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)（详细设计已冻结、Docker 2+2、审计清理基线）。
> 本文定义「业务上什么必须成立」，不定义表结构（见 [02](02-database-design.md)）与接口形状（见 [03](03-api-design.md)）。

## 1. 建模原则

1. **结构体之间只持有 ID**，不嵌套对方实体；不建实体关联图（ADR-0002 阻断项 ①）。
2. **聚合尽量小**：`Order` 是聚合根，`OrderLine` 属于它；`Payment` 独立聚合，销售收款通过可空 `order_id` 引用订单（充值收款不挂订单），不嵌进 `Order`（ADR-0013）。
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
| `order` | `Order` | `OrderLine`、`PriceAdjustment` | B | 单据与所有明细同属一个 `shop_id`；每张单还绑定至多一张 ID 卡与一个房间（ADR-0022，均为可空 ID，提交结账前必须齐备）；金额恒等于明细合计减调整。`PriceAdjustment` 的原因已结构化（ADR-0020）：`type` 五值（`coupon` / `promotion` / `rounding` / `manual` / `other`）+ `title`（账单短标题、必填）+ `description`（详细原因、可选）；券以 `source_id` 只持 ID 预留追溯（无外键，券表未建） |
| `order` | `Payment` | `PaymentEntry`（组合支付的一笔） | B | 「收款」是独立聚合根，按 `kind`（`sale` / `topup` / `other`）区分来源；`sale` 必挂订单、可多次尝试（`failed` 终态、重试新行留审计链）、组合支付逐渠道记录且合计等于收款额；`topup` 必挂会员且不挂订单（ADR-0013）；`method` 只记**真实资金渠道**（`cash` / `balance` / `card`），券不是支付、一律进 `order_price_adjustments`（ADR-0016） |
| `order` | `Refund` | `RefundEntry`（退款的一笔渠道明细） | B | 退款以 `kind`（`sale` / `topup`）区分来源、必等于原收款的 `kind`；`sale` 挂订单、`topup` 挂会员（`member_id`）；引用已成功的支付且不修改原单，累计退款 ≤ 原支付；退款自带 `shift_id` / `operator_id`（退款动作的班次与操作人，不继承订单），常跨班次发生；退款请求显式携带 `refund_entries`（渠道 + 金额）且只退实际支付部分，系统只校验渠道合法性与「单渠道累计退款 ≤ 该渠道原支付额」，不自动分摊、不决定超额处理；**禁止跨店退款、充值余额不足整笔拒绝**（ADR-0013 / 0014 / 0016） |
| `order` | `Shift` | — | B | 一个门店同一时刻最多一个 `open` 班次（数据库部分唯一索引保证） |
| `order`（[ADR-0022](../decisions/0022-order-card-binding-and-companion-groups.md)） | `CompanionGroup` | `CompanionGroupMember`（成员，`left_at` 留拆出史） | B | 同行组 = 多张各自绑卡的单据组成的一组共同结算；**一单同一时刻至多属一个 `open` 组**（部分唯一）；`status` = `open`/`closed`/`cancelled`（后两者终态）。组**不是付款主体、不存金额**：付款仍逐单走 `Payment`，组内应付 / 已付合计为只读派生。合并 / 拆分只动分组归属，不改订单的卡 / 房间绑定、明细、金额 |
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

跨店核销（会员在 A 店的余额于 B 店消费）是一次**显式业务动作**：分别在两家店各写一条流水，并记一条关联 ID，不修改会员档案的归属，也不提升任何查询权限。**门店间差额不做自动清分**（OQ-07 已由 [ADR-0007](../decisions/0007-remaining-open-questions.md) 关闭）：不引入内部往来账、结算单或清分任务，差额只在按店分组的报表里呈现；口径、示例与判断清单见 [09 跨店差额与清分边界](09-cross-shop-difference.md)，将来需要清分时另立 ADR。

**平台基础设施数据不计入 A/B/C 归属判定**：`audit_logs` 记录「谁、在哪个店、做了什么、什么结果」，是运行期数据而非业务数据。按 [ADR-0007](../decisions/0007-remaining-open-questions.md)，它**默认保留 30 天、可配置、由 worker 定时删除**；业务请求对其只追加、不可改删，唯一例外是清理任务。这与「业务数据暂不归档」（ADR-0004）不冲突——审计日志定时删除 ≠ 业务数据归档。清理窗口按 UTC+8 自然日计算，默认规则、错误契约与运行痕迹见 [08 审计日志保留与清理](08-audit-retention.md)。

## 4. 核心聚合字段（概念层，非 DDL）

**`Order`**

| 字段 | 含义 | 约束 |
|---|---|---|
| `id` / `public_id` | 内部主键 / 对外标识 | 分开；`public_id` 用时间有序 UUID |
| `shop_id` | 归属门店 | 创建后不可变 |
| `order_no` | 人类可读单号 | **店内唯一**，由数据库唯一约束保证 |
| `card_id` | 绑定的 ID 卡（可空，草稿可先建后绑；ADR-0022） | 只持 ID、复合外键锁同店；**一单一卡**，有效绑定判据 `card_released_at IS NULL`（部分唯一）；结算 / 取消时同事务置 `card_released_at` 与 `card_release_reason`（`settled`/`cancelled`/`unbound`/`replaced`），并释放该卡当前持卡流水（`release_reason='order_settled'`/`'order_cancelled'`）；**提交结账前必须已绑定有效卡**，否则 `422 ORDER_CARD_REQUIRED`。与 `card_holdings` 的「人持卡」是两个独立维度 |
| `room_id` | 绑定的房间（可空，草稿可先建后绑；ADR-0022） | 只持 ID、复合外键 `(shop_id, room_id)` 锁同店；一房间可关联多张单，不做房间互斥；绑定时房间须 `active`，已绑房间停用不影响结算；**提交结账前必须已绑定房间**，否则 `422 ORDER_ROOM_REQUIRED`；详情展示当前房间 `floor/code/name`，不落快照 |
| `status` | 状态机取值 | 见第 5 节 |
| `shift_id` / `operator_id` | 班次 / 操作人 | 只持 ID |
| `member_id` | 关联会员（可空） | 只持 ID，不嵌会员对象 |
| `total_amount` / `discount_amount` | 应付快照金额（一律 CNY） | 定点十进制 `numeric(14,2)`；不带币种字段；**不可变**；`total = Σ(line) − Σ(adjustment)`。实付 / 已退 / 可退**不落库**，从不可变记录派生（见下，ADR-0014） |
| `version` | 乐观锁版本 | 每次更新 `+1`，更新带 `WHERE version = $n` |
| `created_at` / `updated_at` | 时间 | `timestamptz` 内部 UTC；连接 session 固定 UTC；展示按 `Asia/Shanghai`；业务日由 `created_at` 经统一 `business_date()`（UTC+8 + 门店切点）推导，不依赖 session 默认（ADR-0014） |

**派生金额（不落库，ADR-0014）**：订单不再有 `paid_amount` 字段；下列三个量由用例层从不可变记录实时计算，真相以记录为准：

| 派生量 | 计算口径 |
|---|---|
| 实付 `paid_amount` | `SUM(payments.amount)` where `order_id=? AND kind='sale' AND status='succeeded'` |
| 已退 `refunded_amount` | `SUM(refunds.amount)` where `order_id=? AND kind='sale' AND status='succeeded'`（显式过滤 `kind`，topup 退款不计入订单已退，ADR-0016） |
| 可退 `refundable_amount` | 实付 − 已退（只含实际支付部分，券优惠不在其中） |

订单上的 `refunded` / `partially_refunded` 仅作粗粒度筛选标记，由退款事务更新；金额真相永远以记录聚合为准，二者不一致时以记录为准并视为缺陷。`payments` / `payment_entries` / `refunds` / `refund_entries` 一旦写入不可修改、不可删除，重试支付或再次退款都是新增行（ADR-0013 / 0014）。

**充值可退额（不落库，ADR-0016）**：充值退款以 `kind='topup'`、`member_id` 锚定，按原 `payment_id` 计算；可退额 = 原 `payments.amount`（`kind='topup'`、`succeeded`）− `SUM(refunds.amount)`（`payment_id=? AND kind='topup' AND succeeded`）。退款只涉及**实际支付部分**；余额不足（`member_balances.balance < 退款额`）时**整笔拒绝**（`409 INSUFFICIENT_BALANCE`），不做部分退、不写负余额；**禁止跨店退款**（退款单 `shop_id` 强绑原支付与会员本店账）。

**`OrderLine`**：`order_id`、`catalog_item_id`（只持 ID）、`quantity`、`unit_price`、`amount`、`line_no`。明细不随目录价格变动而回溯改写——下单时快照价格。**ADR-0023 增量**：服务明细另带 `staff_id`（店内 FK 到 `staff`，一条服务行一位执行人；商品行必须为空）与 `staff_name_snapshot`（指定时快照姓名，删人后置空 `staff_id` 仍可读历史）。一单可有多位服务员，各自负责不同服务项；提交结账前每条 `type='service'` 明细必须有合格执行人。

**`StaffServiceItem`（资质关系）**：服务员 ↔ 服务目录项的店内多对多（关联表 `staff_service_items`，带 `shop_id`，复合外键锁服务员同店；目录项仍是 A 类全局行）。只授权 `type='service'` 的目录项；资质用于开单时过滤可选服务员与提交时复验，不驱动排班、计时或业绩归属（ADR-0023）。

**`Payment` / `PaymentEntry`**：一次收款可组合多种方式（现金、卡、余额），每个 `PaymentEntry` 记方式与金额，合计等于本次收款额。禁止把「组合支付」压成一个字符串字段。`method` 只记**真实资金渠道**（`cash` / `balance` / `card`）：**券不是支付，它减少应收、不发生资金转移，一律进 `order_price_adjustments`（按明细分摊），不得进 `payment_entries`**（ADR-0016）；本期无卡券实体 / 发放 / 核销规划，`card` 保留枚举位但本期接入以 API 白名单为准。`Payment` 以 `kind` 区分来源：`sale`（销售收款，`order_id` 必填，可多次尝试，失败终态、重试生成新行）、`topup`（会员充值，`order_id` 为空、`member_id` 必填）、`other`（其他收款，本期不开放端点）；三者由数据库 CHECK 约束钉死，不允许「有订单却是充值」的脏数据（ADR-0013）。`payments` 与 `refunds` 均带 `shift_id` / `operator_id`，记录**动作发生时**的班次与操作人（只持 ID、不建外键导航）；退款的归属不继承原订单，因为退款常发生在另一班次、由另一人操作。

**`Refund` / `RefundEntry`**（ADR-0016）：退款以 `kind`（`sale` / `topup`）区分来源，必等于原收款 `payments.kind`；`sale` 挂订单、`topup` 挂会员（`member_id`），`order_id` 可空。退款只退实际支付部分，`refund_entries.method` 同 `payment_entries` 口径（不含券）；充值退款的余额扣回与本店账强绑，**禁止跨店**。

## 5. 状态机

### 5.1 单据 `Order`

```
draft ──► open ──► pending_payment ──► paid ──► completed
  │         │            │               │          │
  │         │            │               ├──────────┤
  │         │            │               ▼          ▼
  │         │            │      partially_refunded / refunded
  │         │            │               │
  │         │            │               └──► refunded（累计退完）
  └─────────┴────────────┴──────► cancelled
```

**无 `refunding` 态**（ADR-0014）：退款在单个数据库事务内完成，事务外只能观测到 `succeeded` / `failed`，不存在稳定的「退款中」订单态；失败即整体回滚、订单状态不变，无需回退迁移。失败尝试以 `refunds.status='failed'` 留审计行。

| 迁移 | 触发 | 允许的副作用 |
|---|---|---|
| `draft → open` | 开单 | 写明细、锁库存 |
| `open → pending_payment` | 提交结账 | 计价纯函数、生成应付 |
| `pending_payment → paid` | 全部支付成功 | 写支付流水、扣库存、改汇总脏标记、发 `order.paid` |
| `paid → completed` | 日结 | 封账并触发当日汇总重算（业务数据不归档，ADR-0004） |
| `paid` / `completed` / `partially_refunded` → `refunded`（全额）或 `partially_refunded`（部分） | 发起退款 | 单事务内写退款单与 `refund_entries`、校验渠道归属、回滚库存与余额；失败整体回滚、状态不变 |
| `partially_refunded → refunded` | 后续退款把余额退完 | `CanTransition` 必须显式包含此边（ADR-0014） |
| 任意未支付态 → `cancelled` | 取消 | 释放库存 |

**不可变约定（DP-05）**：`paid` 及之后的状态**不允许回退到 `open`**。更正错误单据的方式是「对原单发起退款 + 新建一张正确单据」，而不是反结算改写原单。这样已支付单据始终是审计意义上的事实，报表与对账不需要处理「被改过的历史」。

### 5.2 其他状态机

| 聚合 | 状态流 | 备注 |
|---|---|---|
| `Payment` | `pending → succeeded / failed` | `failed` 是终态；重试生成新的支付尝试 |
| `Refund` | `pending → succeeded / failed` | 累计退款额 ≤ 原支付额；`refund_entries` 各渠道累计 ≤ 该渠道原支付额；`kind='topup'` 另要求本店余额充足，否则整笔拒绝（ADR-0014 / 0016） |
| `Shift` | `open → closing → closed` | 一个门店同时最多一个 `open`（部分唯一索引） |
| `ExportJob` | `queued → running → succeeded / failed / expired` | `expired` 由定时任务清理 |

**实现约束**：状态迁移是 `internal/domain/` 下的纯函数 `CanTransition(from, to, event) error`；非法迁移返回类型化错误，不允许在 Handler 里直接 `UPDATE status`。状态迁移与业务写入必须在同一个事务里。

## 6. 值对象

| 值对象 | 定义 | 规则 |
|---|---|---|
| `Money` | `{amount decimal}`，币种为系统级常量 CNY | 禁止 `float64`；算术用定点十进制；`scale = 2`、`ROUND_HALF_UP`；**不带币种字段、不做币种校验与换算**（ADR-0004） |
| `public_id` | 时间有序 UUID（PG 18 内置 `uuidv7()`） | 对外暴露、可安全排序；降级到 PG 17 时由应用层生成（ADR-0002 联动条款） |
| `order_no` | 人类可读单号 | **店内唯一**，日期部分取 `business_date(now(), shop_id)`（UTC+8 + 门店营业日切点，ADR-0014），与日报 / 班次 / 汇总同口径；序号由数据库序列保证，不靠应用层拼接日期与门店 ID；`INSERT` 时显式调用 `next_order_no(shop_id)`，无字面 DEFAULT |
| `IdempotencyKey` | `{scope, key, request_hash}` | `scope` 含主体与门店；相同 key + 不同 hash → 冲突错误，不静默复用结果 |
| `ShopScope` | 当前主体可见的门店 ID 集合 | 由认证主体 + `user_shops` 推导（一个用户可有 N 家店、一店也可有多个用户），客户端传参只能做「集合内选择」，不能扩大集合；**是请求期推导结果，不对应任何持久化分组对象**（ADR-0005） |
| `CurrentShop` | 本次请求正在操作的单个 `shop_id` | 客户端经 `X-Shop-Id` **显式给出**，服务端与 `ShopScope` 求交后注入 context；是请求期上下文，**不落库、不进 JWT、不是持久化实体、不是授权来源**（ADR-0006） |
| `TimeRange` | 半开区间 `[start, end)` | 统一 UTC，避免 `BETWEEN` 的边界重复计数 |
| `BusinessDay` / `business_date(p_at, p_shop_id)` | 统一业务日函数：`(p_at AT TIME ZONE 'Asia/Shanghai')::date` 再按门店 `shop_settings` 营业日切点偏移 | 全系统唯一口径；日报、班次、汇总、导出、**单号日期**共用；基准时区固定 UTC+8，不接受客户端或门店时区参数（ADR-0004）；只可配切点偏移，不可覆盖基准时区（ADR-0014） |
| 连接 session 时区 | 所有连接初始化执行 `SET TIME ZONE 'UTC'` | 时间列均为 `timestamptz`（内部 UTC）；业务日显式 `AT TIME ZONE`，不依赖 session 默认；不采用无时区 `timestamp`（ADR-0014） |

> 金额与时间的口径收窄后，领域层不再需要任何汇率、时区表或换算函数；`internal/domain/` 保持零外部依赖。

**计价与分摊**：整单折扣、抹零、优惠都要按明细分摊到行，纯函数 `Allocate(total, weights)` 保证「分摊之和恒等于总额」。余数按固定顺序（明细 `line_no` 升序）落到最后一项，保证相同输入永远得到相同结果，可表驱动测试。整单调整按 `type` 区分优惠券 / 门店活动 / 抹零 / 手工 / 其他（ADR-0020），多笔先合并总额再一次性分摊；券以 `source_id` 预留追溯锚点，卡券体系落地时补复合外键与本店校验（本期只做字段级支持，不建券表）。

**统一计价纯函数 `Quote`（ADR-0019）**：`POST /shops/{shopId}/orders/price` 的实时试价与 F-01 `submit` 阶段的后端重算**必须调用同一个 `Quote`**（内部用 `Allocate` 分摊折让），保证「界面所见 = 最终结账」。试价只读、不落库、不开事务、不写幂等记录；取价口径为 `COALESCE(shop_catalog_overrides.price, catalog_items.base_price)`（本店覆盖价优先、否则基础价），与加项快照同口径；下架项（目录 `active=false` 或本店 `available=false`）一律拒绝试价与开单。折扣 / 抹零 / 券一律作为整单调整按金额传入并分摊到行（券不是支付，ADR-0016）；不得在端点与用例层各自重算分摊，口径只在 `Quote` 定义一次。

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
4. 累计退款不超过原支付额；组合支付各笔之和等于收款额；退款渠道明细之和等于退款额，且单渠道累计退款不超该渠道原支付额（ADR-0014）；退款只退实际支付部分，券优惠不得进支付 / 退款渠道（ADR-0016）。
4a. 退款以 `kind` 区分来源且等于原收款：sale 挂订单、topup 挂会员；充值退款要求本店余额 ≥ 退款额，不足整笔拒绝（不做部分退、不写负余额，ADR-0016）。
4b. 禁止跨店退款：退款单 `shop_id` 强绑原支付与会员本店账（ADR-0016 / 0003）。
5. 库存余额等于流水累计；扣减不允许把余额写成负数（除非门店显式允许）。
6. 状态迁移只能通过 `CanTransition` 纯函数，且与写入同事务。
7. 全局实体（会员档案、目录项）不带 `shop_id`，其副作用数据一律带。
8. 领域层不 import 适配器、平台包，也不直接执行 SQL。
9. 金额路径不出现浮点类型；单币种 CNY 下不出现 `currency` 字段、汇率或币种一致性校验（OQ-01 已由 ADR-0004 关闭）。
10. 任何跨店动作都显式声明，且发生在当前主体可见门店集合之内；不存在门店组 / 分组对象，也不按组授权（ADR-0005）。
11. 业务日与单号日期一律由统一 `business_date()`（UTC+8 + 门店营业日切点）推导，二者同口径；`AT TIME ZONE` 必须显式写 `'Asia/Shanghai'`，不依赖会话默认；连接 session 固定 UTC，时间列保持 `timestamptz`（ADR-0014）。
12. 所有写操作在线同步完成；不存在本地队列、离线单据或补传合并路径（ADR-0004）。
13. 一个用户可关联多间门店（`user_shops` 多对多），但当前门店必须显式携带且只是请求期上下文：不落库、不进 JWT、不作为授权来源；不设 `users.shop_id` / `shops.owner_user_id` / 切换端点（ADR-0006）。

## 10. ADR-0024 增补：服务人员分类与目录项分组

- `StaffCategory` 是店内 B 类值对象：`public_id` / `code` / `name` / `sort_order` / `status` / `version`，必须带 `shop_id`；与 `Staff` 是 1:N，`Staff.category` 可空、最多一个分类，重新绑定即替换。
- 分类**不是**资质（ADR-0023）、权限（RBAC）或调度状态（ADR-0015）：只用于筛选与展示。服务端不会因分类而放宽 `staff_service_items` 校验。
- `CatalogItem.category` 可空，指向全局 `catalog_categories`（A 类），只用于开单选人弹窗分组；不改变 `type` / `duration_minutes` / 价格派生。
- 不变量增补：任何服务人员都不能挂两个分类；跨店分类赋值必须被复合外键拒绝；停用的分类不可新绑定，但已绑定关系保留。

## 11. ADR-0025 增补：班次模板

- `ShiftTemplate` 是店内 B 类值对象（`shop_id`），保存每日复用的 `name` / `start_time` / `end_time` / `check_in_lead_minutes` / `next_day_flag` / `sort_order`；与订单域 `shifts` 的营业班次实例完全不同，二者互不读写。
- `ShiftTemplate` 1:N `Staff`，`Staff.shift_template` 可空且最多一个，重新分配即替换；复合外键 `(shop_id, shift_template_id)` 禁止跨店。
- 班次模板不是调度状态、分类、资质、权限或打卡记录；不参与金额与派单。详细契约见 [17 分册](17-staff-shift-templates.md)。

## 12. ADR-0026 增补：工号可编辑与别名

- `Staff.code` 仍是店内业务键（`uq_staff_shop_code`），从建档后不可变改为可在 `PATCH /staff/{staffId}` 中编辑；`public_id` 与内部 `id` 始终不可变，外键、订单明细快照与审计历史继续指向同一人。
- 新增 `Staff.alias`（可选、非唯一、0–60 字符）：只作展示与检索的辅助标签，不参与身份识别、授权、资质、分类、班次、计价或报表分组；主名称与订单快照仍取 `display_name`。
- 工号重复由 DB 唯一约束兜底并映射为 `409 STAFF_CODE_CONFLICT`；别名不做唯一性约束，重名合法。

## 13. ADR-0027 增补：性别与手机号

- 新增 `Staff.gender`（`unknown` / `male` / `female`，默认 `unknown`）与 `Staff.phone`（可选，空串表示未填）。两者都是店内 B 类档案属性，不参与身份识别、授权、资质、分类、班次、调度、计价或报表分组。
- 性别用显式哨兵值而非 `NULL`，避免三值逻辑进入 Go 投影与前端渲染；手机号非唯一，重复号码合法，本期不接短信、不做脱敏、不建索引。
- 新增纯函数 `ValidateGender` / `ValidatePhone`，手机号只接受空或 6–15 位数字（允许 `+`、空格、`-`、`()`），错误统一映射 `400 INVALID_ARGUMENT`。

---

下一册：[02 数据库设计](02-database-design.md) 把这些不变量落成表、索引与唯一约束。

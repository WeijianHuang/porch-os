# 16 · 开单收银工作台功能分析与设计

> 上游：[ADR-0002](../decisions/0002-go-backend-stack.md)（SQL 优先、领域纯函数）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（仅 `shop_id` 隔离）、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种 CNY / 东八区 / 不打印 / 不离线）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0011](../decisions/0011-rooms-and-floors.md)（房间）、[ADR-0013](../decisions/0013-payment-and-refund-ownership.md)、[ADR-0014](../decisions/0014-derived-amounts-and-refund-entries.md)、[ADR-0016](../decisions/0016-refund-source-and-topup-reversal.md)（支付 / 派生金额 / 退款）、[ADR-0017](../decisions/0017-id-cards-and-square-slots.md)（ID 卡与剩余分钟预留）、[ADR-0018](../decisions/0018-service-duration.md)（服务标称时长）、[ADR-0019](../decisions/0019-order-price-preview.md)（实时试价）、[ADR-0020](../decisions/0020-structured-price-adjustments.md)（结构化让利）、[ADR-0021](../decisions/0021-rename-order-price-adjustments.md)（让利表名）、[ADR-0022](../decisions/0022-order-card-binding-and-companion-groups.md)（订单绑定卡 / 房间、同行组、结算自动释放卡）、[ADR-0023](../decisions/0023-service-item-qualifications-and-line-staff.md)（服务项资质与明细执行服务员）。
>
> **本册性质**：对一张既有开单收银界面原型做功能分解，并给出与已冻结订单契约的映射方案。本册**不新增任何契约**：凡现有 ADR / `00`–`15` 已定的 schema、端点、状态机、金额口径一律沿用；凡原型里有而契约未覆盖的能力，一律标注「本期不做 / 需另立 ADR」，**不得据此直接改代码**。全文按脱钩规则只用通用术语。

## 1. 界面原型的功能分解

原型是一张单页收银工作台，共 8 个区域。下表按区域列出可观察元素与语义（行业专属称谓已通用化，见第 2 节）。

> **语义澄清（业务确认）**：初版本册曾把「同行」误读为「多张 ID 卡绑定到同一订单」，那是错误的。正确语义是——**一个订单绑定一张 ID 卡（1:1）与一个房间**；「同行」是把**多个各自已绑卡的单据**关联成一组，一起买单结算，组内**可合并、可拆分**。合并 / 拆分只改变单据的分组归属，**不改变「一张 ID 卡在同一时刻只对应一个订单」的约束**。另经确认：**结算结清后必须自动释放卡**（同时结束订单绑定与人员持卡关系，卡立即可复用），取消单同样释放。相关契约已由 [ADR-0022](../decisions/0022-order-card-binding-and-companion-groups.md) 接受（迁移 `00011`，schema 11），§8 P1 / P2 / P8 不再是「待 ADR」，下文历史描述中的「本期不做 / 待 ADR」以 ADR-0022 为准。另经业务确认：**服务员有两层关系**——维护「可做哪些服务项」的资质，并在每条服务明细上记录实际执行人；服务明细提交前必须有合格服务员。该契约由 [ADR-0023](../decisions/0023-service-item-qualifications-and-line-staff.md) 接受（迁移 `00012`，schema 12），§8 P3 已不再「不做」。

| 区域 | 可见元素 | 语义（通用化） |
|---|---|---|
| R1 顶部上下文 | 房间号、关闭按钮 | 当前服务房间（来自 `rooms.code`，ADR-0011）落到 `orders.room_id`，工作台可换房（ADR-0022）+ 退出工作台。**同一房间可同时有多张单**（多个客人同房）：各自开单、各绑各的卡；分开买单各自收款，合并买单进同一同行组 |
| R2 身份凭证组 | 两个已选卡片号、同行提示「支持多个」、设置同行 | 当前订单绑一张 ID 卡（1:1）；「同行」= 把多张各自绑卡的单据组成一组共同结算，支持合并 / 拆分（见 ADR-0022） |
| R3 操作条 | 选择服务人员、添加项目、添加产品、更换房间、凭证开单输入框、开单按钮 | 分别为：绑定执行人员、加服务项、加商品项、改房间、按卡号快速建单、提交建单 |
| R4 明细表 | 卡号 / 项目·产品 / 人员 / 计时方式 / 价格 / 数量 / 小计 / 业绩归属 / 券抵扣 / 开始时刻 / 状态 / 行操作 | 一行 = 一个 `order_line` 加若干展示列；行操作为「结束计时 / 删除 / 加时 / 加商品」 |
| R5 金额区 | 订单原价、实付金额、凭证选择、券优惠、抹零优惠、折扣选择 | 原价 / 应付 / 让利分解，对应 ADR-0014 的金额模型 |
| R6 抹零与备注 | 抹零金额输入、备注输入、保存备注 | 一笔 `rounding` 让利 + 订单级备注文本 |
| R7 收款区 | 8 个收款方式按钮、收款明细（渠道 + 金额输入） | 组合支付入口，对应 `payment_entries`（ADR-0014） |
| R8 底部动作 | 关闭、收款 | 取消 / 提交支付 |

**原型中两处值得记录的数据质量信号**（仅作分析，不迁移）：
1. 明细「状态」列出现约 `-2503427` 分钟的负剩余值，且两行数值递减。这属于历史算法缺陷（服务已开始却显示负剩余、量级达数年）。本设计按 [ADR-0017](../decisions/0017-id-cards-and-square-slots.md) §4 的既有决策处理：服务端 `service_remaining_minutes` 恒 `null`，前端显示「—」，不迁移该缺陷口径。
2. 券抵扣列是每行一个下拉，而契约里券是**整单让利**、按行金额权重分摊（ADR-0016 / 0019 / 0020）。行级券选择器与契约不一致，见 §3.2、§6。

## 2. 术语通用化对照（脱钩规则）

原型来自含行业专属词汇的旧界面。公开仓库不得出现这些词。统一映射如下，本册后续只用右列。

> 下表左列是原型概念的中性描述；原型中的行业专属称谓已刻意不在公开仓库保留，只保留「原型概念 → 通用术语 → 契约落点」的对应关系。

| 原型概念（中性描述） | 本册 / 契约通用术语 | 落点 |
|---|---|---|
| 房间号 | 房间号 `room.code` | `rooms`（ADR-0011） |
| 身份凭证号 | ID 卡 `id_cards.code` | `id_cards` / `card_holdings`（ADR-0017） |
| 多凭证同行 | 同行组 `companion_groups`：多张已绑卡单据的共同结算分组 | `companion_groups` / `companion_group_members`（ADR-0022） |
| 服务执行人员 | 服务人员 `staff` | `staff`（ADR-0009） |
| 计时方式（含轮次） | 计时方式 | 本期不落库（见 §8 P4） |
| 服务开始时刻 | 服务开始时刻 | 本期不落库（见 §8 P4） |
| 剩余分钟 | 服务剩余分钟 | 恒 `null`，UI 显示「—」（ADR-0017） |
| 业绩归属人员 | 业绩归属人 | 本期不做（见 §8 P5） |
| 结束计时 / 加时 | 结束计时 / 加时 | 本期不做（见 §8 P4） |
| 优惠券抵扣 | 券让利 `adjustment(type=coupon)` | `order_price_adjustments`（ADR-0020） |
| 抹零 | 抹零让利 `adjustment(type=rounding)` | 同上 |
| 纪念卡 | 会员卡 / 储值卡 | `balance` 支付渠道（见 §5） |
| 收款方式 | 支付渠道 `payment_entries.method` | 仅 `cash` / `balance` / `card`（ADR-0014） |

## 3. 与冻结契约的映射结论

### 3.1 已覆盖（可直接对接，无契约变更）

| 页面能力 | 现有契约 | 端点 / 字段 |
|---|---|---|
| 建单（草稿） | ADR-0013 | `POST /shops/{shopId}/orders`，`order.read`+`order.write` |
| 加服务项 / 加商品项 | ADR-0013 / 0018 | `POST /orders/{id}/lines`，取本店覆盖价、快照名称 |
| 删行 | ADR-0013 | `DELETE /orders/{id}/lines?line_no=` |
| 实时看见金额（所见即所得） | ADR-0019 | `POST /orders/price`，只读、免幂等键、共用 `Quote` |
| 价格 / 数量 / 小计 | ADR-0014 | `unit_price` × `quantity` = `amount`，定点 `numeric(14,2)` |
| 订单原价 / 应付 / 让利 | ADR-0014 / 0020 | `total_amount` / `discount_amount`；让利入 `order_price_adjustments` |
| 券优惠 / 抹零 / 折扣 | ADR-0016 / 0020 | 让利 `type` = `coupon` / `rounding` / `promotion` / `manual` / `other` |
| 实付 / 已退 / 可退 | ADR-0014 | 派生，不落库；从 succeeded 的 `payments` / `refunds` 聚合 |
| 组合收款 | ADR-0014 | `POST /orders/{id}/payments`，`entries[]` 合计 = 应付 |
| 提交结账 | ADR-0013 | `POST /orders/{id}/submit`，`draft\|open → pending_payment`，后端重算校验 |
| 收款状态推进 | ADR-0013 | `pending_payment → paid`；`paid → completed` 由业务确认 |
| 退款 | ADR-0013 / 0014 / 0016 | `POST /orders/{id}/refunds`，逐渠道退、单渠道不超原付 |
| 班次归属 | ADR-0013 | `GET/POST /shifts/current|open`，销账单自动带 `shift_id` |
| 房间基础数据 / 订单绑房 | ADR-0011 / ADR-0022 | `GET /shops/{shopId}/rooms`、`POST /orders/{id}/room` |
| ID 卡基础数据 / 持卡 / 绑单 / 自动释放 | ADR-0017 / ADR-0022 | `GET /cards`、`POST /cards/{id}/hold|release`、`POST /orders/{id}/card`；结清时由支付事务自动释放 |
| 服务人员基础数据 | ADR-0009 / 0015 | `GET /staff`、`GET /queues` |

### 3.2 部分覆盖（需设计澄清，仍不新增契约）

| 页面能力 | 现状 | 设计处置 |
|---|---|---|
| 一单一卡绑定与自动释放 | 原 `orders` 无 `card_id` | **ADR-0022 已接受**：`orders.card_id` + `card_released_at/reason` + 部分唯一，提交结账前必须绑卡；支付结清 / 取消同事务自动释放订单绑定与持卡流水；见 `design/03` §8、`design/06` F-09 |
| 订单绑定房间 | 原 `orders` 无 `room_id` | **ADR-0022 已接受**：`orders.room_id` 复合外键锁同店，提交结账前必须绑房；房间停用不影响历史单，删除被引用房间 `409 ROOM_IN_USE` |
| 同行组与合并 / 拆分 | 原无同行组实体与端点 | **ADR-0022 已接受**：`companion_groups` + 成员表 + 合并 / 拆分端点，付款仍逐单；原型「支持多个」指组内多张单据，非一单多卡 |
| 券抵扣显示在每行 | 契约把券作整单让利、按行权重分摊结果在 `Quote` 响应里 | 前端展示**分摊后的每行 `allocated_adjustment`**，不提供行级券选择器（§6.3） |
| 计时方式 / 开始时刻 / 剩余分钟 | `catalog_items.duration_minutes` 只是目录标称时长；剩余分钟恒 `null` | UI 展示标称时长与「—」，不落计时实例（§8 P4） |
| 业绩归属人 | 无字段、无端点 | 本期不采集（§8 P5） |
| 订单备注 | `orders` 无 `note` 字段 | 本期保留前端草稿备注但不落库；持久化见 §8 P6 |
| 8 种收款方式 | 后端只有 3 个渠道 | 按 §5 收敛；第三方渠道本期不接（NQ-15） |
| 更换房间 | 房间实体存在，`orders` 无 `room_id` | **ADR-0022 已接受**：`POST /orders/{id}/room` 写库；开单时也可带 `room_public_id`（§8 P1） |
| 服务人员绑定服务项 / 明细执行人 | `staff` 无资质关系、`order_lines` 无 `staff_id` | **ADR-0023 已接受**：`staff_service_items` 资质表 + `order_lines.staff_id` / `staff_name_snapshot`；服务行提交前必填，商品行不绑定（§8 P3） |

### 3.3 本期明确不做（须另立 ADR，未接受不改代码）

服务计时实例（开始 / 结束 / 加时）、业绩归属、订单备注持久化、第三方支付渠道接入、离线收银（ADR-0004）、打印（ADR-0004）。逐项动机与代价见 §8。**订单绑定 ID 卡 / 房间与同行组已由 ADR-0022 接受，订单明细执行服务员与服务项资质已由 ADR-0023 接受，均不再属于「本期不做」**。

## 4. 已覆盖能力的落地设计

### 4.1 开单主流程（页面时序 → 端点）

```
进入工作台
  ├─ GET /shops/{shopId}/rooms              → 房间上下文（R1）
  ├─ GET /shops/{shopId}/cards              → 可选 ID 卡（含 bound_order，R2）
  ├─ GET /shops/{shopId}/staff              → 可选服务人员（R3）
  ├─ GET /shops/{shopId}/catalog-items?...  → 项目 / 产品目录（R3 添加）
  └─ GET /shops/{shopId}/shifts/current     → 当前班次；无则 POST /shifts/open（需 order.write）
建单           → POST /orders {room_public_id?, card_public_id?}  （order.write，draft；ADR-0022）
绑卡 / 换卡    → POST /orders/{id}/card {card_public_id, version}  （一单一卡）
绑房 / 换房    → POST /orders/{id}/room {room_public_id, version}  （一单一房间）
选服务项 / 产品 → POST /orders/{id}/lines  （行快照，本店覆盖价优先）
每次增减行     → POST /orders/price          （只读试价，刷新 R5）
调整让利       → 本地维护 adjustments[]，再试价
选服务项 / 产品 → POST /orders/{id}/lines  （服务行可带 staff_public_id，需有资质；商品行不带）
改派服务人员   → PUT /orders/{id}/lines/{lineNo}/staff {staff_public_id|null}
点「开单」     → POST /orders/{id}/submit {version, total_amount, adjustments[]}
              → 前置：必须已绑卡、已绑房，且每条服务明细已有合格执行人；
                否则 422 ORDER_CARD_REQUIRED / ORDER_ROOM_REQUIRED / ORDER_LINE_STAFF_REQUIRED
设置同行       → POST /companion-groups     +  POST /companion-groups/{gid}/members {order_public_ids[]}
              → 多单成组共同结算；组内可 POST/DELETE .../members 合并 / 拆分（ADR-0022）
点「收款」     → POST /orders/{id}/payments {entries[]}   （逐单付款，组不代收）
              → 成功返回 payment + order，派生金额刷新；组详情合计随之派生刷新
              → 结清时同事务自动释放卡：订单绑定置 settled，持卡流水置 order_settled（ADR-0022）
取消单据       → POST /orders/{id}/cancel    → 同事务自动释放卡（reason=cancelled / order_cancelled）
```

- **幂等键**：所有写调用带 `Idempotency-Key`（试价例外）；`PayOrder` 的幂等键同时作为 `payments.idempotency_key` 落库防重复收款。
- **乐观锁**：`submit` / `cancel` 带 `version`，冲突 409 `VERSION_CONFLICT` → 前端重取订单并提示金额可能已变，重新试价后再提交。
- **金额一致性**：`submit` 的 `total_amount` 必须等于后端用已落库行快照 + `adjustments` 重算的结果，否则 422；前端的「应付」只来自 `POST /orders/price` 的 `total_amount`，不自算。

### 4.2 明细表（R4）

表格列只用现有 `OrderLine` / `QuoteLineResult` 字段，加少量只读展示：

| 列 | 取值来源 | 可编辑 |
|---|---|---|
| 卡号 | `orders.card_id`（经 `POST /orders/{id}/card` 绑定，ADR-0022）；选卡时前端展示可用卡 | 绑卡即写库（未结算单）；结清 / 取消自动释放 |
| 项目·产品 | `item_name_snapshot` | 否 |
| 人员 | 前端人员草稿（本期不落库，§8 P3） | 仅草稿 |
| 计时方式 / 开始时刻 | 目录 `duration_minutes` 派生展示 / 无 | 否 |
| 价格 / 数量 / 小计 | `unit_price` / `quantity` / `amount` | 数量经删行重建 |
| 业绩归属 | 无（本期不采集） | 否 |
| 券抵扣 | `Quote` 的 `allocated_adjustment`（整单分摊） | 在 R5 统一设券，不在行级 |
| 剩余分钟 | 恒「—」 | 否 |
| 行操作 | 结束计时 / 加时 不提供；删行走 `DELETE .../lines` | — |

> 「加商品」操作 = 在当前订单再加一条 `type=product` 的 `order_line`，不是新实体。

### 4.3 让利区（R5 / R6）

- 券、抹零、折扣统一进 `adjustments[]`，每项 `{seq, type, amount, title, description?, source_id?}`。
- 抹零输入（R6）→ `{type:"rounding", title:"抹零", amount:用户值}`；券选择 → `{type:"coupon", source_id:券实例ID, title:券名}`（本期只校验形状，不查券是否存在，ADR-0020）；折扣 → `{type:"promotion"|"manual", ...}`。
- 多笔让利先合并成单个 `discount_amount`，再一次分摊到行（ADR-0019 §2 决策），余数确定性落位；`Σ allocated_adjustment = discount_amount`。
- 约束：`discount_amount ≤ subtotal`（超出 422），`amount` 非负、定点字符串传输。

### 4.4 金额区（R5）口径

| 展示 | 契约字段 | 是否落库 |
|---|---|---|
| 订单原价 | `Quote.subtotal` / `Σ order_lines.amount` | 由行派生 |
| 让利（券 / 抹零 / 折扣） | `Quote.discount_amount` | 让利明细落 `order_price_adjustments` |
| 应付金额 | `Quote.total_amount` = `orders.total_amount` | 落库 |
| 实付金额 | ADR-0014 派生 `paid_amount` | **不落库**，succeeded payments 聚合 |
| 已退 / 可退 | ADR-0014 派生 | **不落库** |

> 单币种 CNY，UI 金额用 `formatCNY` 定点渲染，不出现浮点运算；时间统一 UTC+8 展示。

### 4.5 收款渠道收敛（R7，重点）

原型列出 8 个渠道，契约只有 `payment_entries.method ∈ {cash, balance, card}`。映射与处置：

| 原型渠道 | 契约映射 | 本期 |
|---|---|---|
| 现金 | `cash` | 支持 |
| 会员卡 / 纪念卡（储值） | `balance` | 支持（走 `member_balances` 条件扣减） |
| 银行卡 | `card` | 支持 |
| 第三方支付（列表中的多个外部渠道名） | 无对应枚举 | **不接**：属 NQ-15 外部集成，R1 不排期、不预留端点；`method` CHECK 不扩展 |
| 券 | 不是支付渠道 | 只作 `order_price_adjustments` 让利（ADR-0016） |

- 组合收款：`entries[]` 各项 `{method, amount}`，`Σ entries.amount` 必须等于应付金额；系统只校验，不自动分摊（ADR-0014）。
- 因第三方渠道不接，工作台对这几个按钮应置灰 / 隐藏并提示「暂未开通」，不得发送非法 `method` 导致 400。
- 「收款明细」区按 `entries[]` 逐行渲染金额输入，`balance` 渠道需先选出会员并校验余额，余额不足整笔拒绝。

### 4.6 状态机与可见操作

页面按钮随 `orders.status` 变化（状态机见 ADR-0013 / `01`）：

| 状态 | 可加行 | 可试价 | 可提交 | 可收款 | 可退款 |
|---|---|---|---|---|---|
| `draft` / `open` | ✓ | ✓ | ✓ | ✗ | ✗ |
| `pending_payment` | ✗ | 只读 | ✗ | ✓ | ✗ |
| `paid` / `completed` | ✗ | 只读 | ✗ | ✗ | ✓ |
| `partially_refunded` / `refunded` | ✗ | 只读 | ✗ | ✗ | 追加退款受可退额限制 |
| `cancelled` | ✗ | 只读 | ✗ | ✗ | ✗ |

无 `refunding` 中间态；已付款单据不可变，更正走退款 + 新单（DP-05）。

## 5. 数据模型映射（页面字段 → 现有表）

| 页面字段 | 现有表 / 列 | 缺口 |
|---|---|---|
| 订单原价 / 应付 / 让利 / 版本 | `orders.total_amount` / `discount_amount` / `version` | 无 |
| 明细行 | `order_lines`（`item_name_snapshot` / `quantity` / `unit_price` / `amount`） | 无人员、无计时列；卡绑在订单级（`orders.card_id`）而非明细行 |
| 让利（券 / 抹零 / 折扣） | `order_price_adjustments`（结构化原因） | 无行级归属 |
| 支付与渠道明细 | `payments` / `payment_entries` | 仅 3 渠道 |
| 退款与渠道明细 | `refunds` / `refund_entries` | — |
| 班次 | `shifts`（`public_id`） | — |
| 房间（落单） | `rooms` / `orders.room_id`（ADR-0022） | 复合外键锁同店；提交结账前必填，不落快照；**同房可同时多单、不做互斥** |
| ID 卡（一单一卡） | `id_cards` / `card_holdings` / `orders.card_id` + `card_released_at/reason`（ADR-0022） | 1:1 有效绑定 + 部分唯一；同房多单各绑各的卡；结清 / 取消自动释放两层绑定 |
| 同行组（多单共同结算） | `companion_groups` / `companion_group_members`（ADR-0022） | 分开买单 = 各单独立支付；合并买单 = 多单入同一组同屏结算；组不存金额、付款仍逐单 |
| 服务人员（上下文 / 执行人） | `staff` / `staff_service_items` / `order_lines.staff_id` + `staff_name_snapshot`（ADR-0023） | 资质按服务项维护；服务行一人、商品行不绑；缺人提交 `422 ORDER_LINE_STAFF_REQUIRED` |
| 服务开始 / 剩余分钟 | 无 | 未建模，恒「—」 |
| 业绩归属 | 无 | 未建模 |
| 订单备注 | 无 | 未建模 |

**所有 B 类业务表带 `shop_id`，复合唯一与前导索引以它为首列**（ADR-0003）。新增任何绑定列都必须是店内唯一 / 店内 FK，不得引入 `tenant_id` 或门店组（ADR-0005）。

## 6. 前端信息架构与交互（porch-console 对接建议）

本节是前端实现建议，不构成新契约。

1. **页面状态**：`draft`（本地购物车）→ 已建单 `open` → `pending_payment` → `paid`。本地购物车只存 `catalog_item_id` + `quantity` + 草稿人员 / 备注（房间与卡在建单时即写入 `orders`，ADR-0022），金额一律以试价响应为准。
2. **查询键**：工作台按 `(user_id, current_shop_id)` 缓存房间 / 卡 / 人员 / 目录 / 班次；试价结果不进跨会话缓存；订单详情按 shop-scope 失效。
3. **切换门店**（ADR-0006）：取消在途单店请求、丢弃旧试价响应、失效 `shop-scope` 缓存并重拉 `/me/permissions`；清空本地草稿。
4. **错误映射**：`VERSION_CONFLICT` → 重取订单 + 重算；`422 UNPROCESSABLE` → 提示金额已变并重刷试价；`INSUFFICIENT_BALANCE` → 提示储值不足并引导改渠道；`403` → 校验 `X-Shop-Id` 与当前可见门店。
5. **权限**：`order.read` 给店员（可试价 / 查看）；`order.write` 仅店长 + 管理员（建单 / 收款 / 退款 / 开班）。店员若需开单收款，须另立 ADR 放开 `order.write`，不在本册默认授予（见 ADR-0019 §1 注）。
6. **既有页面复用**：订单列表页 `/orders`（已完成）作为工作台入口之一；本工作台是「新建 / 收银」独立页，不复用列表的只读抽屉。

## 7. 验收口径

1. 全流程（建单 → 加行 → 试价 → 让利 → 提交 → 组合收款 → 派生金额 → 退款）不产生浮点误差，金额与后端重算一致。
2. 让利合并分摊：`Σ allocated_adjustment = discount_amount`，重复试价结果确定。
3. 组合收款 `Σ entries = 应付`；第三方渠道按钮不可发送非法 `method`；储值不足整笔拒绝。
4. 幂等重放不重复收款；`version` 冲突有明确重取路径。
5. 剩余分钟恒显示「—」；计时方式 / 开始时刻 / 业绩归属 / 备注不落库（本期）。
6. 无卡 / 无房间提交被拒（`422 ORDER_CARD_REQUIRED` / `ORDER_ROOM_REQUIRED`）；结清后订单 `card` 为 null、`card_binding_history` 保留，卡可被他人领取。
7. 越店请求 403、缺 `X-Shop-Id` 400；`/ready` schema_version 随 ADR-0022 升为 11、随 ADR-0023 升为 **12**。
8. 服务明细有合格执行人才能提交（缺人 / 无资质 / 停用分别 `422` / `422` / `409`），商品明细无人员约束；一单两条服务明细可指定两位不同服务员；删人后历史单仍显示快照姓名。

## 8. 提案台账（P1 / P2 / P8 已由 ADR-0022 接受；P3 由 ADR-0023 接受；其余未接受）

| 编号 | 提案 | 动机 | 代价 / 影响 | 本期结论 |
|---|---|---|---|---|
| ~~P1~~ | ~~订单绑定房间（`orders.room_id`，店内 FK 到 `rooms`）~~ | 工作台顶部房间上下文落单 | — | **已接受**（ADR-0022，迁移 `00011`，schema 11）；提交结账前必填，删除被引用房间 `409 ROOM_IN_USE` |
| ~~P2~~ | ~~订单绑定 ID 卡（`orders.card_id`，1:1）~~ | 一单一卡 | — | **已接受**（ADR-0022，迁移 `00011`，schema 11）；实现见 `design/02`/`03`/`06` F-09 |
| ~~P3~~ | ~~`order_lines.staff_id`（店内 FK 到 `staff`）~~ | 明细级服务人员归属 | — | **已接受**（ADR-0023，迁移 `00012`，schema 12）；另增 `staff_service_items` 资质关系 + `staff_name_snapshot` 快照名，服务行提交前必填、商品行不绑 |
| P4 | 服务计时实例（开始 / 结束 / 加时 / 计时方式） | 计时服务与剩余分钟真实计算 | 新建计时域、状态机、与目录 `duration_minutes` 的关系；原型已有负剩余缺陷 | 不做；剩余分钟恒「—」（ADR-0017） |
| P5 | 业绩归属人 | 业绩统计 | 引入归属规则与绩效口径，属行业专属 | 不做 |
| P6 | `orders.note` 备注 | 备注持久化 | 小字段但改 schema，且需定义是否随退款 / 审计 | 不做；草稿临时保留 |
| P7 | 第三方支付渠道接入 | 覆盖原型的 8 渠道 | NQ-15 外部集成、支付网关 SDK、回调与对账，冲击 R1 范围 | 不排期、不预留端点 |
| ~~P8~~ | ~~同行组 + 成员与合并 / 拆分端点~~ | 多单成组共同结算 | — | **已接受**（ADR-0022）；付款仍逐单、组不存金额 |

### 8.1 订单上下文与同行组的概念模型（ADR-0022 已接受，实现依 `02`/`03`/`06` F-09）

```
id_cards ──1:1（P2）──> orders <──N:1── companion_group_members ──N:1── companion_groups
                          │  一张单据同一时刻          组内可合入 / 拆出单据
                          │  只属于一个同行组
                          └──N:1── rooms（P1，一个房间可关联多张单）
```

- **一单一卡（P2）**：`orders.card_id` + `card_released_at/reason`，部分唯一按 `card_released_at IS NULL` 判定有效占用；提交结账前必须绑卡。此处的「单↔卡」与 ADR-0017 的「人↔卡」（`card_holdings`）是两个不同维度：人持卡 → 人用这张卡开单 → 单绑定这张卡。**结清 / 取消时两层同时自动释放**，卡立即可被他人领取；历史单保留卡引用与释放原因。
- **订单绑房间（P1）**：`orders.room_id` 复合外键锁同店，一房间可关联多单，不做房间互斥；提交结账前必须绑房，房间不落快照。
- **同行组（P8）**：组本身是结算视图，不是新的付款主体；付款仍逐单走 `payments`。一单在任意时刻至多属于一个 `open` 组；合并 = 独立单入组（跨组合入自动移出原组、原组空则同事务关闭），拆分 = 成员置 `left_at` 留史、单恢复独立。
- **不变量**：任何合并 / 拆分都不改变「一张卡在同一时刻只对应一个订单」与订单的房间归属；拆分后原单据的卡 / 房间绑定、明细、金额原样保留。
- 权威契约在 ADR-0022 与已同步的 `02-database-design.md` / `03-api-design.md` / `06-key-flows.md` F-09；本册不再重复展开。

其余提案（P3–P7）仍未接受；任一项要落地，须先写 ADR 并被接受、同步修订对应分册，再改代码；不得在本册直接扩张 schema 或 `method` 枚举。

## 9. 通用化边界

本册只用通用术语：房间、ID 卡、服务人员、服务项 / 商品、计时方式、让利 / 券、支付渠道。不含行业专属业务称谓、真实业务数据、私有代码或旧工程名。与冻结契约一致：单币种 CNY、UTC+8、不归档、不打印、不离线、无门店组、无租户层、当前门店请求期上下文、**只做开单收银的读与写编排；本册本身不新增契约，卡 / 房间 / 同行组的契约由 ADR-0022 定义**。

返回 [00 总览](00-overview.md) 查看完整分册地图与已关闭的 OQ / NQ 清单。

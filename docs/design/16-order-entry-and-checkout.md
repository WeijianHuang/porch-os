# 16 · 开单收银工作台功能分析与设计

> 上游：[ADR-0002](../decisions/0002-go-backend-stack.md)（SQL 优先、领域纯函数）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（仅 `shop_id` 隔离）、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种 CNY / 东八区 / 不打印 / 不离线）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0011](../decisions/0011-rooms-and-floors.md)（房间）、[ADR-0013](../decisions/0013-payment-and-refund-ownership.md)、[ADR-0014](../decisions/0014-derived-amounts-and-refund-entries.md)、[ADR-0016](../decisions/0016-refund-source-and-topup-reversal.md)（支付 / 派生金额 / 退款）、[ADR-0017](../decisions/0017-id-cards-and-square-slots.md)（ID 卡与剩余分钟预留）、[ADR-0018](../decisions/0018-service-duration.md)（服务标称时长）、[ADR-0019](../decisions/0019-order-price-preview.md)（实时试价）、[ADR-0020](../decisions/0020-structured-price-adjustments.md)（结构化让利）、[ADR-0021](../decisions/0021-rename-order-price-adjustments.md)（让利表名）。
>
> **本册性质**：对一张既有开单收银界面原型做功能分解，并给出与已冻结订单契约的映射方案。本册**不新增任何契约**：凡现有 ADR / `00`–`15` 已定的 schema、端点、状态机、金额口径一律沿用；凡原型里有而契约未覆盖的能力，一律标注「本期不做 / 需另立 ADR」，**不得据此直接改代码**。全文按脱钩规则只用通用术语。

## 1. 界面原型的功能分解

原型是一张单页收银工作台，共 8 个区域。下表按区域列出可观察元素与语义（行业专属称谓已通用化，见第 2 节）。

| 区域 | 可见元素 | 语义（通用化） |
|---|---|---|
| R1 顶部上下文 | 房间号、关闭按钮 | 当前服务房间（来自 `rooms.code`，ADR-0011）+ 退出工作台 |
| R2 身份凭证组 | 两个已选卡片号、同行提示「支持多个」、设置同行 | 若干 ID 卡绑定到同一单（ADR-0017 的卡实体），「同行」= 多卡共用一单的组 |
| R3 操作条 | 选择服务人员、添加项目、添加产品、更换房间、凭证开单输入框、开单按钮 | 分别为：绑定执行人员、加服务项、加商品项、改房间、按卡号快速建单、提交建单 |
| R4 明细表 | 卡号 / 项目·产品 / 人员 / 计时方式 / 价格 / 数量 / 小计 / 业绩归属 / 券抵扣 / 开始时刻 / 状态 / 行操作 | 一行 = 一个 `order_line` 加若干展示列；行操作为「结束计时 / 删除 / 加时 / 加商品」 |
| R5 金额区 | 订单原价、实付金额、凭证选择、券优惠、抹零优惠、折扣选择 | 原价 / 应付 / 让利分解，对应 ADR-0014 的金额模型 |
| R6 抹零与备注 | 抹零金额输入、备注输入、保存备注 | 一笔 `rounding` 让利 + 订单级备注文本 |
| R7 收款区 | 8 个收款方式按钮、收款明细（渠道 + 金额输入） | 组合支付入口，对应 `payment_entries`（ADR-0014） |
| R8 底部动作 | 关闭、收款 | 取消 / 提交支付 |

**原型中两处值得记录的数据质量信号**（仅作分析，不迁移）：
1. 明细「状态」列出现约 `-2503427` 分钟的负剩余值，且两行数值递减。这属于历史算法缺陷（服务已开始却显示负剩余、量级达数年）。本设计按 [ADR-0017](../../decisions/0017-id-cards-and-square-slots.md) §4 的既有决策处理：服务端 `service_remaining_minutes` 恒 `null`，前端显示「—」，不迁移该缺陷口径。
2. 券抵扣列是每行一个下拉，而契约里券是**整单让利**、按行金额权重分摊（ADR-0016 / 0019 / 0020）。行级券选择器与契约不一致，见 §3.2、§6。

## 2. 术语通用化对照（脱钩规则）

原型来自含行业专属词汇的旧界面。公开仓库不得出现这些词。统一映射如下，本册后续只用右列。

| 原型概念 | 本册 / 契约通用术语 | 落点 |
|---|---|---|
| 房间号 | 房间号 `room.code` | `rooms`（ADR-0011） |
| 手牌 | ID 卡 `id_cards.code` | `id_cards` / `card_holdings`（ADR-0017） |
| 技师 | 服务人员 `staff` | `staff`（ADR-0009） |
| 上钟类型（轮钟等） | 计时方式 | 本期不落库（见 §8 P4） |
| 上钟时间 | 服务开始时刻 | 本期不落库（见 §8 P4） |
| 剩余分钟 | 服务剩余分钟 | 恒 `null`，UI 显示「—」（ADR-0017） |
| 提成员工 | 业绩归属人 | 本期不做（见 §8 P5） |
| 下钟 / 加钟 | 结束计时 / 加时 | 本期不做（见 §8 P4） |
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
| 房间基础数据 | ADR-0011 | `GET /shops/{shopId}/rooms` |
| ID 卡基础数据 / 持卡 | ADR-0017 | `GET /cards`、`POST /cards/{id}/hold|release` |
| 服务人员基础数据 | ADR-0009 / 0015 | `GET /staff`、`GET /queues` |

### 3.2 部分覆盖（需设计澄清，仍不新增契约）

| 页面能力 | 现状 | 设计处置 |
|---|---|---|
| 多卡同行绑一单 | 卡实体存在，但 `orders` / `order_lines` 无 `card_id` | 本期只展示「已按卡筛选的目录」，卡与单的持久绑定见 §8 P2 |
| 券抵扣显示在每行 | 契约把券作整单让利、按行权重分摊结果在 `Quote` 响应里 | 前端展示**分摊后的每行 `allocated_adjustment`**，不提供行级券选择器（§6.3） |
| 计时方式 / 开始时刻 / 剩余分钟 | `catalog_items.duration_minutes` 只是目录标称时长；剩余分钟恒 `null` | UI 展示标称时长与「—」，不落计时实例（§8 P4） |
| 业绩归属人 | 无字段、无端点 | 本期不采集（§8 P5） |
| 订单备注 | `orders` 无 `note` 字段 | 本期保留前端草稿备注但不落库；持久化见 §8 P6 |
| 8 种收款方式 | 后端只有 3 个渠道 | 按 §5 收敛；第三方渠道本期不接（NQ-15） |
| 更换房间 | 房间实体存在，`orders` 无 `room_id` | 本期工作台内只切换上下文、不落单；绑定见 §8 P1 |

### 3.3 本期明确不做（须另立 ADR，未接受不改代码）

服务计时实例（开始 / 结束 / 加时）、业绩归属、订单备注持久化、订单绑定房间 / 卡 / 服务人员、第三方支付渠道接入、离线收银（ADR-0004）、打印（ADR-0004）。逐项动机与代价见 §8。

## 4. 已覆盖能力的落地设计

### 4.1 开单主流程（页面时序 → 端点）

```
进入工作台
  ├─ GET /shops/{shopId}/rooms              → 房间上下文（R1）
  ├─ GET /shops/{shopId}/cards              → 可选 ID 卡（R2）
  ├─ GET /shops/{shopId}/staff              → 可选服务人员（R3）
  ├─ GET /shops/{shopId}/catalog-items?...  → 项目 / 产品目录（R3 添加）
  └─ GET /shops/{shopId}/shifts/current     → 当前班次；无则 POST /shifts/open（需 order.write）
选服务项 / 产品 → POST /orders/{id}/lines  （行快照，本店覆盖价优先）
每次增减行     → POST /orders/price          （只读试价，刷新 R5）
调整让利       → 本地维护 adjustments[]，再试价
点「开单」     → POST /orders/{id}/submit {version, total_amount, adjustments[]}
点「收款」     → POST /orders/{id}/payments {entries[]}
              → 成功返回 payment + order，派生金额刷新
```

- **幂等键**：所有写调用带 `Idempotency-Key`（试价例外）；`PayOrder` 的幂等键同时作为 `payments.idempotency_key` 落库防重复收款。
- **乐观锁**：`submit` / `cancel` 带 `version`，冲突 409 `VERSION_CONFLICT` → 前端重取订单并提示金额可能已变，重新试价后再提交。
- **金额一致性**：`submit` 的 `total_amount` 必须等于后端用已落库行快照 + `adjustments` 重算的结果，否则 422；前端的「应付」只来自 `POST /orders/price` 的 `total_amount`，不自算。

### 4.2 明细表（R4）

表格列只用现有 `OrderLine` / `QuoteLineResult` 字段，加少量只读展示：

| 列 | 取值来源 | 可编辑 |
|---|---|---|
| 卡号 | 前端选卡草稿（本期不落库，§8 P2） | 仅草稿 |
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
| 明细行 | `order_lines`（`item_name_snapshot` / `quantity` / `unit_price` / `amount`） | 无卡、无人员、无计时列 |
| 让利（券 / 抹零 / 折扣） | `order_price_adjustments`（结构化原因） | 无行级归属 |
| 支付与渠道明细 | `payments` / `payment_entries` | 仅 3 渠道 |
| 退款与渠道明细 | `refunds` / `refund_entries` | — |
| 班次 | `shifts`（`public_id`） | — |
| 房间（上下文） | `rooms` | `orders` 无 `room_id` |
| ID 卡（上下文） | `id_cards` / `card_holdings` | `orders` / `order_lines` 无 `card_id` |
| 服务人员（上下文） | `staff` | `order_lines` 无 `staff_id` |
| 服务开始 / 剩余分钟 | 无 | 未建模，恒「—」 |
| 业绩归属 | 无 | 未建模 |
| 订单备注 | 无 | 未建模 |

**所有 B 类业务表带 `shop_id`，复合唯一与前导索引以它为首列**（ADR-0003）。新增任何绑定列都必须是店内唯一 / 店内 FK，不得引入 `tenant_id` 或门店组（ADR-0005）。

## 6. 前端信息架构与交互（porch-console 对接建议）

本节是前端实现建议，不构成新契约。

1. **页面状态**：`draft`（本地购物车）→ 已建单 `open` → `pending_payment` → `paid`。本地购物车只存 `catalog_item_id` + `quantity` + 草稿卡 / 人员 / 备注，金额一律以试价响应为准。
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
6. 越店请求 403、缺 `X-Shop-Id` 400、`/ready` schema_version=10 不变。

## 8. 待另立 ADR 的提案（未接受，不得据此改代码）

| 编号 | 提案 | 动机 | 代价 / 影响 | 本期结论 |
|---|---|---|---|---|
| P1 | `orders.room_id`（店内 FK 到 `rooms`） | 工作台顶部房间上下文落单 | 改 schema、影响列表筛选与报表分组 | 不做；房间仅作上下文 |
| P2 | 订单 / 明细绑定 ID 卡（`card_id`） | 多卡同行与按卡结算 | 需定义同行组基数、与 `card_holdings` 一人一卡约束的兼容 | 不做；卡仅作上下文 |
| P3 | `order_lines.staff_id`（店内 FK 到 `staff`） | 明细级服务人员归属 | 改 schema、影响删除人员时的历史单可读性（需快照名） | 不做；人员仅草稿 |
| P4 | 服务计时实例（开始 / 结束 / 加时 / 计时方式） | 计时服务与剩余分钟真实计算 | 新建计时域、状态机、与目录 `duration_minutes` 的关系；原型已有负剩余缺陷 | 不做；剩余分钟恒「—」（ADR-0017） |
| P5 | 业绩归属人 | 业绩统计 | 引入归属规则与绩效口径，属行业专属 | 不做 |
| P6 | `orders.note` 备注 | 备注持久化 | 小字段但改 schema，且需定义是否随退款 / 审计 | 不做；草稿临时保留 |
| P7 | 第三方支付渠道接入 | 覆盖原型的 8 渠道 | NQ-15 外部集成、支付网关 SDK、回调与对账，冲击 R1 范围 | 不排期、不预留端点 |

任一项要落地，须先写 ADR 并被接受、同步修订对应分册，再改代码；不得在本册直接扩张 schema 或 `method` 枚举。

## 9. 通用化边界

本册只用通用术语：房间、ID 卡、服务人员、服务项 / 商品、计时方式、让利 / 券、支付渠道。不含行业专属业务称谓、真实业务数据、私有代码或旧工程名。与冻结契约一致：单币种 CNY、UTC+8、不归档、不打印、不离线、无门店组、无租户层、当前门店请求期上下文、**只做开单收银的读与写编排，不新增订单契约**。

返回 [00 总览](00-overview.md) 查看完整分册地图与已关闭的 OQ / NQ 清单。

# ADR-0019 · 订单实时试价端点（收银机所见即所得）

- 状态：Accepted（2026-11-23）
- 关联：[ADR-0002](0002-go-backend-stack.md)（计价 = 领域层纯函数）、[ADR-0004](0004-scope-baseline.md)（单币种 CNY / 定点金额）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0014](0014-derived-amounts-and-refund-entries.md)（计价与分摊、金额派生）、[ADR-0016](0016-refund-source-and-topup-reversal.md)（券进 `order_price_adjustments`）
- 影响分册：修订 `design/01`（§6 计价共用）、`design/03`（端点清单 + 请求/响应形状 + 幂等例外）、`design/06`（F-01 阶段 B/C）；不新增分册
- 迁移：**无**（纯 API 扩展，不新增表、不改列）；`EXPECTED_SCHEMA_VERSION` 保持 **9**

## 背景

订单域的契约已由 ADR-0013 / 0014 / 0016 补全，端点清单（`design/03` §8）覆盖了开单、加项、提交结账、组合支付与退款。但真实收银场景还有一个缺口：店员在收银机上**开单并逐项增减明细时，界面需要实时看到当前应付金额与优惠分摊结果**——否则只能等 `submit` 落库后才知道金额，界面无法做到"所见即所得"，店员也不敢在看不到价的情况下确认结账。

这不是要改订单状态机或金额口径，而是要把已有的计价纯函数**在写库之前暴露成只读能力**。现有 `design/06` F-01 阶段 C 的"后端重算金额"逻辑本就是同一套计算；把它提前到试价端点，既满足界面需求，又保证预览与最终结账用同一个口径。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| "试价是不是要建一张草稿单 / 落库" | **不落库**。不插 `orders` / `order_lines`，不占单号，不开事务，不写幂等记录，不发事件。它是把纯函数包成只读端点，纯计算 + 按店读价 |
| "试价的价和最终结账的价可能不同" | 正常路径**必须相同**：试价与 `submit` 后端重算调用**同一个** `Quote` 纯函数。仅当两次调用之间目录价 / 按店覆盖价被改动时才会不同，此时 `submit` 的 422 校验兜住，前端须重新试价 |
| "试价要不要 `Idempotency-Key`" | **不要**。它是只读操作，不产生业务副作用；幂等中间件对此路径豁免（见决策 3） |
| "试价要不要 member_id 算会员价" | **不要**。契约没有会员价概念，取价口径只有 `COALESCE(shop_catalog_overrides.price, catalog_items.base_price)`（本店覆盖价优先、否则基础价），与加项时完全一致 |
| "券 / 折扣怎么进试价" | 折扣、抹零、券优惠都是**整单调整**，以金额进 `adjustments`，由 `AllocateByWeights` 按行金额权重分摊；券永远不是支付渠道（ADR-0016 不变） |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 试价实现方式 | 新增只读端点 `POST /shops/{shopId}/orders/price`，服务端按店取价 + 共用 `Quote` | 前端拿目录价自己算 | 前端算会与后端 422 重算口径分裂，正是"所见即所得"要消灭的问题；定价口径必须只有一处 |
| 端点动词 | `POST`（请求体携带整份明细列表，只读） | `GET` + query 传明细 | 明细是结构化列表，query 表达力不足且易触及长度限制；POST 只读是 REST 里承载结构化查询的常规做法 |
| 价格输入 | 服务端按 `catalog_item_id` 取 `COALESCE(本店覆盖价, 目录基础价)` | 客户端传 `unit_price`，服务端信任 | 与加项取价口径分裂，且把定价权交给客户端；只在加项落库时快照，试价阶段不信任客户端价 |
| 分摊函数 | 试价与 `submit` 共用 `Quote`（含 `AllocateByWeights`） | 试价单独写一份简化计算 | 两份计算必然漂移；共用是"所见即所得"的根本保证 |

## 决策

### 1. 新端点

```text
POST /api/v1/shops/{shopId}/orders/price
```

- 只读试价，**不落库、不占单号、不开事务、不写幂等记录、不发事件**。
- 权限 `order.read`；单店请求，`X-Shop-Id` 与路径 `{shopId}` 必须一致，走 `ShopScope` 求交（ADR-0006）。
- 现有 RBAC 基线（`00002_rbac_baseline.sql`）尚无 `order.*` / `member.topup.write` 权限点；订单域首批迁移须一并补入并分配角色，否则订单端点（含本端点）无权限可用。
- **订单域权限点分配（沿用各域既有惯例：`*.read` 全员、`*.write` 店长 + 管理员）**：

  | 权限点 | staff（店员） | manager（店长） | admin（管理员） | 覆盖端点 |
  |---|---|---|---|---|
  | `order.read` | ✓ | ✓ | ✓ | 试价 `orders/price`、单据列表 / 详情、班次查询 |
  | `order.write` | ✗ | ✓ | ✓ | 开单 / 加删项 / 提交 / 支付 / 退款 / 取消、开班 / 关班 |
  | `member.topup.write` | ✗ | ✓ | ✓ | 充值、充值退款 |

  > 店员只读订单（含试价）；**收银场景若需店员开单 / 收款，须另立 ADR 放开 `order.write` 给 staff**，不在本次默认授予。迁移写法沿用 `00004` / `00007` 的 `role_permissions` 插入模式。

### 2. 请求 / 响应

请求体（金额与数量均为字符串，定点十进制）：

```json
{
  "lines": [
    { "catalog_item_id": "019...", "quantity": "2.000" },
    { "catalog_item_id": "019...", "quantity": "1.000" }
  ],
  "adjustments": [
    { "type": "rounding", "amount": "0.50", "title": "抹零" },
    { "type": "coupon", "amount": "5.00", "source_id": 123, "title": "满100减10", "description": "会员生日券" }
  ]
}
```

- `lines`：当前完整明细列表（**全量**，不是增量）；`catalog_item_id` 为 `public_id`，`quantity` 必须 > 0，`numeric(14,3)`。
- `adjustments`：可选，每项 `{type, amount, source_id?, title, description?}`，原因已结构化（ADR-0020）：`type` 必填且五值之一（`coupon` / `promotion` / `rounding` / `manual` / `other`）、`amount` 非负、`title` 必填非空、`description` 可选；`type='coupon'` 时 `source_id`（bigint，券实例内部 ID）必填且为正整数（本期只校验形状，不查券是否存在），其他类型必须为空。券是折扣不是支付（ADR-0016）。

响应体：

```json
{
  "data": {
    "lines": [
      {
        "line_no": 1,
        "catalog_item_id": "019...",
        "item_name_snapshot": "示例服务",
        "quantity": "2.000",
        "unit_price": "30.00",
        "amount": "60.00",
        "allocated_adjustment": "3.00",
        "line_total": "57.00"
      }
    ],
    "subtotal": "60.00",
    "discount_amount": "5.00",
    "total_amount": "55.00"
  },
  "meta": { "request_id": "req_01J..." }
}
```

- `unit_price` = **`COALESCE(shop_catalog_overrides.price, catalog_items.base_price)`**（覆盖价是整价替换、不是增量；有本店覆盖行就用覆盖价，否则用基础价），`item_name_snapshot` 取当前目录名——与 F-01 阶段 B 加项快照同口径。
- **上下架过滤**：目录项全局 `catalog_items.active = false` 或本店 `shop_catalog_overrides.available = false` 均视为不可用（有覆盖行时以 `available` 为准，无覆盖行时以 `active` 为准），试价直接 `400 INVALID_ARGUMENT`（`details` 指向对应行、`reason: item_unavailable`）；下架项不得进入试价结果，也不得据此开单。
- `amount = unit_price × quantity`（`numeric(14,2)`，scale 2、ROUND_HALF_UP）；`subtotal = Σ amount`；`discount_amount = Σ adjustments.amount`。
- **折让按总额合并分摊**：先把所有 `adjustments[].amount` 合并为单个 `discount_amount`，再由 `AllocateByWeights` 一次性按各行 `amount` 权重分摊到行（不是逐笔优惠各分摊一遍）；余数按 `line_no` 升序落到最后一行，保证 `Σ allocated_adjustment = discount_amount` 且结果确定；`line_total = amount − allocated_adjustment`；`total_amount = subtotal − discount_amount`。响应只给合并后的每行 `allocated_adjustment` 与订单级 `discount_amount`，逐笔优惠的展示由前端按请求里的 `adjustments` 自行渲染。
- 校验失败：`quantity <= 0` / 非数字 / 目录项不存在 / 无本店可用价 / 目录项已下架（见上）→ `400 INVALID_ARGUMENT`（`details` 指向对应行）；`discount_amount > subtotal` → `422 UNPROCESSABLE`。

### 3. 幂等例外（实现说明，非契约变更）

`design/03` §6 的幂等适用范围是"所有产生业务副作用的写操作"。试价是只读 POST，无副作用，故：

- 幂等中间件对该路径豁免，不要求 `Idempotency-Key`；
- 中间件现有实现按方法判断（GET / HEAD 跳过），需增加该只读 POST 路径的例外判断；
- 该例外仅此一个端点，不得扩大到任何写端点。

### 4. 与 `submit` 的一致性约定

- `design/06` F-01 阶段 C 的"后端重算明细合计与客户端提交金额"调用**同一个** `Quote(lines, adjustments)` 纯函数；输入为 `order_lines` 已快照的 `unit_price` × `quantity` 与 `order_price_adjustments`。
- 试价端点与 `submit` 共用该函数，正常路径金额必然一致；422 保留为防御性校验（应对两次调用间目录改价或客户端篡改）。
- 分摊规则（权重、`ROUND_HALF_UP`、余数落最后一行）只在 `Quote` / `AllocateByWeights` 定义一次，端点与用例层都不得自行重算。

## 理由

1. **满足真实收银交互**：增减项时即时显示实际价与优惠，界面不再等落库才知道金额。
2. **口径唯一**：预览与结账共用纯函数，消除前端自算与后端重算分裂，这正是 422 校验想防却防不住的根因。
3. **零 schema 成本**：纯 API 扩展，无迁移、不改 `EXPECTED_SCHEMA_VERSION`、不动已交付代码，是当前代价最小的扩展。
4. **不破坏冻结契约**：不改状态机、不改金额派生口径、不改订单表结构，只是把既有计价能力提前暴露。

## 后果与限制

- 新增端点须同步 `porch-console`：购物车每次增减项调本端点刷新价格，`submit` 仍以快照价重算；两次调用间目录改价时前端收到 422 须重新试价再提交。
- 新增权限点 `order.read`（及订单域其余 `order.*` / `member.topup.write`）须在订单域首批迁移中补入并分配角色。
- 试价不锁库存、不校验余额与班次——那些属于 `submit` / 支付事务的职责；试价只回答"按当前价这套明细应付多少"。
- 高频试价是纯读 + 按店读价，仍受普通请求 5 s 超时与按主体 + 门店限流约束（design/03 §9）。
- 实参 `lines` 为全量明细，请求体大时受 1 MiB 请求体上限约束（中间件既有常量）；超大购物车须分批，本期不开放。

## 复审条件

- 需要会员价 / 阶梯价 / 时段价参与试价 → 属定价规则扩展，另立 ADR（冲击 ADR-0004 单币种之外的价格模型）；
- 需要试价锁库存或预留额度 → 变成有副作用的写操作，须重回幂等与事务契约；
- 需要把试价结果缓存 / 持久化 → 冲击"口径唯一、不落派生真相"的原则，须给出防漂移证明；
- ADR-0014 / 0016 的计价或券口径被修订且影响本端点的输入 / 输出形状。

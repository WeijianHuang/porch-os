# ADR-0013: 收款与退款的归属维度

- 状态：已接受（2026-11-20）
- 决策问题：修正 R3 订单域中三处尚未落地但有概念缺陷的定义——① `payments.order_id NOT NULL` 与「会员充值也写 payments」互相矛盾；② 收款 / 退款不携带班次与操作人，无法按班次 / 人员做归属与统计；③ `refunds` 只有表清单、缺少完整 DDL。确定收款与订单的关系、收款 / 退款的归属维度与可聚合口径，使 R3 实现有可追溯的契约来源
- 影响范围：修订 `design/01`（`Payment` / `Refund` 字段与不变量）、`design/02`（`payments` DDL 修正、补全 `refunds` / `payment_entries` / `price_adjustments` DDL）、`design/03`（充值与退款端点归属）、`design/06`（F-01 / F-02 / F-03 事务步骤）；新增迁移（订单域首批，编号顺延当前最大值）；**不改** `EXPECTED_SCHEMA_VERSION`（订单域未落地，本次只改设计、不加迁移）、不改任何已落地代码
- 上游：[ADR-0001](0001-postgresql-18.md)（PG 18.7）、[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0004](0004-scope-baseline.md)（范围基线）、[ADR-0006](0006-user-shops-and-current-shop.md)（操作人 = 登录账号）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）

## 背景

订单域（开单 / 收款 / 退款 / 班次）是路线图 R3 的内容，`migrations/` 至今没有对应表（当前 `EXPECTED_SCHEMA_VERSION=7`），`design/02` 也只给了 `orders` / `order_lines` / `payments` / `shifts` 四张表的示例 DDL，`price_adjustments` / `payment_entries` / `refunds` 仅有表清单。也就是说：订单域的契约是**半截的**，且这半截里有互相冲突与字段缺失。

在准备以订单域入手实现时，识别出三处必须先解决的概念问题：

1. **`payments.order_id NOT NULL` 与充值流程冲突**：`design/06` F-03 规定会员充值「写 `payments`」，但 `payments.order_id` 非空且外键到 `orders`——充值不是销售，没有订单可挂。现有契约无解。
2. **收款 / 退款缺少归属维度**：`payments` 表无 `shift_id` / `operator_id`，`refunds` 连 DDL 都没有。无法回答「这笔款是谁在哪个班次收的 / 退的」，也就无法做班次交接对账与人员业绩统计。
3. **退款维度有硬业务需求**：一笔已支付订单完全可能在**另一个班次、由另一个人**退款，因此退款必须携带**自己的**班次与操作人，不能继承订单的 `shift_id` / `operator_id`。

按 ADR-0008 的冻结后变更规则，这些改动必须先有 ADR 与设计来源。本 ADR 即该来源，**它是修正而非推翻**：不改动 ADR-0001–0012 的任何条款，只在尚未落地的 R3 订单域里把矛盾消解、把字段补全。

### 理解澄清（本轮最容易被误读的四处）

| 说法 | 准确含义 |
|---|---|
| 「支付失败会产生多个 payment，是设计缺陷」 | **不是缺陷，是刻意设计**。`Payment` 状态机 `pending → succeeded / failed`，`failed` 为终态，重试**生成新的支付尝试**并各留一行，构成审计链；重复入账由「状态迁移只能走一次（`412`）+ 支付合计校验 + 幂等键 `uq_payments_idem`」三道闸防止，不依赖「一单只能有一笔 payment」 |
| 「应该让 `orders` 持有 `payment_id`」 | **不采用**。一次收款可组合多种方式（`PaymentEntry`），一笔订单可能有多次支付尝试，部分退款还要引用原支付——「订单 → 单一 payment」装不下这些。方向保持为 `payment` 引用（`order_id`），而不是反向持有 |
| 「收款与订单要加中间表关联」 | **不采用**。当前 `payments.order_id`（一对多）已能表达「一单多次尝试 / 多笔收款」；中间表只会把一对多拆成多对多，引入无业务含义的关联实体。真正要改的是让**非销售收款（充值）**不必挂订单 |
| 「班次 / 人员是收款的从属实体」 | **不是**。`shift_id` / `operator_id` 与 `orders` 的既有口径一致：**只持 ID，不加外键**（班次 / 操作人是记录归属维度，不是要导航的实体图）；它们用于统计与审计，不引入 ORM 式关联 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 收款与订单的关系 | `payments.order_id` **可空**；销售收款必填并受复合外键 `(shop_id, order_id)` 约束，充值 / 其他收款可空并以 `kind` 区分 | 保持 `order_id NOT NULL`，为每次充值造一张「充值订单」 | 伪造订单会污染 `orders` 语义与单号序列，把非销售塞进销售单据，报表口径立刻失真 |
| 拆分收款表 | 单表 `payments` + `kind`（`sale` / `topup` / `other`） | 拆成 `order_payments` / `topups` 两张表 | 两套表重复支付渠道 / 组合支付 / 幂等逻辑，违反 DRY；未来新增收款类型还要加表 |
| 支付尝试多行 | 保留：失败终态 + 重试新行 | 在原行上改写 `status` / 金额 | 覆盖会丢失尝试历史，审计链断裂；与 DP-05「已生效单据是审计事实」同构 |
| 收款归属维度 | `payments` 增 `shift_id` / `operator_id`（只持 ID） | 通过 `order_id → orders` 间接取班次 / 人员 | 充值等无订单的收款拿不到归属；且「收款动作发生时的班次」与「开单时的班次」本就不同 |
| 退款归属维度 | `refunds` 自带 `shift_id` / `operator_id`，不继承订单 | 退款沿用原订单的 `shift_id` / `operator_id` | 退款常发生在另一班次、由另一人操作，继承会得到错误的归属与统计 |
| 报表粒度 | 本期**只落字段、不改汇总表粒度**；班次 / 人员统计从明细按需聚合 | 同步把 `daily_shop_summary` 改为按 `(shop_id, business_date, shift_id, operator_id)` 粒度 | 汇总表粒度变更是大改且与本议题正交；字段先落下，未来要报表时无需改表，只加查询 / 新汇总表（另立 ADR） |

## 决策

### 1. 概念修正：Payment 从「订单支付」正名为「收款」

`Payment` 是**独立聚合根**，表示「门店收到的一笔钱」，按 `kind` 区分来源：

| `kind` | 含义 | `order_id` | `member_id` | 说明 |
|---|---|---|---|---|
| `sale` | 销售收款（订单支付） | **必填**，受复合外键 `(shop_id, order_id) → orders(shop_id, id)` | 可空 | 可多次尝试；每次尝试一行，`failed` 终态 |
| `topup` | 会员充值收款 | **必须为空** | **必填** | 对应 F-03；与 `member_balances` / `balance_ledger` 同事务 |
| `other` | 其他门店收款 | 空 | 可空 | 本期不开放端点，只保留语义扩展位 |

数据库层用 CHECK 约束钉死组合，避免出现「有订单却算充值」的脏数据：

```sql
CONSTRAINT ck_payments_kind_ref CHECK (
  (kind = 'sale'  AND order_id IS NOT NULL) OR
  (kind = 'topup' AND order_id IS NULL  AND member_id IS NOT NULL) OR
  (kind = 'other' AND order_id IS NULL)
)
```

### 2. `payments` 修正 DDL（替换 `design/02` 现有定义）

```sql
CREATE TABLE payments (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  kind             text        NOT NULL DEFAULT 'sale',
  order_id         bigint,                       -- 由 NOT NULL 改为可空
  member_id        bigint,                       -- topup 必填（CHECK 约束保证）
  shift_id         bigint,                       -- 收款动作发生时所在班次，只持 ID
  operator_id      bigint      NOT NULL,          -- 收款操作人（登录账号），只持 ID
  status           text        NOT NULL DEFAULT 'pending',
  amount           numeric(14,2) NOT NULL,
  idempotency_key  text        NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_payments_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT uq_payments_idem       UNIQUE (shop_id, idempotency_key),
  CONSTRAINT fk_payments_order      FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_payments_status     CHECK (status IN ('pending','succeeded','failed')),
  CONSTRAINT ck_payments_kind       CHECK (kind IN ('sale','topup','other')),
  CONSTRAINT ck_payments_kind_ref   CHECK (
    (kind = 'sale'  AND order_id IS NOT NULL) OR
    (kind = 'topup' AND order_id IS NULL AND member_id IS NOT NULL) OR
    (kind = 'other' AND order_id IS NULL)
  )
);
CREATE INDEX idx_payments_shop_created   ON payments (shop_id, created_at DESC);
CREATE INDEX idx_payments_shop_shift     ON payments (shop_id, shift_id, created_at DESC);
CREATE INDEX idx_payments_shop_operator  ON payments (shop_id, operator_id, created_at DESC);
CREATE INDEX idx_payments_shop_order     ON payments (shop_id, order_id);
```

- `kind = 'sale'` 时复合外键仍把收款钉死在同店订单上（ADR-0003 不松动）。
- `shift_id` / `operator_id` 只持 ID、不加外键，与 `orders` 既有口径一致；`operator_id` 在 `orders` 是可空语义之外的必填，收款同样必填。
- 索引一律 `shop_id` 前导；班次 / 人员维度是为统计预留，本 ADR 不新增对应端点。

### 3. `refunds` 补全 DDL（原仅有表清单）

```sql
CREATE TABLE refunds (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  refund_no        text        NOT NULL,
  payment_id       bigint      NOT NULL,
  order_id         bigint      NOT NULL,
  shift_id         bigint,                       -- 退款动作发生时的班次（不继承订单）
  operator_id      bigint      NOT NULL,          -- 退款操作人（登录账号）
  amount           numeric(14,2) NOT NULL,
  status           text        NOT NULL DEFAULT 'pending',
  reason           text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_refunds_shop_no     UNIQUE (shop_id, refund_no),
  CONSTRAINT uq_refunds_public_id   UNIQUE (public_id),
  CONSTRAINT uq_refunds_shop_id_id  UNIQUE (shop_id, id),
  CONSTRAINT fk_refunds_payment     FOREIGN KEY (shop_id, payment_id)
              REFERENCES payments (shop_id, id),
  CONSTRAINT fk_refunds_order       FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_refunds_status      CHECK (status IN ('pending','succeeded','failed')),
  CONSTRAINT ck_refunds_amount      CHECK (amount > 0)
);
CREATE INDEX idx_refunds_shop_created  ON refunds (shop_id, created_at DESC);
CREATE INDEX idx_refunds_shop_shift    ON refunds (shop_id, shift_id, created_at DESC);
CREATE INDEX idx_refunds_shop_operator ON refunds (shop_id, operator_id, created_at DESC);
CREATE INDEX idx_refunds_shop_payment  ON refunds (shop_id, payment_id);
```

- 退款引用**已成功的支付**（`payments.status = 'succeeded'`，由用例层校验而非 CHECK）；复合外键保证同店。
- `order_id` 为冗余列（可由 `payment_id` 推导），保留用于明细回查与报表 JOIN；与 `payment_id` 必须指向同一订单，由用例层校验。
- `shift_id` / `operator_id` 是**退款动作本身**的归属，不继承原订单——这是本 ADR 的核心修正点。
- 「累计退款 ≤ 原支付额」是不变量：事务内对同一 `payment_id` 加锁后汇总既有 `succeeded` 退款额，超限返回 `409 CONFLICT`（DB 层无法用单条 CHECK 表达）。

### 4. `payment_entries` 补全（概念不变，仅补 DDL）

用户与既有契约一致认可「组合支付必须逐渠道记录」。补全 DDL，`kind` / 渠道字段沿用既有语义：

- 表：`payment_entries`，唯一 `(shop_id, payment_id, seq)`，复合外键 `(shop_id, payment_id) → payments(shop_id, id)`。
- 每笔记 `method`（`cash` / `card` / `balance` / `coupon`…）与 `amount`；合计等于 `payments.amount`，由计价纯函数保证。
- 禁止把组合支付压成字符串字段（`design/01` §6 原条款不变）。

### 5. 报表口径

- `daily_shop_summary` 粒度**不变**，仍为 `(shop_id, business_date)`；本 ADR 只在 `payments` / `refunds` 上落班次 / 人员字段，使数据可被聚合。
- 班次 / 人员维度的统计（交接对账、员工业绩）从上述明细按需聚合；若要固化为新的汇总表或端点，**另立 ADR**，不在本次扩大范围。
- 「业务日」口径不变：UTC+8 + 门店营业日切点；班次归属按动作发生时刻的 `shop_settings` 营业日判定，不按订单创建日。

### 6. 不引入的概念

不改 `orders` 的 `shift_id` / `operator_id` 语义；不引入订单 → 收款的中间关联表；不给 `shift_id` / `operator_id` 加外键或实体导航；不改变 `Payment` / `Refund` 的状态机与既有错误码；不在本次新增收款 / 退款端点或改汇总表粒度。以上任一需求须另立 ADR。

## 理由

1. **消除契约内部矛盾**：`payments.order_id NOT NULL` 与 F-03 充值写 payments 无法并存；用可空 `order_id` + `kind` 让「销售收款」与「会员充值」各得其所，且不伪造订单、不污染单号序列。
2. **归属维度随动作走**：收款 / 退款是独立动作，其班次与操作人应当记录「动作发生时」的值；退款跨班次是常态，继承订单归属会得到错误的统计与审计结果。
3. **保留正确的聚合边界**：`Payment` / `Refund` 仍是独立聚合根，`payment → order` 方向不变；多次支付尝试与组合支付得以保留，不用为「一单多付」引入无意义的中间表。
4. **零落地成本**：订单域尚未落地，本次修正只动设计文档，不产生废弃迁移、不改 `EXPECTED_SCHEMA_VERSION`、不触碰任何已交付代码，是当前代价最小的修正窗口。
5. **为报表留数据、不提前扩大范围**：字段先落下，未来做班次 / 人员报表时无需改表；报表粒度与端点作为独立议题另行决策。

## 后果与限制

- 本 ADR 已同步修订 `design/01`（`Payment` / `Refund` 字段与不变量）、`design/02`（替换 `payments` DDL、补全 `refunds` / `payment_entries` / `price_adjustments`）、`design/03`（充值 / 退款端点归属）、`design/06`（F-01 阶段 D、F-02、F-03 事务步骤），并在 `CHANGELOG.md` 与 `design/00` 变更规则中登记为**第五次受控扩展（R3 契约修正）**。
- `design/02` 的 `payments` 示例 DDL 被本 ADR 替换；任何已基于旧定义写的代码（当前应无）须同步调整。
- 修正 `ck_payments_kind_ref` 后，`kind='sale'` 的充值旧写法被禁用；F-03 充值必须改用 `kind='topup'` 且不写 `order_id`。
- `refunds` 的 `order_id` 与 `payment_id` 一致性由用例层保证，数据库层不做跨表 CHECK；集成测试须覆盖「退款引用他单支付」被拒。
- 累计退款上限在事务内以行锁 + 汇总实现，性能随单笔支付的退款次数线性增长；高频部分退款场景需评估（复审条件之一）。
- **公开仓库脱钩约束不变**：只用 `payment` / `refund` / `shift` / `operator` 等通用词，不出现任何内部项目名或行业专属词。

## 复审条件

- 需要「订单 → 收款」的显式多对多关联（如一笔收款覆盖多张订单）；
- 需要把班次 / 人员维度的统计固化为汇总表或新增报表端点；
- 需要给 `shift_id` / `operator_id` 加外键约束或提供实体导航（即改变「只持 ID」口径）；
- 高频部分退款使事务内汇总成为瓶颈，需要数据库层的累计额约束或独立汇总列；
- 需要 `other` 类收款的实际端点与渠道定义；
- 上游 ADR-0002 / 0003 / 0006 / 0008 任一被修订且影响本 ADR 的隔离、归属或扩展规则。

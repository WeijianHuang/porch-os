# ADR-0014: 订单金额的派生模型与退款渠道明细

- 状态：已接受（2026-11-20）
- 决策问题：在 ADR-0013 接受之后，对 R3 订单域剩余五处契约缺口定案——① `Order` 状态机的 `refunding` 是事务外不可观测的冗余态，且缺 `partially_refunded` 再次退款与失败回退的路径；② `orders.paid_amount` 与支付事实双写必然漂移，「实付 / 已退 / 可退」应由不可变记录派生；③ 退款未记录渠道分摊，无法回答「退到哪个渠道、多少」，且系统不应自行决定超额部分如何处理；④ 单号日期用自然日，与 `business_date`（UTC+8 + 门店营业日切点）口径分裂；⑤ `design/02` 示例 DDL 中的平台表与已落地的 `00001` 迁移字段不一致。确定修正方案，使 R3 实现有一致、可追溯的契约来源。
- 影响范围：修订 `design/01`（状态机、`Order` 金额字段、不变量清单、值对象口径）、`design/02`（`orders` DDL、`refund_entries` 新表、单号 / 业务日函数、平台表示例 DDL 对齐）、`design/03`（退款端点请求体与金额响应）、`design/06`（F-01 阶段 C/D、F-02 重写）；新增迁移（订单域首批）；**不改** `EXPECTED_SCHEMA_VERSION`（订单域未落地，本次只改设计、不加迁移）、不改任何已落地代码、不改 ADR-0013 结论
- 上游：[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图、金额与计价纯函数在 domain）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0004](0004-scope-baseline.md)（单币种 CNY、基准 UTC+8、不归档）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（设计冻结后先改设计再改代码）、[ADR-0013](0013-payment-and-refund-ownership.md)（收款 / 退款的 `kind` 与归属维度）

## 背景

ADR-0013 已把收款正名为按 `kind` 区分的独立聚合，并给 `payments` / `refunds` 补上 `shift_id` / `operator_id` 与完整 DDL。订单域的契约由此完整，但**完整不等于自洽**。在准备落地实现前逐条复核，发现五处会在编码时直接暴露的矛盾：

1. **退款状态机是断的**：`design/01` §5.1 的状态图上 `partially_refunded` 没有出边，但 §5.2 与 F-02 又明确「部分退款后仍可再次退款」；`Refund` 聚合有 `failed` 终态，可 `Order` 一旦进入 `refunding`，失败后如何回退没有任何定义——`CanTransition` 纯函数会把这些合法迁移判为非法，退款失败即成死状态。
2. **金额字段双写**：`orders.paid_amount` 在支付时写入，但退款后是否递减没有定义。它是「历史累计实付」还是「当前净实付」？两种口径都会与 `payments` / `refunds` 的不可变记录产生漂移，且让「已生效事实可被改写」，与 DP-05 冲突。
3. **退款没有渠道维度**：支付端强制组合支付逐渠道记录（`payment_entries`），退款端只有一个 `refunds.amount`，无法回答「这次退的是现金还是余额、各多少」。更关键的是，**「某渠道超出可退额时怎么办」是业务政策**（要与客户协商，或由前端引导重新选择），系统不应自行决定自动改派或按比例分摊。
4. **单号日期口径分裂**：`design/02` 的 `order_no` 用 `to_char(now() AT TIME ZONE 'Asia/Shanghai', 'YYYYMMDD')` 取自然日，而 `business_date` 是 UTC+8 **叠加门店营业日切点**（ADR-0004）。门店若设早 6 点切日，凌晨 3 点的单子业务日归前一天、单号却是当天，`design/02` §401「单号日期与 business_date 一致」的测试必然失败。
5. **示例 DDL 与迁移漂移**：`design/02` 末尾示例块里的 `idempotency_keys` / `outbox_events` 字段与已落地的 `00001` 不一致（详见决策 4）。照示例写代码会引用不存在的列。

按 ADR-0008 的冻结后变更规则，这些必须先有 ADR 与设计来源。本 ADR 即该来源，**是修正而非推翻**：不改动 ADR-0001–0013 的任何条款，只在尚未落地的 R3 订单域里消除矛盾、明确口径。

### 理解澄清（本轮最容易被误读的四处）

| 说法 | 准确含义 |
|---|---|
| 「删掉 `paid_amount`，订单是不是就没有金额了」 | **不是**。应付快照 `total_amount`（= Σ明细 − Σ调整）与 `discount_amount` 保留且不可变；只是「实付 / 已退 / 可退」三个**派生量**不落库，从不可变记录实时计算 |
| 「去掉 `refunding` 是不是不支持『退款中』查询」 | **不是**。退款在单个数据库事务内完成，事务外只能观测到 `succeeded` / `failed`，不存在稳定的「退款中」订单态；失败尝试以 `refunds.status='failed'` 留审计行 |
| 「前端决定退款渠道，是不是系统完全不校验」 | **不是**。系统仍校验渠道合法性、单渠道累计退款不超该渠道原支付额、渠道合计等于退款额；只是**不自动生成分摊方案**、不决定超额部分如何处理——那是业务政策 |
| 「每店自己的单号规则，是不是要加门店级时区」 | **不是**。基准时区仍固定 UTC+8（ADR-0004 不可按店覆盖）；门店可配的只是**营业日切点偏移**（`shop_settings`），且单号序号仍由数据库序列提供，不靠应用层拼接日期与门店 ID |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 退款中间态 | 删除 `refunding`；`paid`/`completed`/`partially_refunded` 可直接转 `refunded`/`partially_refunded`，后者可再转 `refunded` | 保留 `refunding` 并补「失败回退」边 | `refunding` 复制了 `Refund` 自身的状态，事务外不可观测；失败靠事务回滚即可，回退边让 `CanTransition` 无谓复杂且易漏 |
| 实付 / 可退金额 | 从 `payments` / `refunds` 的 `succeeded` 行派生 | 保留 `orders.paid_amount`（及新增 `refundable_amount`），退款时递减 | 订单字段与支付 / 退款记录双写必然漂移；「已生效事实被改写」违反 DP-05，报表与对账要处理被改过的历史 |
| 退款渠道 | 新增 `refund_entries`，退款请求**显式携带**渠道明细，系统只校验 | 后端用 `Allocate` 按原支付比例自动分摊 | 「超额退哪个渠道、按什么比例」是需与客户协商的业务政策，系统自行决定等于替业务做选择；且分摊规则会随业态变化，不属通用底座 |
| 单号日期 | 跟随 `business_date`（UTC+8 + 门店切点），序号仍走数据库序列 | 保持自然日 `to_char(now()...)` | 跨营业日切点后单号日期与日报 / 班次 / 汇总口径分裂，破坏「口径唯一」 |
| 时间字段类型 | 保留 `timestamptz`；session 固定 UTC；业务日显式 `AT TIME ZONE` | 改为 `timestamp without time zone` 存 UTC | 丢失类型层面的时区语义，读取方无法从类型判断是 UTC 还是本地时间，未来跨时区还会踩 DST；用户已确认本项 |
| 平台表示例 DDL | 改 `design/02` 示例块，与 `00001` 迁移对齐 | 改 `00001` 迁移，与示例对齐 | 迁移已落地且 R1 代码依赖其字段（`state` / `dispatched_at` 等），改迁移成本高且收益为负 |

## 决策

### 1. 订单状态机：删除 `refunding`，补齐退款路径

```text
draft ──► open ──► pending_payment ──► paid ──► completed
  │         │            │               │          │
  │         │            │               ├──────────┤
  │         │            │               ▼          ▼
  │         │            │      refunded / partially_refunded
  │         │            │               ▲          ▲
  └─────────┴────────────┴──────► cancelled└──────────┘
                                   partially_refunded ──► refunded（累计退完）
```

`ck_orders_status` 改为：

```sql
CONSTRAINT ck_orders_status CHECK (status IN
  ('draft','open','pending_payment','paid','completed',
   'cancelled','refunded','partially_refunded'))
```

- `paid` / `completed` / `partially_refunded` 均可发起退款（需有可退余额）；全额退完 → `refunded`，部分退 → `partially_refunded`。
- `partially_refunded → refunded`：后续退款把余额退完时迁移，`CanTransition` 必须显式包含此边。
- **无 `refunding` 态**：退款在单事务内完成，失败即整体回滚，`Order` 状态保持原值不变，无需回退迁移。`Refund` 聚合自身的 `pending → succeeded / failed` 不变（ADR-0013 §5.2），失败的退款以 `status='failed'` 留审计行。
- DP-05 不变：`paid` 及之后的状态不回退到 `open`；更正错误单据走「退款 + 新单」。

### 2. 金额派生：`orders` 删除 `paid_amount`

修正后的 `orders` DDL（相对 `design/02` 现有定义的差异：去掉 `paid_amount`；`order_no` 改为显式生成；`status` CHECK 去掉 `refunding`）：

```sql
CREATE TABLE orders (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL REFERENCES shops(id),
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  -- 单号由 next_order_no(shop_id) 在 INSERT 时显式生成，见决策 3
  order_no         text        NOT NULL,
  status           text        NOT NULL DEFAULT 'draft',
  member_id        bigint      REFERENCES members(id),
  shift_id         bigint,
  operator_id      bigint      NOT NULL,
  -- 应付快照 = Σ(order_lines.amount) − Σ(price_adjustments.amount)；不可变
  total_amount     numeric(14,2) NOT NULL DEFAULT 0,
  discount_amount  numeric(14,2) NOT NULL DEFAULT 0,
  version          integer     NOT NULL DEFAULT 0,
  deleted_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_orders_shop_no    UNIQUE (shop_id, order_no),
  CONSTRAINT uq_orders_public_id  UNIQUE (public_id),
  CONSTRAINT uq_orders_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT ck_orders_status     CHECK (status IN
              ('draft','open','pending_payment','paid','completed',
               'cancelled','refunded','partially_refunded')),
  CONSTRAINT ck_orders_amounts    CHECK (total_amount >= 0 AND discount_amount >= 0)
);
```

三个派生量由用例层从**不可变记录**计算，不落库、不缓存为真相来源：

| 派生量 | 计算口径 |
|---|---|
| `paid_amount`（实付） | `SUM(payments.amount)` where `order_id = ? AND kind = 'sale' AND status = 'succeeded'` |
| `refunded_amount`（已退） | `SUM(refunds.amount)` where `order_id = ? AND status = 'succeeded'` |
| `refundable_amount`（可退） | `paid_amount − refunded_amount` |

- `payments` / `payment_entries` / `refunds` / `refund_entries` 一旦写入**不可修改、不可删除**；重试支付或再次退款都是新增行，构成审计链（与 ADR-0013「失败终态、重试新行」同构）。
- `Order` 的 `refunded` / `partially_refunded` 仅作**粗粒度筛选标记**，由退款事务更新；金额真相永远以记录聚合为准，二者不一致时以记录为准并视为缺陷。
- F-01 阶段 D 第 g 步改为「更新 `orders.status` / `version`（**不写 `paid_amount`**）」。

### 3. 退款渠道明细：新增 `refund_entries`

```sql
CREATE TABLE refund_entries (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint        NOT NULL,
  refund_id   bigint        NOT NULL,
  seq         integer       NOT NULL,
  method      text          NOT NULL,
  amount      numeric(14,2) NOT NULL,
  created_at  timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT uq_refund_entries_shop_refund_seq UNIQUE (shop_id, refund_id, seq),
  CONSTRAINT fk_refund_entries_refund FOREIGN KEY (shop_id, refund_id)
              REFERENCES refunds (shop_id, id),
  CONSTRAINT ck_refund_entries_method CHECK (method IN ('cash','card','balance','coupon')),
  CONSTRAINT ck_refund_entries_amount CHECK (amount > 0)
);
CREATE INDEX idx_refund_entries_shop_refund ON refund_entries (shop_id, refund_id);
```

- 退款端点请求体必须显式携带 `refund_entries`（`method` + `amount` 列表）；`SUM(refund_entries.amount)` 必须等于 `refunds.amount`，由用例层校验。
- **系统只校验、不决定**：对同一 `(payment_id, method)` 加锁，汇总既有 `succeeded` 退款的该渠道累计额，要求 `渠道累计退款 ≤ 该渠道原支付额`；渠道必须出现在原支付的 `payment_entries` 中。超限 → `409 CONFLICT`，由前端 / 客户协商后重新提交，**不做自动改派或按比例分摊**。
- 「退余额」的反向流水与会员账回滚仍按 ADR-0013 / F-02 在同事务内完成；新增的只是渠道归属记录与校验。
- 索引沿用 `shop_id` 前导；复合外键把渠道明细钉死在同店退款单上（ADR-0003 不松动）。

### 4. 单号与业务日：统一走 `business_date`，示例 DDL 对齐

新增（或明确）统一业务日函数，作为单号、日报、班次、导出的唯一口径：

```sql
-- 基准 UTC+8，叠加门店 shop_settings 的营业日切点偏移（默认 00:00）；
-- 不依赖 session 时区，不接受门店覆盖基准时区（ADR-0004）
business_date(p_at timestamptz, p_shop_id bigint) RETURNS date;

-- 单号：日期部分取 business_date，序号由数据库序列提供（并发唯一）；
-- 在 INSERT 时显式调用，order_no 不再设字面 DEFAULT
next_order_no(p_shop_id bigint) RETURNS text;
```

- `next_order_no` = `'SO' || to_char(business_date(now(), shop_id),'YYYYMMDD') || '-' || lpad(nextval('order_no_seq')::text, 10, '0')`；号段唯一性仍由序列保证，门店切点只影响日期部分。
- **时间口径**：所有连接把 session `TimeZone` 固定为 `UTC`（连接初始化执行 `SET TIME ZONE 'UTC'`）；所有时间列保持 `timestamptz`（内部按 UTC 存储）；面向业务日的计算一律显式 `AT TIME ZONE 'Asia/Shanghai'` + 门店切点，不依赖 session 默认；前端拿到 UTC 时间按展示地时区渲染。不采用无时区 `timestamp`（理由见备选方案）。
- **`design/02` 平台表示例 DDL 与 `00001` 迁移对齐**（改示例，不改迁移）：
  - `idempotency_keys`：`status` → `state`、`response_code` → `response_status`、`expires_at` 用 `DEFAULT (now() + interval '24 hours')`、补 `created_at` / `completed_at` / `ck_idem_state` 与 `idx_idem_expiry` 部分索引；
  - `outbox_events`：`shop_id` 可空、`aggregate_id` 用 `text`、`occurred_at` → `created_at`、`published_at` → `dispatched_at`、索引改 `idx_outbox_pending ON outbox_events (created_at) WHERE dispatched_at IS NULL`。

### 5. 报表与范围

- `daily_shop_summary` 粒度**不变**，仍为 `(shop_id, business_date)`；本 ADR 不新增班次 / 人员维度汇总。补 `shift_id` / `operator_id` 的目的只是为后续「班次域 / 对账域」留数据，那部分另立 ADR。
- 不改 `orders.shift_id` / `operator_id` 语义；不给它们加外键或实体导航；不改 `Payment` / `Refund` 的 `kind` 与归属维度（ADR-0013 全部保留）。
- 不引入 `coupon` / `card` 渠道的独立实体或核销逻辑——它们目前只是 `method` 枚举值，本期不接受需外部对账的渠道入账。

## 理由

1. **让状态机闭合**：删掉不可观测的中间态、补齐「部分退款再退」路径后，`CanTransition` 的每个合法业务动作都有对应边，失败靠事务回滚天然收敛，不需要维护脆弱的回退逻辑。
2. **金额只存一次真相**：订单只保留不可变的应付快照，实付 / 已退 / 可退从不可变记录派生，从根上消除双写漂移，也让「已生效事实不被改写」在金额域成立。
3. **把业务政策留给业务**：退款渠道由请求方显式给出、系统只校验边界，既保住数据正确性，又不把「超额怎么退」这种需与客户协商的规则写死在通用底座里。
4. **口径唯一**：单号日期与日报 / 班次 / 汇总共用 `business_date`，跨营业日切点不再分裂；时间字段保持 `timestamptz` 并固定 session UTC，既满足「库内 UTC」又保留类型语义。
5. **零落地成本**：订单域尚未落地，本次修正只动设计文档，不产生废弃迁移、不改 `EXPECTED_SCHEMA_VERSION`、不触碰任何已交付代码，是当前代价最小的修正窗口。

## 后果与限制

- 本 ADR 已同步修订 `design/01`（状态机、金额字段、不变量清单、值对象口径）、`design/02`（`orders` DDL、`refund_entries`、`business_date` / `next_order_no`、平台表示例 DDL）、`design/03`（退款请求体与金额响应、单号口径注记）、`design/06`（F-01 阶段 C/D、F-02），并在 `CHANGELOG.md` 与 `design/00` 变更规则中登记为**第六次受控扩展（R3 契约修正）**。
- `orders.paid_amount` 被删除；任何已基于旧定义写的代码（当前应无）须同步调整。订单详情响应中的实付 / 已退 / 可退改为派生字段。
- `order_no` 不再有字面 DEFAULT；所有 `INSERT orders` 必须显式调用 `next_order_no(shop_id)`，或在用例层用同一函数生成，遗漏将触发 `NOT NULL` 约束错误。
- session `TimeZone='UTC'` 必须在连接初始化处落地；若某处连接绕过该初始化，显式 `AT TIME ZONE 'Asia/Shanghai'` 仍保证业务日正确，但时间展示可能因 session 不同而变化——集成测试须覆盖。
- 退款端点的请求体形状变化（新增必填 `refund_entries`）；前端需先展示原支付的渠道分布，再由用户 / 客户选择退款渠道与金额。
- 派生金额使订单详情多出对 `payments` / `refunds` 的聚合查询；可按需缓存响应，但**不得把派生结果当作可持久化的真相字段**。
- 退款校验对同一 `(payment_id, method)` 加行锁并汇总，高频部分退款存在串行化风险（复审条件之一）。
- **公开仓库脱钩约束不变**：只用 `refund_entry` / `method` / `business_date` 等通用词，不出现任何内部项目名或行业专属词。

## 复审条件

- 需要「退款中」状态可被跨请求观测（例如接入异步退款渠道）→ 需重新评估显式状态 + 事件驱动方案；
- 性能要求把实付 / 可退固化为数据库汇总列 → 需重新设计一致性方案（视图 / 物化 / 触发器）并给出防漂移证明；
- 退款分摊改为后端自动决定 → 属业务政策变更，须与客户确认后另立 ADR；
- 门店需要按店时区而非仅营业日切点偏移 → 冲击 ADR-0004 的基准时区约定，须先修订该 ADR；
- 需要 `coupon` / `card` 等渠道的实体、发放与核销 → 另立 ADR；
- 上游 ADR-0002 / 0003 / 0004 / 0008 / 0013 任一被修订且影响本 ADR 的金额、隔离或时间口径。

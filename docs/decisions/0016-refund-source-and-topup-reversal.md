# ADR-0016: 退款来源与充值逆向

- 状态：已接受（2026-11-21）
- 编号说明：ADR 序号取 **0016**——0015 已由 [ADR-0015](0015-staff-state-and-position-controls.md)（服务人员状态 / 轮转 / 位次人工控制）占用；本 ADR 属订单域契约修正，顺延取 0016
- 决策问题：ADR-0013 已让 `payments.order_id` 可空、以 `kind` 区分销售 / 充值，但退款侧仍是「所有退款都属于订单」的旧模型——`refunds.order_id NOT NULL`、无 `kind`、无 `member_id`，充值（topup）退款既建不了单、也无端点入口。本 ADR 定案四件事：① 让 `refunds` 以 `kind` 对齐来源（sale / topup）、`order_id` 可空、补 `member_id` 与 CHECK 约束，使充值退款可表达；② 定义充值退款的端点、单事务资金流与余额硬约束（余额不足**不允许退**）；③ 写死**禁止跨店退款**；④ 把 `coupon` 从支付 / 退款渠道中剥离——**券是折扣不是支付**，一律归入 `price_adjustments`，退款只涉及实际支付部分。使 R3 实现有一致、可追溯的契约来源。
- 影响范围：修订 `design/01`（`Refund` 聚合字段与不变量、派生金额口径的 sale 过滤）、`design/02`（`refunds` DDL 加 `kind` / 可空 `order_id` / `member_id` + CHECK、`refund_entries` 校验适配、表清单）、`design/03`（新增充值退款端点、订单详情派生口径注记）、`design/06`（F-03 加充值退款流程、F-02 的 sale 锚点说明）；新增迁移（订单域首批，与 ADR-0013/0014 合并落地）；**不改** `EXPECTED_SCHEMA_VERSION`（订单域未落地，本次只改设计、不加迁移）、不改任何已落地代码、不改 ADR-0013/0014 结论
- 上游：[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图、金额与计价纯函数在 domain）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0004](0004-scope-baseline.md)（单币种 CNY、基准 UTC+8、余额非负）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（设计冻结后先改设计再改代码）、[ADR-0013](0013-payment-and-refund-ownership.md)（收款 / 退款的 `kind` 与归属维度）、[ADR-0014](0014-derived-amounts-and-refund-entries.md)（金额派生模型与 `refund_entries`）

## 背景

ADR-0013 修正了收款侧：「会员充值也写 payments」与 `payments.order_id NOT NULL` 的矛盾，靠 `payments.order_id` 可空 + `kind`（`sale` / `topup` / `other`）+ CHECK 约束消解。ADR-0014 又补了 `refund_entries`、删了 `paid_amount`、闭合了状态机。**但退款侧仍停留在「退款必属订单」的旧模型**，复核发现三处会在编码时暴露的问题：

1. **充值退款无处安放**：`refunds.order_id bigint NOT NULL`（`design/02`）使退款单必须挂订单，而 topup 收款 `order_id IS NULL`（ADR-0013）；顾客充值后撤销 / 退错充值，建不了退款单。
2. **无会员锚点**：`refunds` 没有 `member_id`，即使放开 `order_id`，充值退款也不知退给谁、无法扣回该店的 `member_balances`；而充值退款的资金流恰恰是「扣回会员余额 + 退回原支付渠道」。
3. **券被当成支付渠道**：`payment_entries` / `refund_entries` 的 CHECK 放行 `cash/card/balance/coupon` 四值，把 `coupon` 与真金白银并列。**券本质是折扣不是支付**——它减少应收，不发生资金转移，应进 `price_adjustments`；把它放进支付渠道会让「实付」虚高、退款时还要处理「退券」这种不存在的资金流。本期无任何卡券实体 / 发放 / 核销规划（ADR-0014 已明确），`coupon` 更不该占着支付枚举位。

这是 ADR-0013 那个矛盾的**同构重演**：收款侧已经承认「订单外收款」，退款侧必须对称跟上，否则模型一半通一半断。按 ADR-0008 的冻结后变更规则，这些必须先有 ADR 与设计来源。本 ADR 即该来源，**是修正而非推翻**：不改动 ADR-0001–0015 的任何条款。

### 理解澄清（本轮最容易被误读的七处）

| 说法 | 准确含义 |
|---|---|
| 「充值退款是不是让会员从余额里提现」 | **不是**。退的是**原充值支付的渠道**（现金 / 卡等，由 `refund_entries.method` 记录），会员余额只是被**同步扣回**以保持账实相符；不提供「余额 → 现金」的提现语义，也不把余额当作退款来源 |
| 「加了 `member_id`，订单退款也要填吗」 | **不填**。`kind='sale'` 的退款 `member_id` 为空，会员信息从订单 / 原支付推导；只有 `kind='topup'` 必填 `member_id`（退给谁）。由 CHECK 约束钉死，杜绝两种来源混填 |
| 「余额已经花掉了，还能退整笔充值吗」 | **不允许退**。系统要求「该会员本店当前余额 ≥ 本次退款额」，**余额不足直接拒绝整笔退款**（`409 INSUFFICIENT_BALANCE`），不做部分退、不交业务协商、不自作主张改退款额，也不把余额写成负数（`ck_member_balances_nonneg` 不松动）。顾客要退只能先消费到余额充足，或由业务走线下处理——系统只守账实相符这条线 |
| 「能给 refunds 加 `kind` 是不是推翻 ADR-0013 的收款模型」 | **不是**。`payments.kind` 完全不动；`refunds.kind` 只是让退款侧对称表达「这笔退的是销售款还是充值款」，且退款的 `kind` 必须等于原支付 `payments.kind`（由引用 + CHECK 保证一致） |
| 「订单详情的『已退』会不会把充值退款算进去」 | **不会**。ADR-0014 的订单已退口径锚在 `order_id = ?`，而 topup 退款 `order_id IS NULL`，天然排除；本 ADR 额外要求该聚合显式过滤 `kind='sale'`，让口径在 SQL 上自解释 |
| 「充值在 A 店、能去 B 店退吗」 | **不允许**。退款单的 `shop_id` 必须与原支付、原会员本店余额同店：端点路径带 `{shopId}`、`payment_id` 由 `(shop_id, id)` 复合外键钉死、会员账只动本店 `(shop_id, member_id)`。跨店退款 = 跨店资金转移，冲击 ADR-0003 单层隔离与「不做自动清分」，**本期明确禁止、不留扩展口** |
| 「券（coupon）是不是一种支付方式、退款要退券吗」 | **不是**。券是**折扣**：它减少应收、不发生资金转移，一律记入 `price_adjustments`（按明细分摊），**不得进 `payment_entries` / `refund_entries`**。退款只涉及**实际支付部分**（cash / balance / card 等真实资金渠道），券优惠部分天然不在可退额内——充值的钱和券优惠是两回事 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 充值退款建模 | `refunds` 加 `kind` + `order_id` 可空 + `member_id` | 新建独立 `topup_refunds` 表 | 两套退款表会让「退款」查询 / 对账 / 状态机分裂成两处，且重复 `refund_no` / 幂等 / 渠道逻辑；单表加鉴别列是同构数据的标准做法 |
| 退款来源对齐 | `refunds.kind` 必须等于原 `payments.kind`（CHECK 钉死组合） | 退款不记来源，靠 `payment_id` 联表推导 | 推导可行但每次都要 join 才能筛选 / 对账；鉴别列冗余一点，换来自解释的查询与约束，与 `payments.kind` 对称 |
| 充值退款端点 | `POST .../members/{id}/topups/{paymentId}/refunds`（会员维度专用） | 通用 `POST .../payments/{paymentId}/refunds` | 通用端点会让 sale 退款出现两个入口（订单维度 + 支付维度），权责与权限点难界定；专用端点语义单一，只处理 topup |
| 订单退款的会员锚点 | `sale` 退款 `member_id` 留空，从订单 / 原支付推导 | `sale` 退款也冗余填 `member_id` | 与订单字段双写，可能不一致；订单本身已持 `member_id`，无理由复制 |
| 余额不足的处理 | 条件更新 `WHERE balance >= $a`，不足 → `409 INSUFFICIENT_BALANCE` **整笔拒绝**，不做部分退 | 允许部分退（退到余额上限）或允许负余额 | 部分退需业务协商退款额、突破「请求多少退多少」的契约且引入新的分摊口径；负余额打破 `ck_member_balances_nonneg` 与单店账不失真。用户明确：余额不足就不允许退充值 |
| 跨店退款 | 退款强绑原支付 / 原会员账的 `shop_id`，**本期禁止跨店** | 允许 A 店充值 B 店退，或留扩展口 | 跨店退款 = 跨店资金转移，与 ADR-0003 单层隔离、「不做自动清分」直接冲突；用户明确禁止，不留悬置选项 |
| 券（coupon）的定位 | 从支付 / 退款 `method` 中**移除 `coupon`**；券一律进 `price_adjustments`，退款只涉及实际支付部分 | 保留 `coupon` 为支付枚举占位，本期 API 拦截 | 占位会让「实付」口径虚高、退款要处理不存在的「退券」资金流；本期本无卡券实体 / 核销规划（ADR-0014 已明确），直接移除最干净，将来真做卡券时另立 ADR 再定义它与支付的关系 |

## 决策

### 1. `refunds` 按来源区分：加 `kind`、`order_id` 可空、补 `member_id`

修正后的 `refunds` DDL（相对 `design/02` 现有定义的差异：新增 `kind`；`order_id` 改为可空；新增 `member_id`；新增 `ck_refunds_kind` 与 `ck_refunds_kind_ref`；`refund_no` 仍店内唯一）：

```sql
CREATE TABLE refunds (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  refund_no        text        NOT NULL,
  kind             text        NOT NULL,             -- sale / topup，等于原 payments.kind
  payment_id       bigint      NOT NULL,             -- 必引用原收款（同店复合外键）
  order_id         bigint,                           -- 由 NOT NULL 改为可空：topup 退款不挂订单
  member_id        bigint,                           -- topup 退款必填：退给哪个会员
  shift_id         bigint,                           -- 退款动作发生时的班次（不继承订单）
  operator_id      bigint      NOT NULL,             -- 退款操作人（登录账号）
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
  CONSTRAINT ck_refunds_amount      CHECK (amount > 0),
  CONSTRAINT ck_refunds_kind        CHECK (kind IN ('sale','topup')),
  CONSTRAINT ck_refunds_kind_ref    CHECK (
    (kind = 'sale'  AND order_id IS NOT NULL AND member_id IS NULL) OR
    (kind = 'topup' AND order_id IS NULL AND member_id IS NOT NULL)
  )
);
```

- `kind` 自解释「退的是什么」；`payment_id` 仍是必填的锚点——两种退款都退某一笔原收款，由 ADR-0013 的复合外键钉死同店。
- `fk_refunds_order` 在 `order_id IS NULL` 时不生效（PG `MATCH SIMPLE` 语义），topup 退款不受约束；`member_id` 只持全局会员档案 ID，与 `orders.member_id` 一致，不加 `shop_id` 外键——按店余额由 `(shop_id, member_id)` 定位，不由本字段承载隔离。
- `refunds.kind` 必须等于原 `payments.kind`：由 API 层从 `payment_id` 查原支付写入、用例层断言一致，数据库辅以 CHECK 约束组合合法性（跨表相等不做数据库外键，沿用「只持 ID」惯例）。
- 索引沿用 `shop_id` 前导；补 `CREATE INDEX idx_refunds_shop_member ON refunds (shop_id, member_id, created_at DESC)` 支撑会员维度的可退额聚合。

### 2. 充值可退额：从不可变记录派生，不落库

与 ADR-0014 的金额派生哲学一致，充值侧的三个量也实时计算、不落库：

| 派生量 | 计算口径 |
|---|---|
| 某笔充值已入账额 | `payments.amount`（该 `payment_id`，`kind='topup'`、`status='succeeded'`） |
| 已退额 | `SUM(refunds.amount)` where `payment_id = ? AND kind='topup' AND status='succeeded'` |
| 可退额 | 入账额 − 已退额（≥ 0，由累计校验保证） |

- 退款校验双闸：① 累计退款 ≤ 原充值额（与 sale 同构）；② 该会员本店当前 `member_balances.balance ≥ 本次退款额`——后者用条件更新实现，**不足 → `409 INSUFFICIENT_BALANCE` 整笔拒绝，不做部分退**（用户定案）。余额已消费的充值无法退，系统不替业务承担这笔坏账，也不生成负余额。
- **订单侧的「已退」派生口径不变但要显式过滤**：`SUM(refunds.amount) where order_id = ? AND kind='sale' AND status='succeeded'`（ADR-0014 的 `order_id=?` 已隐含排除 topup，加 `kind` 使 SQL 自解释）。
- `payments` / `refunds` / `payment_entries` / `refund_entries` 仍只追加、不改写、不删除；充值退款是一条新 `refunds` 行 + 一条反向 `balance_ledger` 行，不冲销原充值记录。

### 3. 充值退款端点与资金流

新增端点（member 维度专用，权限与充值同源 `member.topup.write`）：

```text
POST /api/v1/shops/{shopId}/members/{memberId}/topups/{paymentId}/refunds
请求体：{ amount, reason, refund_entries: [{ method, amount }] }
响应：退款单 public_id + 派生可退额
```

前置校验：`paymentId` 对应收款必须 `kind='topup'`、`status='succeeded'`、属于本店且 `member_id = path.memberId`；请求方可见门店集合含 `shopId`（ADR-0003/0006）；写操作带 `Idempotency-Key`。**跨店退款一律拒绝**：端点只认路径中的 `{shopId}`，原支付与会员余额都由复合外键 / 复合主键钉死在该店，A 店充值不能在 B 店退（本期禁止，无例外）。

**单事务内的顺序**：

1. 对原 `payment_id` 加锁 → 汇总 succeeded 退款额，校验 `累计 + 本次 ≤ 原充值额`。
2. 对涉及的每个 `(payment_id, method)` 加锁 → 校验 `refund_entries` 各 `method` 出现在原充值 `payment_entries` 中，且该渠道累计退款 ≤ 该渠道原入账额；`Σ refund_entries.amount = amount`。超限 → `409`（业务协商，**不自动分摊**）。
3. `INSERT refunds(kind='topup', payment_id, member_id, order_id=NULL, shift_id, operator_id, amount, reason)` + `INSERT refund_entries`。
4. 会员账扣回：条件更新 `member_balances SET balance = balance - $a, version = version + 1 WHERE shop_id=$1 AND member_id=$2 AND balance >= $a`（影响 0 行 → `409 INSUFFICIENT_BALANCE`，**整笔拒绝、不做部分退**，此前步骤在同一事务内一并回滚）。
5. 写反向 `balance_ledger(delta = -$a, ref_type='refund', ref_id=<refund.public_id>)`（只追加）。
6. 写 `refund.succeeded` 与 `shop.summary_dirty` 事件、写幂等记录（同事务）；事务外由 worker 派发并按 `(shop_id, business_date)` 重算日报。
7. 失败分支与 F-02 一致：状态 / 幂等冲突 `409` / `412`，余额不足 `409 INSUFFICIENT_BALANCE`（整笔拒绝），超时先查幂等结果；任意步骤异常整体回滚，会员余额与流水不留半截。

### 4. 券不是支付：从 `method` 剥离，退款只涉及实际支付部分

**原则**：支付方式记录的是**真实资金转移**，券（coupon）不是资金转移而是**应收减免**，属于折扣。因此：

- **`coupon` 从 `payment_entries.method` 与 `refund_entries.method` 的 CHECK 中移除**，两个 CHECK 统一收窄为 `('cash','balance','card')`。本期无任何卡券实体、发放或核销规划（ADR-0014 已明确），`card` 作为真实支付渠道保留枚举位——但不代表本期已接入，实际可用渠道以 API 层白名单为准。
- **券优惠一律记入 `price_adjustments`**：折扣 / 抹零 / 优惠都作为整单调整、按明细分摊（`Allocate` 纯函数），不产生 `payment_entries` 行。订单应付 `total_amount = Σ明细 − Σ调整` 已经包含券的影响，无需任何"券支付"记录。
- **退款只退实际支付部分**：可退额 = Σ(succeeded 实际支付) − Σ(succeeded 退款)，券优惠部分从未作为支付入账，自然不在可退额内；不存在"退券"或"把券折成现金退"的操作。sale 与 topup 退款同此原则。
- `refund_entries` 结构不变；ADR-0014「渠道必须出现在原支付的 `payment_entries` 中、单渠道累计不超原支付额」对 sale 与 topup 同样适用，锚点就是原支付的实际支付明细。
- 这**同步修正** ADR-0013 决策段里"method 含 `coupon`"与 ADR-0014 的 `ck_payment_entries_method` / `ck_refund_entries_method` 四值枚举——以本 ADR 为准（由 design 分册落地时不改历史 ADR 原文，在 `design/02` 备注中标注来源）。将来真要引入卡券体系（实体、发放、核销、与支付的联动），属 ADR-0014 复审条件，另立 ADR 定义它与折扣 / 支付的关系。

## 理由

1. **补对称缺口**：收款侧已支持订单外收款，退款侧必须对称；这是 ADR-0013 修正模式的自然延续，不引入新概念。
2. **资金流可追**：`refund_entries` 记渠道去向、`balance_ledger.ref_type/ref_id` 记余额扣回，充值退款的每一步都有审计痕迹，与「已生效事实不可改写」一致（DP-05）。
3. **守账实相符底线**：余额不足时整笔拒绝退款，不做部分退、不写负余额；跨店退款一律禁止。系统守住「单店账不失真」这条线，坏账与跨店结算留给业务线下处理，不塞进通用底座。
4. **口径自解释**：`refunds.kind` 让「销售退款 / 充值退款」在查询与对账时无需 join 即可区分，订单派生金额显式过滤 `kind='sale'`，杜绝把充值退款算进订单已退；券从支付渠道剥离后，「实付」口径不再被折扣虚增。
5. **零落地成本**：订单域尚未落地，本次修正只动设计文档，不产生废弃迁移、不改 `EXPECTED_SCHEMA_VERSION`、不触碰任何已交付代码。

## 后果与限制

- 本 ADR 已同步修订 `design/01`（`Refund` 聚合字段与不变量、订单已退派生口径加 `kind='sale'` 过滤、充值可退额派生、券归入折扣）、`design/02`（`refunds` DDL 三项变更 + 新索引、`payment_entries` / `refund_entries` 的 `method` CHECK 移除 `coupon`、`refund_entries` 校验适配说明、表清单、`coupon` 口径注记）、`design/03`（新增充值退款端点 + 订单详情派生口径注记）、`design/06`（F-02 的 sale 锚点说明、F-03 加充值退款流程），并在 `CHANGELOG.md` 与 `design/00` 变更规则中登记为**第八次受控扩展（R3 契约修正）**。
- `refunds.order_id` 变为可空；任何依赖「退款必有订单」的查询 / 报表须改为按 `kind` 分支。订单域未落地，当前应无此类代码。
- 新增权限点 `member.topup.write` 覆盖充值退款（与充值同源）；若将来要分离「充值」与「退充值」权限，另立 ADR。
- **余额硬约束**：充值退款要求退款时会员本店余额 ≥ 退款额，不足整笔拒绝；余额已消费的充值不可退。这是用户定案的刻意约束，不是缺陷。
- **禁止跨店退款**：退款单的 `shop_id` 强绑原支付与会员本店账，无跨店例外、不留扩展口。
- 同一退款的单渠道锁定与汇总在高并发下串行化；复审条件之一。
- **公开仓库脱钩约束不变**：只用 `refund` / `kind` / `topup` / `business_date` 等通用词，不出现任何内部项目名或行业专属词。

## 复审条件

- **若要开放跨店退款**（A 店充值、B 店退）→ 本 ADR 已明确禁止，开放即推翻本条并冲击 ADR-0003 单层隔离与「不做自动清分」，须先修订相关 ADR；
- 需要「余额提现」（退回渠道但余额不扣回、或从余额退到现金）→ 属新业务语义，另立 ADR；
- 需要卡券体系（券的实体、发放、核销，及其与折扣 / 支付的关系）→ 本 ADR 已把券归为折扣，任何把它重新引入支付渠道的设计须另立 ADR；
- **若要允许余额不足时仍退充值**（坏账 / 透支 / 部分退）→ 本 ADR 已明确禁止，放开即冲击 `ck_member_balances_nonneg`，须先修订 ADR-0004；
- ADR-0013 / 0014 任一被修订且影响退款来源、金额派生、`method` 枚举或 `refund_entries` 校验。

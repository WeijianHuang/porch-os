# ADR-0020 · 订单调整（优惠）原因结构化与券来源预留

- 状态：Accepted（2026-11-23）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离、复合外键锁同店）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0014](0014-derived-amounts-and-refund-entries.md)（金额派生、分摊）、[ADR-0016](0016-refund-source-and-topup-reversal.md)（券是折扣不是支付、一律进 `order_price_adjustments`）、[ADR-0019](0019-order-price-preview.md)（试价端点 `adjustments` 输入）
- 影响分册：修订 `design/01`（§2 聚合清单 / §4 字段 / §6 计价）、`design/02`（§2 表清单 / §3 `order_price_adjustments` DDL）、`design/03`（§8 端点说明 / §10 试价请求体）；不新增分册
- 迁移：修订 `migrations/00010_orders.sql`（尚未提交、库内无业务数据，直接改首批迁移而非新增版本）；`EXPECTED_SCHEMA_VERSION` 保持 **10**

## 背景

`migrations/00010_orders.sql` 里 `order_price_adjustments` 只有 `amount numeric(14,2)` 与 `reason text`：整单让利"减了多少"有结构，"为什么减"却是一段自由文本。ADR-0016 已定**券是折扣、一律进本表**——也就是说这张表是券优惠的唯一落点，却记不住"用了哪张券"，也没有稳定的展示标题。

`reason text` 带来三个具体问题：

1. **无法统计**：想知道"本月优惠券让利多少"只能对文本做模糊匹配，口径不可靠；
2. **无法追溯**：用了哪张券 / 哪场活动写不进结构，将来卡券体系落地无从反查；
3. **展示契约脆弱**：前端列账单依赖后端往 `reason` 塞约定格式字符串，改一次展示就改一次语义。

本 ADR 把"为什么减"结构化，并为将来卡券**预留字段级支持**——注意这是字段级预留，不是实现卡券体系。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| "这是不是开始做卡券了" | **不是**。只给 `order_price_adjustments` 加 `type` / `source_id` / `title` / `description`，不建券表、不做发放 / 核销 / 库存。卡券体系是 ADR-0014 的复审条件，须另立 ADR |
| "`source_id` 为什么不加外键" | 券表还不存在，加了建不起来。按 `shift_id` / `operator_id` / `card_holdings.card_id` 的"只持 ID"惯例处理；**券表落地后必须补复合外键 `(shop_id, source_id)`**，见后果段 |
| "`source_id` 为什么是 bigint 不是 uuid" | 库内所有表间引用都是内部 bigint（`order_lines.catalog_item_id`、`payments.member_id`、`card_holdings.card_id`），`public_id uuid` 只对外（design/03 §1）；且复合外键钉同店需要 `(shop_id, id)` 这对内部键。对外展示券详情时 join 拿 `public_id` 即可 |
| "抹零和优惠券是一回事吗" | **不是**。抹零是收银动作、券是营销工具，统计口径不同，故 `type` 分设 `rounding` 与 `coupon` |
| "`title` 和 `description` 都要填吗" | `title` **必填**（账单短标题，前端列账用）；`description` 可选（详细原因）。券也要有 `title` |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 区分优惠种类 | `type text` + CHECK 五值 | `type smallint` 数字码 | 项目惯例是 text + CHECK（design/02 §1），数字码要维护映射表、可读性差；加新值改迁移即可 |
| `reason` 字段 | 删除，拆成 `type` + `title` + `description` | 保留 `reason` 再加结构字段 | 双写必然漂移，语义分裂；00010 未提交、库内无数据，现在是零成本删除窗口 |
| 券关联 | `source_id bigint` 只持 ID + CHECK | 建 `coupons` 表并加外键 | 券体系不在本次范围（ADR-0014 复审条件）；只留字段，未来补外键 |
| `source_id` 类型 | `bigint` 内部 ID | `uuid` 对外 public_id | 破坏库内引用一致性；无法直接用 `(shop_id, id)` 复合外键锁同店；uuid 索引大一半 |
| 抹零建模 | 独立 `type='rounding'` | 归入 `other` + 文本说明 | 抹零是高频收银手段、统计口径独立；不值得靠文本区分 |

## 决策

### 1. `order_price_adjustments` 新结构

替换 `migrations/00010_orders.sql` 与 `design/02` §3 的表定义：

```sql
CREATE TABLE order_price_adjustments (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id      bigint        NOT NULL,
  order_id     bigint        NOT NULL,
  seq          integer       NOT NULL,
  type         text          NOT NULL,
  amount       numeric(14,2) NOT NULL,
  source_id    bigint,
  title        text          NOT NULL,
  description  text,
  created_at   timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT uq_order_price_adjustments_shop_order_seq UNIQUE (shop_id, order_id, seq),
  CONSTRAINT fk_order_price_adjustments_order FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_opa_type CHECK (type IN ('coupon','promotion','rounding','manual','other')),
  CONSTRAINT ck_opa_type_source CHECK (
    (type = 'coupon' AND source_id IS NOT NULL) OR
    (type <> 'coupon' AND source_id IS NULL)
  ),
  CONSTRAINT ck_opa_amount CHECK (amount >= 0),
  CONSTRAINT ck_opa_title CHECK (length(btrim(title)) > 0)
);
```

| `type` | 含义 | `source_id` | 典型 `title` |
|---|---|---|---|
| `coupon` | 优惠券 | **必填**（券实例内部 ID，只持 ID、无外键） | 券名，如"满100减10" |
| `promotion` | 门店活动 | 必须为空 | 活动名，如"开业八折" |
| `rounding` | 抹零 | 必须为空 | "抹零" |
| `manual` | 店员手工让利 | 必须为空 | "手工优惠" |
| `other` | 其他 | 必须为空 | 业务自定短标题 |

- `reason` 列**删除**，由 `type` + `title` + `description` 取代；无语义遗漏。
- `amount >= 0` 不变：本表记"减了多少"，不出现负数调整。
- 分摊口径不变：多笔 `adjustments` 先合并总额，再由 `AllocateByWeights` 一次性按行金额权重分摊；`Σ 每行分摊 = Σ adjustments.amount`。

### 2. 试价端点 `adjustments` 输入同步改（ADR-0019）

请求体里每条调整从 `{amount, reason}` 改为：

```json
{
  "type": "coupon",
  "amount": "5.00",
  "source_id": 123,
  "title": "满100减10",
  "description": "会员生日券"
}
```

- `type` 必填且五值之一；`amount` 非负；`title` 必填非空；
- `type='coupon'` 时 `source_id` 必填且为正整数（**此时只校验形状，不校验券是否存在**——券表尚不存在；券的归属 / 有效性在券体系落地后由用例层补校验）；
- `type<>'coupon'` 时 `source_id` 必须为空；
- `description` 可选。
- 响应形状不变（逐行分摊 + 订单级汇总），本表的结构不影响前端展示契约。

### 3. 券来源的待办约束（不实现，只登记）

券表落地时（另立 ADR）必须同时：

1. `source_id` 指向**按店的券实例**（B 类，有 `shop_id`），不是全局券模板；
2. 补复合外键 `FOREIGN KEY (shop_id, source_id) REFERENCES <券实例表>(shop_id, id)`，把跨店用券在 DB 层堵死；
3. 在用例层校验券的归属、状态与核销——本期无此逻辑，`source_id` 仅作可追踪锚点。

## 理由

1. **让"为什么减"可统计、可追溯**：`type` 支持按优惠手段聚合，`source_id` 为券体系留下反查锚点。
2. **展示契约稳定**：`title` 是专门的账单短标题，前端不再解析自由文本。
3. **零成本窗口**：00010 未提交、库内无业务数据，直接改首批迁移，不产生废弃版本、不需数据回填。
4. **不越界**：只做字段级提前支持，不建券表、不改状态机、不改金额派生与分摊口径。

## 后果与限制

- 修订 `00010_orders.sql` 后必须重跑 `up → down-to 9 → up`，确认无残留；已生成的本地库数据（仅测试单号函数调用）随之丢弃，无业务数据损失。
- `design/02` 表清单里 `order_price_adjustments` 的说明改为"整单让利（`type` 区分券 / 活动 / 抹零 / 手工 / 其他），券以 `source_id` 预留追溯锚点"。
- `design/01` 聚合清单里 `PriceAdjustment` 从属实体补字段语义；§6 计价段注明"调整结构见 ADR-0020，分摊仍按合并总额一次完成"。
- `design/03` §8 端点说明与 §10 试价请求体同步；ADR-0019 请求体同步。
- **未实现的校验不得假装存在**：本期 `source_id` 只是形状校验，不查券是否存在；前端不得据此显示"券已核销"之类状态。
- 无回填成本：`order_price_adjustments` 表在本次之前从未落地过任何数据。
- **公开仓库脱钩约束不变**：`coupon` / `promotion` / `rounding` 都是通用词，不出现内部项目名或行业专属词。

## 表的适用边界（与 Order 聚合的归属）

`order_price_adjustments` 是 **Order 聚合的从属表**：`order_id NOT NULL` + 复合外键 `(shop_id, order_id) → orders(shop_id, id)` + 唯一键 `(shop_id, order_id, seq)` 已把它物理上钉死在订单上——它不可能挂到别家店的单据，也不可能脱离订单存在。**归属同时由结构（复合外键）和名字（`order_` 前缀，ADR-0021）保证**。

契约范围内**没有其他业务使用本表**：会员充值的附加金额是余额赠送（`payments.amount=1000` 但 `balance_ledger.delta=+1100`），走 `balance_ledger` 的 `reason` / `ref_type` / `ref_id`，不是应收减免；退款是逆向资金流，不产生优惠；支付渠道不含券（ADR-0016）；库存 / 房间 / 队列 / 排班无金额减免概念。

**未来出现"非订单让利"时的决策规则**（不预先建表、不为假想需求改结构）：

| 判断 | 处理方式 | 理由 |
|---|---|---|
| 新业务与订单让利**同构**：是某单据的应收减免、需按明细行分摊、`title` 账单展示、父实体带 `shop_id` 可建复合外键 | **复用订单本身**，或把本表单一外键泛化（仍是强类型单一外键 + 复合外键锁同店），另立 ADR | 保持 DB 层同店约束与分摊口径一致 |
| **非同构**：如充值赠送（余额赠送而非应收减免）、无明细行可分摊 | **另立新表或走已有流水表**（如 `balance_ledger`），不复用本表 | 语义不同，硬塞只会混淆口径 |

**明确禁止**：为"通用化"把 `order_id` 改成可空 + `parent_type`/`parent_id` 多态关联。那会使本表**无法建立任何外键**，同店隔离从 DB 层退化到应用层，直接违反 ADR-0003 / design/02 §4「复合外键把 `shop_id` 钉死」的红线。同理禁止为多种父实体加多个可空外键列 + CHECK（每来一种业务加一列、唯一键与索引命名空间混杂，是把专用表掰成通用表的反面模式）。

本规则只登记判断标准，**不因此加多态字段**；表名已由 ADR-0021 加 `order_` 前缀以显式表达归属。

## 复审条件

- 卡券体系（券实例表、发放、核销、与订单联动）落地 → 补 `source_id` 复合外键与用例层校验，另立 ADR；
- 出现"非订单让利"需求 → 按上方「表的适用边界」决策表判断是否同构；若同构且需泛化本表，**必须保持单一强类型外键 + 复合外键锁同店**，另立 ADR，禁止多态关联；
- 新增优惠种类（如会员价、时段价）→ 扩 `ck_opa_type` 取值并评估是否需新的来源字段；
- 需要按 `source_id` 做报表聚合 → 评估索引 `idx_pa_shop_source (shop_id, source_id)`，本期不加（无数据、无查询场景）；
- ADR-0016 的"券是折扣"定位或 ADR-0014 的分摊口径被修订且影响本表。

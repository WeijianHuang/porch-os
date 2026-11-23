# ADR-0021 · `price_adjustments` 更名为 `order_price_adjustments`

- 状态：Accepted（2026-11-23）
- 关联：[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0013](0013-payment-and-refund-ownership.md) / [ADR-0014](0014-derived-amounts-and-refund-entries.md) / [ADR-0016](0016-refund-source-and-topup-reversal.md)（历史 ADR 中出现的旧表名）、[ADR-0020](0020-structured-price-adjustments.md)（本表的结构定义与适用边界）
- 影响分册：修订 `design/00` 变更规则、`design/01` / `02` / `03` / `06` 的表名引用；不新增分册
- 迁移：直接修订未提交的 `migrations/00010_orders.sql`（`EXPECTED_SCHEMA_VERSION` 保持 **10**，无数据回填、无废弃迁移版本）

## 背景

ADR-0020 已确认 `price_adjustments` **专属于 Order 聚合**：`order_id NOT NULL` + 复合外键 `(shop_id, order_id) → orders(shop_id, id)` + 唯一键 `(shop_id, order_id, seq)` 把归属钉死在结构上。当时判断"归属由结构保证、不靠名字"，故未改名。

复核后改变决定：表名是**第一识别入口**。本仓库订单域表 `orders` / `order_lines` / `order_no` 均带 `order_` 前缀，唯独本表裸名；在多表工程里，读表名比读表定义快得多，前缀能直接表达聚合归属、降低误用与检索成本。更重要的是**当前是唯一零成本窗口**：`00010_orders.sql` 未提交、库内无业务数据、尚无 Go 代码引用该表——现在改名只是改文本；等迁移落地、代码接入后再改，就要付出废弃迁移版本、数据回填与历史别名期的代价。

## 决策

### 1. 表名与相关标识符更名

| 旧 | 新 |
|---|---|
| `price_adjustments`（表） | `order_price_adjustments` |
| `uq_price_adjustments_shop_order_seq` | `uq_order_price_adjustments_shop_order_seq` |
| `fk_price_adjustments_order` | `fk_order_price_adjustments_order` |
| `idx_price_adjustments_shop_order` | `idx_order_price_adjustments_shop_order` |
| `ck_pa_type` / `ck_pa_type_source` / `ck_pa_amount` / `ck_pa_title` | `ck_opa_type` / `ck_opa_type_source` / `ck_opa_amount` / `ck_opa_title` |

保留 `price` 词：本表记的是**应收价格的减免**，而非泛泛的"订单调整"（后者易与状态 / 备注调整混淆）。带前缀后全表为 `order_price_adjustments`。

### 2. 历史 ADR 的别名处理

ADR-0013 / 0014 / 0016 是已接受的历史决策，按项目惯例**原文不改**。这些文档与 `design/03`、`design/06` 的历史描述中出现的 `price_adjustments` 一律理解为 `order_price_adjustments` 的旧名；本 ADR 即权威别名映射，后续新文档统一用新名。

### 3. 不改的内容

- 表结构、字段、CHECK 语义、分摊口径、适用边界（ADR-0020）**全部不变**；
- 试价端点请求体的 JSON 字段名 `adjustments[]` 不变（它是 API 契约，不是表名）；
- `EXPECTED_SCHEMA_VERSION` 保持 10；无迁移版本新增、无数据回填。

## 理由

1. **识别与管理**：与 `orders` / `order_lines` / `order_no` 命名一致，一眼可见聚合归属；
2. **零成本窗口**：迁移未提交、库内无业务数据、无代码引用，改名只是文本替换；
3. **防误用**：名字与结构（必填 `order_id` + 复合外键）一致，减少被误当通用优惠表复用的风险，与 ADR-0020「表的适用边界」相呼应。

## 后果与限制

- 所有引用同步更新：`00010_orders.sql`、`design/00` / `01` / `02` / `03` / `06`、ADR-0019 / 0020、`README.md`、`CHANGELOG.md`；历史 ADR-0013 / 0014 / 0016 保留原文并通过本 ADR 声明别名。
- 必须重跑 `up → down-to 9 → up` 验证：新表名与约束名生效、旧名无残留。
- 未来任何文档若引用历史 ADR 里的 `price_adjustments`，需注明"即 `order_price_adjustments`"。
- 本更名不改变 ADR-0020 的判断规则：出现非订单让利仍按「同构复用 / 非同构另立」决策，且禁止多态关联。

## 复审条件

- 若将来真出现"同构的第二单据让利"需要泛化本表（ADR-0020 适用边界），届时可再评估是否去掉 `order_` 前缀——须另立 ADR，并保留新名到旧名的别名映射。

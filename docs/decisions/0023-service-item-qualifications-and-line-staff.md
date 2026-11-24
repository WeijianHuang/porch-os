# ADR-0023 · 服务项资质与订单明细执行服务员

- 状态：Accepted（2026-11-24）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（仅 `shop_id` 一层隔离、复合外键锁同店）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0009](0009-queues-and-staff.md) / [ADR-0010](0010-duty-status-and-queue-order.md) / [ADR-0012](0012-scheduling-engine.md) / [ADR-0015](0015-staff-state-and-position-controls.md)（服务人员 / 执行者基线）、[ADR-0018](0018-service-duration.md)（目录项 `type=service` 与标称时长）、[ADR-0022](0022-order-card-binding-and-companion-groups.md)（订单上下文、卡 / 房间 / 同行组）
- 影响分册：修订 `design/01` §2（聚合字段）、`design/02`（§2 表清单 / §3 DDL）、`design/03`（§4 错误码 / §8 端点）、`design/06`（F-01 阶段 B / 阶段 C 的增量校验）、`design/16`（§3.2 / §3.3 / §5 / §7 / §8 P3 由「不做」改为「已接受」）；同步 `design/00`、`ARCHITECTURE.md`、README / CHANGELOG 与 `porch-console` 前端 ADR-0007
- 迁移：新增 `migrations/00012_service_item_qualifications_and_line_staff.sql`；`EXPECTED_SCHEMA_VERSION` 由 11 → **12**

## 背景

用户要求「服务员需要绑定服务，开单的时候还需要添加服务员」。`design/16` 的提案台账把「订单绑定服务人员」列为 P3（本期不做、需另立 ADR），`order_lines` 当前只有 `item_name_snapshot`，没有人员列；服务人员域（ADR-0009 起）只表达「档案 + 队列归属 + 调度状态」，既没有「谁能做哪项服务」，也不记录「这单这项服务是谁做的」。

业务确认后共三点：

1. **两层都要**：维护「服务员可做哪些服务项」的资质关系，同时在开单明细上记录实际执行的服务员；
2. **明细级归属**：一条服务明细对应一位服务员，商品明细不绑定；一张单的不同服务项可以由不同服务员执行；
3. **服务明细必填**：只要订单含服务项，提交结账前每条服务明细都必须已指定服务员；纯商品单不要求。

按 ADR-0008，schema、端点清单与错误语义的改动必须先有 ADR / 设计来源；本 ADR 即该来源。它是冻结后的**第十五次受控扩展**，不推翻 ADR-0001–0022 的任何条款：隔离仍是只有 `shop_id` 一层，目录项仍是 A 类全局数据，金额 / 支付 / 退款 / 订单状态机 / 同行组 / 卡与房间绑定全部不变。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| 「服务员绑定服务」 | 是**两层**关系，不是同一件事：① 资质 = 该服务员**可做哪些服务项**（服务人员 ↔ `catalog_items` 中 `type='service'` 的项）；② 归属 = 某张订单的某条服务明细**实际由谁执行**（`order_lines.staff_id`）。两者分别维护、分别校验 |
| 「资质关系是谁的」 | 服务员是**单店雇员**（ADR-0009），目录项是**全局 A 类**（ADR-0018）。关联表带 `shop_id`，用 `(shop_id, staff_id)` 复合外键钉死服务员同店；`catalog_item_id` 直接引用全局目录项。A 店服务员不可能被授予 B 店的任何关系，因为关系本身就挂在 A 店 |
| 「资质能否给商品项」 | **不能**。只有 `type='service'` 的目录项可进入资质集合；商品项连服务时长都不允许有（ADR-0018 的 CHECK），不存在「做商品」的服务员。数据库无法跨表 CHECK `catalog_items.type`，该约束由服务端校验 + 单测保证（见「后果与限制」） |
| 「服务员挂在哪一层」 | **每条服务明细一行**。`order_lines.staff_id` 可空；服务明细可填、商品明细必须为空；一单可出现多位服务员，各自负责不同服务项。不做整单负责人字段（那会与明细执行人形成两套真源） |
| 「一条服务明细能挂多人吗」 | **不能**。一条行只有一个 `staff_id`；多人共同完成一项服务须拆成多条明细，各自指定。本期不建明细 ↔ 人员多对多 |
| 「提交时谁必填」 | 订单含**任意** `type='service'` 明细且其 `staff_id IS NULL` → `422 ORDER_LINE_STAFF_REQUIRED`；纯商品单即使无人也能提交。这是与卡 / 房间必填（ADR-0022）并列的第三项提交前置校验 |
| 「服务员已停用 / 资质被撤了怎么办」 | 停用（`status='inactive'`）的服务员不可被新指定，已指定的在提交时返回 `409 STAFF_INACTIVE`；资质被撤销时**不回写历史明细**，但提交会重新校验，返回 `422 STAFF_NOT_QUALIFIED`，要求改派合格人员 |
| 「删除服务员后历史单怎么显示」 | 删除前在**同一事务**把其明细的 `staff_id` 置空，但保留 `staff_name_snapshot`（与 `item_name_snapshot` 同构的历史名），历史单仍可读出「当时是谁」；这些未结单在提交前必须重新指定服务员。不因历史引用而禁止删除人员（沿用 ADR-0009「删除同时解绑关联」） |
| 「在岗状态是否限制选择」 | 不限制。资质只看任职状态 `status='active'`；`duty_status` / `executor_state`（ADR-0010 / 0012 / 0015）是调度态，前端可显示「在岗 / 离岗」提示，但服务端不据此拒绝绑定，避免把人工状态和订单历史耦合 |
| 「这是否等于业绩归属 / 服务计时」 | **不是**。本 ADR 只记录执行人，不计算绩效 / 业绩、不建服务开始 / 结束 / 加时实例、不改 `service_remaining_minutes`（仍恒 `null`，ADR-0017）。业绩归属是 P5、计时是 P4，均保持不做 |
| 「是否新增权限点」 | **不新增**。资质维护属服务人员管理，用既有 `queue.write`（读 `queue.read`）；明细挂人属订单写操作，用 `order.write` / `order.read`。与 ADR-0022 的卡 / 房间 / 同行组一致 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 资质存放 | 关联表 `staff_service_items`，复合外键锁同店 | `staff.service_item_ids jsonb` 数组 | 数组无外键、无唯一约束、无法 SQL 联表查询，且并发替换易产生脏写；违反 ADR-0002「SQL 优先、无实体图」 |
| 资质粒度 | 服务员 ↔ 服务目录项（`type='service'`） | 服务员 ↔ 任务类型 `scheduling_task_types`（ADR-0012） | 开单选的是目录项，任务类型是调度域分类；用后者会让收银端再维护一层映射，且两者生命周期不同 |
| 明细归属层级 | `order_lines.staff_id`（明细级） | `orders.staff_id`（整单一个） | 业务明确「一间房多个客人、各自服务项不同」；整单字段无法表达一单多服务员，后续还要再加明细列形成双真源 |
| 历史可读性 | `staff_name_snapshot` + 删除时置空 `staff_id` | 删除人员时禁止引用（`409 STAFF_IN_USE`） | 会让历史单据因为人事变动无法删人；快照与既有 `item_name_snapshot` 同构，改动最小 |
| 校验时机 | 加明细时校验一次 + 提交时再校验一次 | 只在提交时校验 | 只在提交时报错会让收银员在最后一步才发现整单无人；两处共用同一判定，前端可在购物车阶段就拦截 |
| 提交必填范围 | 服务明细必填、商品明细不要求 | 整单至少一人 | 无法保证每项服务都有人执行；「至少一人」与明细执行人不是同一语义 |
| 资质撤销对已有明细 | 不回写，提交时重新校验 | 撤销时把相关明细 `staff_id` 置空 | 撤销资质是人事操作，不应静默改写已录入的开单数据；提交时拒绝并把决定权交还操作员 |

## 决策

### 1. 数据模型（迁移 `00012`，schema 11 → 12）

```sql
-- (1) 资质：服务员可做哪些服务目录项。目录项是 A 类全局行，服务员是 B 类店内行；
--     关联表带 shop_id，用复合外键把服务员钉在本店（ADR-0003）。
CREATE TABLE staff_service_items (
  shop_id         bigint      NOT NULL,
  staff_id        bigint      NOT NULL,
  catalog_item_id bigint      NOT NULL REFERENCES catalog_items(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (staff_id, catalog_item_id),
  CONSTRAINT fk_staff_service_items_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_staff_service_items_shop_item ON staff_service_items (shop_id, catalog_item_id);

-- (2) 明细级执行服务员：一服务行一人；商品行不绑定。保留姓名快照供历史读取。
ALTER TABLE order_lines
  ADD COLUMN staff_id            bigint,
  ADD COLUMN staff_name_snapshot text,
  ADD CONSTRAINT fk_order_lines_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id),
  ADD CONSTRAINT ck_order_lines_staff_snapshot CHECK (
    staff_id IS NULL OR staff_name_snapshot IS NOT NULL
  );
CREATE INDEX idx_order_lines_shop_staff ON order_lines (shop_id, staff_id)
  WHERE staff_id IS NOT NULL;
```

- `staff_service_items` 主键 `(staff_id, catalog_item_id)` 即「同一服务员同一服务项不重复」的幂等约束；`ON DELETE CASCADE` 只作用于服务员侧（删人即清资质），目录项仍由全局外键保护。
- `order_lines.staff_id` 的外键**不带** `ON DELETE CASCADE`：删除服务员要先在事务内置空明细并保留快照，不能让数据库静默删掉关联。
- `staff_name_snapshot` 在指定时写入（CHECK 保证「有 `staff_id` 必有快照」），名称取指定时刻的 `staff.display_name`；后续改名不回写历史行。CHECK **不要求反向成对**：删除服务员时 `staff_id` 被置空而快照保留，历史单仍可读。
- `catalog_items.type='service'` 无法用跨表 CHECK 落到关联表；服务端在授予资质、指定明细、提交结账三处统一校验，并由数据层测试覆盖（见「验收标准」）。
- `orders` / `order_price_adjustments` / `payments` / `refunds` / `card_holdings` / `companion_groups` **均不改动**；本 ADR 不产生新的金额真相，也不改变卡自动释放与同行组口径。

### 2. 端点（R1 扩展，单店显式 `X-Shop-Id`，写带 `Idempotency-Key`）

| Method | Path | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/shops/{shopId}/staff/{staffId}/service-items` | `queue.read` | 该服务员已获资质的服务目录项 `public_id` 列表（按目录名排序） |
| PUT | `/api/v1/shops/{shopId}/staff/{staffId}/service-items` | `queue.write` | `{catalog_item_ids: string[], version}`：**全量替换**资质集合，原子完成；版本取 `staff.version`，冲突 `409 VERSION_CONFLICT`；重复键去重，非服务项 / 不存在项 `400 INVALID_ARGUMENT` |
| POST | `/api/v1/shops/{shopId}/orders/{id}/lines` | `order.write` | 请求体增量可选 `staff_public_id`；服务项可带、商品项带则 `400 INVALID_ARGUMENT`；带上时校验资质与任职状态 |
| PUT | `/api/v1/shops/{shopId}/orders/{id}/lines/{lineNo}/staff` | `order.write` | `{staff_public_id: string \| null}`；仅 `draft` / `open` 可改，`null` 为清空；不改金额、不写让利，递增订单 `version`；服务项改派同样校验资质 |
| GET | `/api/v1/shops/{shopId}/staff`、`/staff/{staffId}` | `queue.read` | 响应增量 `service_item_ids: string[]`，一次列表即可渲染资质（DA-01 扁平 DTO） |
| GET | `/api/v1/shops/{shopId}/orders/{id}` | `order.read` | `lines[]` 增量 `staff_public_id: string \| null` 与 `staff_name_snapshot: string \| null` |

 PUT 全量替换沿用 ADR-0010 队列重排的同构手法（一次事务、版本乐观锁、幂等键指纹），不新增第二种「部分增减」端点；服务员详情与列表同时返回资质 ID，页面无需逐人再发一次请求。

### 3. 校验与错误语义

- **授予资质**：目录项必须存在且 `type='service'`；服务员必须属本店、`status='active'`。版本不符 `409 VERSION_CONFLICT`；非服务项 `400 INVALID_ARGUMENT`。撤销资质只删关联，不触碰已有明细。
- **加明细 / 改派**：商品项携带 `staff_public_id` → `400 INVALID_ARGUMENT`；服务项携带时，服务员不存在 `404 STAFF_NOT_FOUND`、非本店 `403`、停用 `409 STAFF_INACTIVE`、无资质 `422 STAFF_NOT_QUALIFIED`。服务项**允许暂不带人**（草稿可分步录入），必填只在提交时判定。
- **提交结账**（`POST /orders/{id}/submit`，与 ADR-0022 的卡 / 房间校验并列、同事务）：
  1. 逐条读取 `order_lines` 的 `catalog_items.type` 与 `staff_id`；
  2. 任一 `type='service'` 且 `staff_id IS NULL` → `422 ORDER_LINE_STAFF_REQUIRED`，`details` 给出 `line_no`；
  3. 已有 `staff_id` 的服务行再查一次任职状态与资质：停用 → `409 STAFF_INACTIVE`；资质已撤销 → `422 STAFF_NOT_QUALIFIED`（同样带 `line_no`）；
  4. 全部通过后才进入既有 `Quote` 重算与金额一致性校验。商品行不参与任何人员校验。
- 新错误码 `STAFF_INACTIVE`（409）、`STAFF_NOT_QUALIFIED`（422）、`ORDER_LINE_STAFF_REQUIRED`（422）；`STAFF_NOT_FOUND`（404）沿用既有码表。三者均 `retryable=false`，客户端应先改派再重试。

### 4. 删除服务员与历史

`DELETE /staff/{staffId}` 在**同一事务**内依次执行：① `UPDATE order_lines SET staff_id=NULL WHERE shop_id=$1 AND staff_id=$2`（保留 `staff_name_snapshot`）；② 资质行由 `ON DELETE CASCADE` 清理；③ 队列关联由既有级联清理（ADR-0009）；④ 删除服务员档案。事务保证不出现「人已删、明细还指向他」的中间态。被置空的未结服务明细再次提交会命中 `ORDER_LINE_STAFF_REQUIRED`，界面引导改派；已结单不受影响，仍显示快照姓名。

**已知前置限制（非本 ADR 引入）**：`card_holdings.staff_id` 对 `staff` 是强外键（ADR-0017），因此只要该服务员在任何历史持卡记录中出现过，删除就会被数据库拒绝（当前返回 `400`，由通用外键映射产生）。本 ADR 不改变 ADR-0017 的持卡历史或外键口径；若业务需要「有持卡史的人也能删除」，须另立 ADR 决定是改成软删除、还是给持卡表加姓名快照后放宽外键。该限制不影响本 ADR 的快照语义——明细侧的置空与快照保留已在无持卡史的服务员上验证。

### 5. 前端契约（`porch-console` 前端 ADR-0007）

- 服务人员页每张卡片显示资质服务项数量，并提供「维护服务项」入口（多选目录中的服务项，保存走 PUT 全量替换）；只有 `queue.write` 可见可写。
- 开单弹窗的购物车对 `type='service'` 的明细行增加服务员选择框，**候选项只取该目录项有资质且 `status='active'` 的服务员**；商品行不显示选择框。选中随明细一起写入 `POST /lines`，购物车阶段即拦截缺人。
- 订单详情抽屉的明细列显示「执行人」（优先快照姓名），未结单可就地改派（PUT 端点）；已结单只读。
- 不新增权限点、不改门店切换协议：资质与人员列表沿用 `staff` query key，随 `X-Shop-Id` 切换失效。

### 6. 明确不做

不做服务计时实例（P4）、业绩归属 / 绩效规则（P5）、订单备注（P6）、第三方支付（P7）、跨店资质、按店覆盖目录类型、一条明细多人、整单负责人、服务员自动派单与 `executor_state` 联动。以上任一需求须另立 ADR；本 ADR 的资质只读地服务于开单选择，不改变 ADR-0012 的判定链与派单事件。

## 理由

1. **一层关系不足以回答现场问题**：只有资质无法知道这单是谁做的；只有明细执行人无法在开单时过滤「谁能做这项」。两层关系各自最小、彼此用同一校验函数约束，避免规则分叉。
2. **目录项全局、人员店内**：复合外键把人员钉在本店，目录项保持 A 类；不引入 tenant、门店组，也不把目录项复制成店内行（ADR-0003 / 0005）。
3. **明细级 + 快照**同时满足「一单多服务员」与「删人后历史可读」，且沿用 `item_name_snapshot` 的既有模式，不增加聚合根之间的实体图（ADR-0002）。
4. **加项与提交双校验**把错误前移到购物车，同时防止「加项后有资质、提交前资质被撤」的竞态；复用既有错误码体系与幂等 / 审计 / Outbox 机制。
5. **不改金额与支付**：执行人是归属维度，不是计费维度；不产生新的金额真相，也不影响 ADR-0013 / 0014 / 0016 / 0022 的任何派生口径。

## 后果与限制

- `EXPECTED_SCHEMA_VERSION` 升为 **12**；`.env.example`、`scripts/test.sh`、文档与 `/ready` 校验同步。迁移必须提供可执行 `Down`（先删约束 / 索引 / 列，再删资质表）。
- 跨表 `type='service'` 约束无法用 PG `CHECK` 表达，依赖服务端三处校验与数据层测试；绕过 API 直接写库可能插入商品项资质，这是已知限制，复审时若出现该情况再评估触发器或生成列方案。
- 资质撤销不修改历史明细，因此「已录入但未提交」的单可能因人事变更被拒绝提交；这是有意的显式失败，不静默改派。
- `staff_name_snapshot` 只在指定时写入一次；服务员改名后历史行仍显示旧名，符合快照语义。
- 公开仓库脱钩约束不变：只用 `staff` / `service_item` / `qualification` 等通用词，不出现任何内部项目名或行业专属词。

## 复审条件

- 需要一条明细多人协作或按角色拆分执行人；
- 需要业绩 / 绩效归属规则或与调度派单事件联动（`executor_state`）；
- 需要服务计时实例、开始 / 结束时刻或真实剩余分钟；
- 需要服务员跨店复用或资质按店覆盖；
- 上游 ADR-0002 / 0003 / 0008 / 0018 / 0022 任一被修订且影响本 ADR 的隔离、目录类型或订单提交规则。

## 验收标准

1. 迁移 `up → down-to 11 → up` 通过；`/ready` 在 `EXPECTED_SCHEMA_VERSION=12` 返回 200，设为 11 返回 503。
2. 直接写库：跨店资质（A 店服务员 + 任意项但 `shop_id` 不一致）被复合外键拒绝；商品项资质与「有 `staff_id` 无快照」被应用层 / CHECK 拒绝。
3. 资质 PUT 全量替换幂等（同键重放返回首次结果）、版本冲突 409、非服务项 400；`GET /staff` 一次返回 `service_item_ids`。
4. 服务明细指定无资质 / 停用人员分别返回 `422` / `409`；商品明细带人返回 400；提交时缺人 / 资质被撤 / 人员停用分别返回 `ORDER_LINE_STAFF_REQUIRED` / `STAFF_NOT_QUALIFIED` / `STAFF_INACTIVE`，`details.line_no` 正确。
5. 同一单两条服务明细指定两位不同服务员可提交、各自投影正确；纯商品单无人可提交。
6. 删除**无持卡历史**的服务员后：历史已结单明细仍显示快照姓名；未结单该行 `staff_public_id` 变 null 且提交被拒，改派后可通过。有持卡史的人员删除受 ADR-0017 外键前置限制（见上）。
7. 前端：开单购物车服务行只列出有资质人员、商品行无人员选择框；人员页资质维护保存后列表即时更新；1440 / 1024 无横向溢出。

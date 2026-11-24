# ADR-0024 · 服务人员分类与开单辅助选人

- 状态：Accepted（2026-11-24）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离、复合外键锁同店）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0009](0009-queues-and-staff.md) / [ADR-0010](0010-duty-status-and-queue-order.md) / [ADR-0012](0012-scheduling-engine.md) / [ADR-0015](0015-staff-state-and-position-controls.md)（服务人员与调度状态基线）、[ADR-0018](0018-service-duration.md)（目录项类型与时长）、[ADR-0022](0022-order-card-binding-and-companion-groups.md)（订单上下文）、[ADR-0023](0023-service-item-qualifications-and-line-staff.md)（服务项资质与明细执行人）
- 影响分册：修订 `design/01` §2、`design/02` §2/§3、`design/03` §4/§8、`design/06` F-01 阶段 B、`design/10` §2/§4、`design/16` §3.2/§5/§8；同步 `design/00`、`ARCHITECTURE.md`、README / CHANGELOG 与 `porch-console` 前端 ADR-0008
- 迁移：新增 `migrations/00013_staff_categories_and_catalog_item_groups.sql`；`EXPECTED_SCHEMA_VERSION` 由 12 → **13**

## 背景

用户要求：「服务员还需要添加分类功能，一个服务员只能绑定一个分类；开单页面需要有添加服务员按钮，点击后的页面参考给出的选人弹窗。」

现状缺口：

1. `staff` 只有档案、队列归属、任职状态与调度状态，没有分类维度；无法在开单时按分类快速缩小选人范围。
2. `order_lines.staff_id` 与 `staff_service_items` 已由 ADR-0023 落地，但开单弹窗只能逐条明细从下拉框选人，没有「先选人再批量加服务项」的入口。
3. `catalog_categories` 表已有，但 `catalog_items` 没有分类归属，弹窗里的服务项分组标签没有数据来源。

按 ADR-0008，schema、端点与错误语义的改动必须先有 ADR / 设计来源；本 ADR 即该来源。它是冻结后的**第十六次受控扩展**，不推翻 ADR-0001–0023 的任何条款：隔离仍只有 `shop_id` 一层，服务人员仍是单店雇员，目录项仍是全局 A 类数据，金额 / 支付 / 退款 / 订单状态机 / 卡与房间 / 同行组 / 服务项资质规则全部不变。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| 「服务员分类」 | 门店自定义的**服务人员分类**（`staff_categories`），例如按服务组、技能组划分。分类是店内 B 类数据，带 `shop_id`；一个服务员**最多一个**分类，分类可为空 |
| 「一个服务员只能绑定一个分类」 | `staff.category_id` 是标量外键，不是关联表；重新绑定就是**替换**，`NULL` 表示取消分类。数据库无需额外唯一约束，标量天然保证「至多一个」 |
| 「分类是否等于资质」 | **不等于**。分类只用于筛选与展示，不授予任何服务能力；能否执行某项服务仍由 ADR-0023 的 `staff_service_items` 决定。分类通过的服务员若没有资质，仍不能在开单时绑定该项服务 |
| 「分类是否等于调度状态」 | **不等于**。`duty_status` / `executor_state` 是调度态（ADR-0010 / 0012 / 0015），本 ADR 只把它们作为选人弹窗的**筛选与展示维度**，不改变状态机、不自动派单、不回写任何状态 |
| 「分类是否等于权限」 | **不等于**。分类不参与授权；授权仍来自 RBAC 与 `user_shops`。读写权限沿用 `queue.read` / `queue.write`，不新增权限点 |
| 「服务项分类是什么」 | 沿用既有全局 `catalog_categories`；`catalog_items.category_id` 可空，用于弹窗把服务项分到不同标签页。它是**只读分组**，不新增分类维护端点，也不改变 ADR-0018 的 `type` / `duration_minutes` 约束 |
| 「添加服务员按钮做什么」 | 在开单弹窗增加一个入口，打开「选择服务人员」弹窗：按分类 / 状态筛人 → 选中一位服务员 → 在服务项表格点「添加」→ 把该服务项连带执行人加入购物车。它和原有「先加明细再选人」并存，后者仍可逐行改派 |
| 「多人协作一项服务怎么办」 | 仍按 ADR-0023：一条服务明细只有一位执行人，多人协作要拆成多条明细；弹窗允许多次「添加」同一项目，形成多行 |
| 「非服务项能否通过弹窗添加」 | **不能**。弹窗表格只列 `type='service' AND active=true` 的目录项；商品仍走开单弹窗原有的项目下拉框，且不绑定服务员 |
| 「是否做绩效 / 计时」 | **不做**。参考图中的归属人工号 / 计时类字段不在本期范围；不建服务实例、不算绩效、不改 `service_remaining_minutes` |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 分类存放 | `staff_categories` 表 + `staff.category_id` 标量外键 | `staff.category_code text` 自由文本 | 自由文本无外键、无店级唯一、无法直接展示分类名；违反 SQL 优先与 ADR-0003 |
| 一人多分类 | 不做，标量一列 | 关联表 `staff_categories` M:N | 业务明确「只能一个分类」；M:N 会让开单选人筛选语义含糊，且与资质 M:N 混淆 |
| 分类归属 | 店内 B 类，带 `shop_id` + 复合外键 | 全局 A 类 | 分类是门店自己的编组方式；全局化会迫使所有门店共用同一套分类，违反 ADR-0003 的单店隔离 |
| 服务项分组 | `catalog_items.category_id` 指向既有全局 `catalog_categories` | `attributes->>'category'` 自由 JSON | JSON 无外键、无联表约束、无法排序；目录分类表已存在，补一列即可 |
| 选人弹窗数据 | 复用 `GET /staff` + `GET /catalog-items` + `GET /catalog-categories`，一次取回后前端筛选 | 新增专用查询端点 | 数据量小且三份数据本就用于开单；避免 N+1 与第四套投影 |
| 添加动作 | 选人后点服务项「添加」，直接进购物车并带 `staff_public_id` | 只把人填到表单顶部，不改变明细 | 无法表达「一位服务员连续做多个项目」，也仍要逐条手选，收益有限 |
| 状态筛选 | 前端按 `executor_state` 过滤展示 | 服务端加 `state` 查询参数 | 状态值是展示性筛选，不是授权边界；客户端过滤零新增契约面 |

## 决策

### 1. 数据模型（迁移 `00013`，schema 12 → 13）

```sql
CREATE TABLE staff_categories (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  code       text        NOT NULL,
  name       text        NOT NULL,
  sort_order integer     NOT NULL DEFAULT 0,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_sc_public_id UNIQUE (public_id),
  CONSTRAINT uq_sc_shop_code  UNIQUE (shop_id, code),
  CONSTRAINT uq_sc_shop_name  UNIQUE (shop_id, name),
  CONSTRAINT uq_sc_shop_id    UNIQUE (shop_id, id),
  CONSTRAINT ck_sc_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_sc_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_sc_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_sc_shop_order ON staff_categories (shop_id, status, sort_order, code);

ALTER TABLE staff
  ADD COLUMN category_id bigint,
  ADD CONSTRAINT fk_staff_category FOREIGN KEY (shop_id, category_id)
    REFERENCES staff_categories (shop_id, id);
CREATE INDEX idx_staff_shop_category ON staff (shop_id, category_id)
  WHERE category_id IS NOT NULL;

-- 目录项沿用全局分类表；只做弹窗分组，不扩张分类维护面。
ALTER TABLE catalog_items
  ADD COLUMN category_id bigint REFERENCES catalog_categories(id);
CREATE INDEX idx_catalog_items_category ON catalog_items (category_id)
  WHERE category_id IS NOT NULL;
```

- `staff.category_id` 可空，删除分类时先把引用它的服务员置空再删除；不级联删除服务员。
- 复合外键 `(shop_id, category_id)` 在数据库层禁止跨店挂分类，`catalog_items.category_id` 指向全局 A 类目录分类，不需要 `shop_id`。
- 分类 `status='inactive'` 时不可新绑定；已绑定的服务员保留分类引用，只在选人弹窗中不再作为可筛选标签展示。

### 2. 端点与投影

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/shops/{shopId}/staff-categories` | `queue.read` | 返回本店全部分类（含 `status`），按 `sort_order, code` 排序；一次取回供服务人员页与开单选人弹窗共用 |
| POST | `/staff-categories` | `queue.write` | `{code,name,sort_order?}`；`code` / `name` 店内唯一，写带 `Idempotency-Key`，写审计 |
| PATCH | `/staff-categories/{categoryId}` | `queue.write` | `{name?,sort_order?,status?,version}`；`status='inactive'` 只停止新绑定，不回写服务员 |
| PATCH | `/staff/{staffId}` | `queue.write` | 请求体增量 `category_public_id?: string \| null`；`null` 清空，非本店 / 不存在 `404`，停用 `409`；沿用既有 `version` 乐观锁 |
| GET | `/staff`、`/staff/{staffId}` | `queue.read` | 增量返回 `category: {public_id,code,name} \| null` |
| GET | `/catalog-items` | `catalog.read` | 增量返回 `category_public_id` / `category_name`（可空） |

- 不新增「按分类查询服务员」端点：`GET /staff` 已经返回分类，弹窗在前端做筛选，避免新增契约面。
- 分类维护端点与 ADR-0023 的资质端点一样，写操作要求 `Idempotency-Key`；幂等重放返回首次结果。

### 3. 错误码

新增两个冻结错误码，其余复用既有映射：

| 码 | HTTP | 含义 |
|---|---|---|
| `STAFF_CATEGORY_NOT_FOUND` | 404 | 分类不存在或不属于本店 |
| `STAFF_CATEGORY_INACTIVE` | 409 | 分类已停用，不能新绑定 |

- 分类 `code` / `name` 重复沿用既有 `409 CONFLICT`。
- 并发修改分类或服务员沿用 `409 VERSION_CONFLICT`。
- 选人弹窗里给未达资质服务员点「添加」时，服务端仍按 ADR-0023 返回 `422 STAFF_NOT_QUALIFIED`，前端提前禁用按钮，不作为新错误码。

### 4. 开单辅助选人流程

1. 开单弹窗「明细」区新增 **添加服务员** 按钮，与原有项目下拉框并列。
2. 点击后打开「选择服务人员」弹窗：
   - 顶部：服务人员分类标签（含「全部」）+ 调度状态标签（含「全部」）；状态标签使用通用状态名，不引入行业词。
   - 中部：服务人员卡片网格，显示姓名、工号、分类、状态；点击卡片即选中，单选。
   - 下部：服务项分类标签（来自 `GET /catalog-categories`）+ 只读表格（编号 / 项目名 / 持续时间 / 价格 / 操作）。
   - 未选服务员时「添加」禁用；已选但无 ADR-0023 资质时「添加」禁用并给出提示。
3. 点击某服务项的「添加」：向购物车追加一条服务明细，`staffId` 预填为当前选中服务员；弹窗不关闭，可连续添加多条。
4. 关闭弹窗后，购物车表格仍保留 ADR-0023 的逐行「执行服务员」下拉框，可再次改派；缺人禁用提交规则不变。
5. 该入口只加服务项；商品项仍走开单弹窗原项目下拉框，且不绑定服务员。

### 5. 明确不做

不做分类 proficiency / 等级、分类自动派单、分类级权限、跨店分类复用、分类计时与绩效规则、服务开始 / 结束实例、参考图中按业务状态细分的服务项模式标签、参考图中的归属人工号字段。以上任一需求须另立 ADR。

## 后果与限制

- 迁移 `up → down-to 12 → up` 必须通过；`down` 先删两张新索引与 `staff.category_id` 约束 / 列，再删 `catalog_items.category_id`，最后删 `staff_categories` 表。
- `EXPECTED_SCHEMA_VERSION` 升为 **13**；`.env.example`、`scripts/test.sh`、README 与 `/ready` 校验同步。
- 存量服务员 `category_id` 为 `NULL`；存量目录项 `category_id` 为 `NULL`，不影响既有列表与开单。
- 分类仅用于筛选与展示，不能替代 ADR-0023 资质校验；前端必须用同一份 `staff.service_item_ids` 判断可添加性。
- 公开仓库里所有界面文案与测试数据使用通用词，不出现参考图中的行业专属称谓。

## 验收标准

1. 迁移 `up → down-to 12 → up` 无残留；`/ready` 在 `EXPECTED_SCHEMA_VERSION=13` 返回 200，设为 12 返回 503。
2. 创建两个分类，给不同服务员各绑定一个；重复绑定同一服务员后只剩最后一个分类；跨店分类赋值为 `404`。
3. 停用分类后不能新绑定，已绑定服务员仍能列出分类。
4. `GET /staff` / 详情返回 `category`；`GET /catalog-items` 返回 `category_public_id` / `category_name`。
5. 开单弹窗点击「添加服务员」能打开选人弹窗；按分类 / 状态筛选生效；选中人后点服务项「添加」，购物车新增带执行人的服务行；未选人或无资质时按钮禁用。
6. 同一服务项连续添加两次生成两条明细，各自可改派；商品项不会出现在选人弹窗表格中。
7. 所有写端点幂等重放返回首次结果，版本冲突返回 `409`；写审计与 Outbox 不变。
8. `gofmt` / `go vet` / `go test`、前端 typecheck / lint / test / build 全绿；脱钩词扫描零命中。

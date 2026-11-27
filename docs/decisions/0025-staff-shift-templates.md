# ADR-0025 · 班次模板与一人一班

- 状态：Accepted（2026-11-27）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离、复合外键锁同店）、[ADR-0004](0004-scope-baseline.md)（统一东八区业务日、无门店级时区列）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0009](0009-queues-and-staff.md) / [ADR-0012](0012-scheduling-engine.md) / [ADR-0015](0015-staff-state-and-position-controls.md)（服务人员与调度状态基线）、[ADR-0013](0013-payment-and-refund-ownership.md)（订单域 `shifts` 是营业班次实例）、[ADR-0024](0024-staff-categories-and-assisted-order-entry.md)（服务人员分类与选人入口）
- 影响分册：新增 `design/17-staff-shift-templates.md`；修订 `design/01` §2、`design/02` §2/§4、`design/03` §3、`design/06` F-01 阶段 B、`design/10` §2/§4、`design/16` §3.2、`design/00` §变更规则、`ARCHITECTURE.md`、README / CHANGELOG 与 `porch-console` 前端 ADR-0009
- 迁移：新增 `migrations/00014_staff_shift_templates.sql`；`EXPECTED_SCHEMA_VERSION` 由 13 → **14**

## 背景

用户要求：「添加班次管理功能，一个服务人员只能加入一个班次」，并给出班次管理列表参考图：列为班次名、上班时间、下班时间、前置上班打卡时间、次日标记、操作（编辑 / 删除 / 分配服务人员）。

按 ADR-0008，schema、端点与错误语义变化必须先有 ADR / 设计来源；本 ADR 即该来源。它是冻结后的**第十七次受控扩展**，不推翻 ADR-0001–0024 的任何条款：隔离仍只有 `shop_id` 一层，服务人员仍是单店雇员，目录 / 资质 / 分类 / 订单 / 卡 / 房间 / 同行组 / 支付 / 退款 / 调度状态机全部不变。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| 「班次管理」 | 维护门店自定义的**班次模板**（早 / 中 / 晚等），是店内 B 类数据，带 `shop_id`；不是营业班次实例，也不是排班表 |
| 与订单域 `shifts` 的区别 | `shifts`（迁移 `00010`，ADR-0013）是「某天开的一次班」实例，有 `shift_no` / `opened_at` / `closed_at` / `status`，全店同时最多一个 open；本 ADR 的 `shift_templates` 是每天复用的时间模板，可有多条、可被多个服务人员引用，二者互不读写 |
| 「一个服务人员只能加入一个班次」 | `staff.shift_template_id` 是标量外键，不是关联表；天然「至多一个」，重新分配即**替换**，可清空。数据库用复合外键 `(shop_id, shift_template_id)` 锁同店 |
| 「一个班次可以有多人吗」 | 可以。`shift_templates` 与 `staff` 是 1:N；「一人一班」约束的是服务人员侧，不限制班次人数 |
| 「分配服务人员」 | 班次管理页的操作按钮，打开分配弹窗，全量勾选本店服务人员后提交；服务端在同一事务内先解绑本班次旧成员，再绑定新成员，并写审计。一个服务人员若已在别的班次，会被自动换到当前班次 |
| 「前置上班打卡时间」 | 相对上班时间的提前分钟数，整数 `0..1440`，默认 0；只作模板属性展示，本期不做打卡记录、提醒或考勤判定 |
| 「次日是否打头牌」 | 参考图中的行业字段，公开仓库统一为只读布尔标记 `next_day_flag`，含义是「该班次是否跨次日标记」。不实现任何头牌、排位或绩效规则，不参与调度引擎与金额 |
| 「班次时间」 | 每日重复的墙上时间 `time`（`HH:MM`），存本地业务时间而非 `timestamptz`；全局仍是 UTC+8 业务日（ADR-0004），不新增门店级时区列、不接受客户端时区参数 |
| 「班次是否等于调度状态 / 分类 / 资质 / 权限」 | **不等于**。`duty_status` / `executor_state`（ADR-0010 / 0012 / 0015）是调度态，`staff_categories`（ADR-0024）是展示分类，`staff_service_items`（ADR-0023）是资质，RBAC 是权限；班次模板不读写其中任何一项，不自动派单、不改金额 |
| 「是否需要新增权限点」 | **不需要**。沿用 `queue.read` / `queue.write`；管理员与店长全给，服务人员只读 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 与营业班次实例的关系 | 新表 `shift_templates`，与 `shifts` 并存 | 给 `shifts` 加上下班时间字段复用 | 实例是一次开班事件且全店同时最多一个 open；复用会让「早 / 中 / 晚」模板与当天开班记录混在一张表，唯一索引与状态机互相冲突 |
| 一人一班约束 | `staff.shift_template_id` 标量 + 复合外键 | 关联表 + 唯一 `(shop_id, staff_id)` | 标量天然保证至多一个，关联表要多维护一张表与事务，且语义与「队列 M:N」（`queue_staff`）混淆 |
| 分配方向 | `PUT /shift-templates/{id}/staff` 全量替换 | 逐人 `PATCH /staff/{staffId}` | 全量勾选一次提交，天然把换班与解绑放在一个事务；逐人请求会产生中间态，且截图入口是「给班次分配人员」 |
| 时间列类型 | `time`（墙上时间） | `timestamptz` | 模板每天重复，不对应某一时刻；存 timestamp 会引入无意义日期与夏令时问题，而 ADR-0004 已固定 UTC+8 |
| 跨店引用 | `(shop_id, shift_template_id)` 复合外键 | 仅 `shift_template_id` 外键 | 单栏外键无法阻止给 A 店服务员挂 B 店班次；复合外键在 DB 层锁死同店（ADR-0003） |
| 删除班次 | 同事务解绑成员再删模板 | 有成员即 409 拒绝 | 参考图有删除按钮；解绑后服务员变为「未分配班次」，不会丢失人员档案或历史记录 |

## 决策

### 1. 数据模型（迁移 `00014`，schema 13 → 14）

```sql
CREATE TABLE shift_templates (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  name        text        NOT NULL,
  sort_order  integer     NOT NULL DEFAULT 0,
  start_time  time        NOT NULL,
  end_time    time        NOT NULL,
  check_in_lead_minutes integer NOT NULL DEFAULT 0,
  next_day_flag boolean   NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  version     integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_st_public_id UNIQUE (public_id),
  CONSTRAINT uq_st_shop_name  UNIQUE (shop_id, name),
  CONSTRAINT uq_st_shop_id    UNIQUE (shop_id, id),
  CONSTRAINT ck_st_name  CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_st_lead  CHECK (check_in_lead_minutes BETWEEN 0 AND 1440)
);
CREATE INDEX idx_st_shop_order ON shift_templates (shop_id, sort_order, name);

ALTER TABLE staff
  ADD COLUMN shift_template_id bigint,
  ADD CONSTRAINT fk_staff_shift_template FOREIGN KEY (shop_id, shift_template_id)
    REFERENCES shift_templates (shop_id, id);
CREATE INDEX idx_staff_shop_shift_template ON staff (shop_id, shift_template_id)
  WHERE shift_template_id IS NOT NULL;
```

- `name` 店内唯一；`sort_order` 控制展示顺序。不引入 `code`：参考图只维护班次名，少一个必填字段。
- `staff.shift_template_id` 可空；删除班次模板前先 `UPDATE staff SET shift_template_id=NULL`，不级联删除服务人员。
- 不设 `status` 列：班次模板停用需求未提出，删除即移除；若将来需要停用另立 ADR。
- 存量服务人员 `shift_template_id` 为 `NULL`，不影响既有列表、队列、分类、资质与开单。

### 2. 端点与投影

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/shops/{shopId}/shift-templates` | `queue.read` | 返回本店全部模板，按 `sort_order, name` 排序；一次取回供管理页与选人视图共用 |
| POST | `/shift-templates` | `queue.write` | `{name,start_time,end_time,check_in_lead_minutes?,next_day_flag?,sort_order?}`；时间格式 `HH:MM`，写带 `Idempotency-Key`，写审计 |
| PATCH | `/shift-templates/{templateId}` | `queue.write` | 上述字段可空增量 + `version` 乐观锁；同时校验新旧时间格式 |
| DELETE | `/shift-templates/{templateId}` | `queue.write` | 同事务解绑成员、写审计、删除模板 |
| PUT | `/shift-templates/{templateId}/staff` | `queue.write` | `{staff_public_ids: string[], version}` 全量替换成员；非本店人员 `404`，写审计 |
| GET | `/staff`、`/staff/{staffId}` | `queue.read` | 增量返回 `shift_template: {public_id,name,start_time,end_time} \| null` |

- 所有端点要求 `X-Shop-Id`；写操作要求 `Idempotency-Key`，由通用幂等中间件缓存首次结果。
- 不新增「按班次查询服务员」端点：`GET /staff` 已带 `shift_template`，由前端筛选。
- `GET /shifts/current` 等营业班次实例端点保持独立，不受本 ADR 影响。

### 3. 错误码

新增一个冻结错误码，其余复用既有映射：

| 码 | HTTP | 含义 |
|---|---|---|
| `SHIFT_TEMPLATE_NOT_FOUND` | 404 | 班次模板不存在或不属于本店 |

- 模板名重复沿用 `409 CONFLICT`；并发修改模板或分配成员沿用 `409 VERSION_CONFLICT`。
- 时间格式 / 范围非法沿用 `400 INVALID_ARGUMENT`，带 `details.field`。
- 分配里的服务人员 ID 不存在或跨店返回既有 `404 NOT_FOUND`，不新增错误码。

### 4. 分配流程

1. 管理页「分配服务人员」打开弹窗，一次拉取 `GET /staff`，勾选项显示姓名、工号、当前班次。
2. 全量勾选后提交 `PUT /shift-templates/{id}/staff`，携带模板 `version`。
3. 服务端在事务内：解析人员 ID（复合条件 `shop_id + public_id`）→ 检查模板 `version` → 解绑本班次旧成员 → 绑定新成员 → 写一条 `shift_template_staff_assigned` 审计（payload 含前后成员 public_id 列表）→ 返回最新模板。
4. 任何服务人员被换到别的班次时，旧引用随 `UPDATE staff SET shift_template_id=$new` 自动替换；一人一班永不破坏。
5. 删除模板先解绑所有成员再删，审计记录被解绑人员。

### 5. 明确不做

不做打卡记录与迟到判定、不做跨日头牌 / 排位 / 绩效规则、不做排班日历与自动生成营业班次实例、不做班次数控上限与人员资质联动、不做门店级时区设置、不做班次级权限。以上任一需求须另立 ADR。

## 后果与限制

- 迁移 `up → down-to 13 → up` 必须通过；`down` 先删索引与 `staff.shift_template_id` 约束 / 列，再删 `shift_templates` 表。
- `EXPECTED_SCHEMA_VERSION` 升为 **14**；`.env.example`、`scripts/test.sh`、README 与 `/ready` 校验同步。
- 参考图里的行业称谓在公开仓库统一写为「服务人员」「班次」「次日标记」，避免脱钩词扫描命中。
- 分配是全量替换语义：前端必须回显当前成员，漏传即解绑；服务端不猜测增量。

## 验收标准

1. 迁移 `up → down-to 13 → up` 无残留；`EXPECTED_SCHEMA_VERSION=14` 时 `/ready` 200，设为 13 时 503。
2. 创建两个班次，把同一服务员先后分配到两个班次后只保留最后一个；跨店模板 ID 分配返回 `404`。
3. 删除有成员的班次后，服务员 `shift_template` 变为 `null`，人员档案仍在；审计可查到解绑动作。
4. `GET /staff` 与详情返回 `shift_template`；时间按 `HH:MM` 输出，`next_day_flag` 为布尔。
5. 管理页列表字段与参考图一致（班次名 / 上班时间 / 下班时间 / 前置上班打卡时间 / 次日标记 / 编辑 / 删除 / 分配服务人员），新增 / 编辑 / 删除 / 分配全链路可用。
6. 所有写端点幂等重放返回首次结果，版本冲突返回 `409`。
7. `gofmt` / `go vet` / `go test`、前端 typecheck / lint / test / build 全绿；脱钩词扫描零命中。

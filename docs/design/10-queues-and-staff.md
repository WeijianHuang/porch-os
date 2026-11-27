# 前期系统架构详细设计 · 10 队列与服务人员

> 上游：[ADR-0002](../decisions/0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0009](../decisions/0009-queues-and-staff.md)（首次受控扩展）、[ADR-0010](../decisions/0010-duty-status-and-queue-order.md)（在岗状态与队列内顺序，第二次受控扩展）。本册与 `00`–`09` 同为契约基线。

## 1. 需求与范围

一门店多队列；队列有**序号**与**名称**（门店内各自唯一）；服务人员归属单一门店，可在**本店多个队列**中服务；服务人员有当前在岗 / 下班状态；队列内人员顺序可显式调整；前端主页默认展示队列页。

| 在范围内 | 不在范围内 |
|---|---|
| 队列定义（序号 / 名称 / 启停）的增删改查 | 叫号实例、排队号、等待队列状态机 |
| 服务人员建档、启停与在岗 / 下班状态切换 | 跨店复用服务人员（须新 ADR） |
| 人员 ↔ 队列的店内多对多分配 | 考勤流水、排班、班次、按队列的业绩 / 绩效规则 |
| 队列内人员顺序（密集 1..n）与全量原子重排 | 通过重排端点增减队列成员 |
| 主页默认队列视图 | 打印叫号小票（ADR-0004 不打印） |

## 2. 领域模型（概念层）

- **Queue（队列）**：B 类门店实体。字段：`public_id`、`shop_id`、`seq`（正整数）、`name`、`status`（`active` / `inactive`）、`version`、`created_at`、`updated_at`。
- **Staff（服务人员）**：B 类门店实体。字段：`public_id`、`shop_id`、`code`（工号，店内唯一）、`display_name`、`status`（`active` / `inactive`）、`duty_status`（`on` / `off`，当前在岗状态位）、`version`、`created_at`、`updated_at`。
- **QueueStaff（人员-队列分配）**：纯关系，复合主键 `(queue_id, staff_id)`，带 `shop_id`、`joined_at`、`position`（队列内密集 1..n 顺序）。不是聚合根，不对外暴露独立 ID；同一人员在不同队列可有不同位置。

不变量（领域纯函数可脱库单测，见 `internal/domain/queue`）：

1. `seq > 0`，`name` 去空白后非空；
2. 同一门店内 `seq` 不重复、`name` 不重复；
3. 分配关系两端必须同 `shop_id`（数据库复合外键兜底）；
4. 同一 `(queue_id, staff_id)` 重复分配幂等，不改变既有位置；
5. 队列 / 服务人员停用后不再接受新分配，已有分配保留；
6. `duty_status` 与 `status` 独立，但 `status=inactive` 时不得置为 `on`；状态是当前位，不写历史、不按日自动重置；
7. `position` 在同一队列内唯一且密集为 `1..n`；新增追加到队尾，移出后剩余成员压缩；
8. 重排请求必须是当前成员的全量有序列表，不得增减成员或重复。

## 3. 数据库设计

迁移 `00003_queues_and_staff.sql`（结构）、`00004_queue_staff_permissions.sql`（权限点）、`00005_duty_status_and_queue_order.sql`（在岗状态与队列内顺序，含 v4 数据回补）。`EXPECTED_SCHEMA_VERSION` 相应为 `5`。

```sql
CREATE TABLE queues (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  seq        integer     NOT NULL,
  name       text        NOT NULL,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_queues_public_id UNIQUE (public_id),
  CONSTRAINT uq_queues_shop_seq  UNIQUE (shop_id, seq),
  CONSTRAINT uq_queues_shop_name UNIQUE (shop_id, name),
  CONSTRAINT uq_queues_shop_id   UNIQUE (shop_id, id),   -- 供 queue_staff 复合外键引用
  CONSTRAINT ck_queues_seq    CHECK (seq > 0),
  CONSTRAINT ck_queues_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_queues_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_queues_shop_order ON queues (shop_id, seq);

CREATE TABLE staff (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  shop_id      bigint      NOT NULL REFERENCES shops(id),
  code         text        NOT NULL,
  display_name text        NOT NULL,
  status       text        NOT NULL DEFAULT 'active',
  duty_status  text        NOT NULL DEFAULT 'off',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  version      integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_staff_public_id UNIQUE (public_id),
  CONSTRAINT uq_staff_shop_code UNIQUE (shop_id, code),
  CONSTRAINT uq_staff_shop_id   UNIQUE (shop_id, id),
  CONSTRAINT ck_staff_code CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_staff_name CHECK (length(btrim(display_name)) > 0),
  CONSTRAINT ck_staff_status CHECK (status IN ('active','inactive')),
  CONSTRAINT ck_staff_duty CHECK (duty_status IN ('on','off'))
);
CREATE INDEX idx_staff_shop_name ON staff (shop_id, display_name);

CREATE TABLE queue_staff (
  shop_id   bigint      NOT NULL,
  queue_id  bigint      NOT NULL,
  staff_id  bigint      NOT NULL,
  joined_at timestamptz NOT NULL DEFAULT now(),
  position  integer     NOT NULL,
  PRIMARY KEY (queue_id, staff_id),
  CONSTRAINT uq_queue_staff_position UNIQUE (queue_id, position),
  CONSTRAINT fk_queue_staff_queue FOREIGN KEY (shop_id, queue_id) REFERENCES queues (shop_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_queue_staff_staff FOREIGN KEY (shop_id, staff_id) REFERENCES staff (shop_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_queue_staff_shop_staff ON queue_staff (shop_id, staff_id, queue_id);
```

- `uq_queues_shop_id` / `uq_staff_shop_id` 是 PG 复合外键的必要引用键，不是业务唯一约束；与 `uq_queues_shop_seq` 等并行存在。
- `ON DELETE CASCADE` 让删除队列 / 服务人员时关联自动清理，应用无需先显式解绑（ADR-0009 事务内解绑为等价实现）。
- `00005` 在 v4 数据上按 `queue_id` 分区、以 `joined_at, staff_id` 顺序回补 `position = row_number()`；新增列不带业务默认值，避免全表出现重复位置。
- `position` Unique 约束与复合外键一起保证「同店 + 同队列 + 不重复位置」；重排 / 移出时先用负数临时值避开唯一键冲突，再写最终密集位置。
- 所有读查询以 `shop_id` 前导命中索引；不建视图、不用 RLS、不加 `tenant_id`（ADR-0002 / 0003）。

## 4. 接口契约

`{shopId}` / `{queueId}` / `{staffId}` 均为 `public_id` 字符串。所有端点带 `X-Shop-Id`；写操作带 `Idempotency-Key`；响应沿用统一 `{data, meta, page?}` 信封。

| 方法与路径 | 说明 | 状态码 |
|---|---|---|
| `GET /api/v1/shops/{shopId}/queues` | 队列列表，按 `seq` 升序 | 200 |
| `POST /api/v1/shops/{shopId}/queues` | 新建：`{seq, name}` | 201 |
| `GET /api/v1/shops/{shopId}/queues/{queueId}` | 队列详情（含服务人员摘要） | 200 / 404 |
| `PATCH /api/v1/shops/{shopId}/queues/{queueId}` | 修改：`{name?, seq?, status?, version}` | 200 / 409 |
| `DELETE /api/v1/shops/{shopId}/queues/{queueId}` | 删除队列并解绑 | 204 |
| `GET /api/v1/shops/{shopId}/staff` | 服务人员列表 | 200 |
| `POST /api/v1/shops/{shopId}/staff` | 建档：`{code, display_name}` | 201 |
| `GET /api/v1/shops/{shopId}/staff/{staffId}` | 人员详情（含所在队列列表） | 200 / 404 |
| `PATCH /api/v1/shops/{shopId}/staff/{staffId}` | 修改：`{display_name?, status?, duty_status?, version}`；`inactive` 不得置 `on` | 200 / 400 / 409 |
| `DELETE /api/v1/shops/{shopId}/staff/{staffId}` | 删除人员并解绑 | 204 |
| `GET /api/v1/shops/{shopId}/queues/{queueId}/staff` | 队列内服务人员（按 `position` 升序） | 200 |
| `PUT /api/v1/shops/{shopId}/queues/{queueId}/staff/{staffId}` | 加入队列（追加到队尾，幂等） | 204 |
| `DELETE /api/v1/shops/{shopId}/queues/{queueId}/staff/{staffId}` | 移出队列（剩余位置压缩，幂等） | 204 |
| `PUT /api/v1/shops/{shopId}/queues/{queueId}/staff/order` | 全量重排：`{staff_ids, version}`，回传新队列详情 | 200 / 400 / 409 |

错误码复用冻结表（`design/03` §4）：越店 / 无权限 `403 PERMISSION_DENIED`、缺 `X-Shop-Id` `400 SHOP_CONTEXT_REQUIRED`、不存在 `404 NOT_FOUND`、序号或名称冲突 `409 CONFLICT`、版本冲突 `409 VERSION_CONFLICT`、停用后分配 `409 PRECONDITION_FAILED`。**不新增错误码**。

扁平 DTO（无嵌套实体图）：

- `Queue`：`public_id / seq / name / status / version / created_at / updated_at`；详情额外带按 `position` 升序的 `staff: StaffRef[]`。
- `Staff`：`public_id / code / display_name / status / duty_status / version / created_at / updated_at`；详情额外带 `queues: QueueRef[]`。
- `StaffRef`：`public_id / display_name / duty_status / position`；`QueueRef`：`public_id / name / seq`（仅摘要，不嵌实体图）。

## 5. 事务与幂等

- 写操作走 `IdempotentWrite`（业务写入与幂等记录同事务提交，DP-07）；`PUT .../staff/{staffId}` 的重复分配返回同一 `204`，不改变位置。
- 版本号 `version` 用于 `PATCH` 乐观锁：`UPDATE ... WHERE id=$1 AND shop_id=$2 AND version=$5`，影响 0 行 → `409 VERSION_CONFLICT`；新增的 `PATCH duty_status` 与队列重排也沿用此规则。
- 分配写在同一事务内先由 `queue_staff` 复合主键 / 复合外键校验，再插关联；跨店分配被外键直接拒绝，映射为 `403`（不泄露数据库细节）。
- 全量重排在同一事务内校验「请求 ID 集合 == 当前成员集合」→ 先写负数临时位置 → 再写 `1..n` 最终位置并 `queues.version=version+1`；重复 ID、未知 ID、增减成员均为 `400 INVALID_ARGUMENT`。
- 移出成员后压缩位置同样遵循「负数临时值 → 最终位置」，避免与 `UNIQUE (queue_id, position)` 冲突。

## 6. 关键流程

**F-10 主页队列加载**：登录 → 前端跳转 `/queues` → `GET /me/shops` 取可见集合与默认门店 → 带 `X-Shop-Id` 拉 `GET /shops/{shopId}/queues`（按 `seq`）→ 每条队列展示序号、名称与服务人员数量。切换门店按 ADR-0006 取消在途、失效缓存、重拉权限与队列。

**F-11 服务人员多队列分配**：打开人员详情 → 展示其所在队列（`queues` join `queue_staff`）→ 勾选本店其它队列 → `PUT .../queues/{queueId}/staff/{staffId}` → 成功后本地失效该人员的 `queues` 缓存与相关队列的 `staff` 缓存。移出用 `DELETE`，同样幂等，并在同事务内压缩剩余位置。

**F-12 在岗 / 下班切换**：服务人员页或队列卡片触发 → `PATCH .../staff/{staffId}` 带 `{duty_status, version}` → 后端拒绝 `inactive + on` → 成功递增 `staff.version`，前端失效本店 `staff` 与所有队列详情（在岗标记随详情返回）。切换只改当前状态位，不写历史、不自动跨日重置。

**F-13 队列内重排**：拖动卡片或点上移 / 下移 → 前端按当前展示顺序生成全量 `staff_ids`，带上详情中的 `version` → `PUT .../queues/{queueId}/staff/order` → 成功回传新 `QueueDetail` 并失效缓存；`409 VERSION_CONFLICT` 时回滚本地顺序、重取详情并提示「并发冲突，请重读后重试」。下班人员照常参与排序，只是卡片弱化显示。

## 7. 可观测与测试

- 指标沿用 `design/07` 的通用 HTTP / DB 指标，不新增队列专属指标。
- 单测（后端）：序号 / 名称校验、在岗状态校验、`inactive + on` 拒绝、按 `position` 折叠 `StaffRef`、重排列表完整性校验；前端：队列排序、人员多选状态、在岗标记与本地重排回滚。
- 集成测试（PG 18.7）：一店两队列同序号被拒、不同店同序号允许、跨店分配被外键拒绝、重复分配幂等不改变位置、移出后位置压缩、重排唯一键不冲突、删除队列级联解绑、迁移 up/down/up 与 v4→v5 回补。
- 冒烟：登录后主页落地 `/queues`、建队列（序号+名称）、建人员、一人加入两队列、在岗切换后详情带 `duty_status`、按 `staff_ids` 重排后顺序生效、旧 `version` 重排 409、越店请求 403。

## 8. 通用化边界

只用通用词 `queue` / `staff` / `duty_status` / `position`；不含排队号、叫号、业绩绩效、考勤流水、排班、小票等业务规则。与既有冻结契约一致：单币种 CNY、UTC+8（时间字段 `timestamptz` / UTC 传输）、不归档、不打印、不离线、无门店组、无 tenant、当前门店请求期上下文。

## 9. ADR-0024 增补：服务人员分类

`staff_categories` 是店内分类定义（`shop_id` + `code` / `name` 店内唯一），`staff.category_id` 为可空标量外键，一人最多一个分类；复合外键 `(shop_id, category_id)` 在数据库层禁止跨店绑定。分类只用于开单选人筛选与卡片展示，不授予服务能力、不参与 RBAC、不驱动派单；能力仍看 `staff_service_items`（ADR-0023），状态仍看 `duty_status` / `executor_state`（ADR-0010 / 0012 / 0015）。`GET/POST/PATCH /staff-categories` 用 `queue.read` / `queue.write`，写带幂等键与审计；`PATCH /staff/{staffId}` 增 `category_public_id` 可清空；`GET /staff` 与详情增 `category` 投影。停用分类只阻止新绑定，不回写已有服务员。

## 10. ADR-0025 增补：班次模板

`shift_templates` 是店内可复用时间模板（`shop_id` + `name` 店内唯一）。`staff.shift_template_id` 为可空标量外键，一人最多一班，复合外键 `(shop_id, shift_template_id)` 禁止跨店；一个班次可有多名成员。分配端点 `PUT /shift-templates/{id}/staff` 全量替换，`GET /staff` 与详情增量返回 `shift_template`。班次不是调度状态 / 分类 / 资质 / 权限，不驱动派单、不改金额、不产生打卡记录；与订单域营业班次实例 `shifts` 分离。完整模型与流程见 [17 分册](17-staff-shift-templates.md)。

## 11. ADR-0026 增补：工号与别名

`staff.code` 仍是店内唯一业务键，但可在 `PATCH /staff/{staffId}` 中编辑；`public_id` 不可变，历史引用与订单快照不回写。新增 `staff.alias` 是可选、非唯一的展示标签（0–60 字符），只用于卡片/详情展示与列表搜索，不参与授权、资质、分类、班次、计价或报表分组；主名称与快照仍取 `display_name`。重复工号由 `(shop_id, code)` 唯一约束拒绝并映射 `409 STAFF_CODE_CONFLICT`。

返回 [00 总览](00-overview.md) 查看完整分册地图与已关闭的 OQ / NQ 清单。

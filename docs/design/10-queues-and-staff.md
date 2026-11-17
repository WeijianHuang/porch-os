# 前期系统架构详细设计 · 10 队列与服务人员

> 上游：[ADR-0002](../decisions/0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0009](../decisions/0009-queues-and-staff.md)（本领域的范围扩展决策）。本册是 ADR-0008 冻结后的**首次受控扩展**，与 `00`–`09` 同为契约基线。

## 1. 需求与范围

一门店多队列；队列有**序号**与**名称**（门店内各自唯一）；服务人员归属单一门店，可在**本店多个队列**中服务；前端主页默认展示队列页。

| 在范围内 | 不在范围内 |
|---|---|
| 队列定义（序号 / 名称 / 启停）的增删改查 | 叫号实例、排队号、等待队列状态机 |
| 服务人员建档与启停 | 跨店复用服务人员（须新 ADR） |
| 人员 ↔ 队列的店内多对多分配 | 排班、班次、按队列的业绩 / 绩效规则 |
| 主页默认队列视图 | 打印叫号小票（ADR-0004 不打印） |

## 2. 领域模型（概念层）

- **Queue（队列）**：B 类门店实体。字段：`public_id`、`shop_id`、`seq`（正整数）、`name`、`status`（`active` / `inactive`）、`version`、`created_at`、`updated_at`。
- **Staff（服务人员）**：B 类门店实体。字段：`public_id`、`shop_id`、`code`（工号，店内唯一）、`display_name`、`status`、`version`、`created_at`、`updated_at`。
- **QueueStaff（人员-队列分配）**：纯关系，复合主键 `(queue_id, staff_id)`，带 `shop_id`、`joined_at`。不是聚合根，不对外暴露独立 ID。

不变量（领域纯函数可脱库单测，见 `internal/domain/queue`）：

1. `seq > 0`，`name` 去空白后非空；
2. 同一门店内 `seq` 不重复、`name` 不重复；
3. 分配关系两端必须同 `shop_id`（数据库复合外键兜底）；
4. 同一 `(queue_id, staff_id)` 重复分配幂等；
5. 队列 / 服务人员停用后不再接受新分配，已有分配保留。

## 3. 数据库设计

迁移 `00003_queues_and_staff.sql`（结构）、`00004_queue_staff_permissions.sql`（权限点）。

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
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  version      integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_staff_public_id UNIQUE (public_id),
  CONSTRAINT uq_staff_shop_code UNIQUE (shop_id, code),
  CONSTRAINT uq_staff_shop_id   UNIQUE (shop_id, id),
  CONSTRAINT ck_staff_code CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_staff_name CHECK (length(btrim(display_name)) > 0),
  CONSTRAINT ck_staff_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_staff_shop_name ON staff (shop_id, display_name);

CREATE TABLE queue_staff (
  shop_id   bigint      NOT NULL,
  queue_id  bigint      NOT NULL,
  staff_id  bigint      NOT NULL,
  joined_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (queue_id, staff_id),
  CONSTRAINT fk_queue_staff_queue FOREIGN KEY (shop_id, queue_id) REFERENCES queues (shop_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_queue_staff_staff FOREIGN KEY (shop_id, staff_id) REFERENCES staff (shop_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_queue_staff_shop_staff ON queue_staff (shop_id, staff_id, queue_id);
```

- `uq_queues_shop_id` / `uq_staff_shop_id` 是 PG 复合外键的必要引用键，不是业务唯一约束；与 `uq_queues_shop_seq` 等并行存在。
- `ON DELETE CASCADE` 让删除队列 / 服务人员时关联自动清理，应用无需先显式解绑（ADR-0009 事务内解绑为等价实现）。
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
| `PATCH /api/v1/shops/{shopId}/staff/{staffId}` | 修改：`{display_name?, status?, version}` | 200 / 409 |
| `DELETE /api/v1/shops/{shopId}/staff/{staffId}` | 删除人员并解绑 | 204 |
| `GET /api/v1/shops/{shopId}/queues/{queueId}/staff` | 队列内服务人员（按姓名） | 200 |
| `PUT /api/v1/shops/{shopId}/queues/{queueId}/staff/{staffId}` | 加入队列（幂等） | 204 |
| `DELETE /api/v1/shops/{shopId}/queues/{queueId}/staff/{staffId}` | 移出队列（幂等） | 204 |

错误码复用冻结表（`design/03` §4）：越店 / 无权限 `403 PERMISSION_DENIED`、缺 `X-Shop-Id` `400 SHOP_CONTEXT_REQUIRED`、不存在 `404 NOT_FOUND`、序号或名称冲突 `409 CONFLICT`、版本冲突 `409 VERSION_CONFLICT`、停用后分配 `409 PRECONDITION_FAILED`。**不新增错误码**。

扁平 DTO（无嵌套实体图）：

- `Queue`：`public_id / seq / name / status / version / created_at / updated_at`；详情额外带 `staff: StaffRef[]`。
- `Staff`：`public_id / code / display_name / status / version / created_at / updated_at`；详情额外带 `queues: QueueRef[]`。
- `StaffRef` / `QueueRef`：仅 `public_id / name`（`QueueRef` 另带 `seq`）。

## 5. 事务与幂等

- 写操作走 `IdempotentWrite`（业务写入与幂等记录同事务提交，DP-07）；`PUT .../staff/{staffId}` 的重复分配返回同一 `204`。
- 版本号 `version` 用于 `PATCH` 乐观锁：`UPDATE ... WHERE id=$1 AND shop_id=$2 AND version=$5`，影响 0 行 → `409 VERSION_CONFLICT`。
- 分配写在同一事务内先由 `queue_staff` 复合主键 / 复合外键校验，再插关联；跨店分配被外键直接拒绝，映射为 `403`（不泄露数据库细节）。

## 6. 关键流程

**F-10 主页队列加载**：登录 → 前端跳转 `/queues` → `GET /me/shops` 取可见集合与默认门店 → 带 `X-Shop-Id` 拉 `GET /shops/{shopId}/queues`（按 `seq`）→ 每条队列展示序号、名称与服务人员数量。切换门店按 ADR-0006 取消在途、失效缓存、重拉权限与队列。

**F-11 服务人员多队列分配**：打开人员详情 → 展示其所在队列（`queues` join `queue_staff`）→ 勾选本店其它队列 → `PUT .../queues/{queueId}/staff/{staffId}` → 成功后本地失效该人员的 `queues` 缓存与相关队列的 `staff` 缓存。移出用 `DELETE`，同样幂等。

## 7. 可观测与测试

- 指标沿用 `design/07` 的通用 HTTP / DB 指标，不新增队列专属指标。
- 单测（后端）：序号 / 名称校验、默认排序、`QueueRef` 折叠为纯函数；前端：队列排序、人员多选状态。
- 集成测试（PG 18.7）：一店两队列同序号被拒、不同店同序号允许、跨店分配被外键拒绝、重复分配幂等、删除队列级联解绑、迁移 up/down/up。
- 冒烟：登录后主页落地 `/queues`、建队列（序号+名称）、建人员、一人加入两队列、两队列分别返回该人员、越店请求 403。

## 8. 通用化边界

只用通用词 `queue` / `staff`；不含排队号、叫号、业绩绩效、排班、小票等业务规则。与既有冻结契约一致：单币种 CNY、UTC+8（时间字段 `timestamptz` / UTC 传输）、不归档、不打印、不离线、无门店组、无 tenant、当前门店请求期上下文。

返回 [00 总览](00-overview.md) 查看完整分册地图与已关闭的 OQ / NQ 清单。

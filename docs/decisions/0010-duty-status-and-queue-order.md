# ADR-0010: 服务人员在岗状态与队列内顺序

- 状态：已接受（2026-11-17）
- 决策问题：在 ADR-0009 的队列 / 服务人员契约上追加两项通用能力——服务人员的**在岗 / 下班状态**，以及服务人员在**同一队列内的可调整顺序**。明确状态字段、位置约束、端点、并发控制与明确不做的范围
- 影响范围：修订 `staff` / `queue_staff` 两表、新增迁移 `00005`、扩展 `PATCH /staff/{staffId}`、新增队列内排序端点；同步 `design/00` / `02` / `03` / `10`、`ARCHITECTURE.md`、README / CHANGELOG 与前端 `porch-console` 的 ADR-0002
- 上游：[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0009](0009-queues-and-staff.md)（队列 / 服务人员领域）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0006](0006-user-shops-and-current-shop.md)（门店请求期上下文）、[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图）

## 背景

用户要求「服务员还需要上下班状态，并且在队列中可以动态调整服务员的位置」。ADR-0009 已固化服务员归属单店、可在店内多队列服务，但没有表达当前是否在岗，也没有表达同一队列内的先后次序。按 ADR-0008 的冻结后变更规则，schema、端点清单与唯一性范围的改动必须**先有 ADR / 设计来源**，本 ADR 即该来源；它是冻结后的**第二次受控扩展**，不推翻 ADR-0001–0009 的任何条款。

「在岗 / 下班」被理解为**服务人员当前的运营状态**，不是考勤记录、排班或班次。动态位置被理解为**队列内人员卡片的展示与服务顺序**，由同一门店内显式重排决定，不由叫号实例或业务规则隐式推进。

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 在岗状态存放 | `staff.duty_status` 当前状态位，无历史 | 独立 `staff_attendance_events` 追加流水 + 每日汇总 | 需求只要当前状态；流水表引入打卡、补卡、跨日归属与查询聚合，超出通用底座范围 |
| 与雇用状态关系 | `status`（active/inactive）与 `duty_status`（on/off）分列 | 复用 `status` 表示在岗 | 语义不同：停用是人事状态，在岗是当日运营状态；混用会让停用人员「上班」后重新可见 |
| 位置存储 | `queue_staff.position` 整数，队列内密集 1..n，唯一 `(queue_id, position)` | `position` 存 JSON 数组 / 仅前端排序 | 数组无法约束唯一性、无法做 SQL 订单查询；纯前端排序不能跨副本一致，也不可幂等 |
| 重排接口 | 全量有序 `staff_ids` + `version`，事务内原子替换 | 逐个 `PATCH /staff/{staffId}/position` | 逐条更新会出现中间态与唯一键冲突，也难以保证「恰好是当前成员」的完整性 |
| 并发控制 | 复用 `queues.version` 乐观锁，409 `VERSION_CONFLICT` | 新增 `order_version` 列 | 重排改变队列的人员构成， bumps 队列版本语义一致；避免再加一个版本号与前端协议 |

## 决策

### 1. 在岗状态

- `staff` 新增 `duty_status text NOT NULL DEFAULT 'off'`，`CHECK (duty_status IN ('on','off'))`。新档案默认下班；`on` = 在岗，`off` = 下班。
- `duty_status` 与 `staff.status`（`active` / `inactive`）**相互独立**：停用人员不得在岗，应用写入时拒绝 `status=inactive && duty_status=on`（`400 INVALID_ARGUMENT`）；数据库不加跨列 CHECK，以免迁移与历史数据回补产生不可控失败，校验放在领域纯函数与 handler。
- 状态是**当前位**，不按 UTC+8 自然日自动重置，不写历史、不排班、不打卡。
- `PATCH /api/v1/shops/{shopId}/staff/{staffId}` 的请求体扩展为 `{display_name?, status?, duty_status?, version}`；至少提供一个可改字段，`version` 必填并沿用 `409 VERSION_CONFLICT`。`duty_status` 变更同样递增 `staff.version`。
- `Staff` 与队列详情中的 `StaffRef` 都带 `duty_status`；下班人员**仍在队列中、仍计入 `staff_count`**，前端以弱化样式展示，不静默隐藏。

### 2. 队列内顺序

- `queue_staff` 新增 `position integer NOT NULL`，`UNIQUE (queue_id, position)`；同一队列内位置为**密集 1..n**，不同队列可重复，不同人员在同一队列的位置唯一。
- 加入队列：`position = max(position)+1`；移出队列：在**同一事务**内把剩余成员压回 1..n（先用负数临时值避开唯一键冲突，再写最终值）。重复加入仍为幂等 no-op，不改变既有位置。
- 新增 `PUT /api/v1/shops/{shopId}/queues/{queueId}/staff/order`，请求体 `{staff_ids: string[], version: number}`：
  - `staff_ids` 必须是**该队列当前成员的全量有序列表**（`public_id`），不得新增 / 删除成员、不得重复；
  - `version` 取队列详情中的 `queues.version`，成功递增并回传新的 `QueueDetail`；不匹配返回 `409 VERSION_CONFLICT`；
  - 写入在同一事务内完成，沿用 `Idempotency-Key` 与请求指纹；同键重放返回首次结果；
  - 权限仍为 `queue.write`，不新增权限点；错误码不新增。
- `GET /queues/{queueId}`、`GET /queues/{queueId}/staff` 与人员详情中的队列摘要均按 `position ASC` 返回；`staff_count` 语义不变（关联人数，不区分在岗）。

### 3. 前端契约

- 服务人员页显示并允许切换 `duty_status`；队列页小卡片显示在岗标记，下班卡片弱化。
- 队列页在卡片上提供拖拽重排；为保证可访问性同时提供上移 / 下移按钮。两者都调用全量重排端点，失败时回滚本地顺序并重取详情。
- 切换门店继续按 ADR-0006 取消在途请求、失效 `shop-scope` 缓存并重拉权限；重排请求带当前门店 `X-Shop-Id`，不引入第二套缓存。

### 4. 不引入的概念

不建考勤 / 打卡流水、排班与班次、按日的自动上下线任务、叫号实例与排队号状态机、在岗人数统计看板，也不允许通过重排端点增减队列成员。以上任一需求须另立 ADR。

## 理由

1. 当前状态位即可满足「上下班状态」，避免把通用底座拖成考勤系统。
2. 位置下沉到关联表并加唯一约束，兼顾 SQL 可查询性、跨副本一致性与数据库层保护，符合 ADR-0002。
3. 全量重排以队列版本做乐观锁，一次事务完成，避免逐条更新带来的中间态与冲突。
4. 复用既有 `queue.write`、`Idempotency-Key` 与冻结错误码，不扩大权限面，也不改变 ADR-0006 的门店上下文协议。

## 后果与限制

- `EXPECTED_SCHEMA_VERSION` 由 `4` 升为 `5`；迁移 `00005_duty_status_and_queue_order.sql` 必须提供可执行 `Down`，并从 v4 回补现有分配的位置。
- `design/02` 表清单更新 `staff` / `queue_staff` 列；`design/03` 与 `design/10` 更新端点、DTO 与流程；`design/00` 变更规则注明第二次受控扩展。
- 现有队列列表的 `staff_count` 与端点状态码语义不变；只有详情 DTO 新增字段与排序来源变化。
- 下班人员仍可被加入队列、仍出现在卡片中；本 ADR 不改变分配校验，也不按在岗状态过滤。
- 公开仓库脱钩约束不变：只用 `duty_status` 等通用词，不出现任何内部项目名或行业专属词。

## 复审条件

- 需要按日考勤流水、补卡或跨日归属；
- 需要排班、班次或在岗人数统计；
- 需要队列内「叫号 / 服务中」状态机而不是人工顺序；
- 需要跨店复用服务人员或跨店顺序；
- 上游 ADR-0002 / 0003 / 0008 / 0009 任一被修订且影响本 ADR 的隔离或并发规则。

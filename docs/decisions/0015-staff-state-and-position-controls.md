# ADR-0015: 服务人员状态、轮转与位次的人工控制

- 状态：已接受（2026-11-20）
- 决策问题：调度引擎只在派单链路里自动流转执行者状态；操作员需要在人员列表里直接看到「这人现在什么状态」并手工设置状态、调整队列位次、留位、停止轮转
- 影响范围：`GET /shops/{shopId}/staff` 响应增量字段、新增 3 个写端点、`executor_state` 的人工流转规则、`duty_status` 联动、审计动作；**不改 schema**（schema 版本保持 7）、不改引擎自动流转表、不改 `queue_staff` 结构
- 关联：[ADR-0009](0009-queues-and-staff.md)（队列与服务人员）、[ADR-0010](0010-duty-status-and-queue-order.md)（在岗状态与队列内顺序）、[ADR-0012](0012-scheduling-engine.md)（调度引擎）
- 编号说明：ADR 序号取 0015（0013 / 0014 已分别用于订单域的两份契约修正）；设计分册沿用 `design/13`，因为分册按业务域递增、13 号尚未占用

## 背景

`staff.executor_state` 由 ADR-0012 引入，一共 11 个值，但只有三条写入路径：派单（`executing` / `designated_occupied`）、回原位与完成（`available`）、改指派方式。操作员看到的是自动流转的结果，无法在自己判断时把某人标成「时段保留」「请假」「下班」或「加班」。

实际使用里，人员列表是小门店的主要操作界面：店长在这里看今天谁在岗、谁在服务、谁请假，并就地调整而不是先派一单再撤销。因此需要一个受控的人工入口，同时**不能破坏状态机的自动语义**（例如手工把某人置为 `executing`，会让 `current_event_id`、快照、审计三者不一致）。

## 决策

1. **状态桶是展示概念，不是新枚举**。前端把 11 个 `executor_state` 折成 7 个彩色桶 + 1 个中性桶；服务端返回的仍是原始 `executor_state`，映射在前端纯函数里，供 `/staff`、`/queues` 人员视图、`/board` 三处共用。

   | 桶 | 显示名 | 颜色 | 包含的 `executor_state` |
   |---|---|---|---|
   | `available` | 可调度 | 绿 | `available` |
   | `queued` | 排队中 | 琥珀 | `standby`、`pinned` |
   | `serving` | 服务中 | 蓝 | `executing`、`cross_group_support` |
   | `designated` | 指定中 | 橙 | `designated_occupied` |
   | `reserved` | 时段保留 | 紫 | `time_reserved` |
   | `overtime` | 加班中 | 棕 | `overtime` |
   | `leave` | 请假 | 浅蓝 | `leave` |
   | `off` | 下班 / 停用 | 灰 | `off_duty`、`disabled` |

   `pinned` 是「位次被固定」，不是独立业务状态，归入排队中；`disabled` 是任职状态，和下班同色，卡片仍显示「停用」标记。

2. **人工可设状态只有 6 个**：`available`、`standby`、`time_reserved`、`overtime`、`leave`、`off_duty`。`executing` / `designated_occupied` / `cross_group_support` 由派单与完结链路独占，`disabled` 由任职状态派生，都不接受人工设置。

3. **人工流转规则**（`POST /shops/{shopId}/staff/{staffId}/state`）：
   - 目标必须在上述 6 个之内，否则 `400 INVALID_ARGUMENT`。
   - 当前状态属于 `executing` / `designated_occupied` / `cross_group_support` 时一律拒绝，返回 `409 EXECUTOR_BUSY`，提示先完成或取消该事件——避免出现「事件还在，人已下班」。
   - 当前状态为 `disabled`（任职停用）时只允许 `off_duty`，否则 `409 INVALID_STATE`。
   - 目标等于当前状态时返回 `400 STATE_UNCHANGED`，不写审计。
   - 与 `duty_status` 联动：目标为 `leave` / `off_duty` → `duty_status='off'`；目标为 `available` / `standby` / `time_reserved` / `overtime` → `duty_status='on'`，且要求 `staff.status='active'`（沿用 ADR-0010「停用人员不得在岗」）。
   - 该规则是**人工专用**的，不回写 `canTransition`；引擎自动流转仍按 ADR-0012 的单向表执行。

4. **轮转开关**（`PATCH /shops/{shopId}/staff/{staffId}/rotation`，`{stop_rotation}`）：对应「设为不动牌 / 恢复轮转」。字段 `staff.stop_rotation` 已存在（ADR-0012），此前没有写入口；停止轮转后该执行者在判定链中不参与轮转，但位次不移动、不影响正在执行的事件。

5. **队列内位次移动**（`POST /shops/{shopId}/queues/{queueId}/staff/{staffId}/move`，`{to: head|tail|index, index}`）：服务端在同一事务内按当前成员顺序重排并重编号 `1..N`，等价于既有 `PUT /queues/{queueId}/staff/order` 的单人快捷方式，但调用方不需要先拿到完整成员列表和 `version`。`to=index` 时 `index` 从 1 开始，超出范围按边界收敛（1 或 N）。

6. **留位复用既有置顶语义**：留位 = `POST /shops/{shopId}/scheduling/pins`（`pin_type=permanent`），取消留位 = `DELETE /shops/{shopId}/scheduling/pins`。不新增第二种「固定位次」概念；`SetPin` 会把该成员移到队首并固定位次，这是 ADR-0012 既有行为，前端文案写「留位（置顶）」以免误解。

7. **`GET /shops/{shopId}/staff` 响应增量**（向后兼容的加字段）：`state`、`stop_rotation`、`primary_queue_id`，以及 `queues[]`（`public_id`、`name`、`seq`、`position`、`pin_type`）。一次列表即可渲染状态、位次与菜单，避免每张卡片再发一次详情请求（DA-01 的扁平 DTO 原则）。

8. **权限与审计**：改状态与轮转要求 `scheduling.write`；移动位次要求 `queue.write`（沿用队列顺序的既有权限）。审计 `action` 分别为 `staff_state_changed` / `staff_rotation_changed` / `staff_moved`，`target_type='staff'`，`payload` 记录 `from`、`to`、`reason`、`queue_id`、`from_position`、`to_position`，`actor_id` 取当前登录用户。

## 备选方案（不采用）

- **扩 `canTransition` 允许任意人工流转**：会让引擎自动流转与人工操作共用一个矩阵，后续调整自动规则时容易误放人工路径；且 `executing` 需要事件与快照配套，单改字段必然产生不一致。
- **新增 `staff_status` 枚举列**：与 `executor_state` 形成两套真源，派单后需要双写；桶映射放前端即可满足展示需求。
- **新增「班次」模型承载调班**：属于排班领域，本期没有工时、班次与跨天规则，先不做（见边界）。

## 后果与限制

- 不做调班次 / 排班表 / 工时统计；「时段保留」只表达状态，暂不落具体时段窗口（`time_reservations` 字段保留，ADR-0012 已有）。
- 不做状态变更通知与订阅；状态变化只进审计与实时读取。
- 人工状态不触发位次轮转、不改变 `queue_staff.position`。
- 无 schema 变更，无迁移；`EXPECTED_SCHEMA_VERSION` 保持 7。

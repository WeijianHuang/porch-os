# 13 · 服务人员状态、轮转与位次控制（ADR-0015）

本分册只新增「人工控制」契约，不改变 [12 调度引擎](12-scheduling-engine.md) 的判定链、联动与自动状态流转；schema 不变，仍为版本 7。

## 1. 展示状态桶

`executor_state` 是引擎真源（11 个值）；前端按 ADR-0015 §1 折成 8 个展示桶，映射表在 `porch-console/src/shop/staffStatus.ts` 的纯函数里，三处界面共用。

| 桶 | 标签 | 颜色（浅底 / 主色） | 来源状态 |
|---|---|---|---|
| `available` | 可调度 | `#e8f5ee` / `#1f6b4f` | `available` |
| `queued` | 排队中 | `#fdf4dc` / `#8a5c00` | `standby`、`pinned` |
| `serving` | 服务中 | `#e8f1fb` / `#235b93` | `executing`、`cross_group_support` |
| `designated` | 指定中 | `#fdece1` / `#a34a12` | `designated_occupied` |
| `reserved` | 时段保留 | `#f0eafa` / `#5b3a9b` | `time_reserved` |
| `overtime` | 加班中 | `#f6ece5` / `#7a4a2f` | `overtime` |
| `leave` | 请假 | `#e8f4f8` / `#25657a` | `leave` |
| `off` | 下班 / 停用 | `#eef1ef` / `#5d7066` | `off_duty`、`disabled` |

卡片同时显示精确状态文字（如「指定占用」显示为「指定中」），桶只决定底色与图例归类。

## 2. 人工可设状态

只接受 6 个目标：`available`、`standby`、`time_reserved`、`overtime`、`leave`、`off_duty`。

| 当前状态 | 可否人工改 | 说明 |
|---|---|---|
| `available` / `standby` / `time_reserved` / `overtime` / `leave` / `off_duty` | 可 | 6 个目标之间自由切换 |
| `executing` / `designated_occupied` / `cross_group_support` | 否 | `409 EXECUTOR_BUSY`，先完成或取消事件 |
| `disabled` | 仅 `off_duty` | 其余 `409 INVALID_STATE` |

`duty_status` 联动：`leave` / `off_duty` → `off`；其余 → `on`（要求 `staff.status='active'`）。

## 3. 端点

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| `GET` | `/shops/{shopId}/staff` | `queue.read` | 响应增量字段，见 §3.1 |
| `POST` | `/shops/{shopId}/staff/{staffId}/state` | `scheduling.write` | 人工改状态 |
| `PATCH` | `/shops/{shopId}/staff/{staffId}/rotation` | `scheduling.write` | 停止 / 恢复轮转 |
| `POST` | `/shops/{shopId}/queues/{queueId}/staff/{staffId}/move` | `queue.write` | 队列内移动位次 |
| `POST` | `/scheduling/pins` | `scheduling.write` | 留位（既有，`pin_type=permanent`） |
| `DELETE` | `/scheduling/pins` | `scheduling.write` | 取消留位（既有） |

所有写端点走既有幂等（`Idempotency-Key`）与单店上下文（`X-Shop-Id`）中间件；路径与 header 门店不符仍返回 `403`。

### 3.1 `GET /shops/{shopId}/staff` 增量字段

```json
{
  "public_id": "…", "code": "S001", "display_name": "张三",
  "status": "active", "duty_status": "on", "version": 3,
  "state": "available", "stop_rotation": false,
  "primary_queue_id": "…",
  "queues": [
    { "public_id": "…", "name": "一号队列", "seq": 1, "position": 2, "pin_type": "none" }
  ]
}
```

`queues` 按 `seq`、`position` 升序，一次查询折叠（`queue.MembershipsByStaff`），不按卡片再发详情请求。

### 3.2 `POST /shops/{shopId}/staff/{staffId}/state`

```json
{ "state": "leave", "reason": "临时请假", "version": 3 }
```

- `state` 必填且必须属于 6 个可设状态；`reason` 可选（≤200 字），写入审计 `payload.reason`。
- `version` 可选；传入时按乐观锁校验，不匹配返回 `409 VERSION_CONFLICT`。
- 成功返回 `200` + 更新后的 staff（含 §3.1 字段）。
- 错误：`400 INVALID_ARGUMENT`、`400 STATE_UNCHANGED`、`409 EXECUTOR_BUSY`、`409 INVALID_STATE`、`409 VERSION_CONFLICT`、`404 NOT_FOUND`。

事务步骤：`SELECT … FOR UPDATE` 读当前行 → 校验任职状态与人工流转 → 更新 `executor_state`、必要时 `duty_status`、`version+1` → 写 `audit_logs`（`staff_state_changed`）。

### 3.3 `PATCH /shops/{shopId}/staff/{staffId}/rotation`

```json
{ "stop_rotation": true, "version": 3 }
```

返回 `200` + 更新后的 staff；重复设置同值视为成功但不写审计。审计动作 `staff_rotation_changed`。

### 3.4 `POST /shops/{shopId}/queues/{queueId}/staff/{staffId}/move`

```json
{ "to": "index", "index": 3 }
```

- `to`：`head` | `tail` | `index`；`index` 从 1 开始，越界按 1 / N 收敛。
- 服务端在同一事务内读取当前成员顺序、剔除目标后插入到目标位置、重编号 `1..N`，并写 `audit_logs`（`staff_moved`，记录 `from_position`、`to_position`）。
- 目标不在该队列 → `404 NOT_FOUND`；`to` 非法 → `400 INVALID_ARGUMENT`。
- 返回 `200` + `{ queue_id, staff_id, position }`，前端据此失效 `queue` / `staff` / `schedulingBoard` 缓存。

## 4. 并发与审计

- 状态与轮转变更按 `staff` 行加锁（`SELECT … FOR UPDATE`），避免两个操作员互相覆盖 `executor_state`。
- 移动位次按 `queue_staff` 的队列行加 `pg_advisory_xact_lock(shop_id, queue_row_id)`，与派单、置顶、重排共用同一把锁，防止交错重排。
- 审计 `actor_id` 取当前登录用户（`Principal.UserID`），与 `12` 分册里以 staff 为 actor 的派单审计不同——人工操作的责任主体是用户。

## 5. 前端约定

| 页面 | 呈现 |
|---|---|
| `/staff` | 状态墙：图例（可点击筛选）、工号 / 姓名搜索、按状态着色的卡片（位次 + 队列名 + 状态标签）、点击卡片弹出菜单 |
| `/queues` 人员视图 | 人员卡片按状态着色，保留既有的在岗开关与队列胶囊 |
| `/board` 调度看板 | 位次卡左侧色条与状态标签使用同一套桶颜色 |

菜单项与后端能力一一对应，不出现没有端点的动作：

- **修改状态** ▸ 6 个可设状态（当前状态置灰）
- **移动位次** ▸ 队列列表 → 弹窗选择「队首 / 队尾 / 第 N 位」
- **留位 / 取消留位** ▸ 队列列表（复用置顶端点）
- **停止轮转 / 恢复轮转**
- **查看队列**

## 6. 明确不做

- 班次与排班（无工时、跨天、换班模型），因此参考界面里的「调班次」不在本期。
- 状态变更通知、状态历史曲线、按状态的工时统计。
- 人工设置「服务中 / 指定中」：必须走派单链路，保证事件、快照、审计一致。

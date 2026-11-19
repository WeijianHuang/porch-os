# 12 · 多队列调度规则引擎（ADR-0012）

本册定义整合进本仓库的多队列调度规则引擎契约。引擎**不是独立项目**：它复用 `shop_id` 隔离、`idempotency_keys` 幂等、`audit_logs` 审计与双副本 api 部署基线（ADR-0003 / 0007 / 0008）。全文只用通用术语；原行业专有词只存在于仓库外的留痕对照表，不在此出现。

## 1. 逻辑关联：通用术语 ↔ 既有概念

| 通用术语 | 既有 / 新增落点 | 隔离类别（ADR-0002） |
|---|---|---|
| 站点 Site | `shops` / `shop_id` | 顶层 |
| 队列 Queue | `queues` | B（站点业务数据） |
| 执行者 Executor | `staff`（服务员） | B |
| 位次 Position | `queue_staff.position` | B |
| 任务类型 TaskType | `scheduling_task_types` | B |
| 判定链规则 Rule | `scheduling_rules` | B |
| 联动策略 LinkagePolicy | `scheduling_policies` | B |
| 派单事件 / 决策 / 快照 | `scheduling_events` / `scheduling_snapshots` | B |
| 审计 | `audit_logs`（复用） | B |

所有新增表都带 `shop_id`，单店端点显式 `X-Shop-Id`，缺失 `400 SHOP_CONTEXT_REQUIRED`，路径与上下文不符 `403 PERMISSION_DENIED`（ADR-0006）。

## 2. 数据模型（SQL 优先、无实体图）

```sql
-- staff 扩展（执行者状态，复用既有人员档案）
ALTER TABLE staff
  ADD COLUMN executor_state text NOT NULL DEFAULT 'standby',
  ADD COLUMN stop_rotation   boolean NOT NULL DEFAULT false,
  ADD COLUMN time_reservations jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD CONSTRAINT ck_staff_executor_state CHECK (executor_state IN
    ('standby','available','executing','designated_occupied','pinned',
     'time_reserved','overtime','disabled','leave','off_duty','cross_group_support'));

-- queue_staff 扩展（逐队列置顶与位次来源）
ALTER TABLE queue_staff
  ADD COLUMN pin_type text NOT NULL DEFAULT 'none',
  ADD COLUMN pin_effective_from timestamptz,
  ADD COLUMN pin_expires_at timestamptz,
  ADD COLUMN source text NOT NULL DEFAULT 'auto',
  ADD CONSTRAINT ck_qs_pin_type CHECK (pin_type IN ('none','permanent','timed','conditional')),
  ADD CONSTRAINT ck_qs_source CHECK (source IN ('auto','manual','pin','restored','inherited'));
```

新增表：`scheduling_rules`（8 步规则）、`scheduling_policies`（联动策略）、`scheduling_task_types`（任务类型）、`scheduling_events`（派单事件 + 决策摘要）、`scheduling_snapshots`（前后位次快照）。均为扁平场景表，结构体之间只持 ID（DA-01）；多对一关系用「两次查询 + 纯函数折叠」，不在响应里嵌实体图。

唯一性：
- `scheduling_rules (shop_id, id)` 与 `(shop_id, type, scope, scope_id)` 唯一；
- `scheduling_policies (shop_id, primary_queue_id)` 唯一（一主队列一策略）；
- `scheduling_task_types (shop_id, code)` 唯一；
- `scheduling_events (shop_id, public_id)` 唯一，幂等键另由 `idempotency_keys` 承担。

复合外键（同店锁）：所有 `(shop_id, queue_id)` / `(shop_id, staff_id)` 引用既有表的同名唯一键，跨店分配在 DB 层被拒（沿用 ADR-0009 做法）。

## 3. 判定链（8 步、命中即停、可配置）

步骤类型（顺序可配）：`executor_stopped_rotation`、`rotation_master_switch`、`assignment_method_allows_rotation`、`task_type_no_rotation`、`overtime_no_rotation`、`time_reserved`、`group_a_to_b`、`group_b_to_a`。

每步规则字段：`id / type / order / enabled / scope / scope_id / on_hit / window`。
- `scope ∈ {executor, queue, task_type, site, global}`，优先级 `5/4/3/2/1`；高优先级胜出，同级按 `order` 再按 `id`。
- `on_hit ∈ {rotate, hold}`，命中即停止并返回该动作；全不命中则为 `rotate`。
- `window` 为可配本地时段（`HH:MM`–`HH:MM`，支持跨午夜、可选星期），**无系统默认值**。
- 决策上下文不可变：站点、执行者、任务类型、指派方式、源队列、主队列、是否加班、决策时刻。
- 纯函数 `DecideChain` 输出：最终决策、命中的规则 / 作用域、完整链路 trace（供「为什么没轮到我」解释）。

## 4. 联动与各自落位

- 联动策略绑定（站点，主队列），模式：`independent` / `all` / `same_group` / `allowlist` / `exclude` / `frozen`。
- 单向传播：从队列不触发新一轮；同一队列在一次事件内最多被处理一次。
- 从队列按**自身长度**落位（队尾 / 指定位 / 回原位 / 挂起 / 移除），动作映射逐队列配置 `action_map`；不复制主队列位次。
- 置顶队列、冻结队列强制保持；被置顶执行者按上下文记为「位次跳过」。
- `queue_staff.source` 记录位次来源，供审计与展示。

## 5. 状态机（执行者）

`available → executing`（派单）、`executing → available`（完成 / 取消）、`executing → designated_occupied`（指定指派）、`standby/available → pinned/time_reserved/...` 等单向流转，非法流转 `400 INVALID_STATE`。`duty_status=off` 或 `staff.status=inactive` 的执行者不可派单。状态变更全部写审计。

## 6. 并发、事务与幂等

```
POST /scheduling/dispatch  （写，X-Shop-Id + Idempotency-Key）
  ├─ IdempotentWrite：命中幂等键 → 返回首次决策，不改位次
  ├─ BEGIN
  ├─ pg_advisory_xact_lock(hash(shop_id, staff_id))   -- 执行者互斥
  ├─ 读站点 / 执行者 / 成员 / 规则 / 策略 / 任务类型（同事务）
  ├─ DecideChain（纯函数）→ 决策
  ├─ 若轮转：对每个目标队列按自身长度落位（同事务写 queue_staff）
  ├─ 写 executor_state、scheduling_event、snapshot、audit_logs
  └─ COMMIT（任一步失败整体回滚）
```

- advisory lock 用 `pg_advisory_xact_lock`，随事务结束自动释放，避免会话级锁跨事务泄漏。
- 规则 / 策略发布也是写端点，走 `IdempotentWrite`，发布即审计（含操作者与时间）。

## 7. 端点清单

| Method | Path | 权限 | 说明 |
|---|---|---|---|
| GET | `/scheduling/board` | read | 当前站点队列 + 成员位次 + 执行者状态 |
| GET | `/scheduling/rules` | read | 8 步规则链 |
| GET | `/scheduling/policies` | read | 联动策略 |
| GET | `/scheduling/task-types` | read | 任务类型 |
| GET | `/scheduling/events` | read | 派单事件与决策摘要 |
| GET | `/scheduling/audit` | read | 审计流水（append-only） |
| POST | `/scheduling/dispatch` | write | 派单：决策 + 联动，单事务 |
| POST | `/scheduling/pins` | write | 置顶（无原因 / 无权限字段） |
| DELETE | `/scheduling/pins` | write | 解除置顶 |
| POST | `/scheduling/pins/release-expired` | write | 到期 / 不可用自动释放 |
| POST | `/scheduling/events/{eventId}/restore` | write | 一键回原位 |
| POST | `/scheduling/events/{eventId}/cancel` | write | 取消 / 超时 / 拒绝 |
| POST | `/scheduling/events/{eventId}/complete` | write | 完成任务，执行者回到可调度 |
| POST | `/scheduling/events/{eventId}/assignment-method` | write | 指派方式变更补偿 |
| PUT | `/scheduling/rules` | write | 发布规则链新版本 |
| PUT | `/scheduling/policies` | write | 发布联动策略 |

错误码：复用冻结码表 + `EXECUTOR_BUSY`(409) / `NO_PRIMARY_QUEUE`(400) / `SNAPSHOT_MISSING`(404) / `INVALID_STATE`(400)；无行业词。

## 8. 前端调度台（porch-console）

四页面：`/board` 调度看板、`/rules` 判定链配置、`/linkage` 联动策略、`/audit` 审计与指标。站点切换复用既有切换器与 `X-Shop-Id`；前端只持有 UI 过滤状态，所有位次 / 决策来自服务端读取，所有变更走写端点。派单确认路径 ≤3 步；决策解释展示首个命中规则 + 全链路 + 各队列旧新位次 + 回原位。指标只展示实测计数 / 比例，不设目标阈值。UI 文案只用通用词。

## 9. 明确不做的边界

不做薪酬 / 结算、订单 / 收银、余额、考勤智能排班、规则 DSL / 沙箱、跨站点共享队列、离线派单与冲突合并。模板复制 / 继承仅保留字段，不实现版本联动。

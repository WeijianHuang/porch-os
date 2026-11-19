# ADR-0012: 多队列调度规则引擎整合（深度数据整合）

- 状态：已接受（2026-11-20）
- 决策问题：将一个原为站点队列设计的「多队列调度规则引擎」整合进本系统，取代独立仓库形态。确定它与既有队列 / 服务人员领域的关系、持久化边界、术语合规口径与不做的范围，使冻结后的第四次受控扩展有可追溯来源
- 影响范围：新增 `design/12-scheduling-engine.md`、迁移 `00007`、一个领域包 `internal/domain/scheduling`（纯函数与其依赖端口）、一个 PG 仓储 / 编排、HTTP 端点扩展、两个权限点；修订 `design/00` / `02` / `03` / `06`、`ARCHITECTURE.md`、README / CHANGELOG、ADR-0008 修订记录；同步 `porch-console` 调度台与前端 ADR-0004
- 上游：[ADR-0001](0001-postgresql-18.md)（PG 18.7）、[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码、共享状态与互斥全走 PG）、[ADR-0009](0009-queues-and-staff.md) / [ADR-0010](0010-duty-status-and-queue-order.md)（队列 / 服务人员 / 位次基线）

## 背景

一个独立的通用多队列调度规则引擎已按 PRD 完成纯 Go 库（领域 + 判定链 + 联动 + 端口 + 内存适配器）。业务确认：该引擎**本就是站点给队列使用的引擎**，不应作为独立项目维护，而应整合进本仓库，与既有队列 / 服务人员领域共享同一套 `shop_id` 隔离、幂等、审计与部署基线。

按 ADR-0008 铁律，schema、状态机、端点清单与错误语义的改动必须先有 ADR / 设计来源；本 ADR 即该来源。它是冻结后的**第四次受控扩展**，不推翻 ADR-0001–0011 的任何条款。

### 术语合规口径（重要）

引擎交付材料附有一份「术语映射与敏感词替换规范」。该规范是**唯一允许出现原行业专有词的留痕文件**，明确要求其专有词不得复制到任何交付物或代码。据此本 ADR 确立：

- **对照表本体不入任何仓库**：它是本地留痕文件，只用于发布前扫描与术语对照，绝不复制进 `porch-os` / `porch-console` 或任何交付物。
- **项目内只用通用词**：本 ADR、`design/12`、代码标识符 / 注释、接口字段、错误码、错误文案、前端 UI 文案一律使用通用术语（站点 Site、执行者 Executor、队列 Queue、位次 Position、指派方式 AssignmentMethod、任务类型 TaskType、判定链 DecisionChain、调度看板 Board、回原位 Restore 等）。
- **逻辑关联在文档层建立**：`design/12` 用一张映射表说明通用术语与本系统既有概念的对应关系（见下），供开发理解；映射表只列通用词与既有字段，**不复述任何原行业专有词**。
- **扫描门禁**：提交前对本仓库与前端仓库跑敏感词扫描，新增内容必须零命中。

## 备选方案

| 议题 | 方案 A（不采用） | 方案 B（采用） | 采用 B 的原因 |
|---|---|---|---|
| 引擎落点 | 保留独立 Go 库，本系统仅通过 HTTP 调用 | 代码并入 `internal/domain/scheduling` + PG 仓储 / 编排 | 用户明确「不是单独的项目」；同仓才能共享 `shop_id` 隔离、幂等键与审计基线 |
| 持久化 | 规则 / 状态存内存或代码常量，重启即丢 | 规则、策略、执行者状态、置顶、事件、快照全部落 PG | 规则可配置、审计可回溯是引擎的核心价值；内存态撑不起多副本 api（ADR-0007 双副本） |
| 执行者身份 | 新建 `executors` 表 | **复用 `staff`（服务员）作为执行者** | 用户明确「服务员对应的具体执行者」；避免两套人员档案，直接复用既有店内人员与位次 |
| 互斥与协调 | 进程内内存锁 | PG advisory lock（按 `shop_id + 执行者`） | ADR-0008：共享状态与互斥全走 PG，不用 Redis / 内存锁 |
| 审计 | 引擎自建审计表 | 复用 `audit_logs` | 已有 append-only 审计与保留策略（ADR-0008 / design/08），不重复造表 |
| 幂等 | 引擎自建幂等表 | 复用 `idempotency_keys` + `Idempotency-Key` | 与全系统写端点一致（design/05） |

## 决策

### 1. 术语 → 既有概念的逻辑关联（通用词侧）

| 通用术语（项目内唯一用词） | 本系统既有对应 | 说明 |
|---|---|---|
| 站点 Site | `shops` / `shop_id` | 隔离边界，与 ADR-0003 一致 |
| 队列 Queue | `queues` | 一店多队列、店内有序号 + 名称（ADR-0009） |
| 执行者 Executor | `staff`（服务员） | 店内人员即执行者；其 `staff.duty_status` 与新增的 `executor_state` 共同描述可调度性 |
| 位次 Position | `queue_staff.position` | 队列内密集 1..n（ADR-0010），逐队列独立 |
| 指派方式 AssignmentMethod | 新字段 `scheduling_events.assignment_method` | 轮转 / 指定 / 改派 / 延时 / 选择 / 呼叫 |
| 任务类型 TaskType | `scheduling_task_types` | 站点内任务分类，带分组与「不轮转」标记 |
| 判定链 DecisionChain | `scheduling_rules` | 8 步可配置规则，命中即停 |
| 联动策略 LinkagePolicy | `scheduling_policies` | 绑定到（站点，主队列）的传播模式与动作映射 |
| 决策 / 事件 / 快照 | `scheduling_events` / `scheduling_snapshots` | 一次事件一次决策，含前后快照 |
| 置顶 Pin | `queue_staff.pin_type` 等字段 | 逐队列置顶，仅该队列生效 |

### 2. 执行者状态机（复用 ADR-0010 的 `duty_status`）

`staff.executor_state` 新增 11 态：`standby` / `available` / `executing` / `designated_occupied` / `pinned` / `time_reserved` / `overtime` / `disabled` / `leave` / `off_duty` / `cross_group_support`。单向流转，非法流转报 `INVALID_STATE`。`duty_status`（`on`/`off`）仍是「在岗」总开关：`off` 时执行者不可被派单。`status=inactive` 且 `duty_status=on` 仍被 ADR-0010 拒绝。

### 3. 判定链与优先级

8 步链顺序 / 开关 / 作用域 / 命中动作 / 生效时段均可配置，命中即停。作用域优先级 `executor > queue > task_type > site > global`（高优先级胜出，同级按 order 再按 id）。时段窗口可配且**无系统默认值**。纯函数 `DecideChain` 无副作用、可脱库单测。

### 4. 联动与各自落位

联动绑定（站点，主队列），六种传播模式：`independent` / `all` / `same_group` / `allowlist` / `exclude` / `frozen`。联动单向，从队列不触发新一轮。从队列**按自身长度落位**，绝不复制主队列位次；置顶 / 冻结队列强制保持。每个（站点，主队列）动作映射独立配置。

### 5. 事务、并发与幂等

- 一次派单事件 = 一个主决策 + 一次原子提交：所有位次变更、`executor_state` 变更、事件行、快照行在同一次 `pgx.Tx` 内完成（FR-28）。
- 执行者互斥：每次派单前取一把 PG advisory lock，键为 `hash(shop_id, staff_id)`，同一事务内持有到提交；拿不到锁返回 `EXECUTOR_BUSY`。不使用内存锁 / Redis（ADR-0008）。
- 幂等：写端点带 `Idempotency-Key`，`IdempotentWrite` 复用既有 `idempotency_keys`；重复事件不改位次、返回首次决策。
- 审计：所有位次变更、置顶 / 解除、派单 / 回原位 / 取消 / 完成 / 指派方式变更、配置发布均 append 到 `audit_logs`，含操作者与命中规则。

### 6. 端点（R1 扩展，单店显式 `X-Shop-Id`，写带 `Idempotency-Key`）

读：`GET /scheduling/board`、`/scheduling/rules`、`/scheduling/policies`、`/scheduling/task-types`、`/scheduling/events`、`/scheduling/audit`。

写：`POST /scheduling/dispatch`、`/scheduling/pins`、`DELETE /scheduling/pins`、`POST /scheduling/pins/release-expired`、`POST /scheduling/events/{eventId}/restore`、`/cancel`、`/complete`、`/assignment-method`、`PUT /scheduling/rules`、`/scheduling/policies`。

权限点 `scheduling.read` / `scheduling.write`，admin+manager 全给、staff 只读（与 `queue.*` 一致）。错误码复用既有冻结码表，新增领域语义码 `EXECUTOR_BUSY` / `NO_PRIMARY_QUEUE` / `SNAPSHOT_MISSING` / `INVALID_STATE` 映射为 `400`/`409`，均不含行业词。

### 7. 明确不做

不做薪酬 / 结算、订单 / 收银、成员余额、考勤智能排班、规则 DSL / 沙箱、跨站点共享队列、离线派单与冲突合并。模板复制 / 继承模式仅保留 `shops` 级配置字段，不在本期实现模板版本联动。

## 影响与验收

- schema 版本 `6 → 7`，迁移 `00007` 含可执行 Down；`/ready` 校验新版本。
- 领域纯函数单测 + PG 端到端：判定链 8 步每步、优先级、6 种联动、各自落位、置顶逐队列、幂等重放、版本冲突、跨店隔离、非法状态流转、审计不可篡改、回原位。
- 敏感词扫描：本仓库与前端仓库新增内容零命中；对照表本体不在任何仓库。
- 前端调度台并入 `porch-console`，与队列 / 人员 / 房间共用站点切换与设计系统。

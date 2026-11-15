# 前期系统架构详细设计 · 08 审计日志保留与清理

> 状态：**已冻结**（[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)，2026-11-15）。
> 上游：[ADR-0004](../decisions/0004-scope-baseline.md)（统一 UTC+8、暂不归档）、[ADR-0007](../decisions/0007-remaining-open-questions.md) 第 5 节（默认 30 天、可配、定时删除）、[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)（NQ-10/11/12/13）、[03 接口设计](03-api-design.md) 第 2/4 节、[07 部署与运行基线](07-deployment-and-operations.md) §3/§4/§5。
> 本册只管 `audit_logs` 这一张**基础设施 / 运行期数据表**。业务单据与流水全量保留、不参与任何删除（ADR-0004）；清理动作不是业务归档。

## 1. 配置默认规则（NQ-10）

| 项 | 规则 |
|---|---|
| 配置项 | `AUDIT_LOG_RETENTION_DAYS`，全局唯一，**不通过 API 暴露、不可按店覆盖** |
| 默认值 | **30**（**是默认值，不是硬上限**） |
| 取值范围 | `1..3650`；越界、非整数、空值 → **启动即失败**（快速失败，不运行到一半才报错） |
| 关闭清理 | **不允许**：`0` / `-1` / `off` / `never` 一律视为非法配置。审计日志是增长最快、查询频次最低的表，关掉清理等于把库体积交给运气 |
| 超过 30 天 | 允许（上限 3650），属运维显式选择；**仍是物理删除，不是归档、不是分区、不提供归档查询** |
| 生效方式 | **启动时读取一次，变更需重启**（不热加载）；启动时把实际值写入结构化日志与 `porch_audit_retention_days` 指标 |
| 基准时区 | 固定 `Asia/Shanghai`（UTC+8），不接受客户端时区参数，也不设门店级配置（ADR-0004） |

**变更风险（必须进发布清单）**：

- **缩短天数**：下一轮清理会一次性删除更多记录，**已删除数据不可恢复**。发布前确认无合规举证需求。
- **延长天数**：已删除的记录不会回来，只有新产生的记录按新窗口保留；窗口变化期间报表口径要说清楚。
- 任何超出 `1..3650` 或要求按店覆盖、热加载、关闭清理的需求，属范围变更，另立 ADR（ADR-0008 复审条件）。

## 2. 清理窗口口径：UTC+8 自然日（NQ-11）

**决策：按 UTC+8 自然日，不采用滚动 30×24h。**

| 对比 | UTC+8 自然日（采用） | 滚动 30×24h |
|---|---|---|
| cutoff | 每天固定为「当日 00:00（UTC+8）− 30 天」，一天内不变 | 每次执行时刻都不同，秒级漂移 |
| 可解释性 | 「最早可查 10 月 16 日」一句话说清 | 「最早可查 30 天前的此刻」，解释与客服口径都别扭 |
| 可复现 | 集成测试可用固定时间点断言 | 依赖执行时刻，断言要留容差 |
| 与业务口径 | 与业务日、单号日期、日报分组同为 UTC+8（ADR-0004） | 另立一套时间口径 |
| 删除边界 | 按完整自然日整批淘汰，不会把同一天的记录分两次删 | 同一天记录可能跨两次任务 |

**cutoff 计算（不使用数据库会话时区）**：

```sql
-- $1 = AUDIT_LOG_RETENTION_DAYS（默认 30）
-- now() 是 timestamptz；AT TIME ZONE 先取东八区当地时刻，
-- date_trunc 落到当日 0 点，减 N 天后再转回 timestamptz。
SELECT (date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai')
        - make_interval(days => $1::int)) AT TIME ZONE 'Asia/Shanghai' AS cutoff_at;
```

示例：UTC+8 `2026-11-15 03:30` 执行、保留 30 天 → cutoff = 东八区 `2026-10-16 00:00` = **`2026-10-15T16:00:00Z`**。删除条件恒为 `created_at < cutoff`，即保留最近 30 个完整自然日 + 当日至今。查询接口返回的「最早可查时间」与导出的窗口下界都用同一个 `cutoff_at`，不允许两处各算一次。

**调度**：每日 **UTC+8 03:30** 一次（`robfig/cron/v3` 显式 `WithLocation(Asia/Shanghai)`，不依赖服务器本地时区）；`worker` 启动后补跑一次当前业务日，由 `run_key` 与 advisory lock 保证不重复。执行键 `job:audit_cleanup:<UTC+8 业务日>`，锁与双副本行为见 [07 §4.2](07-deployment-and-operations.md)。

## 3. 清理任务实现

| 项 | 规则 |
|---|---|
| 唯一删除者 | `cmd/worker` 的清理任务（或运维显式拉起 `cmd/job audit-cleanup`）；**业务代码不得暴露按 ID 删除 / 修改审计记录的端点或直接 SQL** |
| 单批行数 | **5000**，`ORDER BY created_at, id LIMIT 5000`，每批**独立事务提交** |
| 批次间隔 | 批间短暂让出（约 20 ms），避免长时间占用主库 IO 影响在线请求 |
| 可重入 | 重跑同 `run_key` 只会删除已不存在的行，`deleted_rows = 0`，无任何额外副作用；失败后下一次调度自然重试 |
| 失败语义 | 记录 `failed` + 脱敏错误信息 + 指标 + **立即告警**；**不允许静默吞异常**，也不允许「清理失败就悄悄缩短或延长保留窗口」 |
| 不做的事 | 不导出归档、不建月度分区、不 `VACUUM FULL`（排他锁）、不写回 `audit_logs`、不触碰任何业务表 |

分批删除 SQL（`FOR UPDATE SKIP LOCKED` 是 advisory lock 之外的行级保险）：

```sql
WITH victims AS (
  SELECT id
  FROM audit_logs
  WHERE created_at < $1            -- cutoff_at
  ORDER BY created_at, id
  LIMIT $2                         -- 5000
  FOR UPDATE SKIP LOCKED
)
DELETE FROM audit_logs a USING victims v WHERE a.id = v.id;
```

Go 侧用 `CommandTag.RowsAffected()` 累计行数，循环到返回 0 为止；每批带独立 `statement_timeout`，context 取消时停止并记 `failed`。

**索引与表膨胀策略**（[02](02-database-design.md) §5/§7）：

- 依赖 `idx_audit_logs_created_at (created_at)`，删除条件以它为前导；不分区（ADR-0004：分区会与复合唯一 / 外键设计冲突）。
- 依赖 autovacuum；迁移中为 `audit_logs` 设表级 `autovacuum_vacuum_scale_factor = 0.02`、`autovacuum_analyze_scale_factor = 0.01`，让高频删除产生的死元组及时回收。
- 监控 `n_dead_tup` 与表体积；超过 [07 §5](07-deployment-and-operations.md) 阈值时由**运维**执行 `VACUUM (ANALYZE) audit_logs`（非 FULL、不锁表），不纳入自动任务。

## 4. 查询契约：超期必须明确报错（NQ-12）

**决策：`400` + `AUDIT_RETENTION_WINDOW_EXCEEDED`，不提供专用审计导出端点。**

| 项 | 约定 |
|---|---|
| 触发条件 | 列表 / 报表 / 导出请求中的 `created_at_from < cutoff_at` |
| HTTP / `code` | `400` / `AUDIT_RETENTION_WINDOW_EXCEEDED` |
| `retryable` | `false` |
| `message` | 面向人，例：`查询起始时间早于审计日志保留窗口，最早可查时间为 2026-10-15T16:00:00Z` |
| `details` | `created_at_from` → `outside_retention_window`；另带 `earliest_queryable_at`（ISO 8601 UTC）、`retention_days`、`cutoff_at` |
| `created_at_from` 缺省 | 服务端默认从 `cutoff_at` 开始，**不报错** |
| `created_at_to > now()` | `INVALID_ARGUMENT`（参数错误，与超期区分） |
| 窗口内导出 | **复用统一导出任务**（`POST /exports` + `GET /exports/{id}/download`），创建任务时执行同一窗口校验、返回同一错误码 |
| 专用端点 | **不新增** `/audit-logs/export`，范围排除清单补一条；避免两套分页 / 权限 / 限流 / 窗口校验 |
| 审计查询界面 | **不在 R1 首批端点范围**（ADR-0008 §3），但错误契约现在冻结，将来加视图时直接套用 |

响应示例（错误结构遵循 [03 第 2 节](03-api-design.md)）：

```json
{
  "error": {
    "code": "AUDIT_RETENTION_WINDOW_EXCEEDED",
    "message": "查询起始时间早于审计日志保留窗口，最早可查时间为 2026-10-15T16:00:00Z",
    "retryable": false,
    "details": [
      { "field": "created_at_from", "reason": "outside_retention_window" },
      { "field": "earliest_queryable_at", "reason": "2026-10-15T16:00:00Z" },
      { "field": "retention_days", "reason": "30" },
      { "field": "cutoff_at", "reason": "2026-10-15T16:00:00Z" }
    ]
  },
  "meta": { "request_id": "req_01J..." }
}
```

**为什么不能用空列表 / `404` / `410`**：审计日志是唯一会被物理删除的表，若超期查询返回空列表，前端必然渲染成「这段时间没有操作记录」——这是不报错的错数据，与 `00` 质量属性第 1 条同类。`404` 表示资源不存在、`410` 描述单个已永久消失的资源，而这里是**范围参数越界**，客户端拿到 `earliest_queryable_at` 就能自己修正。

前端约定（`porch-console`）：收到该错误码时展示明确的「超出保留期」提示（含最早可查日期），可提供「从最早可查时间重试」的操作；**禁止渲染成空态，禁止静默把起始时间裁剪到窗口内**（必须由用户确认或显式操作）。

## 5. 运行痕迹与可观测（NQ-13）

### 5.1 独立运行记录表

**不写回 `audit_logs`**：清理动作若产生审计记录，会不断生成新日志形成自我循环，且这些记录自己又会被清理。因此痕迹放在独立的 P 类基础设施表。

```sql
-- +goose Up
CREATE TABLE audit_log_cleanup_runs (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  run_key        text        NOT NULL,   -- job:audit_cleanup:<UTC+8 业务日>
  attempt        integer     NOT NULL DEFAULT 1,
  retention_days integer     NOT NULL,
  cutoff_at      timestamptz NOT NULL,
  started_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz,
  status         text        NOT NULL DEFAULT 'running',
  deleted_rows   bigint      NOT NULL DEFAULT 0,
  batches        integer     NOT NULL DEFAULT 0,
  error_message  text,                    -- 脱敏，不含 SQL 全文 / 密钥 / PII
  CONSTRAINT uq_audit_cleanup_runs_key_attempt UNIQUE (run_key, attempt),
  CONSTRAINT ck_audit_cleanup_runs_status CHECK (status IN ('running','succeeded','failed')),
  CONSTRAINT ck_audit_cleanup_runs_retention CHECK (retention_days BETWEEN 1 AND 3650)
);
CREATE INDEX idx_audit_cleanup_runs_started ON audit_log_cleanup_runs (started_at DESC);
```

| 规则 | 说明 |
|---|---|
| 互斥来源 | **advisory lock**（[07 §4.2](07-deployment-and-operations.md)），不是这张表；运行记录只记痕迹 |
| `attempt` | 同一 `run_key` 失败后补跑时取 `MAX(attempt)+1`；并发下也不会冲突，因为同一时刻只有一个执行者持锁 |
| 崩溃残留 | 状态停在 `running` 的记录保留，由「`running` 超 2 小时」告警发现；下一次调度用新业务日的 `run_key` 自然继续 |
| 保留与体积 | 每日最多数行（含少量补跑），**不设自动清理**，体积可忽略；不带 `shop_id`、不参与业务隔离判定、不是审计记录 |
| 记录内容 | 「已清理至某日期」= `cutoff_at` + `deleted_rows` + `status`；不记录被删行的内容 |

### 5.2 指标（Prometheus）

| 指标 | 类型 | 标签 / 说明 |
|---|---|---|
| `porch_audit_cleanup_deleted_rows_total` | counter | 累计删除行数 |
| `porch_audit_cleanup_batches_total` | counter | 累计批次数 |
| `porch_audit_cleanup_duration_seconds` | histogram | 单次任务耗时 |
| `porch_audit_cleanup_runs_total` | counter | `status=succeeded\|failed\|skipped`（`skipped` = 未抢到锁） |
| `porch_audit_cleanup_last_success_timestamp_seconds` | gauge | 上次成功时刻，用于「多久没跑成功」 |
| `porch_audit_retention_days` | gauge | 当前生效的保留天数 |
| `porch_audit_earliest_queryable_timestamp_seconds` | gauge | 当前 `cutoff_at`，与接口返回值同源 |
| `porch_audit_logs_table_bytes` / `porch_audit_logs_dead_tuples` | gauge | 表体积与死元组，来自 `pg_total_relation_size` / `pg_stat_user_tables` |

结构化日志字段：`run_key`、`attempt`、`retention_days`、`cutoff_at`、`deleted_rows`、`batches`、`duration_ms`、`status`、`error_code`（失败时）；沿用 [05 §9](05-cross-cutting.md) 的 JSON 日志约定，不记 PII。

### 5.3 告警规则

| # | 条件 | 级别 / 动作 |
|---|---|---|
| 1 | `porch_audit_cleanup_runs_total{status="failed"}` 增量 ≥ 1 | **立即告警**：清理失败会让表无限增长 |
| 2 | `time() - porch_audit_cleanup_last_success_timestamp_seconds > 48h` | 告警：连续两个 UTC+8 自然日无成功运行，检查调度与锁残留 |
| 3 | 单次 `deleted_rows > 200000` 或 `batches > 40` | 告警：可能长期未跑或保留天数被改小，核对发布变更 |
| 4 | 单次 `duration_seconds > 600` | 告警：检查索引、锁竞争与批大小 |
| 5 | `SELECT count(*) FROM audit_logs WHERE created_at < cutoff_at - interval '1 day'` > 0 | 告警：清理落后于窗口，存在应删未删行 |
| 6 | `porch_audit_logs_table_bytes > 20 GB` 或 `dead_tuples > 1000000` | 运维执行 `VACUUM (ANALYZE)`；同时复核 [07 §5](07-deployment-and-operations.md) 容量阈值 |
| 7 | 存在 `status='running' AND started_at < now() - interval '2 hours'` | 告警：进程崩溃残留，检查 worker 与 advisory lock |

## 6. 测试清单

| 场景 | 断言 |
|---|---|
| 窗口口径 | 在 UTC `15:59:59Z` / `16:00:01Z` 两侧各插一条，按 UTC+8 30 天 cutoff 断言归属正确；cutoff 一天内不变 |
| 删除边界 | 只删 `created_at < cutoff` 的 `audit_logs` 行；**业务单据、支付、余额、库存流水一行不动** |
| 业务请求不可删 | 不存在按 ID 修改 / 删除审计记录的端点；直接 SQL 走业务角色时被拒 |
| 可重入 | 同一 `run_key` 重跑第二次 `deleted_rows = 0`，不产生新副作用 |
| 运行痕迹 | 有 `succeeded` 运行记录（`cutoff_at` / `deleted_rows` / `batches`），`audit_logs` 中**没有**清理动作自身的新记录 |
| 双副本 | `worker` × 2 时该 `run_key` 只有一条 `succeeded`，另一副本记 `skipped` 且不残留 advisory lock |
| 错误契约 | 超期查询返回 `400 AUDIT_RETENTION_WINDOW_EXCEEDED` + `earliest_queryable_at`；窗口内查询正常；缺省 `created_at_from` 不报错 |
| 配置校验 | `0` / `-1` / `3651` / 非整数 / 空值 → 启动失败并给出可读原因 |
| 失败可观测 | 注入失败后记录 `failed` + 指标 + 告警，保留窗口不被悄悄改写 |

---

上一册：[07 部署与运行基线](07-deployment-and-operations.md) · 下一册：[09 跨店差额与清分边界](09-cross-shop-difference.md)

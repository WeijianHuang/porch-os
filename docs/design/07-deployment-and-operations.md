# 前期系统架构详细设计 · 07 部署与运行基线

> 状态：**已冻结**（[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)，2026-11-15）。
> 上游：[00 总览](00-overview.md) 第 2/3 节、[02 数据库设计](02-database-design.md) 第 6/7 节、[04 应用架构](04-application-architecture.md) 第 3/8 节、[05 横切机制](05-cross-cutting.md) 第 7/10 节、[ADR-0007](../decisions/0007-remaining-open-questions.md) 第 3 节、[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)（NQ-04/05/06/07/08/09）。
> 本册把「多副本」从原则变成可以照着执行的数值与例子：部署形态、发布顺序、共享与互斥、连接池预算、容量阈值、压测基线。审计清理的窗口与错误契约见 [08](08-audit-retention.md)。

## 1. 部署形态：本期是 Docker，副本数 2+2

| 项 | 基线 | 说明 |
|---|---|---|
| 落地平台 | **Docker**（Compose / Docker Engine 多副本） | NQ-05；**本期不产出 Kubernetes / Nomad / 云容器服务清单** |
| `api` 副本数 | **2** | 在线请求；无状态、无需会话粘滞 |
| `worker` 副本数 | **2** | 预聚合 / Outbox / 导出 / 定时任务 |
| `job` | **一次性 Go 进程**，非常驻服务 | 迁移与按需任务，跑完退出 |
| 数据库 | 单主库 PostgreSQL 18.7，**无只读副本、无读写分离** | ADR-0007 第 3 节 |
| 外部协调组件 | **R1 无**（不引入 Redis 做锁 / 调度） | DP-07；幂等、Outbox、锁全在 PG |
| 本地 `docker-compose.yml` | 仍**只启动开发库** | 不是生产部署清单；生产清单是 R1 交付物 |

**「支持编排平台」与「本期用 Docker」不矛盾**：ADR-0007 第 3 节约束的是**架构能力**——进程无状态、配置从环境变量注入、迁移只跑一次、定时任务靠数据库锁防重、连接池按副本数推导、探针与优雅停机齐备。这些都继续有效，且副本数确定后可以给出具体数值。换平台时只需换「谁拉起容器」，不需要改「谁跑迁移、谁抢到锁、连接数怎么算」。

**本期明确不定义（NQ-09）**：ConfigMap / Secret 的具体注入方式、Ingress 与 TLS 终止位置、滚动发布策略与优雅停机秒数。只保留三条原则：配置与密钥一律环境变量注入且启动即校验；`/health` 判存活、`/ready` 判就绪；优雅停机先停止接新请求 / 领取新任务，再等在途任务收尾且有超时上限。这些细节需要平台级答案时另立 ADR。

## 2. 进程划分与发布顺序

```
                  ┌──────────────────────────────┐
                  │ PostgreSQL 18.7（单主库）      │
                  │ schema_migrations / 运行记录表 │
                  │ advisory lock / Outbox / 幂等  │
                  └──────────────▲───────────────┘
                                 │ 同一份连接配置，独立连接池上限
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
   ┌────┴─────┐            ┌─────┴──────┐          ┌──────┴──────┐
   │ api × 2  │            │ worker × 2 │          │ job（一次性）│
   │ HTTP     │            │ 定时任务/派发│          │ migrate 等  │
   └──────────┘            └────────────┘          └─────────────┘
```

**发布顺序（不可颠倒）**：

1. `job migrate`：执行 `goose up`，成功后退出；失败则**中止发布**，不启动任何应用副本。
2. 校验 `schema_migrations` 的当前版本等于镜像构建时注入的 `EXPECTED_SCHEMA_VERSION`。
3. 起 / 更新 `api` × 2 与 `worker` × 2；每个实例启动只做「校验配置 → 连库 → 校验迁移版本 → 注册路由 / 调度」。
4. `/ready` 通过后才接流量；滚动过程中旧副本继续服务，直到新副本就绪。

**铁律**：`api` / `worker` 启动时**不得执行迁移或隐式建表**（ADR-0007 第 3 节第 4 条）。多副本下每个 Pod 各自跑迁移会并发改 schema，是上线级事故。`job` 自身也用 advisory lock 防重，避免两个发布同时触发。

`/ready` 判定：数据库连通 + `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1` 等于 `EXPECTED_SCHEMA_VERSION`；不符返回 `503`，并写一条带 `expected` / `actual` 的结构化日志。`/health` 只判进程存活，不因版本不符失败，避免编排层把「不接流量」误判成「进程死了」而反复重启。

## 3. 一次性 Job：Go 实现 + 数据库共享（NQ-06）

### 3.1 形态

| 项 | 约定 |
|---|---|
| 入口 | `cmd/job`，Go 二进制；复用 `internal/platform`（配置、日志、指标）与 `internal/adapter/postgres` |
| 子命令 | `migrate`（goose up）、`audit-cleanup`（按需补跑）、`recompute-summary`（按需重算） |
| 调度方式 | 由发布步骤 / 运维显式拉起，**不使用平台 Job、CronJob、shell 编排或外部调度服务** |
| 禁止 | 不在 `cmd/api` / `cmd/worker` 启动流程里调用迁移；不把「当前跑到哪一步」放本地文件 |

示例（部署命令形态，非生产清单）：

```bash
# 1) 只跑一次迁移；job 内部也取 advisory lock，重复触发会被跳过
docker compose run --rm job migrate

# 2) 校验版本后再起常驻进程
docker compose up -d --scale api=2 --scale worker=2 api worker
```

### 3.2 「数据库方式共享」的四类状态

用户要求「使用数据库方式共享」，具体指跨副本的**全部**共享状态与互斥都落在 PostgreSQL，不依赖进程内存、本地文件、环境变量当锁、Redis 或平台能力：

| 共享内容 | 落在哪 | 作用 |
|---|---|---|
| 迁移版本 | `schema_migrations`（goose） | 谁跑过迁移、当前 schema 版本、`/ready` 的判定依据 |
| 任务互斥 | `pg_try_advisory_lock`（会话级）+ `pg_advisory_xact_lock`（事务级） | 同一执行键任意副本数下只跑一次；`job` 与 `worker` 用同一套工具 |
| 运行痕迹 | `audit_log_cleanup_runs`（[08](08-audit-retention.md) §5） | 「已清理至某日期」的可查记录，不写回 `audit_logs` |
| 工作队列 | `outbox_events` / `idempotency_keys` / `export_jobs`（[02](02-database-design.md) §2） | 用 `FOR UPDATE SKIP LOCKED` 领取，天然支持多副本竞争消费 |

> 关键取舍：**锁与队列同库**意味着 DB 不可用任务就不跑，这在线强一致系统里是可接受且可观测的失败；反过来若引入 Redis 做锁，就要同时处理「DB 正常但 Redis 不可用」与「锁过期但任务没跑完」两类新故障，R1 不需要。DP-07（R1 数据库实现、R2 再评估 Redis 加速）不变，且 Redis 只做加速，不改变原子提交与锁语义。

## 4. 连接池预算与多副本防重（NQ-07 / NQ-08）

### 4.1 连接池预算（副本数 2+2）

PG `max_connections` 保持 **100** 不变；预算按 ADR-0007 第 3 节第 6 条的公式推导，**禁止沿用 pgx 默认值**：

| 消费者 | 副本数 | 每实例 `max_conns` | 小计 |
|---|---|---|---|
| `api` | 2 | 20 | 40 |
| `worker` | 2 | 10 | 20 |
| `job`（一次性，迁移 / 补跑） | 1 | 5 | 5 |
| 运维预留（psql、监控采集、迁移复核、临时排查） | — | — | 15 |
| **合计** | | | **80 ≤ 100**（余量 20） |

配套：

- `min_conns` 取 `max_conns` 的 1/4（api 5 / worker 2 / job 1），避免冷启动瞬间抢连接；`max_conn_lifetime = 30m`、`max_conn_idle_time = 5m`、`health_check_period = 30s`。
- 每实例设 `statement_timeout = 5s`（报表会话 30s）、`idle_in_transaction_session_timeout = 10s`、`lock_timeout = 3s`（[05](05-cross-cutting.md) §10）。
- **扩副本先算预算**：`api` 每增加 1 副本 +20、`worker` 每 +1 +10；水位超过 80% 时优先下调每实例上限，**不要**直接调大 PG `max_connections`。副本数变更属设计变更，须同步本表与压测基线。
- 连接池指标：`db_pool_acquired_conns` / `db_pool_wait_duration_seconds` / `db_pool_max_conns`，按实例与进程类型打标签。

### 4.2 worker 双副本定时任务：完整参考例子（NQ-08）

**执行键命名**：`job:<任务名>:<业务日或维度>`，全部使用 UTC+8 业务日，不用服务器本地时区。

| 任务 | 执行键示例 | 锁粒度 |
|---|---|---|
| 审计清理（每日） | `job:audit_cleanup:2026-11-15` | 全局一把，按天 |
| 日汇总重算 | `job:daily_summary:<shop_id>:2026-11-15` | **每店每天一把**，可并行重算不同店 |
| Outbox 派发 | 不用锁，靠 `FOR UPDATE SKIP LOCKED` | 行级竞争领取 |
| 幂等键清理（每日） | `job:idem_cleanup:2026-11-15` | 全局一把，按天 |

**规则**：每个执行键一把**会话级**锁；`pg_try_advisory_lock` 抢不到立即跳过（不是错误、不排队等待、不重试到别的副本）；解锁必须用**同一条连接**；任务失败后锁随 defer 释放，下一次调度自然重试；任何副本都可能抢到锁，**禁止按副本索引分片任务**；副本从 2 扩到 N 不改任务语义。

```go
// internal/platform/joblock/lock.go（示意，R1 起引入 pgx/v5）
package joblock

import (
    "context"
    "fmt"
    "hash/fnv"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    "log/slog"
)

// RunOnce 保证同一 runKey 在任意副本数下只有一个执行者。
// 返回 ran=false 表示"另一副本正在跑，本次跳过"，不是错误。
func RunOnce(ctx context.Context, pool *pgxpool.Pool, runKey string,
    fn func(context.Context) error) (ran bool, err error) {

    // 会话级 advisory lock 绑定在连接上：必须从 pool 取一条固定 Conn，
    // 不能用 pool.QueryRow（每次可能落在不同的物理连接上）。
    conn, err := pool.Acquire(ctx)
    if err != nil {
        return false, fmt.Errorf("acquire conn for %s: %w", runKey, err)
    }
    defer conn.Release() // 注意：Release 不会释放会话级 advisory lock

    h := fnv.New64a()
    _, _ = h.Write([]byte(runKey))
    key := int64(h.Sum64())

    var got bool
    if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
        return false, fmt.Errorf("try lock %s: %w", runKey, err)
    }
    if !got {
        return false, nil
    }

    defer func() {
        // 用独立短超时 context：任务 ctx 可能已取消，但锁必须显式归还。
        uctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
        defer cancel()
        var released bool
        if uerr := conn.QueryRow(uctx, `SELECT pg_advisory_unlock($1)`, key).Scan(&released); uerr != nil {
            slog.Error("advisory_unlock_failed", "run_key", runKey, "err", uerr)
            // 连接被销毁时 PG 会在会话结束时自动释放锁；此处仍需告警
        }
    }()

    return true, fn(ctx)
}
```

```go
// cmd/worker/main.go（节选）
shanghai, err := time.LoadLocation("Asia/Shanghai") // ADR-0004：固定东八区
if err != nil { /* 启动即失败 */ }

c := cron.New(cron.WithLocation(shanghai))
_, err = c.AddFunc("30 3 * * *", func() {
    day := time.Now().In(shanghai).Format("2006-01-02")
    runKey := "job:audit_cleanup:" + day
    ran, err := joblock.RunOnce(ctx, pool, runKey, func(ctx context.Context) error {
        return auditcleanup.Run(ctx, pool, runKey, cfg.AuditRetentionDays)
    })
    switch {
    case err != nil:
        metrics.AuditCleanupRunsTotal.WithLabelValues("failed").Inc()
        slog.Error("audit_cleanup_failed", "run_key", runKey, "err", err)
    case !ran:
        metrics.AuditCleanupRunsTotal.WithLabelValues("skipped").Inc() // 另一副本持有锁
    default:
        metrics.AuditCleanupRunsTotal.WithLabelValues("succeeded").Inc()
    }
})
```

**两个必须避开的坑**：

1. `pgxpool.Pool.QueryRow` 可能每次命中不同连接，加锁与解锁落在不同会话 → 锁永远不被释放，第二天任务全部跳过。必须从 `pool.Acquire` 得到 `*pgxpool.Conn` 并在其上加 / 解锁。
2. `conn.Release()` 只是把连接归还池，**不会**释放会话级锁；必须显式 `pg_advisory_unlock`。若确需「连接归还即释放」，改用事务级 `pg_advisory_xact_lock`，但锁的生命周期就被事务边界绑住了，长任务不适用。

排查锁残留：

```sql
-- 当前持有的 advisory lock（key 由 classid 高 32 位 + objid 低 32 位拼成）
SELECT pid, granted, ((classid::bigint << 32) | objid::bigint) AS lock_key
FROM pg_locks WHERE locktype = 'advisory';
```

### 4.3 压测基线（NQ-07）

| 项 | 基线 |
|---|---|
| 拓扑 | 与生产一致：`api` × 2 / `worker` × 2 / 单主库 / 无只读副本 |
| 数据集 | 2 家店、3 个账号、10 万行单据、每单约 3 行明细；金额与时间均为合成数据 |
| 加压方式 | 客户端并发按 8 → 16 → 32 递增，每档稳定后采集；不做无限加压，观察 CPU 与连接池水位 |
| 必采指标 | p50/p95/p99 延迟、错误码分布、连接池使用率与等待时长、进程 RSS、大表 `Seq Scan` 比例、慢查询数 |
| 断言 | 单店常用列表 p95 < 200 ms；跨店日报 < 2 s；导出 RSS 不随结果集线性增长；连接池水位 ≤ 80% 预算 |
| 交付 | 压测报告写入运维清单，包含 `EXPLAIN (ANALYZE, BUFFERS)` 存档；阈值需要调整时更新本文 §5 与 ADR-0008，不在配置里悄悄改 |

## 5. 容量与告警阈值（NQ-04，默认基线）

业务数据暂不归档（ADR-0004），因此**容量必须可观测且设阈值**。下表是 R1 默认值，2 副本压测后校准；达到阈值时**另立 ADR** 再评估分区 / 归档，不在代码里加保留窗口。

| 类别 | 指标 | 告警阈值 | 严重阈值 / 动作 |
|---|---|---|---|
| 库体积 | 数据库总大小 | > 200 GB | 磁盘使用率 > 85%：停止非必要写入并评估扩容 |
| 表体积 | 单张业务表（含索引） | > 50 GB | > 100 GB：另立 ADR 评估分区 |
| 索引体积 | 单个索引 | > 20 GB | 检查冗余索引与 `INVALID` 索引 |
| 慢查询 | `> 1s` 语句占全部语句比例 | > 1% | > 5%：暂停发布，抓 `EXPLAIN` |
| 大表扫描 | 业务大表 `Seq Scan` 行数占比 | > 1% | 新增查询出现大表 `Seq Scan` → PR 不予合并（[02](02-database-design.md) §5） |
| 连接池 | 单实例 `acquired / max_conns` | > 80% | > 95%：下调每实例上限或缩副本，禁止直接调大 PG `max_connections` |
| 连接池等待 | `db_pool_wait_duration_seconds` p95 | > 50 ms | > 200 ms：检查慢事务与 `idle_in_transaction` |
| Outbox | 最老未派发事件年龄 / 死信数 | > 5 min / > 0 | 持续增长：worker 副本或订阅者故障 |
| 审计表 | `audit_logs` 体积 / 死元组 | > 20 GB / `n_dead_tup` > 100 万 | 运维执行 `VACUUM (ANALYZE)`（非 FULL、不锁表） |
| 审计清理 | 失败次数 / 距上次成功 | ≥ 1 次 / > 48 h | 立即告警，详见 [08](08-audit-retention.md) §5 |

采集方式：Prometheus exporter 查 `pg_database_size` / `pg_total_relation_size` / `pg_stat_user_tables` / `pg_stat_statements`；结构化日志与指标名沿用 [05](05-cross-cutting.md) §9。阈值变更属设计变更，须同步本文与 ADR-0008 摘要表。

## 6. 测试与验收清单

| 场景 | 断言 |
|---|---|
| 迁移只跑一次 | 两个 `job migrate` 并发触发，只有一个真正执行；`schema_migrations` 无重复版本；`api` / `worker` 启动日志中无 DDL |
| `/ready` 版本不符 | 把 `EXPECTED_SCHEMA_VERSION` 改成旧值后启动，`/ready` 返回 503、`/health` 仍 200 |
| 定时任务防重 | `worker` × 2 同时到调度点，`audit_log_cleanup_runs` 中该 `run_key` 只有一条 `succeeded`，另一副本记 `skipped` 指标；锁不残留 |
| 连接池预算 | 2+2 全量启动后 `SELECT count(*) FROM pg_stat_activity` ≤ 80；扩容演练时重新计算预算 |
| 优雅停机 | 停机期间不下发新任务，在途任务在超时上限内收尾；锁被显式释放 |
| 压测 | §4.3 的指标与断言全部有报告，阈值与本文 §5 一致 |

---

上一册：[06 关键流程](06-key-flows.md) · 下一册：[08 审计日志保留与清理](08-audit-retention.md)

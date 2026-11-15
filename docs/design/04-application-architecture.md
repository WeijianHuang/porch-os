# 前期系统架构详细设计 · 04 应用架构

> 上游：[00 总览](00-overview.md) 第 4、6 节（模块与分层）、[ADR-0002](../decisions/0002-go-backend-stack.md)（技术栈与 9 条阻断项）、[ADR-0004](../decisions/0004-scope-baseline.md)（范围基线）、[ADR-0005](../decisions/0005-no-shop-groups.md)（无门店组）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（shopscope 校验当前门店、无服务端切换端点）。
> 本册把原则落成目录、依赖规则、事务边界与中间件链；横切机制见 [05](05-cross-cutting.md)，业务时序见 [06](06-key-flows.md)。

## 1. 目录结构

```
porch-os/
├── cmd/
│   ├── api/main.go            # HTTP 进程：装配 + 启动 + 优雅停机
│   └── worker/main.go         # 后台进程：预聚合 / Outbox 派发 / 导出 / 定时任务
├── internal/
│   ├── domain/                # 零外部依赖：值对象、状态机、计价、折叠纯函数
│   │   ├── money/             #   Money、分摊 Allocate
│   │   ├── order/             #   Order 聚合、CanTransition、Pricing
│   │   ├── member/            #   会员余额规则
│   │   ├── inventory/         #   库存规则
│   │   ├── reporting/         #   汇总口径的纯函数部分
│   │   └── errs/              #   领域错误（类型化）
│   ├── usecase/               # 用例编排：事务边界、权限、跨模块协作
│   │   ├── openorder/  checkout/  refund/  member/  inventory/  report/  export/
│   │   └── port/              #   用例层定义的仓储/网关接口（由 adapter 实现）
│   ├── adapter/
│   │   ├── http/
│   │   │   ├── router.go      #   路由注册（对应 03 第 8 节端点清单）
│   │   │   ├── middleware/    #   recover / requestid / log / timeout / ratelimit / auth / shopscope / idempotency
│   │   │   ├── handler/       #   参数解析 → 调用例 → 格式化响应，不写业务规则
│   │   │   └── dto/           #   请求/响应结构体，场景专用
│   │   ├── postgres/
│   │   │   ├── query/         #   sqlc 的 .sql 源文件（静态 SQL）
│   │   │   ├── gen/           #   sqlc 生成代码，勿手改
│   │   │   ├── dynamic/       #   squirrel 动态筛选，PlaceholderFormat(Dollar)
│   │   │   ├── derived/       #   复杂口径的普通 SQL（CTE / 条件聚合），不是视图
│   │   │   └── repo/          #   实现 port 接口，返回扁平 DTO
│   │   ├── redis/             #   R2 起：缓存 / 锁
│   │   └── gateway/           #   外部系统适配器（OQ-03 待定；打印已明确排除，ADR-0004）
│   ├── platform/
│   │   ├── config/  log/  tx/  errors/  obs/  idgen/
│   └── di/wire.go             #   编译期依赖注入
├── migrations/                # goose 版本化 SQL（Up / Down）
├── api/openapi.yaml           # 生成并提交，CI 校验无 diff
├── docs/                      # 架构原则、详细设计、ADR、本地开发
├── testdata/                  # 契约快照、计价表驱动用例
├── sqlc.yaml
├── docker-compose.yml
└── go.mod
```

**R0 现状**：仅 `cmd/porch-os/main.go`（标准库 `/health`）与单测；上表目录从 R1 起按模块逐步建立，不一次性创建空目录。

## 2. 依赖规则（用 lint 强制，不靠自觉）

| 包 | 允许 import | 禁止 import |
|---|---|---|
| `domain/*` | 标准库、`domain/errs`、`domain/money` | `adapter/*`、`platform/*`、`usecase/*`、任何数据库驱动 |
| `usecase/*` | `domain/*`、`usecase/port`、标准库 | `adapter/*`、`platform/*` 的具体实现、HTTP 类型 |
| `adapter/*` | `usecase/port`、`domain/*`、`platform/*` | 其他 `adapter` 的私有实现、`cmd/*` |
| `platform/*` | 标准库与第三方基础设施库 | 任何 `domain` / `usecase` 业务包 |
| `cmd/*` | 全部（只做装配） | 业务逻辑 |

用 `golangci-lint` 的 `depguard` 规则把上表写成配置，CI 中失败即阻断。模块化单体最容易退化成「谁都能 import 谁」，边界必须机器可查。

## 3. 进程划分

| 进程 | 职责 | 不做 |
|---|---|---|
| `cmd/api` | 在线请求、WebSocket、健康检查、指标端点 | 不跑定时汇总、不跑长任务 |
| `cmd/worker` | 日报预聚合、Outbox 派发、导出任务、运行期数据清理、定时任务 | 不监听 HTTP 业务端口（仅内部健康探针）；**不做业务数据归档**（ADR-0004） |

两者共用 `internal/` 与同一套迁移；数据库配置相同但连接池上限独立设置。请求处理器里**不允许**直接启动 goroutine 跑长任务：长任务一律落 `export_jobs` / Outbox，由 worker 领取。

## 4. 中间件链（顺序有语义，不可随意调整）

| 序 | 中间件 | 作用 | 为什么在这个位置 |
|---|---|---|---|
| 1 | `recover` | 兜住 panic，转 500 结构化错误 | 必须最外层 |
| 2 | `requestid` + 结构化日志 | 生成/透传 `X-Request-Id`，记录耗时与状态码，脱敏 | 后续所有错误都能带上同一个 id |
| 3 | `timeout` | 普通 5 s / 报表 30 s，context 一路下传 | 让整个链路（含中间件）共享同一个截止时间 |
| 4 | `ratelimit` | 按「主体 + 门店」限流 | 认证后才有稳定主体键；放在认证前只能按 IP |
| 5 | `auth` | 校验 JWT、解析主体 | 后续权限与幂等作用域都依赖主体 |
| 6 | `shopscope` | 从主体 + `user_shops` 推导可见门店集合，校验路径/头参数并注入 context | **必须在所有业务 handler 之前**；注入的是 shop scope，不是 tenant scope（ADR-0003），也不解析任何分组 / 层级对象（ADR-0005）；单店请求缺 `X-Shop-Id` 直接 `400`、不隐式默认，且不存在服务端切换端点（ADR-0006） |
| 7 | `idempotency` | 写操作校验 `Idempotency-Key`、请求指纹、落库判定 | 依赖主体与门店生成 `scope`，故在 6 之后；只读请求跳过 |
| 8 | `handler` | 参数校验 → 调用例 → 格式化响应 | 业务规则不在此层 |

> 顺序 4 与 5 有取舍：限流放在认证前可以按 IP 挡掉未认证的洪泛流量。R1 先用「IP 粗限流 + 认证后细限流」两层，若只保留一层，按认证后细限流实现，并在部署层用网关补 IP 粗限流。

## 5. Unit of Work 与事务边界

Go 没有声明式事务，显式边界反而能迫使巨型写方法被拆开。

```go
// platform/tx（示意接口，R1 起引入 pgx）
type UnitOfWork interface {
    Within(ctx context.Context, fn func(ctx context.Context) error) error
}

// 用例层：事务边界清晰可见
func (uc *Checkout) Pay(ctx context.Context, cmd PayCommand) (Result, error) {
    var out Result
    err := uc.uow.Within(ctx, func(txCtx context.Context) error {
        // 1. 读单据（带 shop_id 与 version）
        // 2. 领域层校验状态机与金额
        // 3. 写 payments / payment_entries、更新 orders、扣库存、写流水
        // 4. 写 idempotency_keys 与 outbox_events —— 必须与 3 在同一事务
        out = ...
        return nil
    })
    return out, err
}
```

规则：

1. `*sql.Tx` 挂在 context 上，仓储从 context 取 `DBTX`；用例层看不到具体连接类型。
2. **事务内不做外部 IO**（不发 HTTP、不推消息、不写文件）；需要通知就写 Outbox，由 worker 事务外派发。
3. 事务尽量短：计价、校验、纯函数计算在进事务前完成；进事务后只做读-校验-写。
4. 更新带 `WHERE id = $1 AND shop_id = $2 AND version = $n`；影响行数为 0 返回 `VERSION_CONFLICT`。
5. 只读查询不走 UoW；跨店报表严禁在事务里长时间持有连接。

## 6. 数据访问分工

| 场景 | 工具 | 约定 |
|---|---|---|
| 固定查询（按 ID、按门店列表） | sqlc（`.sql` → 生成代码） | 类型安全、零反射；SQL 里显式写 `shop_id = $1` |
| 报表 / 可组合筛选 | squirrel | **必须 `PlaceholderFormat(squirrel.Dollar)`**；排序字段白名单，不接受客户端原始字符串 |
| 复杂口径（原可放视图的逻辑） | `derived/*.sql` 普通 SQL | 条件聚合 `FILTER`、CTE、窗口函数；受版本控制、可 diff、可 `EXPLAIN`；**不建数据库视图** |
| 一对多结果 | 默认「两次查询 + Go 侧 group」 | 折叠逻辑是 `internal/domain/` 的纯函数，可脱离 DB 单测 |
| 子集合小且总是全量 | `jsonb_agg(...) FILTER (WHERE ...)` | 仅在确认不会放大行数时使用 |
| 批量写入 | `COPY` | 禁止循环单条 INSERT |
| 幂等写入 | `INSERT ... ON CONFLICT` | 与业务写入同事务 |

**每条查询返回一个场景专用扁平 DTO**；DTO 里嵌套的是「值」而不是「实体对象」。结构体之间只持 ID（ADR-0002 阻断项 ①②）。

## 7. 错误处理

| 层 | 职责 |
|---|---|
| `domain/errs` | 定义类型化领域错误（含是否可重试） |
| `adapter/postgres` | 把 PG 错误码映射成领域错误（唯一约束、外键、死锁），**不向上抛驱动原始错误** |
| `usecase` | 补充业务语义，不吞错 |
| `adapter/http` | 全局中间件把错误映射为 `03` 第 2 节的统一结构，写日志，绝不返回堆栈 |

错误码语义与 `retryable` 见 [03 第 4 节](03-api-design.md)。`recover` 兜住的 panic 记为 `INTERNAL` 并打 ERROR 日志（含 request id），同时告警。

## 8. 配置、启动与依赖注入

- `koanf` 从环境变量与 `.env` 合并配置；**启动时集中校验**，缺关键配置立即退出，不运行到一半才报错。
- 必配：`DATABASE_URL`、`PORT`、JWT 签名密钥、日志级别、连接池上限、各类超时。密钥只存在本地环境，不进仓库（CI 的 hygiene job 拦截）。
- 固定常量：`APP_TIMEZONE=Asia/Shanghai`、币种 `CNY`，启动时校验且**不允许按店或按请求覆盖**；业务日与单号日期的时间换算集中在 `internal/domain/`，SQL 中显式写 `AT TIME ZONE 'Asia/Shanghai'`（ADR-0004）。
- `wire` 编译期生成装配代码，无反射、无运行时容器；`cmd/api` 与 `cmd/worker` 各自有独立的装配入口。
- 启动顺序：校验配置 → 连通数据库（`/ready`）→ 注册路由 → 监听；停机：摘流量 → 等待在途请求（有上限）→ 关闭连接池与 worker。
- 连接池基线（对应架构评审里的超时治理）：`max_conns` 显式设置、`statement_timeout` 5 s（报表 30 s）、`idle_in_transaction_session_timeout` 10 s；禁止使用驱动默认值。

## 9. 测试分层

| 层 | 工具 | 覆盖 |
|---|---|---|
| 领域单测 | `testing` + 表驱动 | 计价与分摊、状态机 `CanTransition`、折叠纯函数 |
| 用例单测 | `testify` | 编排与错误分支（port 用假实现） |
| 集成测试 | `testcontainers-go` 真实 PG 18.7 | 迁移 Up/Down、唯一约束、复合外键、隔离、幂等重放、`EXPLAIN` 计划 |
| 契约测试 | OpenAPI diff + 响应快照 | 字段集合与类型，动态值归一化 |
| 端到端 | 关键流程冒烟 | 开单 → 结账 → 退款 → 日报 |

集成测试**不 mock 服务层**（ADR-0002 阻断项 ⑨）；每个写用例强制重放一次幂等键，断言副作用只发生一次。

## 10. 代码评审检查表

在 [架构原则第 8 节](../ARCHITECTURE.md) 的 10 条阻断项之外，详细设计新增：

1. 新增查询是否在 SQL 里显式带 `shop_id`，复合索引是否以它开头？
2. 子表是否用复合外键 `(shop_id, parent_id)` 保证与父表同店？
3. 唯一约束是否写明「店内唯一」还是「全局唯一」？
4. 写操作是否与幂等记录、Outbox 事件在同一事务？
5. 事务内是否混入了外部 IO 或长任务？
6. 状态迁移是否走 `CanTransition` 纯函数？
7. 新增包是否违反第 2 节的 import 规则？
8. 金额路径是否出现浮点；是否误加了 `currency` 字段、汇率或币种换算（单币种下应无）？
9. 报表是否走汇总表或 keyset 分页，而非大表全扫 / 深 `OFFSET`？业务日是否按 UTC+8 显式换算，而非依赖会话时区？
10. 端点变更是否同步更新 [03 第 8 节](03-api-design.md) 与 `api/openapi.yaml`？
11. 是否引入了范围外机制：本地离线队列、补传/同步水位、打印任务、业务数据归档或保留窗口（ADR-0004 一律禁止）？
12. 是否引入了 `ShopGroup` / `shop_group_*` / 父子门店 / 门店标签分组或按组授权（ADR-0005 一律禁止）？跨店范围是否只用可见门店集合 + 显式 `shop_ids`？
13. 单店请求是否强制显式 `X-Shop-Id`、缺头即 `400` 且不隐式默认？是否未引入 `users.shop_id` / `shops.owner_user_id` / `current_shop` 表、切换端点，或把当前门店写进 JWT / 会话（ADR-0006）？缓存键是否含 `shop_id`，切换时是否处理在途请求？

---

下一册：[05 横切机制](05-cross-cutting.md)。

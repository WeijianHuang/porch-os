# Porch OS · 门廊

> **One backend, many shops.** 一个后台，管好几家店。

面向小微门店的开源经营系统后端：多门店统一管理、跨店会员与库存、按店出账与汇总报表。

**状态：早期工程阶段。** 数据库已选定 **PostgreSQL 18.7**（[ADR-0001](docs/decisions/0001-postgresql-18.md)），后端技术栈已选定 **Go + SQL 优先**（[ADR-0002](docs/decisions/0002-go-backend-stack.md)），数据隔离模型已锁定为**仅门店层 `shop_id`、无租户层**（[ADR-0003](docs/decisions/0003-shop-level-isolation.md)），业务范围基线已锁定为**单币种 CNY / 统一 UTC+8 / 暂不归档 / 不打印 / 不离线**（[ADR-0004](docs/decisions/0004-scope-baseline.md)），门店组概念已取消、跨店改用显式门店集合（[ADR-0005](docs/decisions/0005-no-shop-groups.md)），一个用户可拥有多间门店且当前门店可在页面切换（[ADR-0006](docs/decisions/0006-user-shops-and-current-shop.md)），**剩余开放问题已全部关闭**（[ADR-0007](docs/decisions/0007-remaining-open-questions.md)：R1 不接外部服务 / 前端框架归属 / 支持编排平台且暂不读写分离 / 不做跨店自动清分 / 审计日志保留 30 天可配并定时删除），**NQ 全部决议、详细设计冻结与 R1 运行基线已锁定**（[ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md)：Docker 2+2 + Go `cmd/job` + 数据库共享）。`cmd/api` 已提供无状态 HTTP 服务（认证、门店、目录、会员、余额等首批端点），`cmd/job` 提供 migrate / seed-user / audit-cleanup 一次性任务，`cmd/porch-os` 保留为 CI 的零依赖健康探针基线。**前期系统架构详细设计已冻结**（[ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md)），见 [`docs/design/00-overview.md`](docs/design/00-overview.md)：OQ / NQ 全部关闭、DP-01–07 已接受、R1 首批范围与验收标准已定，本期落地 Docker 2+2 + Go Job + 数据库共享。前端框架由 `porch-console` 独立决策（NQ-14 已决：以既有内部前端工程实践为依据，工程名称不写入公开仓库），不阻塞后端。

License: MIT · 语言：中文文档（发布前补英文版）

## 为什么叫「门廊」

门廊是店铺的入口：它撑起整间店，但顾客不会把它当成商品。我们希望这套系统也是这样——承载多店经营的全部结构，保持中性，不预设任何具体业态。

## 项目简介

一个人开一家店时，记账靠本子就够；开到第二家、第三家，数据就开始散落在不同的表格、账号和设备里。Porch OS 要解决的就是这一步：用一套系统管理任意数量的门店，让每笔单据、每件库存、每位会员都明确属于哪家店——单店能独立核算，多店能汇总对比（直接按门店选择范围，不设门店组），并且从数据层就防止把 A 店的数据读进 B 店的页面。

它只做通用的经营底座：门店、商品与服务、跨店会员、开单收银、报表与权限。项目刻意不预设任何行业规则，餐饮、零售、服务业的用法由使用方自行扩展。数据库、后端技术栈与「仅门店层」的隔离模型均已锁定，仓库已固定多店模型、工程约定、可复现的本地数据库环境，并落地 R1 首批后端实现（领域 / 应用 / 接口 / 数据访问四层与数据库迁移）；后续模块级决策继续以 ADR 逐条记录。

**边界（不做什么）**

- 不绑定具体业态，不内置行业专属规则
- **不引入租户 / 组织 / 公司等上层隔离维度**，只按 `shop_id` 隔离（[ADR-0003](docs/decisions/0003-shop-level-isolation.md)）
- **不设门店组 / 店群**：不做分组管理、按组汇总与按组授权；跨店直接按门店选择范围（[ADR-0005](docs/decisions/0005-no-shop-groups.md)）
- 不做 SaaS 多租户平台（所有门店同属一个经营主体，单库单 schema）
- 不做加盟 / 连锁总部体系
- 不替代财务软件、开票与报税系统
- 现阶段不提供托管服务
- **不做多币种 / 多时区**：全系统只有人民币 `¥`，展示与业务日统一东八区（UTC+8）
- **不做业务数据归档**：单据、支付与流水在线全量保留，不设保留窗口；运行期数据（幂等键、已投递事件、临时文件）照常清理。**审计日志是例外**：默认保留 30 天、可配置、由 worker 定时删除，不归档、不分区（[ADR-0007](docs/decisions/0007-remaining-open-questions.md)）
- **R1 不接外部服务**：不引入支付 / 短信 / 对象存储 SDK 与回调端点，只留网关接口位置（[ADR-0007](docs/decisions/0007-remaining-open-questions.md)）
- **暂不读写分离**：本期落地 Docker 多副本（`api` × 2 / `worker` × 2），但所有读写走同一个主库，不引入只读副本（[ADR-0007](docs/decisions/0007-remaining-open-questions.md) / [ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md)，见 `docs/design/07`）
- **不做跨店自动清分**：只记录消费发生在哪家店，差额用按店分组的报表呈现（[ADR-0007](docs/decisions/0007-remaining-open-questions.md)）
- **不支持打印**：无小票模板、打印端点与设备适配；Excel / CSV 文件导出不受影响
- **不支持离线收银**：写操作全程在线强一致，不做本地队列、补传与冲突合并（[ADR-0004](docs/decisions/0004-scope-baseline.md)）

## About (English)

Porch OS is an open-source backend for small businesses running more than one shop. Every order, stock item and membership is tied to a specific shop, so each location can be accounted for on its own while several shops can still be rolled up and compared — with shop-level isolation enforced in the data layer, not left to the client.

It provides only a shared foundation: shops, catalog, members, checkout, reports and permissions. It assumes no particular industry. PostgreSQL 18.7 (ADR-0001), a Go, SQL-first backend stack (ADR-0002) and a shop-only isolation model (ADR-0003) are locked: `shop_id` is the only isolation dimension, with no tenant layer. Scope is fixed by ADR-0004: one currency (CNY), one timezone (UTC+8), no business-data archiving, no printing and no offline checkout. There is no shop-group concept (ADR-0005): cross-shop views select shops explicitly from the caller's visible set, and group-based authorization does not exist. One user can own several shops (ADR-0006): the relationship is many-to-many via `user_shops`, and the active shop is a request-time context carried explicitly on every single-shop request, validated server-side on each call and switchable in the console without any backend state or endpoint. ADR-0007 closes the remaining open questions: no external integrations in R1, the frontend stack is decided by `porch-console`, orchestrated deployment is supported without read/write splitting or read replicas, cross-shop balance differences are not auto-settled, and audit logs are retained for a configurable 30 days and deleted by a scheduled worker job (this is runtime cleanup, not business-data archiving). The repository currently provides the multi-shop model, engineering conventions, a reproducible local database, a compilable bootstrap and a **frozen** detailed design baseline in `docs/design/` (ADR-0008: all OQ/NQ closed, DP-01–07 accepted, R1 scope fixed). The runtime baseline is Docker with two API replicas, two worker replicas, a Go one-shot job for migrations and PostgreSQL-only coordination (no Redis-based locking in R1). The frontend stack choice belongs to `porch-console` and follows its existing internal frontend practice.

## 核心能力（规划）

| 能力 | 说明 |
|---|---|
| 多门店 | 一个后台管理任意数量门店，所有业务数据带 `shop_id` 维度；**一个用户可拥有多间门店并在页面切换当前门店**（[ADR-0006](docs/decisions/0006-user-shops-and-current-shop.md)） |
| 跨店汇总 | 直接按可见门店集合选择门店范围，报表按 `shop_id` 分列；**不设门店组 / 店群**（[ADR-0005](docs/decisions/0005-no-shop-groups.md)） |
| 跨店会员 | 会员档案全局唯一；余额、消费流水带 `shop_id`，跨店核销需显式授权 |
| 商品与服务 | 目录统一维护，价格、库存、上下架可按店覆盖 |
| 开单与收银 | 每笔单据绑定门店与操作人，写入幂等 |
| 报表 | 单店明细、跨店汇总、按店对比；导出流式写出，不全量读进内存 |
| 权限 | 角色控制 + `shop_id` 数据级校验，防止跨店越权 |

## 多店模型

- `Shop` = 单店，**不设 `ShopGroup` / 店群 / 门店组**（[ADR-0005](docs/decisions/0005-no-shop-groups.md)）；也不使用 `Chain` / `Franchise`（暗示加盟体系，对小门店过重）。
- 所有业务表带 `shop_id`；默认查询必须带门店条件，跨店查询需显式声明。
- **数据隔离只有一层 `shop_id`**：单库、单 schema、一个经营主体；不存在 `tenant_id` / `org_id` / `company_id` 或 RLS 按会话变量过滤，见 [ADR-0003](docs/decisions/0003-shop-level-isolation.md)。
- 一条业务记录始终只属于一个 `shop_id`。会员档案、商品目录等全局实体不带隔离维度，但其流水、余额、库存与单据必须带 `shop_id`。
- 授权是「用户 ↔ 可见门店集合」的直接关系（`user_shops` 唯一来源），不经过任何上层实体或分组对象；跨店汇总直接用显式门店 ID 集合，不按组汇总。门店维度校验放在服务层统一入口，不依赖前端传参是否可信。
- **一个用户可以拥有多间门店**（[ADR-0006](docs/decisions/0006-user-shops-and-current-shop.md)）：从主体视角是 1:N，关系实现仍是 `user_shops` 多对多（同一家店也会有多个账号），因此不用 `users.shop_id` 外键，也不在 `shops` 上记 owner。「拥有」指授权关系，不是拥有店里的业务数据。
- **当前门店可在页面切换**：它是请求期上下文，不落表、不进 JWT、不作授权来源；登录返回可见门店集合与默认门店，之后每个单店请求显式带 `X-Shop-Id`，服务端每次与可见集合求交。切换是纯客户端行为，没有服务端切换端点，也不改变权限与数据归属。

详见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) 第 2.0 节。

## 设计原则

集中配置并在启动时校验、快速失败 · 类型化错误与全局错误处理 · 结构化 JSON 日志贯穿 request id · 边界处校验所有输入 · 多步写入事务化 · 数据库变更通过可回滚迁移 · 缓存一律带 TTL 且写后失效 · 后台任务幂等、可重试、有死信 · 健康检查与优雅停机 · 日志不记录密码、令牌与隐私数据。

## 决策状态

| 项目 | 状态 |
|---|---|
| 数据库 | **已定：PostgreSQL 18.7** — [ADR-0001](docs/decisions/0001-postgresql-18.md) |
| 数据隔离模型 | **已定：仅门店层 `shop_id`，无租户层** — [ADR-0003](docs/decisions/0003-shop-level-isolation.md) |
| 门店组 / 店群 | **已定：不设**，跨店改用显式门店集合 — [ADR-0005](docs/decisions/0005-no-shop-groups.md) |
| 用户-门店关系 / 当前门店 | **已定：一人多店（`user_shops` 多对多），当前门店可在页面切换、不落库** — [ADR-0006](docs/decisions/0006-user-shops-and-current-shop.md) |
| 后端语言 / Web 框架 | **已定：Go 1.25 / Gin v1.10** — [ADR-0002](docs/decisions/0002-go-backend-stack.md) |
| 接口风格 | **已定：REST + OpenAPI**（`swaggo/swag`） |
| 数据访问 | **已定：sqlc + squirrel + pgx/v5 + 自建 Unit of Work**；无实体关联图、无数据库视图 |
| 迁移管理 | **已定：goose + atlas** |
| 认证与密码 | **已定：golang-jwt/v5 短时效访问令牌 + 服务端刷新令牌；x/crypto/bcrypt** |
| 缓存 | **已定：Redis + ristretto**（带 TTL、写后失效；跨店权限判定不缓存） |
| 实时能力 | **已定：WebSocket**（`coder/websocket`） |
| 配置 / 日志 / 可观测性 | **已定：koanf + slog + Prometheus + OpenTelemetry** |
| 测试 | **已定：testing + testify + testcontainers-go** |
| 币种 / 时区 | **已定：单币种 CNY（`¥`）；统一 UTC+8 `Asia/Shanghai`** — [ADR-0004](docs/decisions/0004-scope-baseline.md) |
| 数据归档 | **已定：暂不归档**，业务数据在线全量保留、不分区 — [ADR-0004](docs/decisions/0004-scope-baseline.md) |
| 打印 / 离线收银 | **已定：均不支持**；文件导出保留 — [ADR-0004](docs/decisions/0004-scope-baseline.md) |
| 外部集成（支付网关 / 短信 / 对象存储） | **已定：R1 不接**，只留网关接口位置，接入前另立 ADR — [ADR-0007](docs/decisions/0007-remaining-open-questions.md) |
| 部署形态 | **已定：Docker 部署 `api` × 2 / `worker` × 2 + 一次性 Go `cmd/job`；暂不读写分离 / 只读副本**；跨副本共享状态与互斥全落 PG、连接池预算 80 ≤ 100 — [ADR-0007](docs/decisions/0007-remaining-open-questions.md) + [ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md)（见 `docs/design/07`） |
| 跨店差额清分 | **已定：不做自动清分**，只记录消费发生店、按店分组呈现 — [ADR-0007](docs/decisions/0007-remaining-open-questions.md) |
| 审计日志留存 | **已定：默认 30 天、可配置、定时删除**；业务请求只追加不可改删 — [ADR-0007](docs/decisions/0007-remaining-open-questions.md)（默认 30 / 取值 `1..3650` / UTC+8 自然日窗口 / 超期 `400` 错误码 / 运行痕迹与告警，见 `docs/design/08`） |
| 前端框架 / API 客户端 / 组件库 / E2E | **归属与依据已定：由 `porch-console` 独立决策**，以既有内部前端工程（待重构）实践为依据；工程名称不写入公开仓库 — [ADR-0007](docs/decisions/0007-remaining-open-questions.md) + [ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md)（NQ-14） |
| 详细设计状态 / R1 范围 | **已冻结**：`docs/design/00`–`09` + ADR-0001–0008；R1 首批端点与验收标准见 [ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md) §3 |
| 运行基线 | **已定**：Docker `api` × 2 / `worker` × 2、Go `cmd/job`、连接池 80 ≤ 100、审计 UTC+8 自然日窗口、容量阈值 — [ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md) / `docs/design/07` |

R0 的 `cmd/porch-os` 引导程序只用标准库、零外部依赖，作为 CI 的编译与测试基线保留；R1 已按模块引入 ADR-0002 中的首批依赖（Gin、pgx/v5、goose、golang-jwt/v5、squirrel、decimal、bcrypt），其余依赖仍按模块逐步引入，不一次性堆进 `go.mod`。

## 快速开始

前置：Docker Desktop（含 Compose v2）、Go 1.25+（与 `go.mod` 一致，CI 按此编译）。数据库、迁移任务、`cmd/api` 服务均已可运行。

```bash
git clone https://github.com/WeijianHuang/porch-os.git
cd porch-os
cp .env.example .env       # 把 POSTGRES_PASSWORD 改成随机值，并同步 DATABASE_URL 中的密码
docker compose up -d db    # 启动 PostgreSQL 18.7
docker compose ps          # 等待 STATUS 变为 healthy

go build ./...
go test ./...
go run ./cmd/job migrate                                   # 唯一允许改 schema 的进程
SEED_LOGIN=admin SEED_PASSWORD=<本地口令> go run ./cmd/job seed-user
go run ./cmd/api           # /health 与 /ready，默认 8080
curl -s localhost:8080/health   # {"status":"ok"}
```

`cmd/api` 连接数据库并提供 `/health`（存活）与 `/ready`（就绪，校验 schema 版本）；`cmd/porch-os` 是不连库的早期引导程序，仅为 CI 基线保留。连接串、版本校验与重置方式见 [`docs/LOCAL-DEV.md`](docs/LOCAL-DEV.md)。

## 仓库结构

```
.github/            CI 工作流（Go 构建/测试 + 仓库卫生）与 PR 模板
AGENTS.md           给 AI 编码助手的仓库约定与红线（与 CONTRIBUTING.md 互补）
cmd/api/            R1 无状态 HTTP 服务（/health、/ready 与 /api/v1）
cmd/job/            一次性任务：migrate / seed-user / audit-cleanup
cmd/porch-os/       早期零依赖引导程序（/health），CI 基线保留
internal/           app 装配 · adapter/http 接口 · adapter/postgres 数据访问 · domain 领域 · platform 配置与错误 · security 令牌
docs/               架构原则、详细设计、本地环境、决策记录
  ARCHITECTURE.md   隔离模型、分层、技术栈落地与稳健性原则
  design/           前期详细设计 00–16（总览 / 领域 / 数据库 / 接口 / 应用架构 / 横切 / 流程 / 部署 / 审计 / 跨店差额 / 队列与服务人员 / 房间与楼层 / 多队列调度规则引擎 / 服务人员状态控制 / ID 卡与方形服务格子 / 服务类目录项时长 / 开单收银工作台）
  LOCAL-DEV.md      本地 PostgreSQL 启动、验证与重置
  decisions/        ADR：0001 数据库、0002 后端技术栈、0003 仅门店层隔离、0004 范围基线、0005 取消门店组、0006 用户-门店关系与切换、0007 剩余开放问题决议、0008 设计冻结与运行基线、0009 队列与服务人员、0010 在岗状态与队列顺序、0011 房间与楼层、0012 多队列调度规则引擎、0013 收款与退款归属、0014 金额派生与退款渠道、0015 服务人员状态控制、0016 充值退款与券口径、0017 ID 卡、0018 服务时长、0019 试价、0020 让利结构化、0021 金额调整更名、0022 订单卡 / 房间与同行组、0023 服务资质与明细执行人、0024 服务人员分类与辅助选人
migrations/         goose 迁移：0001 初始化 schema、0002 RBAC 基线、0003 队列与服务人员、0004 队列服务人员权限、0005 在岗状态与队列顺序、0006 房间与楼层、0007 多队列调度规则引擎、0008 ID 卡、0009 服务时长、0010 R3 订单域、0011 订单卡 / 房间与同行组、0012 服务项资质与明细执行服务员、0013 服务人员分类与目录项分组
go.mod              Go 模块定义（go 1.25；已引入 Gin / pgx / goose 等首批依赖）
docker-compose.yml  本地数据库（PostgreSQL 18.7）
.env.example        配置占位（仅示例值）
CONTRIBUTING.md     贡献流程
CHANGELOG.md        变更记录
```

分层依赖方向见架构文档；`cmd/worker` 尚在路线图中，未实现。

## R1 quick start

Prerequisites: Go 1.25+ (matching `go.mod`) and a running PostgreSQL 18.7 (`docker compose up -d db`).

```bash
cp .env.example .env          # set DATABASE_URL / JWT_SECRET / EXPECTED_SCHEMA_VERSION=16
go run ./cmd/job migrate      # the ONLY process allowed to change the schema
SEED_LOGIN=admin SEED_PASSWORD=<local-password> go run ./cmd/job seed-user
go run ./cmd/api              # /health and /ready on PORT (default 8080)
```

Smoke check: `POST /api/v1/auth/login` with `Idempotency-Key`, then use the
returned bearer token with `X-Shop-Id: <default_shop_id>` on single-shop routes.
A missing `X-Shop-Id` returns `400 SHOP_CONTEXT_REQUIRED` rather than silently
defaulting to the only visible shop (ADR-0006).

Processes: `cmd/api` (stateless HTTP), `cmd/job` (one-shot migrate / seed /
on-demand audit cleanup). `cmd/worker` is next on the roadmap; neither `api`
nor `worker` runs migrations at startup.

## 文档导航

| 文档 | 看什么 |
|---|---|
| `README.md` | 定位、边界、能力规划、决策状态 |
| `docs/ARCHITECTURE.md` | 隔离模型（2.0）、分层、技术栈落地与稳健性原则 |
| [`docs/design/00-overview.md`](docs/design/00-overview.md) | **前期详细设计总览（已冻结）**：质量属性、模块划分、数据归属三分法、已接受的设计决策与已关闭的 OQ / NQ 清单、分册地图 |
| `docs/design/01`–`06` | 领域模型 / 数据库设计 / 接口设计 / 应用架构 / 横切机制 / 关键流程 |
| `docs/design/07`–`09` | 部署与运行基线 / 审计日志保留与清理 / 跨店差额与清分边界 |
| `docs/design/10` | 队列与服务人员（一店多队列、店内多队列分配、在岗状态与队列内顺序、主页默认队列视图） |
| `docs/design/11` | 房间与楼层（房间号店内唯一 + 名称、楼层整数属性、按楼层分组展示） |
| `docs/design/12` | 多队列调度规则引擎（复用 staff 作为执行者、8 步判定链 + 6 种联动 + 各自落位 + 逐队列置顶、PG 落表与审计） |
| `docs/LOCAL-DEV.md` | 本地数据库与引导程序的启动、验证与重置 |
| `docs/decisions/0001-postgresql-18.md` | 为什么选 PG 18.7、代价与复审条件 |
| `docs/decisions/0002-go-backend-stack.md` | 为什么选 Go + SQL 优先、评审阻断项与不选什么 |
| `docs/decisions/0003-shop-level-isolation.md` | 为什么只有 `shop_id` 一层、禁止清单与复审条件 |
| `docs/decisions/0004-scope-baseline.md` | 单币种 / 东八区 / 暂不归档 / 不打印 / 不离线：范围基线与复审条件 |
| `docs/decisions/0005-no-shop-groups.md` | 为什么取消门店组、跨店范围如何表达与复审条件 |
| `docs/decisions/0006-user-shops-and-current-shop.md` | 一人多店的关系基数、当前门店的定义与页面切换协议 |
| `docs/decisions/0007-remaining-open-questions.md` | 剩余开放问题决议：外部集成、前端框架归属、编排平台部署、跨店差额、审计日志 30 天保留（其遗留的 NQ-01–16 已由 [ADR-0008](docs/decisions/0008-design-freeze-and-nq-resolutions.md) 全部关闭） |
| `docs/decisions/0009-queues-and-staff.md` | 队列 / 服务人员领域扩展、主页默认队列视图（受控扩展 R1） |
| `docs/decisions/0010-duty-status-and-queue-order.md` | 服务人员在岗状态与队列内顺序（第二次受控扩展，schema 版本 5） |
| `docs/decisions/0011-rooms-and-floors.md` | 房间与楼层（第三次受控扩展，schema 版本 6） |
| `docs/decisions/0012-scheduling-engine.md` | 多队列调度规则引擎深度整合（第四次受控扩展，复用 staff 作为执行者，schema 版本 7） |
| `docs/decisions/0013-payment-and-refund-ownership.md` | 收款 `kind` 与归属、支付 / 退款动作班次与操作人（第五次受控扩展，R3 契约修正，落地于 schema 10） |
| `docs/decisions/0014-derived-amounts-and-refund-entries.md` | 删除 `paid_amount` / `refunding`、金额派生与逐渠道退款明细（第六次受控扩展，R3 契约修正，落地于 schema 10） |
| `docs/decisions/0015-staff-state-and-position-controls.md` | 服务人员状态、停止轮转与位次人工控制（第七次受控扩展，无 schema 变更） |
| `docs/decisions/0016-refund-source-and-topup-reversal.md` | 充值退款锚点、余额不足整笔拒绝、禁止跨店退款、券归折扣（第八次受控扩展，R3 契约修正，落地于 schema 10） |
| `docs/decisions/0017-id-cards-and-square-slots.md` | 店内 ID 卡与方形服务格子（第九次受控扩展，schema 版本 8） |
| `docs/decisions/0018-service-duration.md` | 服务类目录项时长 `duration_minutes` 与幂等种子（第十次受控扩展，schema 版本 9） |
| `docs/decisions/0019-order-price-preview.md` | 订单实时试价端点 `orders/price` 与统一计价函数（第十一次受控扩展）；订单域落地于 `00010_orders.sql`，schema 版本 10，契约另见 ADR-0013 / 0014 / 0016 |
| `docs/decisions/0020-structured-price-adjustments.md` | 订单让利原因结构化（`type` 五值 + `title`/`description`、券以 `source_id` 预留追溯，第十二次受控扩展，无独立迁移版本） |
| `docs/decisions/0021-rename-order-price-adjustments.md` | `price_adjustments` 更名为 `order_price_adjustments`（含约束名，第十三次受控扩展；历史 ADR 旧名别名映射） |
| `docs/decisions/0022-order-card-binding-and-companion-groups.md` | 订单绑定 ID 卡（`orders.card_id` + 释放时间 / 原因，结算 / 取消自动释放订单与持卡两层）与房间（`orders.room_id`，提交前必填）、同行组（`companion_groups` / 成员表，多单成组共同结算、可合并 / 拆分、付款仍逐单）；第十四次受控扩展，迁移 `00011`，schema 11；修订 ADR-0017 / ADR-0011 相关边界 |
| `docs/decisions/0023-service-item-qualifications-and-line-staff.md` | 服务员 ↔ 服务目录项资质关系（`staff_service_items`）与明细级执行人（`order_lines.staff_id` + `staff_name_snapshot`）；服务行提交前必填、商品行不绑定，删人保留快照；第十五次受控扩展，迁移 `00012`，schema 12 |
| `docs/decisions/0024-staff-categories-and-assisted-order-entry.md` | 店内服务人员分类（`staff_categories` + `staff.category_id`，一人最多一类）与目录项分组（`catalog_items.category_id`）；开单弹窗「添加服务员」按分类 / 状态选人后连续添加服务项；第十六次受控扩展，迁移 `00013`，schema 13 |
| `docs/decisions/0025-staff-shift-templates.md` | 店内班次模板（`shift_templates` + `staff.shift_template_id`，一人最多一班）与全量分配 / 删除解绑；与订单域营业班次实例 `shifts` 分离；第十七次受控扩展，迁移 `00014`，schema 14，设计落 `17` 分册 |
| `docs/decisions/0026-editable-staff-code-and-alias.md` | 服务人员工号可编辑（`staff.code` 店内唯一不变，新增 `409 STAFF_CODE_CONFLICT`）与可选别名 `staff.alias`（展示 / 检索标签，非唯一、不授权）；第十八次受控扩展，迁移 `00015`，schema 15 |
| `docs/decisions/0027-staff-gender-and-phone.md` | 服务人员性别（`staff.gender` 三值哨兵 + CHECK）与可选手机号 `staff.phone`（非唯一、不建索引、不接短信）；编辑弹窗三区两列重排；第十九次受控扩展，迁移 `00016`，schema 16 |
| `docs/decisions/0008-design-freeze-and-nq-resolutions.md` | NQ-01–16 全部决议、DP-01–07 接受、详细设计冻结、R1 范围与验收、Docker 2+2 / Go Job / 连接池 / 审计 / 容量基线 |
| `CONTRIBUTING.md` | 分支、提交与 PR 约定 |
| `CHANGELOG.md` | 变更记录 |

## 路线图

- **R0** 工程约定、架构原则、社区文件、数据库 / 后端技术栈 / 隔离模型 / 范围基线 / 取消门店组 / 用户-门店关系与切换 / 剩余开放问题决议 ADR、可编译引导骨架、真实 CI 与**前期系统架构详细设计**（OQ / NQ 全部关闭、DP-01–07 已接受，**详细设计已冻结**，R1 范围与验收标准已定 — ADR-0008）
- **R1** 领域模型 + `shop_id` 单层隔离 + 单店 CRUD + 集成测试基线
- **R2** 多店汇总（按显式门店集合）、门店可见范围授权、跨店会员
- **R3** 开单收银、幂等与单号生成、报表流式导出（**订单域后端已落地**：班次 / 订单 / 支付 / 退款 / 充值退款，迁移 `00010`、schema 版本 10；报表流式导出与前端页面待续）
- **R4** 实时能力、可观测性、生产加固清单

## 配套仓库

前端控制台为同一账号下的 **[porch-console](https://github.com/WeijianHuang/porch-console)** 仓库。两个仓库独立发版、独立 issue 分类。

## 贡献

见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。欢迎从文档、领域模型和测试用例入手。

## 许可证

[MIT](LICENSE)。若后续需要明确的专利授权条款，可在发版前评估切换 Apache-2.0。

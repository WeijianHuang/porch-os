# Porch OS · 门廊

> **One backend, many shops.** 一个后台，管好几家店。

面向小微门店的开源经营系统后端：多门店统一管理、跨店会员与库存、按店出账与汇总报表。

**状态：早期工程阶段。** 数据库已选定 **PostgreSQL 18.7**（[ADR-0001](docs/decisions/0001-postgresql-18.md)），后端技术栈已选定 **Go + SQL 优先**（[ADR-0002](docs/decisions/0002-go-backend-stack.md)），数据隔离模型已锁定为**仅门店层 `shop_id`、无租户层**（[ADR-0003](docs/decisions/0003-shop-level-isolation.md)），业务范围基线已锁定为**单币种 CNY / 统一 UTC+8 / 暂不归档 / 不打印 / 不离线**（[ADR-0004](docs/decisions/0004-scope-baseline.md)）。`cmd/porch-os` 目前只提供零依赖的健康探针，作为 CI 的编译与测试基线；业务代码尚未开始。**前期系统架构详细设计已启动**，见 [`docs/design/00-overview.md`](docs/design/00-overview.md)（设计提案，尚未冻结；含模块划分、schema、接口契约、关键流程、已确认决议与剩余开放问题）。前端框架由 `porch-console` 独立决策，不阻塞后端。

License: MIT · 语言：中文文档（发布前补英文版）

## 为什么叫「门廊」

门廊是店铺的入口：它撑起整间店，但顾客不会把它当成商品。我们希望这套系统也是这样——承载多店经营的全部结构，保持中性，不预设任何具体业态。

## 项目简介

一个人开一家店时，记账靠本子就够；开到第二家、第三家，数据就开始散落在不同的表格、账号和设备里。Porch OS 要解决的就是这一步：用一套系统管理任意数量的门店，让每笔单据、每件库存、每位会员都明确属于哪家店——单店能独立核算，店群能汇总对比，并且从数据层就防止把 A 店的数据读进 B 店的页面。

它只做通用的经营底座：门店与店群、商品与服务、跨店会员、开单收银、报表与权限。项目刻意不预设任何行业规则，餐饮、零售、服务业的用法由使用方自行扩展。数据库、后端技术栈与「仅门店层」的隔离模型均已锁定，当前仓库先固定多店模型、工程约定、可复现的本地数据库环境与可编译的引导骨架；后续模块级决策继续以 ADR 逐条记录。

**边界（不做什么）**

- 不绑定具体业态，不内置行业专属规则
- **不引入租户 / 组织 / 公司等上层隔离维度**，只按 `shop_id` 隔离（[ADR-0003](docs/decisions/0003-shop-level-isolation.md)）
- 不做 SaaS 多租户平台（所有门店同属一个经营主体，单库单 schema）
- 不做加盟 / 连锁总部体系
- 不替代财务软件、开票与报税系统
- 现阶段不提供托管服务
- **不做多币种 / 多时区**：全系统只有人民币 `¥`，展示与业务日统一东八区（UTC+8）
- **不做业务数据归档**：在线全量保留，不设保留窗口（仅清理幂等键、已投递事件等运行期数据）
- **不支持打印**：无小票模板、打印端点与设备适配；Excel / CSV 文件导出不受影响
- **不支持离线收银**：写操作全程在线强一致，不做本地队列、补传与冲突合并（[ADR-0004](docs/decisions/0004-scope-baseline.md)）

## About (English)

Porch OS is an open-source backend for small businesses running more than one shop. Every order, stock item and membership is tied to a specific shop, so each location can be accounted for on its own while a group of shops can still be rolled up and compared — with shop-level isolation enforced in the data layer, not left to the client.

It provides only a shared foundation: shops and shop groups, catalog, members, checkout, reports and permissions. It assumes no particular industry. PostgreSQL 18.7 (ADR-0001), a Go, SQL-first backend stack (ADR-0002) and a shop-only isolation model (ADR-0003) are locked: `shop_id` is the only isolation dimension, with no tenant layer. Scope is fixed by ADR-0004: one currency (CNY), one timezone (UTC+8), no business-data archiving, no printing and no offline checkout. The repository currently provides the multi-shop model, engineering conventions, a reproducible local database, a compilable bootstrap and a detailed design baseline in `docs/design/` (proposal, not frozen). The frontend stack is decided separately in `porch-console`.

## 核心能力（规划）

| 能力 | 说明 |
|---|---|
| 多门店 | 一个后台管理任意数量门店，所有业务数据带 `shop_id` 维度 |
| 店群 | `ShopGroup` 只是汇总与授权的分组标签，不是数据归属层 |
| 跨店会员 | 会员档案全局唯一；余额、消费流水带 `shop_id`，跨店核销需显式授权 |
| 商品与服务 | 目录统一维护，价格、库存、上下架可按店覆盖 |
| 开单与收银 | 每笔单据绑定门店与操作人，写入幂等 |
| 报表 | 单店明细、跨店汇总、按店对比；导出流式写出，不全量读进内存 |
| 权限 | 角色控制 + `shop_id` 数据级校验，防止跨店越权 |

## 多店模型

- `Shop` = 单店；`ShopGroup` = 店群。不使用 `Chain` / `Franchise`（暗示加盟体系，对小门店过重）。
- 所有业务表带 `shop_id`；默认查询必须带门店条件，跨店查询需显式声明。
- **数据隔离只有一层 `shop_id`**：单库、单 schema、一个经营主体；不存在 `tenant_id` / `org_id` / `company_id` 或 RLS 按会话变量过滤，见 [ADR-0003](docs/decisions/0003-shop-level-isolation.md)。
- `ShopGroup` 只用于汇总与授权；一条业务记录始终只属于一个 `shop_id`。会员档案、商品目录等全局实体不带隔离维度，但其流水、余额、库存与单据必须带 `shop_id`。
- 授权是「用户 ↔ 可见门店集合」的直接关系，不经过任何上层实体；门店维度校验放在服务层统一入口，不依赖前端传参是否可信。

详见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) 第 2.0 节。

## 设计原则

集中配置并在启动时校验、快速失败 · 类型化错误与全局错误处理 · 结构化 JSON 日志贯穿 request id · 边界处校验所有输入 · 多步写入事务化 · 数据库变更通过可回滚迁移 · 缓存一律带 TTL 且写后失效 · 后台任务幂等、可重试、有死信 · 健康检查与优雅停机 · 日志不记录密码、令牌与隐私数据。

## 决策状态

| 项目 | 状态 |
|---|---|
| 数据库 | **已定：PostgreSQL 18.7** — [ADR-0001](docs/decisions/0001-postgresql-18.md) |
| 数据隔离模型 | **已定：仅门店层 `shop_id`，无租户层** — [ADR-0003](docs/decisions/0003-shop-level-isolation.md) |
| 后端语言 / Web 框架 | **已定：Go 1.23+ / Gin v1.10** — [ADR-0002](docs/decisions/0002-go-backend-stack.md) |
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
| 外部集成（支付网关 / 短信 / 对象存储） | 待定 — OQ-03 剩余项，R1 默认不接 |
| 前端框架 / API 客户端 / 组件库 / E2E | 待定 — 由 `porch-console` 独立决策，不阻塞后端 |

R0 的 `cmd/porch-os` 引导程序只用标准库、零外部依赖，目的是让 CI 有可复现的编译与测试基线；ADR-0002 列出的依赖从 R1 起按模块逐步引入，不一次性堆进 `go.mod`。

## 快速开始

前置：Docker Desktop（含 Compose v2）、Go 1.23+。业务代码尚未开始，当前能跑的是数据库与健康探针。

```bash
git clone https://github.com/WeijianHuang/porch-os.git
cd porch-os
cp .env.example .env       # 把 POSTGRES_PASSWORD 改成随机值
docker compose up -d db    # 启动 PostgreSQL 18.7
docker compose ps          # 等待 STATUS 变为 healthy

go build ./...
go test ./...
PORT=8080 go run ./cmd/porch-os
curl -s localhost:8080/health   # {"status":"ok"}
```

引导程序**不连接数据库**，`/health` 只用于验证进程与 CI；`DATABASE_URL` 从 R1 起接入。连接串、版本校验与重置方式见 [`docs/LOCAL-DEV.md`](docs/LOCAL-DEV.md)。

## 仓库结构

```
.github/            CI 工作流（Go 构建/测试 + 仓库卫生）与 PR 模板
cmd/porch-os/       R0 引导程序（/health）与单测
docs/               架构原则、详细设计、本地环境、决策记录
  ARCHITECTURE.md   隔离模型、分层、技术栈落地与稳健性原则
  design/           前期详细设计 00–06（总览 / 领域 / 数据库 / 接口 / 应用架构 / 横切 / 流程）
  LOCAL-DEV.md      本地 PostgreSQL 启动、验证与重置
  decisions/        ADR：0001 数据库、0002 后端技术栈、0003 仅门店层隔离、0004 范围基线
go.mod              Go 模块定义（当前零外部依赖）
docker-compose.yml  本地数据库（PostgreSQL 18.7）
.env.example        配置占位（仅示例值）
CONTRIBUTING.md     贡献流程
CHANGELOG.md        变更记录
```

`internal/` 将在 R1 按领域层 / 应用层 / 接口层 / 数据访问层建立，依赖方向见架构文档。

## 文档导航

| 文档 | 看什么 |
|---|---|
| `README.md` | 定位、边界、能力规划、决策状态 |
| `docs/ARCHITECTURE.md` | 隔离模型（2.0）、分层、技术栈落地与稳健性原则 |
| [`docs/design/00-overview.md`](docs/design/00-overview.md) | **前期详细设计总览**：质量属性、模块划分、数据归属三分法、已确认决议与剩余开放问题、分册地图 |
| `docs/design/01`–`06` | 领域模型 / 数据库设计 / 接口设计 / 应用架构 / 横切机制 / 关键流程 |
| `docs/LOCAL-DEV.md` | 本地数据库与引导程序的启动、验证与重置 |
| `docs/decisions/0001-postgresql-18.md` | 为什么选 PG 18.7、代价与复审条件 |
| `docs/decisions/0002-go-backend-stack.md` | 为什么选 Go + SQL 优先、评审阻断项与不选什么 |
| `docs/decisions/0003-shop-level-isolation.md` | 为什么只有 `shop_id` 一层、禁止清单与复审条件 |
| `docs/decisions/0004-scope-baseline.md` | 单币种 / 东八区 / 暂不归档 / 不打印 / 不离线：范围基线与复审条件 |
| `CONTRIBUTING.md` | 分支、提交与 PR 约定 |
| `CHANGELOG.md` | 变更记录 |

## 路线图

- **R0** 工程约定、架构原则、社区文件、数据库 / 后端技术栈 / 隔离模型 / 范围基线 ADR、可编译引导骨架、真实 CI 与**前期系统架构详细设计**（设计提案，当前阶段）
- **R1** 领域模型 + `shop_id` 单层隔离 + 单店 CRUD + 集成测试基线
- **R2** 多店汇总、店群授权、跨店会员
- **R3** 开单收银、幂等与单号生成、报表流式导出
- **R4** 实时能力、可观测性、生产加固清单

## 配套仓库

前端控制台为同一账号下的 **[porch-console](https://github.com/WeijianHuang/porch-console)** 仓库。两个仓库独立发版、独立 issue 分类。

## 贡献

见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。欢迎从文档、领域模型和测试用例入手。

## 许可证

[MIT](LICENSE)。若后续需要明确的专利授权条款，可在发版前评估切换 Apache-2.0。

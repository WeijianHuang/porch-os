# AGENTS.md

面向 AI 编码助手的仓库约定。给人类贡献者的完整流程见 [`CONTRIBUTING.md`](CONTRIBUTING.md)；本文是它的高密度摘要，两者冲突时以 ADR 与 `docs/design/` 为准。

## 这是什么

Porch OS 是面向小微门店的多门店经营系统后端（One backend, many shops）。单库单 schema，所有业务数据按 `shop_id` 隔离。前端在独立仓库 `porch-console`。仓库含三个入口：`cmd/api`（无状态 HTTP 服务）、`cmd/job`（一次性任务）、`cmd/porch-os`（早期 CI 健康探针，仅为基线保留）；`cmd/worker` 尚在路线图中。

## 先读这些（权威性从高到低）

1. `docs/decisions/0001`–`0008`：架构决策，**已接受且不可随意推翻**。
2. `docs/design/00`–`09`：**已冻结**的详细设计（领域 / 数据库 / 接口 / 应用架构 / 横切 / 流程 / 部署 / 审计 / 跨店差额）。
3. `docs/ARCHITECTURE.md`：隔离模型、分层、依赖方向、代码评审阻断项。
4. `README.md` / `docs/LOCAL-DEV.md`：定位与本地环境。

改 schema、隔离条件、状态机、错误码语义、端点清单，或副本数 / 连接池 / 保留天数等冻结基线时，**必须先改对应的 ADR 或 `docs/design/`，再改代码**，并在 PR 中说明影响章节。只改实现、不碰契约的改动不需要动设计文档。

## 不可违反的红线

**隔离模型（ADR-0003 / 0005 / 0006）**
- 只有 `shop_id` 一层隔离。**禁止**引入 `tenant_id` / `org_id` / `company_id` 或任何上层隔离维度，禁止 RLS。
- 不设门店组 / 店群 / 父子门店 / 分组实体；跨店范围只用「用户可见门店集合 + 显式门店 ID 集合」。
- 默认查询必须带门店条件；跨店查询要显式声明并做授权校验。权限结果不因缓存而与实时授权不一致。
- 用户与门店走 `user_shops` 多对多。**禁止** `users.shop_id` 外键、`shops.owner_user_id`、`current_shop` 表或任何服务端「切换当前门店」端点。
- 当前门店不落库、不进 JWT。单店请求必须显式带 `X-Shop-Id`，缺失返回 `400 SHOP_CONTEXT_REQUIRED`，**绝不隐式默认**为唯一可见门店。缓存键必须含 `shop_id`。

**数据访问与事务**
- SQL-first：静态 SQL 手写，动态查询用 squirrel 且必须 `PlaceholderFormat(squirrel.Dollar)`，排序字段走白名单。**不用 ORM**，不建实体关联图（结构体之间只持 ID），不用数据库视图承载计算口径。
- 一条查询返回一个场景专用扁平 DTO。
- 多步写入走事务（Unit of Work）；所有写操作必须带幂等键，业务写入与幂等记录同事务；单号唯一性靠数据库约束。
- **迁移只由 `cmd/job migrate` 执行**；常驻进程启动时不得跑迁移或隐式建表。数据库变更必须附带可回滚迁移。

**金额、时区与范围基线（ADR-0004）**
- 金额用 `shopspring/decimal` 对应 PG `numeric`，**禁用 `float64`**；单币种 CNY，无币种字段与汇率。
- 全系统统一 `Asia/Shanghai`（UTC+8），`timestamptz` 以 UTC 存储；不引入按店时区。
- 不引入币种 / 汇率、打印设备、离线同步机制；不给业务数据加保留窗口或归档查询。唯一例外是 `audit_logs`：默认保留 30 天、可配置、按 UTC+8 自然日由 worker 清理，业务请求只追加不可改删。

**外部依赖与部署（ADR-0007 / 0008）**
- R1 不接外部 SDK / 回调（支付 / 短信 / 对象存储），不引只读副本与读写分离、内部往来账或自动清分。
- 进程保持无状态；本期形态为 Docker `api` × 2 / `worker` × 2 + 一次性 `cmd/job`，连接池预算 80 ≤ 100。
- 跨副本共享状态与互斥全部走 PostgreSQL（版本表 / advisory lock / 运行记录表）；定时任务每个执行键一把锁，用同一条连接加解锁。

**测试与安全**
- 领域逻辑用纯函数单测；涉及数据库的集成测试用真实 PG 容器，不在集成测试里 mock 服务层。
- 日志不记录密码、令牌与隐私数据；令牌不进 URL 查询参数。
- 接口变更须同步更新文档与 `porch-console` 对应 issue。

## 常用命令

```bash
cp .env.example .env                 # 改 POSTGRES_PASSWORD，并同步 DATABASE_URL 中的密码
docker compose up -d db              # 本地 PostgreSQL 18.7（仅开发用，非生产清单）

go build ./...
go vet ./...
gofmt -l .                           # 有输出即格式不合格
go test ./... -race -count=1

go run ./cmd/job migrate             # 建表 / 升级 schema
SEED_LOGIN=admin SEED_PASSWORD=<本地口令> go run ./cmd/job seed-user
go run ./cmd/api                     # /health 与 /ready，默认 8080
```

CI（`.github/workflows/ci.yml`）在 push 到 main 与所有 PR 上运行 `build` 与 `hygiene` 两个 job：前者跑 build / vet / gofmt / test -race，后者拦截 `.env`、密钥文件与备份文件被提交。提交前自检这几项，并确认无 `.env`、无真实经营数据进入暂存区。提交信息遵循 Conventional Commits，分支命名 `feat/xxx`、`fix/xxx`、`docs/xxx`。

# 本地开发环境

> 状态：数据库、R1 后端（无状态 `cmd/api` 服务 + 一次性 `cmd/job` 任务）与 R0 引导程序均可运行。`cmd/api` 连接数据库，`cmd/porch-os` 是不连库的早期 CI 基线。
>
> **本文只覆盖本地开发**。按 [ADR-0007](decisions/0007-remaining-open-questions.md) 与 [ADR-0008](decisions/0008-design-freeze-and-nq-resolutions.md)，本期落地为 **Docker 部署、无状态 `api` × 2 / `worker` × 2**，单主库读写、暂不读写分离；迁移只由 Go `cmd/job migrate` 执行，跨副本共享状态与定时任务互斥全部走 PostgreSQL（advisory lock + 版本表 + 运行记录表），连接池预算 80 ≤ 100。`docker-compose.yml` 只是开发库，不是生产部署清单；平台级配置注入 / Ingress / TLS / 滚动发布时长本期不定义（NQ-09）。完整基线见 [`design/07-deployment-and-operations.md`](design/07-deployment-and-operations.md)。

## 前置条件

- Docker Desktop（含 Compose v2）
- Go 1.25+（与 `go.mod` 的 `go 1.25` 一致，CI 按此编译）
- 本机 `5432` 端口空闲；被占用时改 `.env` 里的 `PORCH_PG_PORT`，无需改其他文件

## 一次性准备

```bash
cp .env.example .env
# 把 POSTGRES_PASSWORD 改成随机值，并同步修改 DATABASE_URL 中的密码
```

`.env` 已被 `.gitignore` 忽略，绝不提交。Compose 只从 `.env` 读取 `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` / `PORCH_PG_PORT`；`TZ` / `PGTZ` 与 `-c timezone` 在 Compose 里固定为 `Asia/Shanghai`，不需要也不允许按环境覆盖。`DATABASE_URL` / `APP_TIMEZONE` / `JWT_SECRET` / `EXPECTED_SCHEMA_VERSION` 等由 `cmd/api` 与 `cmd/job` 读取，其中敏感项无默认值、缺失即启动失败（见 `.env.example` 与 `internal/platform/config`）。

## 启动与停止

```bash
docker compose up -d db          # 首次会拉取 postgres:18.7
docker compose ps                # 等 STATUS 变成 healthy
docker compose logs -f db
docker compose stop db           # 停服务，保留数据
docker compose down              # 移除容器，保留命名卷
docker compose down -v           # 连数据卷一起删除，不可恢复，仅用于重置
```

## 验证版本与连接

```bash
docker compose exec db psql -U porch -d porch -tAc 'show server_version;'
# 期望输出：18.7

docker compose exec db psql -U porch -d porch -c 'select current_database(), current_user;'
```

## 验证业务时区（ADR-0004）

全系统固定 `Asia/Shanghai`（UTC+8）；`timestamptz` 仍以 UTC 存储，这里只影响人工查库读数与默认会话时区。

```bash
docker compose exec db psql -U porch -d porch -tAc 'show timezone;'
# 期望输出：Asia/Shanghai

docker compose exec db psql -U porch -d porch -tAc \
  "select to_char(timestamptz '2026-11-15 16:00:00+00' at time zone 'Asia/Shanghai', 'YYYY-MM-DD HH24:MI');"
# 期望输出：2026-11-16 00:00 —— UTC 16:00 即东八区次日 0 点，是业务日与单号日期的切换点
```

从旧版 Compose 配置升级时执行 `docker compose up -d --force-recreate db` 让 `-c timezone=...` 生效；命名卷会保留，不需要重新初始化数据。

宿主机连接串：

```text
postgresql://porch:<password>@localhost:5432/porch?sslmode=disable
```

## 编译与运行

```bash
go build ./...
go test ./...
go run ./cmd/job migrate                                    # 唯一允许改 schema 的进程
SEED_LOGIN=admin SEED_PASSWORD=<本地口令> go run ./cmd/job seed-user   # 仅本地，创建 admin 用户
go run ./cmd/api                                            # /health、/ready，默认 8080
curl -s localhost:8080/health     # {"status":"ok"}
curl -s localhost:8080/ready      # 校验 goose schema 版本 = EXPECTED_SCHEMA_VERSION
```

`PORT` 与 `LOG_LEVEL` 来自环境变量（见 `.env.example`），缺省分别为 `8080` / `info`。`cmd/job` 另有 `audit-cleanup` 子命令用于按需清理过期审计日志；它和 `migrate`、`seed-user` 都是一次性任务，跑完即退出，常驻进程启动时绝不跑迁移。冒烟测试：带 `Idempotency-Key` 调 `POST /api/v1/auth/login`，再用返回的 token 加 `X-Shop-Id` 访问单店路由（缺 `X-Shop-Id` 返回 `400 SHOP_CONTEXT_REQUIRED`）。CI 执行同样的 `go build` / `go vet` / `gofmt` / `go test -race`，本地跑通即可预期 CI 通过。

早期引导程序仍可单独运行，仅为 CI 基线保留，不连接数据库：

```bash
PORT=8080 go run ./cmd/porch-os
curl -s localhost:8080/health     # {"status":"ok"}
```

## 版本与镜像约定

- 大版本固定 **18.7**，依据见 [`decisions/0001-postgresql-18.md`](decisions/0001-postgresql-18.md)。本地、CI、生产必须保持同一大版本。
- 使用 Docker Hub 官方 `postgres` 镜像并按小版本 pin。若官方源不可达，可从可达的镜像加速源拉取后重新打标签为 `postgres:18.7`；**不要**把其他厂商打包的镜像直接指向已有数据卷——环境变量约定与数据目录不同，会报数据目录版本不兼容。
- **PG 18 镜像的卷路径已变**：官方镜像的 `PGDATA` 是 `/var/lib/postgresql/18/docker`，只需把命名卷挂到 `/var/lib/postgresql`。沿用旧版的 `/var/lib/postgresql/data` 会被镜像判定为「残留的旧版本数据」并直接拒绝启动；从旧集群迁移数据必须走 `pg_dump` / `pg_upgrade`，不能复用卷。
- 小版本升级（18.7 → 18.8）换 tag 重启即可；跨大版本升级必须走 `pg_dump` / `pg_upgrade`，不能就地替换镜像。

## 数据与密钥

- 数据存放在 Compose 命名卷 `pgdata`（实际卷名带项目名前缀），不在仓库目录内。
- `.env`、数据库导出与备份一律不提交；CI 的 `hygiene` job 会拦截这类文件。
- 本地实例只用于开发，不要写入任何真实经营数据。

------------

# 本地开发环境

> 状态：目前只有数据库环境；应用与前端代码尚未开始。本文只描述本地 PostgreSQL 实例，不预设语言与框架。

## 前置条件

- Docker Desktop（含 Compose v2）
- 本机 `5432` 端口空闲；被占用时改 `.env` 里的 `PORCH_PG_PORT`，无需改其他文件

## 一次性准备

```bash
cp .env.example .env
# 把 POSTGRES_PASSWORD 改成随机值，并同步修改 DATABASE_URL 中的密码
```

`.env` 已被 `.gitignore` 忽略，绝不提交。Compose 只读取 `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` / `PORCH_PG_PORT`；`DATABASE_URL` 留给未来的应用进程。

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

宿主机连接串：

```text
postgresql://porch:<password>@localhost:5432/porch?sslmode=disable
```

## 版本与镜像约定

- 大版本固定 **18.7**，依据见 [`decisions/0001-postgresql-18.md`](decisions/0001-postgresql-18.md)。本地、CI、生产必须保持同一大版本。
- 使用 Docker Hub 官方 `postgres` 镜像并按小版本 pin。若官方源不可达，可从可达的镜像加速源拉取后重新打标签为 `postgres:18.7`；**不要**把其他厂商打包的镜像直接指向已有数据卷——环境变量约定与数据目录不同，会报数据目录版本不兼容。
- **PG 18 镜像的卷路径已变**：官方镜像的 `PGDATA` 是 `/var/lib/postgresql/18/docker`，只需把命名卷挂到 `/var/lib/postgresql`。沿用旧版的 `/var/lib/postgresql/data` 会被镜像判定为「残留的旧版本数据」并直接拒绝启动；从旧集群迁移数据必须走 `pg_dump` / `pg_upgrade`，不能复用卷。
- 小版本升级（18.7 → 18.8）换 tag 重启即可；跨大版本升级必须走 `pg_dump` / `pg_upgrade`，不能就地替换镜像。

## 数据与密钥

- 数据存放在 Compose 命名卷 `pgdata`（实际卷名带项目名前缀），不在仓库目录内。
- `.env`、数据库导出与备份一律不提交；CI 的骨架检查会拦截这类文件。
- 本地实例只用于开发，不要写入任何真实经营数据。

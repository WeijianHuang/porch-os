# Changelog

本文件遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与语义化版本。

## [Unreleased]

### 新增

- 仓库骨架：README、MIT 许可证、环境变量占位、CI 骨架检查、PR 模板、贡献指南。
- `docs/ARCHITECTURE.md`：多店模型、术语表与架构原则。
- 待定决策清单：语言 / 数据库 / 接口风格 / 认证 / 实时方案 / 前端框架。
- `README.md` 补写「项目简介 / About (English)」章节，明确项目定位与「不做什么」的边界。
- 新增「文档导航」小节，统一各文档入口。
- `docs/decisions/0001-postgresql-18.md`：数据库选型 ADR（已接受）。
- `docs/LOCAL-DEV.md`：本地 PostgreSQL 的启动、版本校验与重置方式。
- `docs/decisions/0002-go-backend-stack.md`：后端技术栈 ADR（已接受）——Go + SQL 优先、逐项理由、代码评审阻断项与复审条件。
- `go.mod` 与 `cmd/porch-os/`：零外部依赖的 R0 引导程序（`/health`）及单测，作为可复现的编译基线。

### 变更

- 数据库选定 **PostgreSQL 18.7**；README 的「待定决策」整理为「决策状态」，标出唯一已定项，其余五项继续待定。
- 新增 `docker-compose.yml`（官方 `postgres:18.7` 镜像 + 命名卷 + 健康检查）。
- `.env.example` 补充数据库变量占位；真实口令只放本地 `.env`，不提交。
- `docs/ARCHITECTURE.md` 补第 2.1 节「数据库层落地」，并把 ADR 章节指向已接受的 0001。
- **后端技术栈锁定**：README「决策状态」补齐语言 / 框架 / 接口 / 数据访问 / 迁移 / 认证 / 缓存 / 实时 / 可观测性 / 测试各项；`ARCHITECTURE.md` 新增第 8 节技术栈落地与九条评审阻断项，并去掉「与技术栈无关」「仍待定」等过期表述；ADR-0001 与 `LOCAL-DEV.md` 同步链接到 ADR-0002。
- **CI 由骨架检查升级为真实构建**：`build` job 执行 `go build` / `go vet` / `gofmt` / `go test -race`，`hygiene` job 保留必需文件与密钥、真实数据防护。
- 前端框架、API 客户端、组件库与 E2E 工具仍待定，由 `porch-console` 独立决策。

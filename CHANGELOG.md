# Changelog

本文件遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与语义化版本。

## [Unreleased]

### 新增

- 仓库骨架：README、MIT 许可证、环境变量占位、CI 骨架检查、PR 模板、贡献指南。
- `docs/ARCHITECTURE.md`：多店模型、术语表与技术栈无关的架构原则。
- 待定决策清单：语言 / 数据库 / 接口风格 / 认证 / 实时方案 / 前端框架。
- `README.md` 补写「项目简介 / About (English)」章节，明确项目定位与「不做什么」的边界。
- 新增「文档导航」小节，统一各文档入口。
- `docs/decisions/0001-postgresql-18.md`：数据库选型 ADR（已接受）。
- `docs/LOCAL-DEV.md`：本地 PostgreSQL 的启动、版本校验与重置方式。

### 变更

- 数据库选定 **PostgreSQL 18.7**；README 的「待定决策」整理为「决策状态」，标出唯一已定项，其余五项继续待定。
- 新增 `docker-compose.yml`（官方 `postgres:18.7` 镜像 + 命名卷 + 健康检查）。
- `.env.example` 补充数据库变量占位；真实口令只放本地 `.env`，不提交。
- `docs/ARCHITECTURE.md` 补第 2.1 节「数据库层落地」，并把 ADR 章节指向已接受的 0001。

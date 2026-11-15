# 贡献指南

感谢参与 Porch OS。项目处于早期设计阶段，文档、领域模型和测试用例的贡献与写代码同样有价值。

## 开始之前

- 先读 [`README.md`](README.md) 与 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)，尤其是「多店模型」和「设计原则」。
- 大改动先在 issue 里讨论方案，避免直接提一个大 PR。
- 参与讨论请遵守 [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/)。

## 开发流程

1. Fork 后从 `main` 建分支，命名 `feat/xxx`、`fix/xxx`、`docs/xxx`。
2. 本地配置：`cp .env.example .env`，填入本机值；`.env` 不提交。
3. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)：`feat: 支持店群汇总`、`fix: 跨店查询漏加 shop_id`。
4. 提交前自检：无 `.env`、无密钥、无真实经营数据、无备份文件被加入暂存区。
5. 开 PR 并填写模板，关联对应 issue。

## 提交内容约定

- **多店相关改动**：任何新增查询都必须明确 `shop_id` 维度；跨店查询要在 PR 描述里写明授权校验方式。禁止引入 `tenant_id` / `org_id` / `company_id` 等上层隔离维度（隔离模型的唯一权威见 [ADR-0003](docs/decisions/0003-shop-level-isolation.md)）。
- **数据库变更**：必须附带可回滚迁移，禁止只改代码不改 schema。
- **接口变更**：同步更新文档与 `porch-console` 对应 issue，保持前后端契约一致。
- **测试**：领域逻辑用纯函数单测；涉及数据库的集成测试使用真实数据库（容器或事务回滚），不要为提速在集成测试里 mock 服务层。

## 报告问题

在 issue 里说明：期望行为、实际行为、复现步骤、影响范围（单店 / 跨店）、环境信息。请勿粘贴真实门店数据、令牌或密钥。

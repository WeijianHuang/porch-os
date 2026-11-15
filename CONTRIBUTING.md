# 贡献指南

感谢参与 Porch OS。项目处于早期设计阶段，文档、领域模型和测试用例的贡献与写代码同样有价值。

## 开始之前

- 先读 [`README.md`](README.md) 与 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)，尤其是「多店模型」和「设计原则」。
- 大改动先在 issue 里讨论方案，避免直接提一个大 PR。
- 参与讨论请遵守 [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/)。

## 开发流程

1. Fork 后从 `main` 建分支，命名 `feat/xxx`、`fix/xxx`、`docs/xxx`。
2. 本地配置：`cp .env.example .env`，填入本机值；`.env` 不提交。
3. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)：`feat: 支持跨店日报汇总`、`fix: 跨店查询漏加 shop_id`。
4. 提交前自检：无 `.env`、无密钥、无真实经营数据、无备份文件被加入暂存区。
5. 开 PR 并填写模板，关联对应 issue。

## 提交内容约定

- **多店相关改动**：任何新增查询都必须明确 `shop_id` 维度；跨店查询要在 PR 描述里写明授权校验方式。禁止引入 `tenant_id` / `org_id` / `company_id` 等上层隔离维度（隔离模型的唯一权威见 [ADR-0003](docs/decisions/0003-shop-level-isolation.md)）。
- **不要引入门店组**：不设 `ShopGroup` / 店群 / 父子门店 / 门店标签分组，也不要按组授权；跨店范围只用「可见门店集合 + 显式 `shop_ids`」（[ADR-0005](docs/decisions/0005-no-shop-groups.md)）。
- **用户与门店**：一个用户可管理多间门店，关系走 `user_shops`；禁止 `users.shop_id` 外键、`shops.owner_user_id`、服务端「切换当前门店」端点或把当前门店写进 JWT / 会话。单店请求缺 `X-Shop-Id` 应报 400 而非隐式默认；缓存键必须含 `shop_id`（[ADR-0006](docs/decisions/0006-user-shops-and-current-shop.md)）。
- **范围基线**：单币种 CNY、统一 UTC+8、暂不归档、不打印、不离线收银已由 [ADR-0004](docs/decisions/0004-scope-baseline.md) 锁定；不要引入币种/汇率、按店时区、打印设备、离线同步机制，也**不要给业务数据加保留窗口或归档查询**。唯一例外是 `audit_logs`：默认保留 30 天、可配置、由 worker 定时删除（[ADR-0007](docs/decisions/0007-remaining-open-questions.md)），它是运行期数据，不是业务归档。需要扩大范围时先开 issue 并新建 ADR。
- **部署与清理（ADR-0007）**：R1 不接入外部 SDK / 回调；不引入只读副本与读写分离、内部往来账或自动清分；进程保持无状态，**启动时不得跑迁移**、定时任务必须靠数据库锁防重、连接池按副本数推导。审计记录对业务请求只追加不可改删，唯一删除路径是 worker 清理任务（分批、可重入、有指标与告警、不写回审计表）。
- **数据库变更**：必须附带可回滚迁移，禁止只改代码不改 schema。
- **接口变更**：同步更新文档与 `porch-console` 对应 issue，保持前后端契约一致。
- **测试**：领域逻辑用纯函数单测；涉及数据库的集成测试使用真实数据库（容器或事务回滚），不要为提速在集成测试里 mock 服务层。

## 报告问题

在 issue 里说明：期望行为、实际行为、复现步骤、影响范围（单店 / 跨店）、环境信息。请勿粘贴真实门店数据、令牌或密钥。

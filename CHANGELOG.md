# Changelog

本文件遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与语义化版本。

## [Unreleased]

### 新增

- `docs/decisions/0008-design-freeze-and-nq-resolutions.md`：NQ-01–16 全部决议 ADR（已接受）——接受 DP-01–07、确认 `user_shops` 多对多、**冻结详细设计**、固定 R1 首批端点范围与验收标准，并把 Docker 2+2 部署、Go `cmd/job` + 数据库共享、连接池预算、审计清理窗口与容量阈值写成可执行基线；含四组理解澄清与六组备选方案。
- `docs/design/07-deployment-and-operations.md`：部署与运行基线——Docker `api` × 2 / `worker` × 2、发布顺序与 `/ready` 版本校验、四类数据库共享状态、连接池预算 80 ≤ 100、**worker 双副本 advisory lock 的完整 Go 例子**（含「同一条 `pgx.Conn` 加解锁」陷阱）、2 副本压测基线、容量与告警阈值表。
- `docs/design/08-audit-retention.md`：审计日志保留与清理——默认 30（取值 `1..3650`、不允许关闭、变更需重启）、**UTC+8 自然日** cutoff SQL、每日 03:30 / 单批 5000 行 / autovacuum 策略、`400 AUDIT_RETENTION_WINDOW_EXCEEDED` 错误契约、`audit_log_cleanup_runs` 痕迹表 DDL、8 个指标与 7 条告警、测试清单。
- `docs/design/09-cross-shop-difference.md`：跨店差额与清分边界详细说明——差额来源、按店流水与 `transfer_ref`、条件聚合报表口径、合成数据示例、为何不做自动清分、经营需要判断清单、六组边界澄清与未来启用的七项必答条件。
- 新增 `audit_log_cleanup_runs` DDL、UTC+8 自然日 cutoff、分批删除与表级 autovacuum 参数已在本地真实 PostgreSQL 18.7 事务中验证通过并回滚（`public` schema 0 表）；cutoff 实测为东八区 `2026-10-16 00:00:00+08`，与 `design/08` 示例一致。

- 仓库骨架：README、MIT 许可证、环境变量占位、CI 骨架检查、PR 模板、贡献指南。
- `docs/ARCHITECTURE.md`：多店模型、术语表与架构原则。
- 待定决策清单：语言 / 数据库 / 接口风格 / 认证 / 实时方案 / 前端框架。
- `README.md` 补写「项目简介 / About (English)」章节，明确项目定位与「不做什么」的边界。
- 新增「文档导航」小节，统一各文档入口。
- `docs/decisions/0001-postgresql-18.md`：数据库选型 ADR（已接受）。
- `docs/LOCAL-DEV.md`：本地 PostgreSQL 的启动、版本校验与重置方式。
- `docs/decisions/0002-go-backend-stack.md`：后端技术栈 ADR（已接受）——Go + SQL 优先、逐项理由、代码评审阻断项与复审条件。
- `go.mod` 与 `cmd/porch-os/`：零外部依赖的 R0 引导程序（`/health`）及单测，作为可复现的编译基线。
- `docs/decisions/0003-shop-level-isolation.md`：隔离模型 ADR（已接受）——只有 `shop_id` 一层、禁止租户 / 组织 / 公司等上层维度、跨店共享实体的处理规则与复审条件。
- `docs/design/00`–`06`：**前期系统架构详细设计**（当时为设计提案，现已由 ADR-0008 冻结）——00 总览（质量属性、模块划分、数据归属三分法、7 项设计提案与 8 个开放问题）、01 领域模型与状态机、02 数据库 schema 与可执行 DDL 节选、03 接口契约与错误码、04 应用架构与依赖规则、05 横切机制、06 关键流程时序与测试清单。（其中 4 个开放问题随后由 ADR-0004 关闭，其余 4 个由 ADR-0007 关闭，OQ 现已全部关闭；门店组是初版内置假设，由 ADR-0005 取消；门店切换流程 F-07 由 ADR-0006 补充、审计清理流程 F-08 由 ADR-0007 补充，见下方「变更」。）
- `docs/decisions/0004-scope-baseline.md`：业务范围基线 ADR（已接受）——单币种 CNY、统一东八区、暂不归档、不支持打印、不支持离线收银；含备选方案、理由、边界澄清与复审条件。
- `docs/decisions/0005-no-shop-groups.md`：取消门店组 ADR（已接受）——不设 `ShopGroup` / 分组表 / 分组端点 / 按组授权，跨店能力改由「可见门店集合 + 显式 `shop_ids`」表达；修订 ADR-0003 第 3 条，并明确「取消门店组 ≠ 取消跨店汇总」。
- `docs/decisions/0006-user-shops-and-current-shop.md`：用户-门店关系与切换协议 ADR（已接受）——一个用户可关联多间门店（`user_shops` 多对多，禁止 `users.shop_id` 单外键），「当前门店」是客户端持有、服务端每次校验的请求期上下文；登录返回门店集合与默认门店，单店请求必须显式带 `X-Shop-Id`，切换零写入、无服务端端点。
- `docs/decisions/0007-remaining-open-questions.md`：剩余开放问题决议 ADR（已接受）——关闭 OQ-03 其余 / 04 / 05 / 07 / 08，使 **OQ-01–08 全部关闭**；R1 不接外部服务、前端框架归属 `porch-console`、支持编排平台且暂不读写分离、不做跨店自动清分、审计日志默认保留 30 天可配并定时删除；含三组理解澄清、五组备选方案、部署与清理后果、NQ 清单（其后由 ADR-0008 全部关闭）与复审条件（修订 ADR-0004 第 3 节第 3 条）。

### 变更

- **关闭全部 NQ 并冻结详细设计（ADR-0008）**：ADR-0006 的「严格 1:N 悬置假设」关闭（确认 `user_shops` 多对多、schema 与切换协议零改动）；ADR-0007 的「仍需确认」全部关闭、历史状态订正；ADR-0004 / 0005 的「尚未冻结」表述订正。`design/00` 状态改为 Frozen、第 7 节 DP 升格为已接受决策、8.2 节换成 NQ-01–16 已决议清单、分册地图扩到 `00`–`09`。
- **运行基线落地**：`design/02` 表清单新增 `audit_log_cleanup_runs`、迁移改由 Go `cmd/job` 执行并写明连接池数值与容量阈值；`design/03` 错误码表新增 `AUDIT_RETENTION_WINDOW_EXCEEDED`、范围排除清单新增「审计专用导出端点」、审计查询边界改为已冻结契约；`design/04` 目录与进程表新增 `cmd/job`、部署约束改为 Docker 2+2、配置增 `EXPECTED_SCHEMA_VERSION` 与连接池上限；`design/05` 补执行键命名、锁例子引用、指标名与数值；`design/06` F-08 按 UTC+8 自然日窗口与运行痕迹重写。
- **架构与仓库文件同步**：`ARCHITECTURE.md` 的 ADR 列表、部署行、详细设计分册地图（7 册 → 10 册）与冻结后变更规则；README（状态 / About / 边界 / 决策状态 / 导航 / 仓库结构 / 路线图）、`CONTRIBUTING.md`、PR 模板、`LOCAL-DEV.md`、`.env.example`（新增 `API_MAX_CONNS` / `WORKER_MAX_CONNS` 与版本校验说明）、`docker-compose.yml` 注释一并订正。
- **`porch-console` 同步**：NQ-14 选型依据、审计超期查询错误码与「不渲染空态」约定、后端契约已冻结、边界与前端待定决策章节改写；两仓均保持与内部项目脱钩（工程名称不出现在公开仓库）。

- 数据库选定 **PostgreSQL 18.7**；README 的「待定决策」整理为「决策状态」，标出唯一已定项，其余五项继续待定。
- 新增 `docker-compose.yml`（官方 `postgres:18.7` 镜像 + 命名卷 + 健康检查）。
- `.env.example` 补充数据库变量占位；真实口令只放本地 `.env`，不提交。
- `docs/ARCHITECTURE.md` 补第 2.1 节「数据库层落地」，并把 ADR 章节指向已接受的 0001。
- **后端技术栈锁定**：README「决策状态」补齐语言 / 框架 / 接口 / 数据访问 / 迁移 / 认证 / 缓存 / 实时 / 可观测性 / 测试各项；`ARCHITECTURE.md` 新增第 8 节技术栈落地与九条评审阻断项，并去掉「与技术栈无关」「仍待定」等过期表述；ADR-0001 与 `LOCAL-DEV.md` 同步链接到 ADR-0002。
- **CI 由骨架检查升级为真实构建**：`build` job 执行 `go build` / `go vet` / `gofmt` / `go test -race`，`hygiene` job 保留必需文件与密钥、真实数据防护。
- **隔离模型收敛为唯一权威（ADR-0003）**：README 决策状态新增「仅门店层」一行、去掉「会员档案归属组织」等隐含上层实体表述，边界与多店模型改为明确禁止 `tenant_id` / `org_id` / `company_id`；`ARCHITECTURE.md` 新增 2.0「隔离模型」小节、术语表把「总部视角」改为「跨店视角」、评审阻断项增加第 ⑩ 条；ADR-0001 / ADR-0002、PR 模板、CONTRIBUTING 同步指向 ADR-0003；`porch-console` 明确只有门店上下文、没有组织 / 租户切换。
- 前端框架、API 客户端、组件库与 E2E 工具的**决策权由 ADR-0007 确认归 `porch-console`**；**NQ-14 已由 ADR-0008 收口**：选型以既有内部前端工程（待重构）实践为依据，由前端仓独立记录，工程名称不写入公开仓库。
- README 与 `ARCHITECTURE.md` 增加详细设计导航（新增第 10 节）；`porch-console` 的错误重试约定改为按响应体 `error.retryable` 判定，并指向 `03-api-design.md`。
- `docs/design/02` 的核心 DDL 节选已在本地 PostgreSQL 18.7 容器事务中实测通过并回滚，未污染 `public` schema。
- **关闭 4 项开放问题并全量同步设计口径（ADR-0004）**：`00` 第 8 节拆成「已确认 / 仍待确认」，OQ-01、OQ-02、OQ-03 打印部分、OQ-06 标记已决；`01` 的 `Money` 去掉币种、`Order` 去掉 `currency`、新增 `BusinessDay` 值对象与 3 条不变量；`02` 去掉 `shops.timezone` 与 `orders.currency` 列、单号日期改 `AT TIME ZONE 'Asia/Shanghai'`、第 7 节由「归档与容量」改为「容量与保留策略（暂不归档）」；`03` 补币种/时区约定与「范围排除清单」；`04` 固定 `APP_TIMEZONE=Asia/Shanghai` 并新增评审检查项；`05` 新增 4.1「在线强一致」、明确运行期清理不等于归档、导出不触发打印；`06` 补共同前提与 UTC+8 日界测试。`ARCHITECTURE.md` 第 2.1、4、5、8、9、10 节与 README 的边界、决策状态、导航同步；ADR-0001 / 0002 / 0003 中过期的时区、打印、归档表述一并订正，PR 模板与 `CONTRIBUTING.md` 增加范围基线自查。
- **`docker-compose.yml` 与本地环境对齐东八区**：容器 `TZ` / `PGTZ` 与 PostgreSQL `timezone` 均设为 `Asia/Shanghai`；`timestamptz` 仍以 UTC 存储，仅影响人工查库读数与默认会话时区。`.env.example` 增加 `APP_TIMEZONE` 固定值说明，`LOCAL-DEV.md` 增加时区验证命令。
- **取消门店组并全量同步（ADR-0005）**：ADR-0003 第 3 条改写并标注修订记录，ADR-0004 增加交叉引用；`design/00` 的 8.1 节新增已确认决议、`01` 去掉 `ShopGroup` 聚合与分组关系、`02` 表清单删除 `shop_groups` / `shop_group_members` 并补充「无分组列」约束测试、`03` 删除店群列表与成员调整两个端点并列入范围排除清单、`04` 的 `shopscope` 中间件与评审清单、`05` 的授权唯一来源与报表口径、`06` 的共同前提与 F-05 流程同步；`ARCHITECTURE.md` 术语表 / 2.0 / ADR 列表、README、PR 模板、`CONTRIBUTING.md` 与 `porch-console` 的边界说明一并订正。**跨店汇总 / 对比 / 导出能力不变**，范围改由显式门店 ID 集合表达。
- **冻结用户-门店关系与切换协议（ADR-0006）**：ADR-0003 / 0005 加交叉引用；`design/00` 的 8.1 节新增已确认决议与三组边界、质量属性补「切换后无旧店残留」；`01` 增加 `CurrentShop` 值对象、一人多店不变量与术语澄清；`02` 的 DDL 注释、约束测试、容量章节明确禁止 `users.shop_id` / `shops.owner_user_id` / `current_shop` 表；`03` 重写第 3 节（单店请求缺头一律 400、不再隐式默认）并新增 3.1「登录响应与门店切换协议」；`04` 的 `shopscope` 与评审第 ⑬ 项、`05` 的授权 / 缓存键、`06` 新增 F-07 门店切换流程与测试清单同步；`ARCHITECTURE.md`、README、PR 模板、`CONTRIBUTING.md` 与 `porch-console` 的切换约定一并订正。**隔离模型与跨店范围表达不变**，只是把「当前看哪家店」定为请求期上下文。
- **关闭全部剩余开放问题（ADR-0007）**：ADR-0004 加修订记录并把 `audit_logs` 补入「运行期清理」清单，ADR-0002 / 0005 / 0006 补交叉引用与历史状态订正；`design/00` 第 8 节改为「8.1 已确认决议（含 OQ-03/04/05/07/08 五行）」+「8.2 NQ-01–16 待确认清单」，系统上下文与 DP-01 提示补编排平台；`01` 明确不做自动清分、审计日志属运行期数据；`02` 表清单与第 7 节移除「审计日志不设保留年限」、新增 `(created_at)` 清理索引、迁移只跑一次与保留测试；`03` 范围排除清单新增外部集成 / 清分 / 审计改删三类端点并写明超期查询语义；`04` gateway 注释、进程表、编排平台约束、配置项与评审第 ⑭⑮ 项；`05` 后台任务审计清理、只读副本改为已决、指标与连接池预算、安全基线措辞；`06` 共同前提、F-03/F-05 口径、**新增 F-08 审计日志定时清理流程**与测试 / 追溯矩阵。
- **配置与环境**：`.env.example` 增加 `AUDIT_LOG_RETENTION_DAYS=30`（全局、启动时校验）；`docker-compose.yml` 与 `LOCAL-DEV.md` 明确本地 Compose 仅用于开发、生产走编排平台；`ARCHITECTURE.md`、README（决策状态 / 边界 / About / 导航 / 路线图）、`CONTRIBUTING.md` 与 PR 模板同步；`porch-console` 的前端框架决策权与部署边界一并订正。**隔离模型、单币种 / 东八区、业务数据暂不归档均不变**，审计日志定时删除属运行期清理而非归档。

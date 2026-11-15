# Porch OS · 门廊

> **One backend, many shops.** 一个后台，管好几家店。

面向小微门店的开源经营系统后端：多门店统一管理、跨店会员与库存、按店出账与汇总报表。

**状态：早期设计阶段。** 本仓库目前只有工程约定与架构文档，尚无可运行代码。技术栈尚未锁定，确定后会同步更新本文件与 `docs/ARCHITECTURE.md`。

License: MIT · 语言：中文文档（发布前补英文版）

## 为什么叫「门廊」

门廊是店铺的入口：它撑起整间店，但顾客不会把它当成商品。我们希望这套系统也是这样——承载多店经营的全部结构，保持中性，不预设任何具体业态。

## 项目简介

一个人开一家店时，记账靠本子就够；开到第二家、第三家，数据就开始散落在不同的表格、账号和设备里。Porch OS 要解决的就是这一步：用一套系统管理任意数量的门店，让每笔单据、每件库存、每位会员都明确属于哪家店——单店能独立核算，店群能汇总对比，并且从数据层就防止把 A 店的数据读进 B 店的页面。

它只做通用的经营底座：门店与店群、商品与服务、跨店会员、开单收银、报表与权限。项目刻意不预设任何行业规则，餐饮、零售、服务业的用法由使用方自行扩展；技术栈也尚未锁定，当前仓库先固定多店模型与工程约定，选型后以 ADR 逐条记录。

**边界（不做什么）**

- 不绑定具体业态，不内置行业专属规则
- 不做 SaaS 多租户平台（这是单库多店，不是租户隔离）
- 不做加盟 / 连锁总部体系
- 不替代财务软件、开票与报税系统
- 现阶段不提供托管服务

## About (English)

Porch OS is an open-source backend for small businesses running more than one shop. Every order, stock item and membership is tied to a specific shop, so each location can be accounted for on its own while a group of shops can still be rolled up and compared — with shop-level isolation enforced in the data layer, not left to the client.

It provides only a shared foundation: shops and shop groups, catalog, members, checkout, reports and permissions. It assumes no particular industry and deliberately leaves the tech stack undecided for now; this repository currently fixes the multi-shop model and the engineering conventions, with each choice to be recorded as an ADR once made.

## 核心能力（规划）

| 能力 | 说明 |
|---|---|
| 多门店 | 一个后台管理任意数量门店，所有业务数据带 `shop_id` 维度 |
| 店群 | `ShopGroup` 把若干门店归为一组，支持按组汇总与授权 |
| 跨店会员 | 会员档案归属组织，余额与消费记录可跨店核销 |
| 商品与服务 | 目录统一维护，价格、库存、上下架可按店覆盖 |
| 开单与收银 | 每笔单据绑定门店与操作人，写入幂等 |
| 报表 | 单店明细、跨店汇总、按店对比；导出流式写出，不全量读进内存 |
| 权限 | 角色控制 + `shop_id` 数据级校验，防止跨店越权 |

## 多店模型

- `Shop` = 单店；`ShopGroup` = 店群。不使用 `Chain` / `Franchise`（暗示加盟体系，对小门店过重）。
- 所有业务表带 `shop_id`；默认查询必须带门店条件，跨店查询需显式声明。
- 这是**单库多店**，不是 SaaS 多租户，因此不使用 `tenant_id`，避免误导架构。
- 门店维度校验放在服务层统一入口，不依赖前端传参是否可信。

详见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 设计原则（与技术栈无关）

集中配置并在启动时校验、快速失败 · 类型化错误与全局错误处理 · 结构化 JSON 日志贯穿 request id · 边界处校验所有输入 · 多步写入事务化 · 数据库变更通过可回滚迁移 · 缓存一律带 TTL 且写后失效 · 后台任务幂等、可重试、有死信 · 健康检查与优雅停机 · 日志不记录密码、令牌与隐私数据。

## 待定决策

| 项目 | 状态 |
|---|---|
| 后端语言 / 框架 | 待定 |
| 数据库 / 连接池 / 迁移工具 | 待定 |
| 接口风格（REST / GraphQL / gRPC） | 待定 |
| 认证（JWT 短时效 + 服务端刷新令牌等） | 待定 |
| 实时能力（轮询 / SSE / WebSocket） | 待定 |
| 前端框架与 API 客户端方案 | 待定 |

骨架阶段刻意不锁定这些选项，也刻意不写入任何语言相关的目录结构。

## 快速开始

技术栈确定后补齐。当前只需：

```bash
git clone https://github.com/WeijianHuang/porch-os.git
cd porch-os
cp .env.example .env   # 按实际配置项填写，.env 不提交
```

## 仓库结构

```
.github/           CI 工作流与 PR 模板
docs/              架构原则与决策记录
.env.example       配置占位（仅示例值）
CONTRIBUTING.md    贡献流程
CHANGELOG.md       变更记录
```

源码目录待技术栈确定后添加。

## 路线图

- **R0** 工程约定、架构原则、社区文件（当前阶段）
- **R1** 领域模型 + `shop_id` 隔离 + 单店 CRUD + 集成测试基线
- **R2** 多店汇总、店群授权、跨店会员
- **R3** 开单收银、幂等与单号生成、报表流式导出
- **R4** 实时能力、可观测性、生产加固清单

## 配套仓库

前端控制台为同一账号下的 **[porch-console](https://github.com/WeijianHuang/porch-console)** 仓库。两个仓库独立发版、独立 issue 分类。

## 贡献

见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。欢迎从文档、领域模型和测试用例入手。

## 许可证

[MIT](LICENSE)。若后续需要明确的专利授权条款，可在发版前评估切换 Apache-2.0。

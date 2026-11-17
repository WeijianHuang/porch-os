# ADR-0009: 队列与服务人员（主页默认视图）

- 状态：已接受（2026-11-17）
- 决策问题：在已冻结的 R1 契约上新增两个通用门店领域——**队列（queue）**与**服务人员（staff）**，并把前端主页默认视图定为队列页。明确两者的归属、基数、唯一性范围与端点，使设计冻结后的首次范围扩展有可追溯的契约来源
- 影响范围：新增 `design/10-queues-and-staff.md`、三张表（`queues` / `staff` / `queue_staff`）、R1 端点扩展、两个权限点、前端主页路由与队列/服务人员页面；修订 `design/00` / `02` / `03`、`ARCHITECTURE.md`、README / CHANGELOG
- 上游：[ADR-0001](0001-postgresql-18.md)（PG 18.7）、[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0004](0004-scope-baseline.md)（范围基线）、[ADR-0006](0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（设计冻结与变更后先改设计的规则）

## 背景

用户要求「添加服务员设计，在主页默认展示队列页面，一个门店可以有多个队列，服务员可在多个队列中，队列需要序号和名称」。这是 ADR-0008 冻结设计后的第一个新增领域需求。

按 ADR-0008 的冻结后变更规则，改动 schema / 端点清单 / 唯一性范围必须**先改设计或 ADR、再改代码**。本 ADR 即该扩展的设计来源：它不推翻 ADR-0001–0008 的任何条款，只在同一隔离模型与同一技术栈内**追加**两个领域，并显式扩大 R1 首批端点清单。

「队列 / 服务人员」是通用门店概念（排队叫号与人员排班），不含任何行业专属规则；公开仓库只用通用词 `queue` / `staff`，不引入任何内部项目标识。

### 理解澄清（本轮最容易被误读的四处）

| 说法 | 准确含义 |
|---|---|
| 「队列需要序号和名称」 | `queues.seq`（正整数）与 `queues.name` **都是必填**，唯一性范围是**门店内**：`(shop_id, seq)` 与 `(shop_id, name)` 各自唯一。不同门店可以有相同序号和名称 |
| 「一个门店可以有多个队列」 | `shops` 1:N `queues`，`queues.shop_id` 非空；队列是 B 类门店业务数据，所有单店端点必须带 `X-Shop-Id`，服务端用 `user_shops` 实时求交 |
| 「服务员可在多个队列中」 | `staff` 与 `queues` 是 **M:N**，通过关联表 `queue_staff` 表达；一个服务员可属于多家门店吗？**不可以**——服务人员是门店雇员，归属单一 `shop_id`（B 类），但可在**本店**的多个队列中服务；跨店复用人员不在本需求范围，若需要另立 ADR（与 ADR-0003 单店隔离一致） |
| 「主页默认展示队列页面」 | 前端登录后落地路由改为队列页（`/queues`），队列页按当前门店展示该店队列及其服务人员；这是**前端默认视图**的改变，不改变 ADR-0006 的门店上下文协议（仍显式带 `X-Shop-Id`、切换零写入） |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 服务人员归属 | 单店雇员，`staff.shop_id` 非空（B 类） | 全局人员 + 授权关系（A 类 + C 类，如 `user_shops`） | 需求只要求「一个服务员在多个队列」，不要求跨店；做成全局会带来跨店人员可见性与归属歧义，超出冻结的隔离模型 |
| 队列 ↔ 服务人员关系 | 关联表 `queue_staff` + 复合外键锁定同店 | 在 `staff` 上加 `queue_ids jsonb` 数组 | 数组无法做外键、唯一性与联表查询；违反 ADR-0002「SQL 优先、无实体图」，且多对多更新易产生脏写 |
| 序号唯一范围 | `(shop_id, seq)` 门店内唯一 | `seq` 全局唯一 | 每家门店都应能从 1 开始编号；全局唯一迫使序号带上门店偏移，违背「序号」语义 |
| 主页路由 | 改为 `/queues` 默认，队列页为落地页 | 保留门店列表为首页，仅加队列入口 | 用户明确要求主页默认展示队列页面 |
| 关联表主键 | `(queue_id, staff_id)` 复合主键 | 独立自增 `id` | 同一队列同一人员不应重复关联；复合主键即天然幂等约束，少一列与一个唯一索引 |

## 决策

### 1. 领域与基数

- `shops` **1:N** `queues`：一个门店多个队列；队列必属单一门店。
- `queues` **M:N** `staff`：通过 `queue_staff`（复合主键 `(queue_id, staff_id)`）表达；一个队列多名服务人员、一个服务人员在本店多个队列。
- `staff` **N:1** `shops`：服务人员是门店雇员，归属单一门店；不支持跨店复用。

### 2. 三张新表（PG 18.7）

| 表 | 类别 | 关键列 | 唯一性范围 |
|---|---|---|---|
| `queues` | B | `public_id`、`shop_id`、`seq`、`name`、`status`、`version` | `(shop_id, seq)`、`(shop_id, name)`、`public_id` |
| `staff` | B | `public_id`、`shop_id`、`code`、`display_name`、`status`、`version` | `(shop_id, code)`、`public_id` |
| `queue_staff` | B | `shop_id`、`queue_id`、`staff_id`、`joined_at` | `(queue_id, staff_id)`，另设 `(shop_id, staff_id)` 索引 |

**同店约束**：`queue_staff` 除复合主键外，用两条**复合外键**锁定归属一致——

```sql
FOREIGN KEY (shop_id, queue_id) REFERENCES queues (shop_id, id)
FOREIGN KEY (shop_id, staff_id) REFERENCES staff (shop_id, id)
```

这要求 `queues` 与 `staff` 各增一条 `(shop_id, id)` 唯一约束作为被引用键（PG 复合外键的引用目标必须唯一）。因此**不可能**把 A 店的服务员挂到 B 店的队列——数据库层直接拒绝，与 ADR-0003「只有 `shop_id` 一层隔离」一致，不需要 tenant、分组或触发器。

### 3. 端点（R1 扩展）

全部为单店资源，路径含 `{shopId}`（`public_id`），必须显式带 `X-Shop-Id`；写操作要求 `Idempotency-Key`。

| 模块 | 方法与路径 | 说明 |
|---|---|---|
| queue | `GET / POST /api/v1/shops/{shopId}/queues` | 队列列表（按 `seq` 升序）/ 新建（序号+名称） |
| queue | `GET / PATCH / DELETE /api/v1/shops/{shopId}/queues/{queueId}` | 队列详情 / 修改 / 删除（删除同时解绑关联） |
| staff | `GET / POST /api/v1/shops/{shopId}/staff` | 服务人员列表 / 建档 |
| staff | `GET / PATCH / DELETE /api/v1/shops/{shopId}/staff/{staffId}` | 服务人员详情 / 修改 / 停用删除（删除同时解绑关联） |
| queue-staff | `GET /api/v1/shops/{shopId}/queues/{queueId}/staff` | 队列内的服务人员 |
| queue-staff | `PUT / DELETE /api/v1/shops/{shopId}/queues/{queueId}/staff/{staffId}` | 将服务人员加入 / 移出队列（`PUT` 幂等） |

权限点新增 `queue.read` / `queue.write`，`admin` 与 `manager` 全给、`staff` 只读；迁移 `00004_queue_staff_permissions.sql`。

### 4. 前端默认视图

- 登录后落地路由从 `/shops` 改为 `/queues`；侧边栏菜单第一项为「队列」，首页按当前门店展示队列（序号 + 名称）及其服务人员。
- 服务人员页面展示每个人所在的多个队列（由 `queue_staff` 查询得出），可在一个页面内完成多队列分配。
- 仍遵循 ADR-0006：切换门店取消在途单店请求、失效 `shop-scope` 缓存、重拉权限；主页变更不改变这一协议。

### 5. 不引入的概念

不建「叫号 / 等待队列实例」「排队号状态机」「服务人员的排班 / 班次关联」「按队列的绩效 / 业绩规则」——这些都是行业专属或 R2+ 范围。本期只有**队列定义**与**人员-队列分配**两个通用能力。

## 理由

1. **需求逐字落实**：序号 + 名称必填、一店多队列、一人多队列、主页默认队列页，四条需求都有明确的表、约束与路由对应，不含未答项。
2. **沿用冻结契约的隔离模型**：三张表都带 `shop_id`，复合外键把「同店」下沉到数据库，不新增上层维度，也不靠应用代码自律。
3. **多选多归属用关联表而非数组**：保持 SQL-first 与可查询性，`queue_staff` 的复合主键同时是幂等保证。
4. **主页默认视图只是路由改变**：不动认证、门店上下文与缓存失效协议，改动面可控、可回退。

## 后果与限制

- **修订 [ADR-0008](0008-design-freeze-and-nq-resolutions.md) 的 R1 首批端点清单**：追加上述队列 / 服务人员端点，作为冻结后的**受控扩展**；ADR-0008 的其余条款、运行基线与验收标准不变。
- **修订 `design/00` / `02` / `03` 与 `ARCHITECTURE.md` / README / CHANGELOG**：分册地图新增 `10`，表清单新增三表，端点清单新增两组，冻结说明注明「首次扩展见 ADR-0009」。
- 服务人员**不支持跨店**；若未来需要，须新 ADR 把 `staff` 从 B 类改为 A 类 + 授权关系，并重做隔离判定。
- 删除队列 / 服务人员采用解绑关联后删除的显式流程（应用事务内完成）；不设软删除与历史版本表。
- 序号在门店内唯一，删除后是否复用由前端排序决定，后端不做序号压缩或重排。
- **公开仓库脱钩约束不变**：仅使用通用词 `queue` / `staff`，不出现任何内部项目名或行业专属词。

## 复审条件

- 需要服务人员跨店复用、按队列的叫号 / 排队实例或状态机；
- 需要队列维度的绩效 / 业绩 / 排班规则（行业专属，须脱钩评估）；
- 序号允许门店间重复即产生业务冲突，需改为全局唯一；
- 需要软删除、关联历史或队列变更审计回放；
- 上游 ADR-0002 / 0003 / 0008 任一被修订且影响本 ADR 的隔离或扩展规则。

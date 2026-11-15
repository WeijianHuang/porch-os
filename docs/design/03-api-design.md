# 前期系统架构详细设计 · 03 接口设计

> 上游：[00 总览](00-overview.md) 第 3 节（系统上下文）、[01 领域模型](01-domain-model.md)（状态机与值对象）、[ADR-0003](../decisions/0003-shop-level-isolation.md)。
> 本册是 `porch-console` 与第三方集成的对接依据。前端框架待定不影响本册：后端只保证 REST + OpenAPI。

## 1. 风格与版本

| 项 | 约定 |
|---|---|
| 风格 | REST + JSON，OpenAPI 3.x 契约由注释生成（`swaggo/swag`） |
| 前缀 | `/api/v1`；破坏性变更升 `/api/v2`，不在 v1 里悄悄改语义 |
| 资源名 | 复数小写、连字符：`/catalog-items`、`/shop-groups` |
| 对外 ID | 一律用 `public_id`（UUID）；内部 `bigint` 主键不出现在 URL 与响应中 |
| 门店 | 门店维度放路径 `/shops/{shopId}/...`，**不是**查询参数裸传 |
| 编码 | UTF-8；时间 ISO 8601 UTC（`2026-11-01T00:00:00Z`）；金额用**字符串** `"19.90"` |
| 幂等 | 所有写操作要求 `Idempotency-Key` 头，见第 6 节 |
| 追踪 | 每个请求带 `X-Request-Id`（缺失由服务端生成），贯穿日志与错误响应 |

**金额为什么用字符串**：JSON 数字在多数运行时是双精度浮点，19.90 这类值跨语言往返会出现二进制误差。字符串由服务端定点十进制序列化，客户端按需解析为定点类型。

## 2. 统一响应形状

成功（单资源）：

```json
{
  "data": { "public_id": "019...", "order_no": "SO20261115-0000000123", "status": "paid" },
  "meta": { "request_id": "req_01J..." }
}
```

成功（列表）：

```json
{
  "data": [ { "public_id": "019...", "created_at": "2026-11-15T09:12:33Z" } ],
  "page": { "next_cursor": "eyJ...", "has_more": true },
  "meta": { "request_id": "req_01J..." }
}
```

失败：

```json
{
  "error": {
    "code": "PERMISSION_DENIED",
    "message": "当前账号无权访问该门店",
    "retryable": false,
    "details": [ { "field": "shop_id", "reason": "outside_visible_shops" } ]
  },
  "meta": { "request_id": "req_01J..." }
}
```

规则：错误结构固定三字段；`message` 面向人，不含内部细节、堆栈、SQL 或数据库错误原文；`details` 只用于参数级说明。

## 3. 门店上下文（最关键的一条）

客户端**不能**决定「能看哪些店」，只能在服务端允许的范围内选择「当前看哪家」。

| 输入 | 服务端行为 |
|---|---|
| 未带 `X-Shop-Id` | 可见集合恰有 1 家 → 默认该店；多店且无跨店权限 → `400 SHOP_CONTEXT_REQUIRED` |
| 带 `X-Shop-Id` | 与「当前主体可见门店集合」求交；命中 → 作为本次 shop scope；未命中 → `403 PERMISSION_DENIED` |
| 路径里的 `{shopId}` | 同样做交集校验，不因出现在 URL 里就被信任 |
| 跨店聚合 `scope=all` 或 `shop_ids=a,b` | 需 `report.cross_shop` 权限；请求列表**再与可见集合求交**，越权项直接拒绝而非静默忽略 |
| 跨店写操作 | 默认禁止；调拨、跨店核销等显式动作有专用端点与单独权限 |

**防枚举**：门店不存在与无权访问统一返回 `403`，不区分二者；资源不存在返回 `404`，且只在先通过门店校验之后才查资源。任何「不存在的 ID 触发 500」都视为缺陷。

`ShopScope` 由中间件从认证主体 + `user_shops` 推导并注入 context；用例层只读这个 scope，不重新解析请求参数。跨店权限判定结果**不缓存**（ADR-0002 阻断项 ⑧）。

## 4. 错误码与可重试语义（DP-04）

客户端是否需要重试由 `retryable` 决定，不靠猜 HTTP 状态码。

| `code` | HTTP | retryable | 典型触发 |
|---|---|---|---|
| `INVALID_ARGUMENT` | 400 | 否 | 参数格式、缺失必填、非法排序字段 |
| `SHOP_CONTEXT_REQUIRED` | 400 | 否 | 多店主体未指定当前门店 |
| `UNAUTHENTICATED` | 401 | 否 | 令牌缺失/过期/无效 |
| `PERMISSION_DENIED` | 403 | 否 | 角色不足、门店不在可见集合、缺跨店权限 |
| `NOT_FOUND` | 404 | 否 | 资源不存在（已通过门店校验） |
| `CONFLICT` | 409 | 否 | 业务规则冲突（如重复开班次） |
| `IDEMPOTENCY_CONFLICT` | 409 | 否 | 同一幂等键配了不同请求体 |
| `IDEMPOTENCY_IN_PROGRESS` | 409 | 是 | 首次请求仍在处理，带 `Retry-After` |
| `VERSION_CONFLICT` | 409 | 是 | 乐观锁冲突，客户端应重读后重试 |
| `PRECONDITION_FAILED` | 412 | 否 | 状态机非法迁移（如已支付单据再改明细） |
| `UNPROCESSABLE` | 422 | 否 | 语义校验失败（如明细合计与提交金额不一致） |
| `RATE_LIMITED` | 429 | 是 | 带 `Retry-After` |
| `INTERNAL` | 500 | 是 | 未分类内部错误（细节只进日志） |
| `DEPENDENCY_FAILURE` | 502 | 是 | 下游依赖不可用 |
| `TIMEOUT` | 504 | 是 | 超时；客户端须先查幂等结果再决定是否重试 |

数据库错误映射：唯一约束冲突 → `CONFLICT` 或 `IDEMPOTENCY_CONFLICT`；外键冲突 → `INVALID_ARGUMENT`；死锁/序列化失败 → `VERSION_CONFLICT`（可重试）。**不允许**把 PG 原始错误文本返回给客户端。

## 5. 列表、过滤与分页

| 参数 | 规则 |
|---|---|
| `limit` | 默认 20，上限 100，超出报 `INVALID_ARGUMENT` |
| `cursor` | keyset 分页，编码 `(created_at, id)`；**不提供 `offset` 深翻页** |
| `sort` | 白名单枚举，如 `created_at_desc` / `created_at_asc`；非法值报 400，不拼接进 SQL |
| 时间过滤 | `created_at_from` / `created_at_to`，半开区间 `[from, to)`，ISO 8601 UTC |
| 状态过滤 | `status` 取状态机枚举值 |
| 跨店 | `scope=all` + 可选 `shop_ids`，需跨店权限 |

响应中 `page.has_more` 与 `next_cursor` 必须同时给出；`next_cursor` 不透明，客户端不得解析。导出场景复用同一过滤条件，但走异步任务 + 流式下载（第 8 节）。

## 6. 幂等

| 项 | 约定 |
|---|---|
| 适用范围 | **所有产生业务副作用的写操作**（POST/PUT/PATCH/DELETE），包括退款、调拨、导出创建 |
| 请求头 | `Idempotency-Key: <客户端生成的 UUID>`，缺失写操作返回 `400 INVALID_ARGUMENT` |
| 作用域 | `scope = user:<id> \| shop:<id>`；跨店显式动作使用动作自身 ID 作为 scope |
| 请求指纹 | `sha256(method + path + canonical_body)` 存 `request_hash` |
| 相同 key + 相同指纹 | 直接返回首次响应（含原状态码与响应体），不重复执行 |
| 相同 key + 不同指纹 | `409 IDEMPOTENCY_CONFLICT`，绝不静默复用 |
| 首次仍在处理 | `409 IDEMPOTENCY_IN_PROGRESS` + `Retry-After` |
| 保留期 | 24 小时；过期键由 worker 清理 |
| 落库位置 | `idempotency_keys` 表，与业务写入**同一事务** |

> 幂等不是「加个 Redis SETNX」：业务写入与幂等记录必须原子提交，否则进程崩溃会留下「业务已生效但幂等键没写」的窗口。R1 用数据库表实现（DP-07），R2 再评估缓存加速。

## 7. 认证与令牌

- `Authorization: Bearer <access_token>`，短时效（15 分钟）；刷新令牌服务端存储哈希、可撤销；令牌不进 URL 查询参数。
- 登录后返回 `access_token` + `refresh_token`；刷新时重新推导权限与可见门店集合。
- 授权变更（角色、门店可见范围）在**下一个请求**生效：权限与 shop scope 每次请求实时推导，不做跨请求缓存。
- `GET /api/v1/me/shops` 返回当前可见门店列表，供前端渲染门店切换器；前端不得自行缓存为权限依据。

## 8. 核心端点清单（R1–R3）

`{shopId}` 一律为 `public_id`。所有写操作默认要求 `Idempotency-Key`，表中不再重复。

| 模块 | 方法与路径 | 说明 |
|---|---|---|
| auth | `POST /api/v1/auth/login` | 登录 |
| auth | `POST /api/v1/auth/refresh` | 刷新访问令牌 |
| auth | `POST /api/v1/auth/logout` | 撤销刷新令牌 |
| identity | `GET /api/v1/me` | 当前账号与角色 |
| identity | `GET /api/v1/me/shops` | 可见门店集合（门店切换器数据源） |
| identity | `GET /api/v1/me/permissions` | 当前权限点 |
| shop | `GET / POST /api/v1/shops` | 门店列表 / 新建 |
| shop | `GET / PATCH /api/v1/shops/{shopId}` | 门店详情 / 修改 |
| shop | `GET /api/v1/shop-groups` | 店群列表 |
| shop | `PUT /api/v1/shop-groups/{id}/shops` | 调整店群成员 |
| shop | `GET / PATCH /api/v1/shops/{shopId}/settings` | 门店参数 |
| catalog | `GET / POST /api/v1/catalog-items` | 目录列表 / 新建 |
| catalog | `GET / PATCH /api/v1/catalog-items/{id}` | 目录项详情 / 修改 |
| catalog | `GET /api/v1/catalog-categories` | 分类树 |
| catalog | `GET / PUT /api/v1/shops/{shopId}/catalog-overrides/{itemId}` | 按店价格与上下架 |
| member | `GET / POST /api/v1/members` | 会员检索 / 建档（全局） |
| member | `GET / PATCH /api/v1/members/{id}` | 会员档案 |
| member | `GET /api/v1/shops/{shopId}/members/{id}/balance` | 该店余额 |
| member | `GET /api/v1/shops/{shopId}/members/{id}/ledger` | 该店余额流水 |
| member | `POST /api/v1/shops/{shopId}/members/{id}/topups` | 充值（店内账） |
| order | `POST /api/v1/shops/{shopId}/orders` | 开单（`draft`） |
| order | `GET /api/v1/shops/{shopId}/orders` | 单据列表（keyset 分页） |
| order | `GET /api/v1/shops/{shopId}/orders/{id}` | 单据详情（含明细） |
| order | `POST / DELETE /api/v1/shops/{shopId}/orders/{id}/lines` | 加项 / 删项 |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/submit` | 提交结账 → `pending_payment` |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/payments` | 组合支付 → `paid` |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/refunds` | 退款（生成退款单） |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/cancel` | 取消未支付单据 |
| order | `GET /api/v1/shops/{shopId}/shifts/current` | 当前班次 |
| order | `POST /api/v1/shops/{shopId}/shifts/open` | 开班 |
| order | `POST /api/v1/shops/{shopId}/shifts/{id}/close` | 关班结算 |
| inventory | `GET /api/v1/shops/{shopId}/inventory` | 库存余额 |
| inventory | `POST /api/v1/shops/{shopId}/inventory/adjustments` | 出入库调整 |
| inventory | `GET /api/v1/shops/{shopId}/inventory/movements` | 库存流水 |
| inventory | `POST /api/v1/shops/{shopId}/stock-transfers` | 跨店调拨（显式动作，单独权限） |
| report | `GET /api/v1/shops/{shopId}/reports/daily` | 单店日报 |
| report | `GET /api/v1/reports/daily-summary?scope=all` | 跨店日报汇总（需 `report.cross_shop`） |
| report | `GET /api/v1/reports/sales` | 销售明细报表 |
| report | `POST /api/v1/shops/{shopId}/exports` | 创建导出任务 → `202` + 任务 URL |
| report | `GET /api/v1/shops/{shopId}/exports/{id}` | 导出任务状态 |
| report | `GET /api/v1/shops/{shopId}/exports/{id}/download` | 流式下载 |
| platform | `GET /health`、`GET /ready` | 存活 / 就绪（不经 `/api/v1`） |

## 9. 超时、限流与契约冻结（DP-06）

| 项 | 约定 |
|---|---|
| 普通请求 | 5 s 超时，context 一路传到数据库；客户端断开后服务端计算必须终止 |
| 报表查询 | 30 s；走汇总表，不做大表实时全扫 |
| 导出 | 异步任务，`202` 立即返回；下载用流式写出，不把文件读进内存 |
| 限流 | 按「主体 + 门店」维度，返回 `429` + `Retry-After` |
| WebSocket | 30 s 心跳、断线重连；推送携带 `shop_id`，不跨店广播 |

**契约冻结**：

1. `api/openapi.yaml` 由注释生成并提交进仓库；CI 校验「重新生成后无 diff」，防止代码与契约漂移。
2. 响应快照测试只断言**字段集合与类型**，动态值（时间、ID、金额）做归一化，不比对字面值。
3. 破坏性变更清单：删除/重命名字段、改类型、改错误码语义、改分页形状、改门店上下文规则。这类变更须升版本或走弃用期，并同步 `porch-console`。
4. 端点清单（第 8 节）与 OpenAPI 一一对应；新增端点必须同时更新本册与契约文件。

---

下一册：[04 应用架构](04-application-architecture.md)。

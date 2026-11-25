# 前期系统架构详细设计 · 03 接口设计

> 上游：[00 总览](00-overview.md) 第 3 节（系统上下文）、[01 领域模型](01-domain-model.md)（状态机与值对象）、[ADR-0003](../decisions/0003-shop-level-isolation.md)、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种、东八区、不打印、不离线）、[ADR-0005](../decisions/0005-no-shop-groups.md)（无门店组端点）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（登录返回门店集合、当前门店必须显式携带、切换协议）、[ADR-0007](../decisions/0007-remaining-open-questions.md)（R1 不接外部服务、不做自动清分、审计日志保留 30 天）、[ADR-0009](../decisions/0009-queues-and-staff.md)（队列 / 服务人员端点扩展）。
> 本册是 `porch-console` 与第三方集成的对接依据。**前端框架由该仓独立决策**（ADR-0007），不影响本册：后端只保证 REST + OpenAPI、不做 BFF。

## 1. 风格与版本

| 项 | 约定 |
|---|---|
| 风格 | REST + JSON，OpenAPI 3.x 契约由注释生成（`swaggo/swag`） |
| 前缀 | `/api/v1`；破坏性变更升 `/api/v2`，不在 v1 里悄悄改语义 |
| 资源名 | 复数小写、连字符：`/catalog-items`、`/payment-entries`；**不存在 `/shop-groups` 等分组资源**（ADR-0005） |
| 对外 ID | 一律用 `public_id`（UUID）；内部 `bigint` 主键不出现在 URL 与响应中 |
| 门店 | 门店维度放路径 `/shops/{shopId}/...`，**不是**查询参数裸传 |
| 编码 | UTF-8；时间 ISO 8601 UTC（`2026-11-01T00:00:00Z`）传输；金额用**字符串** `"19.90"` |
| 币种 | **单一 CNY**：请求/响应都不带 `currency` 字段，不提供汇率或折算参数；展示固定 `¥`（ADR-0004） |
| 时区 | 传输统一 UTC；**展示与业务日固定 `Asia/Shanghai`（UTC+8）**；接口不接受客户端时区参数，也不提供按店时区设置 |
| 幂等 | 所有写操作要求 `Idempotency-Key` 头，见第 6 节 |
| 追踪 | 每个请求带 `X-Request-Id`（缺失由服务端生成），贯穿日志与错误响应 |

**金额为什么用字符串**：JSON 数字在多数运行时是双精度浮点，19.90 这类值跨语言往返会出现二进制误差。字符串由服务端定点十进制序列化，客户端按需解析为定点类型。单币种下不需要币种字段，但**精度约定不变**。

**时间怎么用**：UTC 只用于传输与存储，避免歧义；前端把 `Z` 时间统一按 UTC+8 渲染，后端按同一口径切分业务日、生成单号日期、汇总日报。前端**不得**用浏览器本地时区格式化业务时间，否则境外设备会看到错日期；也不要传 `tz` / `timezone` 参数，服务端一律忽略或报 `INVALID_ARGUMENT`。

**明确不提供的能力**：打印端点、离线同步/补传端点、归档历史查询端点、币种与汇率端点、按店时区设置端点（ADR-0004）；门店组 / 店群端点、分组筛选参数、按组授权接口（ADR-0005）；服务端「切换当前门店」写入端点（ADR-0006，切换是纯客户端行为）。文件导出端点保留，不属于打印；跨店汇总端点保留，只是范围由显式门店 ID 集合给出；门店列表 `/me/shops` 保留，是切换器的唯一数据源。

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

客户端**不能**决定「能看哪些店」，只能在服务端允许的范围内选择「当前看哪家」。一个用户可关联多间门店（`user_shops`，ADR-0006）；**「当前门店」是客户端持有、服务端每次校验的请求期上下文**——不是持久化实体、不落表、不进 JWT、不作为授权来源。

| 输入 | 服务端行为 |
|---|---|
| 单店业务请求未带 `X-Shop-Id` | `400 SHOP_CONTEXT_REQUIRED`；**不再隐式默认到唯一门店**（ADR-0006）——漏带必须明确报错，不能静默落到另一家店 |
| 带 `X-Shop-Id` | 与「当前主体可见门店集合」求交；命中 → 作为本次 shop scope；未命中 / 已撤销 / 门店停用 → `403 PERMISSION_DENIED` |
| 路径里的 `{shopId}` | 同样做交集校验，不因出现在 URL 里就被信任；与 `X-Shop-Id` 同时出现时两者必须一致 |
| 不需要当前门店的请求 | `/auth/*`、`/api/v1/me*`、`/health`、`/ready`；跨店聚合用 `scope=all` + `shop_ids` 表达范围，**不受当前门店影响** |
| 跨店聚合 `scope=all` 或 `shop_ids=a,b` | 需 `report.cross_shop` 权限；请求列表**再与可见集合求交**，越权项直接拒绝而非静默忽略；**不接受组 ID / 分组筛选参数**（ADR-0005） |
| 跨店写操作 | 默认禁止；调拨、跨店核销等显式动作有专用端点与单独权限 |

**防枚举**：门店不存在与无权访问统一返回 `403`，不区分二者；资源不存在返回 `404`，且只在先通过门店校验之后才查资源。任何「不存在的 ID 触发 500」都视为缺陷。

`ShopScope` 由中间件从认证主体 + `user_shops` 推导并注入 context；用例层只读这个 scope，不重新解析请求参数。跨店权限判定结果**不缓存**（ADR-0002 阻断项 ⑧）。`user_shops` 是可见门店集合的**唯一来源**，不存在分组对象，`scope=all` 也不是可持久化的实体（ADR-0005）；当前门店只是该集合中被选中的一个元素，不新增授权来源（ADR-0006）。

### 3.1 登录响应与门店切换协议（ADR-0006）

`POST /api/v1/auth/login` 在令牌之外返回 `shops`（可见集合：`public_id`、`name`、`status`）与 `default_shop_id`（集合中 `status = active` 且 `granted_at` 最早者；集合为空为 `null`）。`GET /api/v1/me/shops` 是切换器的权威数据源：进入应用、刷新页面、重新登录、收到 `403` 时都要重新拉取，前端不得把它当作永久权限依据缓存。切换器不接受本地伪造或 URL 传入的门店 ID。

页面切换只改变本地「当前门店」与后续请求的 `X-Shop-Id`：

| 步骤 | 要求 |
|---|---|
| 服务端 | **没有切换端点**（不存在 `POST /me/current-shop`）、不写库、不改变权限与数据归属；服务端保持无状态 |
| 在途请求 | 切换前取消所有在途单店请求；无法取消时丢弃来自旧门店的响应（响应体带 `shop_id`，渲染前与当前门店比对） |
| 缓存 | 派生查询与缓存以 `(user_id, current_shop_id, query_key)` 为键失效重取；跨店视图按 `shop_ids` 单独缓存，**不随切换失效** |
| 权限 | 重新拉取 `/me/permissions`；无权限时给可读提示，不显示空白页 |
| 恢复 | 当前门店可存 localStorage（非令牌、非权限依据），刷新后用最新 `/me/shops` 复核；被撤销或停用时自动回退到 `default_shop_id` |
| 解耦 | 切换器只管单店视图；跨店汇总由显式 `shop_ids` + `report.cross_shop` 决定，不因切换缩小或扩大 |

> 边界：**一人多店 ≠ 一店一人**（不能据此改成 `users.shop_id` 外键）、**当前门店 ≠ 授权来源**（服务端每次仍用 `user_shops` 求交）、**页面切换 ≠ 跨店汇总**。

## 4. 错误码与可重试语义（DP-04）

客户端是否需要重试由 `retryable` 决定，不靠猜 HTTP 状态码。

| `code` | HTTP | retryable | 典型触发 |
|---|---|---|---|
| `INVALID_ARGUMENT` | 400 | 否 | 参数格式、缺失必填、非法排序字段 |
| `STATE_UNCHANGED` | 400 | 否 | 服务人员已处于目标状态（ADR-0015 人工改状态） |
| `CARD_INACTIVE` | 400 | 否 | 领取已失效的 ID 卡（ADR-0017） |
| `CARD_HELD` | 409 | 否 | ID 卡已被他人持有（ADR-0017） |
| `STAFF_ALREADY_HOLDS_CARD` | 409 | 否 | 该服务员已持有一张卡（ADR-0017） |
| `CARD_IN_USE` | 409 | 否 | 持有的 ID 卡被置为失效，须先释放（ADR-0017） |
| `CARD_NOT_HELD` | 409 | 否 | 绑定订单时该 ID 卡当前无人持有（ADR-0022） |
| `CARD_BOUND_TO_ORDER` | 409 | 否 | 该 ID 卡已被另一张活跃订单占用（ADR-0022） |
| `ORDER_CARD_REQUIRED` | 422 | 否 | 提交结账的订单尚未绑定有效 ID 卡（ADR-0022） |
| `ORDER_ROOM_REQUIRED` | 422 | 否 | 提交结账的订单尚未绑定房间（ADR-0022） |
| `ORDER_LINE_STAFF_REQUIRED` | 422 | 否 | 提交结账时某条服务明细尚未指定执行服务员（ADR-0023，`details.line_no`） |
| `STAFF_NOT_QUALIFIED` | 422 | 否 | 指定 / 提交的服务员没有该项服务的资质，或资质已被撤销（ADR-0023） |
| `STAFF_INACTIVE` | 409 | 否 | 指定 / 提交的服务员任职状态为 `inactive`（ADR-0023） |
| `ROOM_NOT_FOUND` | 404 | 否 | 订单绑定 / 更换的房间不存在（已通过门店校验，ADR-0022） |
| `ROOM_INACTIVE` | 409 | 否 | 已停用的房间不可被新订单绑定（ADR-0022） |
| `ROOM_IN_USE` | 409 | 否 | 房间被历史订单引用，不可删除（ADR-0022） |
| `GROUP_NOT_FOUND` | 404 | 否 | 同行组不存在（已通过门店校验，ADR-0022） |
| `GROUP_NOT_OPEN` | 409 | 否 | 同行组非 `open`，不可合并 / 拆分（ADR-0022） |
| `ORDER_SETTLED` | 409 | 否 | 订单已结算 / 有成功支付，不可改分组、绑卡或绑房间（ADR-0022） |
| `GROUP_HAS_UNSETTLED_MEMBERS` | 409 | 否 | 关组时仍有未结清的活跃成员，须先逐单结清或拆出（ADR-0022） |
| `GROUP_HAS_PAID_MEMBERS` | 409 | 否 | 取消组时已有成员产生成功支付，只能关闭、不能取消（ADR-0022） |
| `MEMBER_NOT_IN_GROUP` | 404 | 否 | 订单不是目标同行组的活跃成员（ADR-0022） |
| `MEMBER_UNCHANGED` | 400 | 否 | 订单已属于目标组，无需重复合入（ADR-0022） |
| `AUDIT_RETENTION_WINDOW_EXCEEDED` | 400 | 否 | 审计查询 / 导出的 `created_at_from` 早于保留窗口 cutoff；`details` 带 `earliest_queryable_at`、`retention_days`、`cutoff_at`（ADR-0008 / [08](08-audit-retention.md) §4） |
| `SHOP_CONTEXT_REQUIRED` | 400 | 否 | 单店业务请求未显式指定当前门店（不再按可见门店数量隐式默认，ADR-0006） |
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
| 时间过滤 | `created_at_from` / `created_at_to`，半开区间 `[from, to)`，ISO 8601 UTC；按业务日筛选时用 `business_date_from` / `business_date_to`（日期按 UTC+8 口径） |
| 状态过滤 | `status` 取状态机枚举值 |
| 跨店 | `scope=all` + 可选 `shop_ids`，需跨店权限；**不提供按组筛选 / 组 ID 参数**（ADR-0005） |

响应中 `page.has_more` 与 `next_cursor` 必须同时给出；`next_cursor` 不透明，客户端不得解析。导出场景复用同一过滤条件，但走异步任务 + 流式下载（第 8 节）。

## 6. 幂等

| 项 | 约定 |
|---|---|
| 适用范围 | **所有产生业务副作用的写操作**（POST/PUT/PATCH/DELETE），包括退款、调拨、导出创建；**例外：只读试价 `POST /shops/{shopId}/orders/price` 不要求幂等键**（无副作用，ADR-0019；中间件对该单一路径豁免，不得扩大到其他端点） |
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
- 登录后返回 `access_token` + `refresh_token` + `shops`（可见集合）+ `default_shop_id`；刷新时重新推导权限与可见门店集合。集合为空时 `default_shop_id = null`，前端显示「暂无可用门店」而非空白页（ADR-0006）。
- 授权变更（角色、门店可见范围）在**下一个请求**生效：权限与 shop scope 每次请求实时推导，不做跨请求缓存。
- `GET /api/v1/me/shops` 返回当前可见门店列表，供前端渲染门店切换器；前端不得自行缓存为权限依据。
- **令牌不放门店清单、不放当前门店**（ADR-0002 阻断项 ⑧，ADR-0006）：当前门店由客户端持有并每次显式携带，服务端无状态、不提供切换端点。

## 8. 核心端点清单（R1–R3）

`{shopId}` 一律为 `public_id`。所有写操作默认要求 `Idempotency-Key`，表中不再重复。

| 模块 | 方法与路径 | 说明 |
|---|---|---|
| auth | `POST /api/v1/auth/login` | 登录；返回令牌 + `shops` + `default_shop_id`（ADR-0006） |
| auth | `POST /api/v1/auth/refresh` | 刷新访问令牌 |
| auth | `POST /api/v1/auth/logout` | 撤销刷新令牌 |
| identity | `GET /api/v1/me` | 当前账号与角色 |
| identity | `GET /api/v1/me/shops` | 可见门店集合（**门店切换器唯一数据源**；集合可为空，返回 `default_shop_id = null`） |
| identity | `GET /api/v1/me/permissions` | 当前权限点 |
| shop | `GET / POST /api/v1/shops` | 门店列表 / 新建 |
| shop | `GET / PATCH /api/v1/shops/{shopId}` | 门店详情 / 修改 |
| shop | `GET / PATCH /api/v1/shops/{shopId}/settings` | 门店参数 |
| catalog | `GET / POST /api/v1/catalog-items` | 目录列表 / 新建（服务项带 `duration_minutes`，ADR-0018） |
| catalog | `GET / PATCH /api/v1/catalog-items/{id}` | 目录项详情 / 修改 |
| catalog | `GET /api/v1/catalog-categories` | 分类树 |
| catalog | `GET / PUT /api/v1/shops/{shopId}/catalog-overrides/{itemId}` | 按店价格与上下架 |
| member | `GET / POST /api/v1/members` | 会员检索 / 建档（全局） |
| member | `GET / PATCH /api/v1/members/{id}` | 会员档案 |
| member | `GET /api/v1/shops/{shopId}/members/{id}/balance` | 该店余额 |
| member | `GET /api/v1/shops/{shopId}/members/{id}/ledger` | 该店余额流水 |
| member | `POST /api/v1/shops/{shopId}/members/{id}/topups` | 充值（店内账）；落 `payments(kind='topup', order_id=NULL, member_id=<id>)`，带本次 `shift_id` / `operator_id`（ADR-0013） |
| queue（[ADR-0009](../decisions/0009-queues-and-staff.md)） | `GET / POST /api/v1/shops/{shopId}/queues` | 队列列表（按 `seq` 升序）/ 新建（序号 + 名称） |
| queue | `GET / PATCH / DELETE /api/v1/shops/{shopId}/queues/{queueId}` | 队列详情（含服务人员）/ 修改 / 删除 |
| queue | `GET / POST /api/v1/shops/{shopId}/staff` | 服务人员列表 / 建档 |
| queue | `GET / PATCH / DELETE /api/v1/shops/{shopId}/staff/{staffId}` | 人员详情（含所在队列）/ 修改（`duty_status` 在岗切换，见 [ADR-0010](../decisions/0010-duty-status-and-queue-order.md)）/ 删除 |
| queue | `GET /api/v1/shops/{shopId}/queues/{queueId}/staff` | 队列内服务人员（按 `position` 升序） |
| queue | `PUT / DELETE /api/v1/shops/{shopId}/queues/{queueId}/staff/{staffId}` | 加入（追加到队尾）/ 移出（剩余位置压缩，幂等） |
| queue | `PUT /api/v1/shops/{shopId}/queues/{queueId}/staff/order` | 队列内全量重排：`{staff_ids, version}`，成功回传新队列详情，409 版本冲突 |
| room（[ADR-0011](../decisions/0011-rooms-and-floors.md)） | `GET / POST /api/v1/shops/{shopId}/rooms` | 房间列表（按 `floor, code` 升序）/ 新建：`{floor, code, name}` |
| room | `GET / PATCH / DELETE /api/v1/shops/{shopId}/rooms/{roomId}` | 房间详情 / 修改（`{floor?, code?, name?, status?, version}`，409 版本冲突）/ 删除；被订单引用时删除返回 `409 ROOM_IN_USE`（ADR-0022） |
| scheduling（[ADR-0012](../decisions/0012-scheduling-engine.md)） | `GET /api/v1/shops/{shopId}/scheduling/board` | 调度看板：队列 + 成员位次 + 执行者状态 |
| scheduling | `GET / POST /api/v1/shops/{shopId}/scheduling/rules` | 判定链读取 / 发布新版本 |
| scheduling | `GET / PUT /api/v1/shops/{shopId}/scheduling/policies` | 联动策略读取 / 发布 |
| scheduling | `GET /api/v1/shops/{shopId}/scheduling/task-types` | 任务类型列表 |
| scheduling | `GET /api/v1/shops/{shopId}/scheduling/events` | 派单事件与决策摘要 |
| scheduling | `GET /api/v1/shops/{shopId}/scheduling/audit` | 审计流水（append-only） |
| scheduling | `POST /api/v1/shops/{shopId}/scheduling/dispatch` | 派单：决策 + 联动单事务提交 |
| scheduling | `POST / DELETE /api/v1/shops/{shopId}/scheduling/pins` | 置顶 / 解除置顶（逐队列） |
| scheduling | `POST /api/v1/shops/{shopId}/scheduling/pins/release-expired` | 到期 / 不可用自动释放 |
| scheduling | `POST /api/v1/shops/{shopId}/scheduling/events/{eventId}/{action}` | `restore` 回原位 / `cancel` 取消 / `complete` 完成 / `assignment-method` 指派方式变更 |
| order | `POST /api/v1/shops/{shopId}/orders` | 开单（`draft`）；请求体可选 `{room_public_id?, card_public_id?}`（ADR-0022），校验同专属绑定端点；两者可空，提交结账前补齐 |
| order | `POST /api/v1/shops/{shopId}/orders/price` | 实时试价（只读、不落库、无需 `Idempotency-Key`，ADR-0019；权限 `order.read`，店员可用）：请求全量 `lines[{catalog_item_id,quantity}]` + 可选 `adjustments[{type,amount,source_id?,title,description?}]`（`type` 五值见 ADR-0020，券必带 `source_id`），返回逐行快照价与折让分摊、`subtotal` / `discount_amount` / `total_amount`；服务端按 `COALESCE(本店覆盖价, 目录基础价)` 取价，与 `submit` 共用同一 `Quote` 纯函数实现所见即所得；折让 > 小计 → `422`，目录项不存在 / 数量非法 / 已下架 → `400`。详见第 10 节 |
| order | `GET /api/v1/shops/{shopId}/orders` | 单据列表（keyset 分页） |
| order | `GET /api/v1/shops/{shopId}/orders/{id}` | 单据详情（含明细）；金额响应含不可变 `total_amount` / `discount_amount` 与**派生** `paid_amount` / `refunded_amount` / `refundable_amount`（从 `payments` / `refunds` 的 succeeded 行聚合，不落库；已退显式过滤 `kind='sale'`，不含充值退款，ADR-0014/0016）；上下文含 `card`（当前有效绑定，已释放为 null）/ `card_binding_history`（最近一次卡 + 释放原因）/ `room: {public_id, floor, code, name} | null` / `companion_group: {public_id, status} | null`（当前活跃成员关系所属组，单值——`uq_cgm_active_order` 保证一单至多一个活跃成员关系；组状态含 `open` / `closed` / `cancelled`，由前端判断可操作性；只读派生，不落 `orders` 列，拆出后为 null，ADR-0022 §5） |
| order | `POST / DELETE /api/v1/shops/{shopId}/orders/{id}/lines` | 加项 / 删项 |
| staff（[ADR-0023](../decisions/0023-service-item-qualifications-and-line-staff.md)） | `GET / PUT /api/v1/shops/{shopId}/staff/{staffId}/service-items` | 查询 / 全量替换服务项资质：`{catalog_item_ids[], version}`；仅 `type='service'` 目录项，读 `queue.read`、写 `queue.write`；`GET /staff` 与 `/staff/{id}` 增量返回 `service_item_ids[]` |
| order（[ADR-0023](../decisions/0023-service-item-qualifications-and-line-staff.md)） | `PUT /api/v1/shops/{shopId}/orders/{id}/lines/{lineNo}/staff` | 改派 / 清空明细执行服务员：`{staff_public_id: string \| null}`；仅 `draft` / `open`，服务行改派校验资质与任职状态，商品行拒绝；`POST /orders/{id}/lines` 请求体同步支持可选 `staff_public_id`；订单详情 `lines[]` 增量返回 `staff_public_id` / `staff_name_snapshot` |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/submit` | 提交结账 → `pending_payment`；**前置校验：必须已绑定一张有效 ID 卡**（本店 / `active` / 未释放）与一个本店有效房间，否则 `422 ORDER_CARD_REQUIRED` / `ORDER_ROOM_REQUIRED`（ADR-0022，修订 ADR-0017「不校验开单必须持卡」与 ADR-0011「房间不与单据绑定」边界） |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/payments` | 组合支付 → `paid`；**结清时同事务自动释放卡**：订单卡绑定置 `card_released_at/reason=settled`，该卡当前持有流水置 `released_at/release_reason='order_settled'`，并写审计 / outbox；部分支付未结清不释放（ADR-0022） |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/refunds` | 销售退款（生成 `kind='sale'` 退款单）；请求体 `{amount, reason, refund_entries:[{method, amount}]}` 必须显式携带渠道明细（ADR-0014），`Σ refund_entries = amount`、单渠道累计退款 ≤ 该渠道原支付额，超限返回 `409`（由前端 / 客户协商，系统不自动分摊）；只退实际支付部分、不含券优惠；**禁止跨店退款**；退款单记**退款动作发生时**的 `shift_id` / `operator_id`，不继承原单（ADR-0013/0016） |
| order | `POST /api/v1/shops/{shopId}/members/{id}/topups/{paymentId}/refunds` | 充值退款（生成 `kind='topup'` 退款单，权限 `member.topup.write`）；请求体同上；前置校验原收款 `kind='topup' AND status='succeeded'` 且属本店会员，**余额不足 → `409 INSUFFICIENT_BALANCE` 整笔拒绝、不做部分退**；单事务内扣回本店 `member_balances` 并写反向 `balance_ledger`，**禁止跨店**（ADR-0016） |
| order | `POST /api/v1/shops/{shopId}/orders/{id}/cancel` | 取消未支付单据；同事务自动释放卡（原因 `cancelled` / `order_cancelled`），避免卡被废弃单占用（ADR-0022） |
| order | `GET /api/v1/shops/{shopId}/shifts/current` | 当前班次 |
| order | `POST /api/v1/shops/{shopId}/shifts/open` | 开班 |
| order | `POST /api/v1/shops/{shopId}/shifts/{id}/close` | 关班结算 |
| order（[ADR-0022](../decisions/0022-order-card-binding-and-companion-groups.md)） | `POST /api/v1/shops/{shopId}/orders/{orderId}/card` | 绑定 / 更换 ID 卡：`{card_public_id, version}`；卡须本店 + `active` + 当前被本店人员持有，订单须未结算；对卡 `FOR UPDATE` 串行化 + 部分唯一索引兜底「一卡一活跃单」，冲突 `409 CARD_BOUND_TO_ORDER` / `CARD_NOT_HELD` |
| order | `DELETE /api/v1/shops/{shopId}/orders/{orderId}/card` | 解绑 ID 卡：`?version=`，订单侧置 `card_released_at` + `reason=unbound`（保留 `card_id` 供追溯），**不释放人员持卡**，仅未结算单可用 |
| order（[ADR-0022](../decisions/0022-order-card-binding-and-companion-groups.md)） | `POST /api/v1/shops/{shopId}/orders/{orderId}/room` | 绑定 / 更换房间：`{room_public_id, version}`；房间须本店 + `active`，订单须未结算；停用房间不可新绑，已绑房间停用不影响结算；不存在 `404 ROOM_NOT_FOUND`、停用 `409 ROOM_INACTIVE`（ADR-0022 §7） |
| order | `POST /api/v1/shops/{shopId}/companion-groups` | 建同行组：`{order_public_ids?}`（可空组）；一单一卡、多单成组共同结算，权限 `order.write` |
| order | `GET /api/v1/shops/{shopId}/companion-groups` | 同行组列表（按 `status` 筛选、keyset 分页），权限 `order.read` |
| order | `GET /api/v1/shops/{shopId}/companion-groups/{groupId}` | 同行组详情：成员单 + 各自派生金额 + 组内只读合计 `payable_total` / `paid_total`（**不落库、不新增付款主体，付款仍逐单 `POST /orders/{id}/payments`**） |
| order | `POST /api/v1/shops/{shopId}/companion-groups/{groupId}/members` | 合并：`{order_public_ids[], version}`；入组单须未结算，已在别组则同事务从原组移出、原组空则自动 `closed`；组 `FOR UPDATE` + 版本锁，唯一索引兜底「一单一活跃组」 |
| order | `DELETE /api/v1/shops/{shopId}/companion-groups/{groupId}/members/{orderId}` | 拆分：`?version=`，成员 `left_at = now()` 留史，订单恢复独立，卡 / 房间绑定与金额不变 |
| order | `POST /api/v1/shops/{shopId}/companion-groups/{groupId}/close` | 关闭组：`{version}`，要求全部活跃成员已结算或无活跃成员 |
| order | `POST /api/v1/shops/{shopId}/companion-groups/{groupId}/cancel` | 取消组：`{version}`，要求成员无成功支付 |
| card | `GET /api/v1/shops/{shopId}/cards` | 卡片列表（ADR-0017）响应增量 `bound_order: {public_id, order_no, status} | null`（当前有效占用该卡的订单，已释放为 null，ADR-0022） |

> 收款 / 退款归属（[ADR-0013](../decisions/0013-payment-and-refund-ownership.md) / [ADR-0014](../decisions/0014-derived-amounts-and-refund-entries.md) / [ADR-0016](../decisions/0016-refund-source-and-topup-reversal.md)）：订单支付落 `payments(kind='sale', order_id=<id>)`，会员充值落 `kind='topup'` 且不写 `order_id`；两类收款都记录本次动作的 `shift_id` / `operator_id`。退款以 `kind` 区分销售 / 充值：销售退款挂订单、充值退款挂会员，均自带退款时的班次与操作人（可与原单不同），累计退款不超过原支付额；退款请求显式带 `refund_entries` 渠道明细，系统只校验、不自动分摊，**只退实际支付部分**（券优惠进 `order_price_adjustments`、不进支付 / 退款渠道），**禁止跨店退款**，充值退款余额不足整笔拒绝。订单详情的实付 / 已退 / 可退为派生字段（从 succeeded 记录聚合，已退过滤 `kind='sale'`），订单表不存 `paid_amount`；单号日期取 `business_date`（UTC+8 + 门店切点），与日报 / 班次 / 汇总同口径。班次 / 人员维度统计从明细按需聚合，`daily_shop_summary` 粒度不变；本次不新增其他收款 / 退款专属端点。
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

**范围排除清单**（ADR-0004 / 0005 / 0007，以下端点不存在，前端不要预留入口）：

| 能力 | 状态 | 替代做法 |
|---|---|---|
| 打印 / 小票模板 / 打印任务 | 不提供 | 用导出端点下载文件后在系统外处理 |
| 离线单据提交、补传队列、同步水位 | 不提供 | 网络失败明确报错，恢复后由用户重新发起（复用幂等键） |
| 业务数据归档查询、业务保留窗口 API | 不提供 | 业务数据在线全量保留；**审计日志保留 30 天是运维配置项 `AUDIT_LOG_RETENTION_DAYS`，不是 API，也不能按店调整**（ADR-0007） |
| 币种、汇率、按店时区设置 | 不提供 | 全局固定 CNY + UTC+8 |
| `/shop-groups`、店群成员管理、按组筛选 / 按组授权 | 不提供（ADR-0005） | 跨店汇总用 `scope=all` 或 `shop_ids=a,b`，与可见门店集合求交 |
| 服务端「切换当前门店」端点、`current_shop` / 用户偏好表 | 不提供（ADR-0006） | 切换是纯客户端上下文变化：改带 `X-Shop-Id`、失效本地缓存、重取权限 |
| 支付 / 短信 / 对象存储的回调、投递与配置端点 | R1 不提供（ADR-0007） | R1 不接任何外部服务，Outbox 订阅者全部在进程内；接入前另立 ADR |
| 跨店差额清分、内部往来账、结算单端点 | 不提供（ADR-0007） | 差额只用按店分组的报表呈现，不做自动清分 |
| 按 ID 修改 / 删除审计记录、审计归档查询端点 | 不提供（ADR-0007） | 审计记录对业务请求只追加；超期记录仅由 worker 清理任务删除，不导出归档 |
| 审计专用导出端点 `/audit-logs/export` | 不提供（ADR-0008） | 窗口内导出复用统一 `POST /exports` + 流式下载，并执行同一保留窗口校验 |

**审计日志的查询边界（ADR-0007 / ADR-0008，已冻结）**：窗口按 UTC+8 自然日计算（`cutoff = 当日 00:00 − 30 天`）；`created_at_from < cutoff` 一律返回 **`400 AUDIT_RETENTION_WINDOW_EXCEEDED`**（`retryable=false`），`details` 带 `earliest_queryable_at` / `retention_days` / `cutoff_at`，`message` 给出可读的最早可查时间；**不得伪装成空结果、不得静默把起始时间裁剪进窗口**。缺省 `created_at_from` 时服务端默认从 `cutoff` 起查，不报错。**不单独提供审计导出端点**：窗口内导出复用统一 `POST /exports` + 流式下载，并在创建任务时执行同一校验、返回同一错误码。保留天数与窗口口径是运维配置，不通过 API 暴露；完整契约见 [08](08-audit-retention.md) §4。R1 首批不含审计查询界面，但错误码已冻结。

## 9. 超时、限流与契约冻结（DP-06）

| 项 | 约定 |
|---|---|
| 普通请求 | 5 s 超时，context 一路传到数据库；客户端断开后服务端计算必须终止 |
| 报表查询 | 30 s；走汇总表，不做大表实时全扫 |
| 导出 | 异步任务，`202` 立即返回；下载用流式写出，不把文件读进内存 |
| 限流 | 按「主体 + 门店」维度，返回 `429` + `Retry-After` |
| WebSocket | 30 s 心跳、断线重连；推送携带 `shop_id`，不跨店广播 |

**契约冻结（已由 ADR-0008 冻结，DP-06）**：

1. `api/openapi.yaml` 由注释生成并提交进仓库；CI 校验「重新生成后无 diff」，防止代码与契约漂移。
2. 响应快照测试只断言**字段集合与类型**，动态值（时间、ID、金额）做归一化，不比对字面值。
3. 破坏性变更清单：删除/重命名字段、改类型、改错误码语义、改分页形状、改门店上下文规则。这类变更须升版本或走弃用期，并同步 `porch-console`。
4. 端点清单（第 8 节）与 OpenAPI 一一对应；新增端点必须同时更新本册与契约文件。

## 10. 订单实时试价 `POST /shops/{shopId}/orders/price`（ADR-0019）

收银场景增减项时即时显示应付与优惠分摊。该端点**只读、不落库、不占单号、不开事务、不写幂等记录、不发事件**；权限 `order.read`（店员 / 店长 / 管理员均可；`order.write` 仅店长 + 管理员，店员开单需另立 ADR），单店上下文同 §3。价格取 `COALESCE(本店覆盖价, catalog_items.base_price)`（覆盖是整价替换、非增量），与加项快照、F-01 `submit` 后端重算**共用同一个 `Quote` 纯函数**，保证所见即所得（仅当两次调用间目录改价才可能不同，由 `submit` 的 `422` 兜住并触发前端重新试价）。

请求体（`lines` 为**全量**明细，金额 / 数量均为定点字符串）：

```json
{
  "lines": [
    { "catalog_item_id": "019...", "quantity": "2.000" },
    { "catalog_item_id": "019...", "quantity": "1.000" }
  ],
  "adjustments": [
    { "type": "rounding", "amount": "0.50", "title": "抹零" },
    { "type": "coupon", "amount": "5.00", "source_id": 123, "title": "满100减10", "description": "会员生日券" }
  ]
}
```

响应体：

```json
{
  "data": {
    "lines": [
      {
        "line_no": 1,
        "catalog_item_id": "019...",
        "item_name_snapshot": "示例服务",
        "quantity": "2.000",
        "unit_price": "30.00",
        "amount": "60.00",
        "allocated_adjustment": "3.00",
        "line_total": "57.00"
      }
    ],
    "subtotal": "60.00",
    "discount_amount": "5.00",
    "total_amount": "55.00"
  },
  "meta": { "request_id": "req_01J..." }
}
```

| 项 | 规则 |
|---|---|
| 权限 | `order.read`：staff / manager / admin 均可试价；`order.write`（开单 / 收款 / 退款）仅 manager + admin，店员开单须另立 ADR。`member.topup.write` 仅 manager + admin |
| 取价 | `unit_price` = `COALESCE(shop_catalog_overrides.price, catalog_items.base_price)`（覆盖价是整价替换、不是增量；有本店覆盖行用覆盖价，否则用基础价）；`item_name_snapshot` 取当前目录名（与加项快照同口径） |
| 上下架 | `catalog_items.active = false` 或（有覆盖行时）`shop_catalog_overrides.available = false` 视为不可用 → `400 INVALID_ARGUMENT`（`details.reason: item_unavailable`）；下架项不得进入试价结果，也不得据此开单 |
| 行金额 | `amount = unit_price × quantity`（`numeric(14,2)`，scale 2、ROUND_HALF_UP）；`subtotal = Σ amount` |
| 折让输入 | `adjustments[]` 每项：`type` 必填且为 `coupon` / `promotion` / `rounding` / `manual` / `other` 之一（ADR-0020）；`amount` 非负；`title` 必填非空（账单短标题）；`description` 可选；`type='coupon'` 时 `source_id`（bigint，券实例内部 ID）必填且为正整数（本期只校验形状、不查券是否存在），其他类型必须为空。券不是支付（ADR-0016）；`discount_amount = Σ adjustments.amount` |
| 分摊 | **先把全部 `adjustments` 合并为单个 `discount_amount`，再一次性分摊**（不是逐笔优惠各分摊一遍）；`AllocateByWeights` 按行 `amount` 权重分摊，余数按 `line_no` 升序落最后一行；`Σ allocated_adjustment = discount_amount` 且结果确定；`line_total = amount − allocated_adjustment`。响应只给合并后的每行分摊，逐笔优惠展示由前端按请求 `adjustments` 自行渲染 |
| 合计 | `total_amount = subtotal − discount_amount` |
| 校验 | `quantity <= 0` / 非数字 / 目录项不存在 / 已下架 → `400 INVALID_ARGUMENT`（`details` 指向行）；`discount_amount > subtotal` → `422 UNPROCESSABLE` |
| 幂等 | **不需要 `Idempotency-Key`**（见第 6 节例外）；只读无副作用 |
| 限流 / 超时 | 同 §9：按主体 + 门店限流、5 s 超时；请求体受 1 MiB 上限约束 |
| 不做 | 不锁库存、不校验余额 / 班次（属 `submit` 与支付事务职责）；不做会员价 / 时段价（另立 ADR） |

## 11. ADR-0024 增补：分类端点与投影

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/shops/{shopId}/staff-categories` | `queue.read` | 列出本店全部分类（含 `status`），按 `sort_order, code` 排序 |
| POST | `/staff-categories` | `queue.write` | `{code,name,sort_order?}`；店内唯一，写带 `Idempotency-Key` |
| PATCH | `/staff-categories/{categoryId}` | `queue.write` | `{name?,sort_order?,status?,version}`；停用只阻止新绑定 |
| PATCH | `/staff/{staffId}` | `queue.write` | 增量 `category_public_id?: string \| null`，沿用 `version` 乐观锁 |
| GET | `/staff` / `/staff/{staffId}` | `queue.read` | 增量 `category: {public_id,code,name} \| null` |
| GET | `/catalog-items` | `catalog.read` | 增量 `category_public_id` / `category_name`（可空）与只读 `attributes`；属性只供商品弹窗展示，不写回订单行 |

新增错误码：`STAFF_CATEGORY_NOT_FOUND`（404，分类不存在或跨店）、`STAFF_CATEGORY_INACTIVE`（409，分类停用不能新绑定）；重复 `code` / `name` 与并发修改沿用既有 409。开单辅助选人不新增端点，复用 `GET /staff`、`GET /catalog-items`、`GET /catalog-categories` 一次取回后前端筛选；服务端资质校验仍按 ADR-0023 返回 `STAFF_NOT_QUALIFIED`。

---

下一册：[04 应用架构](04-application-architecture.md)。

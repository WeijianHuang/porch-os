# 前期系统架构详细设计 · 02 数据库设计

> 上游：[01 领域模型](01-domain-model.md)（聚合与不变量）、[ADR-0001](../decisions/0001-postgresql-18.md)（PostgreSQL 18.7）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（只有 `shop_id`）、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种、东八区、暂不归档）、[ADR-0005](../decisions/0005-no-shop-groups.md)（无门店组表）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（`user_shops` 关系基数与无当前门店表）、[ADR-0007](../decisions/0007-remaining-open-questions.md)（`audit_logs` 保留 30 天定时删除、暂不读写分离、迁移只跑一次）、[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)（设计冻结、Docker 2+2、Go Job、审计清理与容量基线）、[ADR-0009](../decisions/0009-queues-and-staff.md)（队列 / 服务人员领域扩展）。
> 本文给的是**设计定稿用的 schema 形状**。第 3 节 DDL 是依赖自洽、可在 PG 18.7 上直接执行的**核心表节选**（不含全部表、不含完整 `Down`）；真实迁移文件从 R1 起放 `migrations/`，由 goose 执行。

## 1. 全局约定

| 主题 | 约定 | 理由 |
|---|---|---|
| 方言 | PostgreSQL 18.7，只用它原生能力 | 不写兼容层、不做方言抽象 |
| 库结构 | 单库、单 schema | ADR-0003：没有租户层、没有 schema-per-shop |
| 命名 | 表名复数小写下划线，索引 `idx_<表>_<列>`，唯一约束 `uq_<表>_<列>`，外键 `fk_<表>_<列>` | 可 grep、可生成 |
| 主键 | `bigint GENERATED ALWAYS AS IDENTITY` | 内部主键不外露，便于迁移与重编号；暂不归档，无需预留归档主键段 |
| 对外标识 | `public_id uuid NOT NULL DEFAULT uuidv7()`，单独唯一约束 | 时间有序、可安全按创建顺序排序；PG 17 降级时改应用层生成 |
| 金额 | `numeric(14,2)`；数量 `numeric(14,3)` | 禁止 `float` / `double precision`；**单一币种 CNY，不设 `currency` 列、不存汇率**（ADR-0004） |
| 时间 | `timestamptz`，UTC 存储；所有连接初始化执行 `SET TIME ZONE 'UTC'`；区间一律半开 `[start, end)` | 类型层面保留时区语义，内部按 UTC 存储；展示与业务日显式转换、不依赖 session 默认；不采用无时区 `timestamp`（ADR-0014） |
| 业务日 | `(p_at AT TIME ZONE 'Asia/Shanghai')::date` + 门店营业日切点偏移，统一封装为 `business_date(p_at, p_shop_id)` 函数 | 全系统固定 UTC+8，**不设门店级 `timezone` 列**、不接受客户端时区参数（ADR-0004）；日报、班次、单号日期、导出共用；门店只可配切点偏移，不可覆盖基准时区（ADR-0014） |
| 保留 | 业务数据在线全量保留 | 暂不归档、不设保留窗口、不分区；详见第 7 节 |
| 枚举 | `text + CHECK`，不建 PG `ENUM` 类型 | 取值变更走迁移，不需要 `ALTER TYPE` |
| JSON | `jsonb + 默认 '{}'`，只放非查询维度属性 | 需要检索的字段必须独立成列 |
| 审计字段 | `created_at`、`updated_at`、`created_by`、`version` | `version` 用于乐观锁 |
| 删除 | 业务单据不物理删除；软删除用 `deleted_at` + 部分索引 | 保留审计链；`WHERE deleted_at IS NULL` 必须显式 |
| 外键 | 业务表之间建外键；跨库/外部系统引用只存 ID 不建外键 | 保证单据与明细同店，见第 4 节 |
| 视图 | **不创建任何数据库视图** | 计算口径写受版本控制的 SQL（ADR-0002 阻断项 ③） |
| 触发器 / 存储过程 | 不使用 | 业务逻辑只在应用层与受控 SQL 中 |

## 2. 表清单

类别：A=全局主数据（无 `shop_id`）、B=门店业务数据（必有 `shop_id`）、C=授权/关系、P=平台基础设施、D=派生可重算。

| 模块 | 表 | 类别 | 说明 | 唯一性范围 |
|---|---|---|---|---|
| shop | `shops` | A | 门店主表，`id` 即 `shop_id`；**不带分组 / 层级 / 标签列**（ADR-0005） | `code` 全局唯一 |
| shop | `shop_settings` | B | 门店参数（营业日切点等）；基准时区全局固定，不按店配置 | `(shop_id, key)` |
| identity | `users` | A | 账号 | `login` 全局唯一 |
| identity | `roles` / `permissions` | A | 角色与权限点 | `code` 全局唯一 |
| identity | `role_permissions` | C | 角色 ↔ 权限 | `(role_id, permission_id)` |
| identity | `user_roles` | C | 用户 ↔ 角色 | `(user_id, role_id)` |
| identity | **`user_shops`** | C | **用户 ↔ 可见门店集合**（授权的唯一来源；不存在分组授权中间表）；**一人多店 / 一店多人**——`users` 与 `shops` 都不带单店外键或 owner 列，也不设 `current_shop` / 偏好表（ADR-0006） | `(user_id, shop_id)` |
| identity | `refresh_tokens` | A | 刷新令牌（存哈希） | `token_hash` 全局唯一 |
| identity | `audit_logs` | P | 业务请求只追加、不可改删；与门店相关的事件带 `shop_id`，账号级事件为 `NULL`；属**基础设施 / 运行期数据表**，不参与业务隔离判定；**默认保留 30 天、可配置、由 worker 定时分批删除**（ADR-0007），不归档、不分区 | — |
| catalog | `catalog_categories` | A | 目录分类 | `(parent_id, code)` |
| catalog | `catalog_items` | A | 商品/服务目录（服务项带 `duration_minutes`，ADR-0018） | `sku` 全局唯一 |
| catalog | `shop_catalog_overrides` | B | 按店价格、上下架 | `(shop_id, catalog_item_id)` |
| member | `members` | A | 会员档案 | `phone` 全局唯一 |
| member | `member_balances` | B | 按店余额 | `(shop_id, member_id)` |
| member | `balance_ledger` | B | 余额变动流水（只追加） | `(shop_id, public_id)` |
| queue | `queues` | B | 门店队列（序号 + 名称，门店内各自唯一） | `(shop_id, seq)` / `(shop_id, name)` / `public_id` |
| queue | `staff` | B | 门店服务人员（店内雇员，可在本店多队列服务；`duty_status` 表示当前在岗 / 下班，无历史） | `(shop_id, code)` / `public_id` |
| queue | `queue_staff` | B | 服务人员 ↔ 队列（店内多对多，复合外键锁同店；`position` 为队列内密集 1..n 顺序） | `(queue_id, staff_id)` / `(queue_id, position)` |
| room（[ADR-0011](../decisions/0011-rooms-and-floors.md)） | `rooms` | B | 门店房间（`floor` 整数 `-5..200` 为分组维度，非实体；房间号店内唯一） | `(shop_id, code)` / `public_id` |
| scheduling（[ADR-0012](../decisions/0012-scheduling-engine.md)） | `scheduling_rules` | B | 8 步判定链规则（顺序 / 开关 / 作用域 / 命中动作 / 时段可配） | `(shop_id, type, scope, scope_id)` / `public_id` |
| scheduling | `scheduling_policies` | B | 联动策略（六种传播模式 + 逐队列动作映射，绑定主队列） | `(shop_id, primary_queue_id)` / `public_id` |
| scheduling | `scheduling_task_types` | B | 任务类型（分组 + 不轮转标记） | `(shop_id, code)` / `public_id` |
| scheduling | `scheduling_events` | B | 派单事件与决策摘要（幂等锚点） | `(shop_id, public_id)` |
| scheduling | `scheduling_snapshots` | B | 一次事件的各队列位次前后快照 | `(shop_id, event_id)` |
| member | `member_visits` | B | 到店记录 | `(shop_id, public_id)` |
| order | `shifts` | B | 收银班次 | `(shop_id, shift_no)`；部分唯一：一店最多一个 `open` |
| order | `orders` | B | 单据 | **`(shop_id, order_no)` 店内唯一** |
| order | `order_lines` | B | 单据明细（价格快照） | `(shop_id, order_id, line_no)` |
| order | `price_adjustments` | B | 整单折扣/抹零（已分摊到行） | `(shop_id, order_id, seq)` |
| order | `payments` | B | 收款（`kind` = `sale` / `topup` / `other`；`sale` 挂订单、`topup` 挂会员） | `(shop_id, idempotency_key)`；`sale` 另受 `(shop_id, order_id)` 外键 |
| order | `payment_entries` | B | 组合支付的一笔（逐实际资金渠道，不含券） | `(shop_id, payment_id, seq)` |
| order | `refunds` | B | 退款单（`kind`=sale/topup，`order_id`/`member_id` 二选一；自带退款班次 / 操作人，无 `refunding` 态；禁止跨店） | `(shop_id, refund_no)` |
| order | `refund_entries` | B | 退款逐渠道明细（请求显式携带，只退实际支付部分，系统只校验不自动分摊，ADR-0014/0016） | `(shop_id, refund_id, seq)` |
| inventory | `inventory_balances` | B | 按店库存 | `(shop_id, catalog_item_id)` |
| inventory | `stock_movements` | B | 出入库流水（只追加） | `(shop_id, public_id)` |
| inventory | `stock_transfers` | B | 跨店调拨单（显式动作） | `(shop_id, transfer_no)` |
| reporting | `daily_shop_summary` | D | 日汇总，可重算 | `(shop_id, business_date)` |
| reporting | `export_jobs` | D | 导出任务 | `(shop_id, public_id)` |
| platform | `idempotency_keys` | P | 幂等记录 | `(scope, key)` |
| platform | `outbox_events` | P | 事务内事件，worker 派发 | `id` |
| platform | `dead_letters` | P | 投递失败终态 | `id` |
| platform | `schema_migrations` | P | goose 版本记录 | `version` |
| platform | **`audit_log_cleanup_runs`** | P | **审计清理的运行痕迹**（`run_key` + `attempt` 唯一、cutoff、删除行数、状态）；**不写回 `audit_logs`**、不带 `shop_id`、不是审计记录（ADR-0008 / [08](08-audit-retention.md) §5） | `(run_key, attempt)` |

> `user_shops` 含 `shop_id` 列，但它是**授权关系**（C 类），也是可见门店集合的唯一来源，不是把业务数据挂到上层实体——ADR-0003 禁止的是 tenant/org/company 这类上层归属维度。按 [ADR-0005](../decisions/0005-no-shop-groups.md)，schema 中**不存在** `shop_groups` / `shop_group_members` 之类的分组表，也不允许给 `shops` 加 `group_id` / `parent_id` / 标签列变相分组。按 [ADR-0006](../decisions/0006-user-shops-and-current-shop.md)，`user_shops` 表达「一人多店 / 一店多人」：`users` 不带 `shop_id` / `current_shop_id`，`shops` 不带 `owner_user_id`，schema 中不设 `current_shop`、用户偏好表或任何服务端当前门店状态；当前门店只在请求期由客户端显式携带。

## 3. 核心 DDL（PG 18.7 语法示例）

以下示例刻意包含「复合外键把 `shop_id` 钉死」这一关键手法：明细无法通过外键挂到别家店的单据上。订单域的收款 / 退款按 [ADR-0013](decisions/0013-payment-and-refund-ownership.md) 与 [ADR-0016](decisions/0016-refund-source-and-topup-reversal.md) 定义：`payments.kind` 区分销售 / 充值 / 其他，`sale` 与 `topup` 由 CHECK 约束钉死订单 / 会员的填法；`payments` 与 `refunds` 的 `shift_id` / `operator_id` 记录动作发生时的班次与操作人（只持 ID、不加外键），退款归属不继承订单。`refunds.kind` 对齐原收款（`sale`/`topup`）、`order_id` 可空、`topup` 退款填 `member_id`；退款只退实际支付部分，**券不是支付渠道**、一律进 `price_adjustments`，故 `payment_entries` / `refund_entries` 的 `method` CHECK 不含 `coupon`（仅 `cash`/`balance`/`card`）。

```sql
-- +goose Up
CREATE TABLE shops (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  code         text        NOT NULL,
  name         text        NOT NULL,
  -- 基准时区为全局常量 Asia/Shanghai（ADR-0004），不设门店级 timezone 列；
  -- 营业日切点等业务参数放 shop_settings。
  status       text        NOT NULL DEFAULT 'active',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_shops_code      UNIQUE (code),
  CONSTRAINT uq_shops_public_id UNIQUE (public_id),
  CONSTRAINT ck_shops_status    CHECK (status IN ('active', 'inactive'))
);

-- 以下三张是复合外键示例所需的依赖表（字段为节选）
CREATE TABLE users (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  login         text        NOT NULL,
  password_hash text        NOT NULL,
  status        text        NOT NULL DEFAULT 'active',
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_users_login UNIQUE (login)
);

CREATE TABLE members (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  phone        text        NOT NULL,
  display_name text        NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_members_phone UNIQUE (phone)
);

CREATE TABLE catalog_items (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  sku         text        NOT NULL,
  name        text        NOT NULL,
  type        text        NOT NULL DEFAULT 'product',
  base_price  numeric(14,2) NOT NULL DEFAULT 0,
  attributes  jsonb       NOT NULL DEFAULT '{}'::jsonb,
  -- ADR-0018：服务标称时长（分钟）；商品必须为 NULL，服务必须 1..1440。
  -- 与看板 service_remaining_minutes（订单派生、本期恒 null）是两个字段。
  duration_minutes integer,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_catalog_items_sku UNIQUE (sku),
  CONSTRAINT ck_catalog_items_type CHECK (type IN ('product','service')),
  CONSTRAINT ck_catalog_items_duration CHECK (
    (type = 'product' AND duration_minutes IS NULL) OR
    (type = 'service' AND duration_minutes BETWEEN 1 AND 1440)
  )
);

-- 一人多店 / 一店多人（ADR-0006）：可见门店集合的唯一授权来源。
-- users / shops 均不带单店外键或 owner 列；当前门店是请求期上下文，
-- 不落表，也不设 current_shop / user_preferences 之类的偏好表。
CREATE TABLE user_shops (
  user_id    bigint      NOT NULL REFERENCES users(id),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, shop_id)
);
CREATE INDEX idx_user_shops_shop ON user_shops (shop_id, user_id);

-- 业务日与单号统一函数（ADR-0014）：基准 UTC+8 + 门店 shop_settings 营业日切点偏移；
-- 不依赖 session 时区（连接初始化已 SET TIME ZONE 'UTC'），不接受门店覆盖基准时区（ADR-0004）。
-- 详情见文末「业务日与单号函数」小节；此处给出签名，供 orders.order_no 显式调用。
-- CREATE FUNCTION business_date(p_at timestamptz, p_shop_id bigint) RETURNS date ...;
-- CREATE FUNCTION next_order_no(p_shop_id bigint) RETURNS text ...;
-- 单号由数据库序列生成序号，杜绝应用层拼接导致并发重复
CREATE SEQUENCE order_no_seq AS bigint;

CREATE TABLE orders (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL REFERENCES shops(id),
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  -- 单号日期取 business_date（UTC+8 + 门店切点），与日报 / 班次 / 汇总同口径（ADR-0014）；
  -- 无字面 DEFAULT，INSERT 时必须显式调用 next_order_no(shop_id)
  order_no         text        NOT NULL,
  status           text        NOT NULL DEFAULT 'draft',
  member_id        bigint      REFERENCES members(id),
  shift_id         bigint,
  operator_id      bigint      NOT NULL,
  -- 单一币种 CNY（ADR-0004）：金额列不带 currency，不做币种校验与换算
  -- 应付快照不可变：total = Σ(line) − Σ(adjustment)；实付 / 已退 / 可退从记录派生，不落库（ADR-0014）
  total_amount     numeric(14,2) NOT NULL DEFAULT 0,
  discount_amount  numeric(14,2) NOT NULL DEFAULT 0,
  version          integer     NOT NULL DEFAULT 0,
  deleted_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_orders_shop_no    UNIQUE (shop_id, order_no),      -- 店内唯一
  CONSTRAINT uq_orders_public_id  UNIQUE (public_id),               -- 全局唯一
  CONSTRAINT uq_orders_shop_id_id UNIQUE (shop_id, id),             -- 供复合外键引用
  CONSTRAINT ck_orders_status     CHECK (status IN
              ('draft','open','pending_payment','paid','completed',
               'cancelled','refunded','partially_refunded')),       -- 无 refunding（ADR-0014）
  CONSTRAINT ck_orders_amounts    CHECK (total_amount >= 0 AND discount_amount >= 0)
);
CREATE INDEX idx_orders_shop_created ON orders (shop_id, created_at DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX idx_orders_shop_status_created ON orders (shop_id, status, created_at DESC);

CREATE TABLE order_lines (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id              bigint      NOT NULL,
  order_id             bigint      NOT NULL,
  line_no              integer     NOT NULL,
  catalog_item_id      bigint      NOT NULL REFERENCES catalog_items(id),
  item_name_snapshot   text        NOT NULL,
  quantity             numeric(14,3) NOT NULL,
  unit_price           numeric(14,2) NOT NULL,
  amount               numeric(14,2) NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_order_lines_shop_order_line UNIQUE (shop_id, order_id, line_no),
  -- 复合外键：明细的 shop_id 必须与单据一致，数据库层兜住隔离约束
  CONSTRAINT fk_order_lines_order FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_order_lines_amount CHECK (amount >= 0)
);
CREATE INDEX idx_order_lines_shop_order ON order_lines (shop_id, order_id);

-- 收款：kind 区分销售 / 充值 / 其他（ADR-0013）；order_id 仅 sale 必填
CREATE TABLE payments (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  kind             text        NOT NULL DEFAULT 'sale',
  order_id         bigint,                       -- 由 NOT NULL 改为可空
  member_id        bigint,                       -- topup 必填（CHECK 约束保证）
  shift_id         bigint,                       -- 收款动作发生时所在班次，只持 ID
  operator_id      bigint      NOT NULL,          -- 收款操作人（登录账号），只持 ID
  status           text        NOT NULL DEFAULT 'pending',
  amount           numeric(14,2) NOT NULL,
  idempotency_key  text        NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_payments_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT uq_payments_idem       UNIQUE (shop_id, idempotency_key),
  CONSTRAINT fk_payments_order      FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_payments_status     CHECK (status IN ('pending','succeeded','failed')),
  CONSTRAINT ck_payments_kind       CHECK (kind IN ('sale','topup','other')),
  CONSTRAINT ck_payments_kind_ref   CHECK (
    (kind = 'sale'  AND order_id IS NOT NULL) OR
    (kind = 'topup' AND order_id IS NULL AND member_id IS NOT NULL) OR
    (kind = 'other' AND order_id IS NULL)
  )
);
CREATE INDEX idx_payments_shop_created  ON payments (shop_id, created_at DESC);
CREATE INDEX idx_payments_shop_shift    ON payments (shop_id, shift_id, created_at DESC);
CREATE INDEX idx_payments_shop_operator ON payments (shop_id, operator_id, created_at DESC);
CREATE INDEX idx_payments_shop_order    ON payments (shop_id, order_id);

-- 组合支付：一次收款可按渠道拆多笔，合计等于 payments.amount
CREATE TABLE payment_entries (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint      NOT NULL,
  payment_id  bigint      NOT NULL,
  seq         integer     NOT NULL,
  method      text        NOT NULL,
  amount      numeric(14,2) NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_payment_entries_shop_payment_seq UNIQUE (shop_id, payment_id, seq),
  CONSTRAINT fk_payment_entries_payment FOREIGN KEY (shop_id, payment_id)
              REFERENCES payments (shop_id, id),
  CONSTRAINT ck_payment_entries_method CHECK (method IN ('cash','balance','card')),  -- 券是折扣不在此列（ADR-0016）
  CONSTRAINT ck_payment_entries_amount CHECK (amount >= 0)
);
CREATE INDEX idx_payment_entries_shop_payment ON payment_entries (shop_id, payment_id);

-- 整单折扣 / 抹零：分摊到行由计价纯函数完成，表本身挂订单级
CREATE TABLE price_adjustments (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint      NOT NULL,
  order_id    bigint      NOT NULL,
  seq         integer     NOT NULL,
  amount      numeric(14,2) NOT NULL,
  reason      text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_price_adjustments_shop_order_seq UNIQUE (shop_id, order_id, seq),
  CONSTRAINT fk_price_adjustments_order FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_price_adjustments_amount CHECK (amount >= 0)
);
CREATE INDEX idx_price_adjustments_shop_order ON price_adjustments (shop_id, order_id);

-- 退款：shift_id / operator_id 是退款动作本身的归属，不继承订单（ADR-0013）
-- kind 对齐原收款（sale/topup）、order_id 可空、topup 填 member_id（ADR-0016）；
-- 禁止跨店退款：shop_id 强绑原支付与会员本店账；充值退款余额不足整笔拒绝。
CREATE TABLE refunds (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  refund_no        text        NOT NULL,
  kind             text        NOT NULL,             -- sale / topup，等于原 payments.kind
  payment_id       bigint      NOT NULL,             -- 必引用原收款（同店复合外键）
  order_id         bigint,                           -- sale 必填；topup 为空（可空）
  member_id        bigint,                           -- topup 必填：退给哪个会员
  shift_id         bigint,                           -- 退款动作发生时的班次（不继承订单）
  operator_id      bigint      NOT NULL,             -- 退款操作人（登录账号）
  amount           numeric(14,2) NOT NULL,
  status           text        NOT NULL DEFAULT 'pending',
  reason           text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_refunds_shop_no     UNIQUE (shop_id, refund_no),
  CONSTRAINT uq_refunds_public_id   UNIQUE (public_id),
  CONSTRAINT uq_refunds_shop_id_id  UNIQUE (shop_id, id),
  CONSTRAINT fk_refunds_payment     FOREIGN KEY (shop_id, payment_id)
              REFERENCES payments (shop_id, id),
  CONSTRAINT fk_refunds_order       FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_refunds_status      CHECK (status IN ('pending','succeeded','failed')),
  CONSTRAINT ck_refunds_amount      CHECK (amount > 0),
  CONSTRAINT ck_refunds_kind        CHECK (kind IN ('sale','topup')),
  CONSTRAINT ck_refunds_kind_ref    CHECK (
    (kind = 'sale'  AND order_id IS NOT NULL AND member_id IS NULL) OR
    (kind = 'topup' AND order_id IS NULL AND member_id IS NOT NULL)
  )
);
CREATE INDEX idx_refunds_shop_created  ON refunds (shop_id, created_at DESC);
CREATE INDEX idx_refunds_shop_shift    ON refunds (shop_id, shift_id, created_at DESC);
CREATE INDEX idx_refunds_shop_operator ON refunds (shop_id, operator_id, created_at DESC);
CREATE INDEX idx_refunds_shop_payment  ON refunds (shop_id, payment_id);
CREATE INDEX idx_refunds_shop_member   ON refunds (shop_id, member_id, created_at DESC);  -- 会员维度可退额聚合（ADR-0016）

-- 退款逐渠道明细（ADR-0014）：请求显式携带，系统只校验不自动分摊。
-- SUM(refund_entries.amount) 必须等于 refunds.amount；单渠道累计退款 ≤ 该渠道原支付额。
CREATE TABLE refund_entries (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint        NOT NULL,
  refund_id   bigint        NOT NULL,
  seq         integer       NOT NULL,
  method      text          NOT NULL,
  amount      numeric(14,2) NOT NULL,
  created_at  timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT uq_refund_entries_shop_refund_seq UNIQUE (shop_id, refund_id, seq),
  CONSTRAINT fk_refund_entries_refund FOREIGN KEY (shop_id, refund_id)
              REFERENCES refunds (shop_id, id),
  CONSTRAINT ck_refund_entries_method CHECK (method IN ('cash','balance','card')),  -- 同上，只退实际支付部分
  CONSTRAINT ck_refund_entries_amount CHECK (amount > 0)
);
CREATE INDEX idx_refund_entries_shop_refund ON refund_entries (shop_id, refund_id);

CREATE TABLE shifts (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  shift_no    text        NOT NULL,
  status      text        NOT NULL DEFAULT 'open',
  opened_at   timestamptz NOT NULL DEFAULT now(),
  closed_at   timestamptz,
  CONSTRAINT uq_shifts_shop_no UNIQUE (shop_id, shift_no)
);
-- 部分唯一索引：一家店同时最多只有一个 open 班次
CREATE UNIQUE INDEX uq_shifts_one_open_per_shop ON shifts (shop_id) WHERE status = 'open';

-- 平台表示例 DDL 与已落地的 migrations/00001 严格对齐（ADR-0014）；改示例、不改迁移。
CREATE TABLE idempotency_keys (
  scope           text        NOT NULL,
  key             text        NOT NULL,
  request_hash    bytea       NOT NULL,
  state           text        NOT NULL DEFAULT 'in_progress',
  response_status integer,
  response_body   jsonb,
  expires_at      timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
  created_at      timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,
  PRIMARY KEY (scope, key),
  CONSTRAINT ck_idem_state CHECK (state IN ('in_progress','succeeded','failed'))
);
CREATE INDEX idx_idem_expiry ON idempotency_keys (expires_at) WHERE state <> 'succeeded';

CREATE TABLE outbox_events (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id        bigint,
  aggregate_type text        NOT NULL,
  aggregate_id   text        NOT NULL,
  event_type     text        NOT NULL,
  payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
  attempts       integer     NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  dispatched_at  timestamptz
);
CREATE INDEX idx_outbox_pending ON outbox_events (created_at) WHERE dispatched_at IS NULL;

CREATE TABLE daily_shop_summary (
  shop_id        bigint      NOT NULL REFERENCES shops(id),
  -- business_date 由 UTC+8 + 门店营业日切点推导，是全系统唯一口径
  business_date  date        NOT NULL,
  order_count    integer     NOT NULL DEFAULT 0,
  gross_amount   numeric(14,2) NOT NULL DEFAULT 0,
  refund_amount  numeric(14,2) NOT NULL DEFAULT 0,
  net_amount     numeric(14,2) NOT NULL DEFAULT 0,
  recomputed_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (shop_id, business_date)
);

-- +goose Down
-- 按依赖逆序 DROP；此处省略，真实迁移文件必须完整给出
```

**为什么单号这样设计**（ADR-0014）：单号由数据库序列提供序号 + `(shop_id, order_no)` 唯一约束双保险，杜绝「应用层用日期 + 门店 ID + 计数器拼字符串」在并发下重复且无数据库兜底的问题。日期部分改由 `business_date(now(), shop_id)` 提供，与日报 / 班次 / 汇总同口径——门店设早 6 点营业日切点时，凌晨 3 点的单子单号日期归前一天，与 business_date 一致；`order_no` 无字面 DEFAULT，`INSERT` 时必须显式调用 `next_order_no(shop_id)`，遗漏将触发 `NOT NULL` 约束错误（宁可显式失败，不可口径分裂）。

**业务日与单号函数**（PG 18.7 语法示意，供订单域首批迁移落地）：

```sql
-- 业务日：基准 UTC+8 + 门店 shop_settings 的营业日切点偏移（默认 00:00，可为负数表示前移）。
-- 只可配切点偏移，不接受门店覆盖基准时区（ADR-0004）；不依赖 session 时区。
CREATE FUNCTION business_date(p_at timestamptz, p_shop_id bigint) RETURNS date
LANGUAGE sql STABLE AS $$
  SELECT ((p_at AT TIME ZONE 'Asia/Shanghai')
          - COALESCE(
              (SELECT (value->>'cutoff_minutes')::integer
               FROM shop_settings
               WHERE shop_id = p_shop_id AND key = 'business_day'), 0) * interval '1 minute'
         )::date;
$$;

-- 单号：日期部分取 business_date，序号由 order_no_seq 提供（并发唯一）。
-- INSERT orders 时显式调用；号段唯一性仍由序列保证，切点只影响日期部分。
CREATE FUNCTION next_order_no(p_shop_id bigint) RETURNS text
LANGUAGE sql AS $$
  SELECT 'SO' || to_char(business_date(now(), p_shop_id), 'YYYYMMDD') || '-'
         || lpad(nextval('order_no_seq')::text, 10, '0');
$$;
```

> 连接初始化统一 `SET TIME ZONE 'UTC'`；所有时间列保持 `timestamptz`，面向业务日的计算一律显式 `AT TIME ZONE 'Asia/Shanghai'`，前端拿到 UTC 时间按展示地时区渲染（ADR-0014）。

## 4. 用数据库约束兜住隔离

应用层显式写 `shop_id` 是第一道防线，但漏写不会报错。增加三道结构性防线：

| 防线 | 做法 |
|---|---|
| 复合外键 | 子表 `(shop_id, parent_id)` 引用父表 `(shop_id, id)`，物理上无法把 A 店明细挂到 B 店单据 |
| 复合唯一约束 | 所有唯一键以 `shop_id` 为前导，并写明「店内唯一」还是「全局唯一」 |
| 索引前导列 | 所有业务表索引以 `shop_id` 开头，让「漏加门店条件」在 `EXPLAIN` 里一眼可见 |

**不使用 RLS**：按会话变量隐式过滤会让隔离条件从 SQL 与代码评审中消失，与 ADR-0003 第 7 条直接冲突。

## 5. 索引策略

| 查询场景 | 索引 | 备注 |
|---|---|---|
| 单店单据列表 | `(shop_id, created_at DESC)` 部分索引 `WHERE deleted_at IS NULL` | 分页用 keyset，不用深 `OFFSET` |
| 单店按状态筛选 | `(shop_id, status, created_at DESC)` | 状态选择性低，靠前导 `shop_id` 收敛 |
| 单据明细 | `(shop_id, order_id)` | 复合外键自带 |
| 日报 / 经营分析 | `daily_shop_summary (shop_id, business_date)` 主键；明细回查 `(shop_id, created_at)` | 报表先走汇总表，不走大表实时扫；`business_date` 按 UTC+8 口径，跨营业日切点的记录归入同一天 |
| 会员余额流水 | `(shop_id, member_id, created_at DESC)` | 按店账，不跨店混算 |
| 库存流水 | `(shop_id, catalog_item_id, created_at DESC)` | 同上 |
| 待派发事件 | `outbox_events (created_at) WHERE dispatched_at IS NULL` | 部分索引，随投递完成自动缩小 |
| 幂等清理 | `idempotency_keys (expires_at)` | 定期清理过期键 |
| 审计清理 | `audit_logs (created_at)` | 定时按批删除超期记录（默认 30 天，ADR-0007）；截止点按 UTC+8 自然日计算，配合表膨胀 / `VACUUM` 监控，口径见 [08](08-audit-retention.md) §2 |

**验收要求**：每个列表与报表查询在集成测试中跑 `EXPLAIN (ANALYZE, BUFFERS)` 并存档；新增查询若在大表上出现 `Seq Scan`，PR 不予合并。索引一律可回滚，`CREATE INDEX CONCURRENTLY` 的迁移必须标记 `-- +goose NO TRANSACTION`（并发建索引不能在事务块内执行）。

## 6. 迁移与版本管理

1. 迁移文件放 `migrations/`，命名 `<version>_<name>.sql`，由 **goose** 执行；**atlas** 在 CI 中比对期望 schema 与实际 schema，检测漂移。
2. 每个迁移必须有可执行的 `Down`；禁止手工改库，禁止在应用启动时隐式建表。**迁移只由 Go `cmd/job migrate` 执行**（ADR-0008，见 [07](07-deployment-and-operations.md) §2/§3），`api` / `worker` 启动一律不跑迁移或隐式建表；`cmd/job` 自身也用 advisory lock 防重；`/ready` 用构建时注入的 `EXPECTED_SCHEMA_VERSION` 校验 `schema_migrations`，版本不符返回 503 不接流量。
3. 破坏性变更走 **expand → backfill → contract** 三步：先加可空列/新表 → 回填与双读 → 再删旧列，任何一步都可单独回滚。
4. 加唯一约束前必须先在预发布环境跑重复数据检查，否则迁移会中途失败。
5. 大表加索引用 `CONCURRENTLY`，失败会留下 `INVALID` 索引，需有清理脚本。

## 7. 容量与保留策略（ADR-0004：业务数据暂不归档；ADR-0007：审计日志保留 30 天）

- **业务数据在线全量保留**：单据、支付、余额与库存流水不设保留年限、不导出历史归档、不做冷热分离，也不提供「归档数据专用查询」路径。原 OQ-02 的「在线 24 个月 + 归档」假设已废弃。**`audit_logs` 不在此列**：它是基础设施 / 运行期数据表，按 ADR-0007 单独设定保留窗口。
- **审计日志默认保留 30 天、可配置、定时删除**（ADR-0007）：`AUDIT_LOG_RETENTION_DAYS` 全局生效、启动时校验；worker 按 `created_at` 与 UTC+8 日界计算截止点，分批 `DELETE`、可重入、失败重试并告警。业务请求对审计记录仍**只追加、不可更新删除**，唯一允许的删除是清理任务；不导出归档、不建月度分区、不提供超期归档查询（`03` 第 8 节给出「超出保留期」的明确语义）。清理动作记结构化日志与指标，**不写回 `audit_logs`** 以免自我循环。默认规则（30 天 / `1..3650` / 不允许关闭 / 变更需重启）、UTC+8 自然日窗口、单批 5000 行、超期错误契约、运行痕迹与 `VACUUM` 策略见 [08 审计日志保留与清理](08-audit-retention.md)（ADR-0008 / NQ-10–13）。
- **不做分区**：PG 声明式分区要求唯一约束包含分区键，会与「`(shop_id, order_no)` 店内唯一」及复合外键设计冲突；在没有归档需求时，分区只增加迁移与维护复杂度。审计日志的定期删除靠 `(created_at)` 索引 + 分批提交解决，不为它单独引入分区。容量靠索引、预聚合、keyset 分页与流式导出扛住。
- **运行期数据仍要清理**（这不是业务归档）：`idempotency_keys` 过期键（24 h）、已投递且超期的 `outbox_events`、导出任务的临时文件、**`audit_logs` 超期记录（30 天，可配）**；未投递事件永不删除。**业务单据与流水不参与任何定期删除**。
- **单主库读写**（ADR-0007）：支持编排平台但暂不读写分离、不引入只读副本；重报表走 `daily_shop_summary` 与流式导出。`max_conns` 按「副本数 × 每实例上限 + 运维预留 ≤ PG `max_connections`」推导，禁止沿用驱动默认值；**Docker 2+2 基线为 `api` 20×2 + `worker` 10×2 + `job` 5 + 预留 15 = 80 ≤ 100**（ADR-0008，见 [07](07-deployment-and-operations.md) §4）。
- **监控与复审**：表/索引体积、大表 `Seq Scan` 比例、慢查询数、库体积、**审计清理删除行数与失败次数**是必配指标；容量阈值已由 ADR-0008 / [07](07-deployment-and-operations.md) §5 定为默认基线，2 副本压测后校准并写回运维清单。达到阈值时**另立 ADR** 再评估分区或归档，不在代码里悄悄加保留窗口。
- 汇总表 `daily_shop_summary` 是**派生数据**：允许从流水全量重算，重算结果必须与源一致；这是报表可信度的基础，与归档无关。
- **不设范围外表**：不做离线收银与打印（ADR-0004），因此不存在本地待同步单据、补传队列、同步水位、打印任务/模板表；`export_jobs` 只服务于文件导出。不设门店组（ADR-0005），因此也不存在 `shop_groups` / `shop_group_members` 之类的分组表、成员关系表或 `shops` 上的分组 / 层级列。按 ADR-0006，当前门店是请求期上下文，因此也不存在 `current_shop` 表、`users.current_shop_id` 列或用户偏好表；一人多店完全由 `user_shops` 表达。

## 8. 数据层测试

| 层次 | 内容 |
|---|---|
| 迁移测试 | 在真实 PG 18.7 容器里跑 `up` → `down` → `up`，断言无残留对象 |
| 约束测试 | 并发插入相同单号断言唯一约束生效；插入跨店明细断言复合外键拒绝；断言 schema 中不存在 `shop_group*` 表 / 列，`shops` 无分组或层级列（ADR-0005）；断言 `users` / `shops` 无 `shop_id` / `current_shop_id` / `owner_user_id` 列、不存在 `current_shop` 或偏好表，且 `user_shops` 支持「一人多店 / 一店多人」双向查询（ADR-0006） |
| 精度测试 | 大额与分摊场景断言 `numeric` 无舍入漂移；断言金额列不存在 `currency` 字段与浮点类型 |
| 业务日测试 | 在 UTC+8 日界（UTC 16:00）前后各插入一条，断言归入正确的 `business_date`，且单号日期与之一致（现在单号走同一 `business_date()`，含门店切点，ADR-0014）；另测门店设切点后跨切点单号日期与日报口径一致 |
| 隔离测试 | 用两个门店、两个用户交叉请求，断言任一响应不含对方门店数据 |
| 计划测试 | 关键查询 `EXPLAIN` 存档，断言不出现大表 `Seq Scan` |
| 保留测试 | 插入跨越 30 天日界（UTC+8）的审计记录，断言清理任务只删超期行、不触碰任何业务单据与流水，且业务请求无法更新 / 删除审计记录；`audit_log_cleanup_runs` 留下成功痕迹、双 worker 副本不重复执行（ADR-0007 / 0008，见 [08](08-audit-retention.md) §6） |

集成测试用**真实 PG 18.7**，不 mock 数据访问层（ADR-0002 阻断项 ⑨）。起库支持两种并列模式，按开发者环境任选：① `testcontainers-go` 由测试进程自起容器（本机有 Go + Unix socket / CI）；② compose 组网（`docker compose up -d db` + `TEST_DATABASE_URL`，适合全 Docker / Windows Docker Desktop）。两者共用同一套用例，差异仅在如何取得连接串——测试代码统一从辅助函数读取（有环境变量则直连，否则回退 testcontainers），详见 [ADR-0002](../decisions/0002-go-backend-stack.md)「集成测试的两种模式」。

---

下一册：[03 接口设计](03-api-design.md)。

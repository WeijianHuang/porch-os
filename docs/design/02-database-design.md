# 前期系统架构详细设计 · 02 数据库设计

> 上游：[01 领域模型](01-domain-model.md)（聚合与不变量）、[ADR-0001](../decisions/0001-postgresql-18.md)（PostgreSQL 18.7）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（只有 `shop_id`）、[ADR-0004](../decisions/0004-scope-baseline.md)（单币种、东八区、暂不归档）。
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
| 时间 | `timestamptz`，UTC 存储；区间一律半开 `[start, end)` | 避免 `BETWEEN` 边界重复计数 |
| 业务日 | `(occurred_at AT TIME ZONE 'Asia/Shanghai')::date` + 门店营业日切点偏移 | 全系统固定 UTC+8，**不设门店级 `timezone` 列**、不接受客户端时区参数（ADR-0004）；日报、班次、单号日期、导出共用 |
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
| shop | `shops` | A | 门店主表，`id` 即 `shop_id` | `code` 全局唯一 |
| shop | `shop_groups` | A | 店群标签 | `code` 全局唯一 |
| shop | `shop_group_members` | C | 门店 ↔ 店群 | `(shop_group_id, shop_id)` |
| shop | `shop_settings` | B | 门店参数（营业日切点等）；基准时区全局固定，不按店配置 | `(shop_id, key)` |
| identity | `users` | A | 账号 | `login` 全局唯一 |
| identity | `roles` / `permissions` | A | 角色与权限点 | `code` 全局唯一 |
| identity | `role_permissions` | C | 角色 ↔ 权限 | `(role_id, permission_id)` |
| identity | `user_roles` | C | 用户 ↔ 角色 | `(user_id, role_id)` |
| identity | **`user_shops`** | C | **用户 ↔ 可见门店集合**（授权的唯一来源） | `(user_id, shop_id)` |
| identity | `refresh_tokens` | A | 刷新令牌（存哈希） | `token_hash` 全局唯一 |
| identity | `audit_logs` | P | 追加写、不可改；与门店相关的事件带 `shop_id`，账号级事件为 `NULL`；属基础设施表，不参与业务隔离判定 | — |
| catalog | `catalog_categories` | A | 目录分类 | `(parent_id, code)` |
| catalog | `catalog_items` | A | 商品/服务目录 | `sku` 全局唯一 |
| catalog | `shop_catalog_overrides` | B | 按店价格、上下架 | `(shop_id, catalog_item_id)` |
| member | `members` | A | 会员档案 | `phone` 全局唯一 |
| member | `member_balances` | B | 按店余额 | `(shop_id, member_id)` |
| member | `balance_ledger` | B | 余额变动流水（只追加） | `(shop_id, public_id)` |
| member | `member_visits` | B | 到店记录 | `(shop_id, public_id)` |
| order | `shifts` | B | 收银班次 | `(shop_id, shift_no)`；部分唯一：一店最多一个 `open` |
| order | `orders` | B | 单据 | **`(shop_id, order_no)` 店内唯一** |
| order | `order_lines` | B | 单据明细（价格快照） | `(shop_id, order_id, line_no)` |
| order | `price_adjustments` | B | 整单折扣/抹零（已分摊到行） | `(shop_id, order_id, seq)` |
| order | `payments` | B | 一次收款 | `(shop_id, idempotency_key)` |
| order | `payment_entries` | B | 组合支付的一笔 | `(shop_id, payment_id, seq)` |
| order | `refunds` | B | 退款单 | `(shop_id, refund_no)` |
| inventory | `inventory_balances` | B | 按店库存 | `(shop_id, catalog_item_id)` |
| inventory | `stock_movements` | B | 出入库流水（只追加） | `(shop_id, public_id)` |
| inventory | `stock_transfers` | B | 跨店调拨单（显式动作） | `(shop_id, transfer_no)` |
| reporting | `daily_shop_summary` | D | 日汇总，可重算 | `(shop_id, business_date)` |
| reporting | `export_jobs` | D | 导出任务 | `(shop_id, public_id)` |
| platform | `idempotency_keys` | P | 幂等记录 | `(scope, key)` |
| platform | `outbox_events` | P | 事务内事件，worker 派发 | `id` |
| platform | `dead_letters` | P | 投递失败终态 | `id` |
| platform | `schema_migrations` | P | goose 版本记录 | `version` |

> `user_shops` 含 `shop_id` 列，但它是**授权关系**（C 类），不是把业务数据挂到上层实体——ADR-0003 禁止的是 tenant/org/company 这类上层归属维度。

## 3. 核心 DDL（PG 18.7 语法示例）

以下示例刻意包含「复合外键把 `shop_id` 钉死」这一关键手法：明细无法通过外键挂到别家店的单据上。

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
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_catalog_items_sku UNIQUE (sku),
  CONSTRAINT ck_catalog_items_type CHECK (type IN ('product','service'))
);

CREATE TABLE user_shops (
  user_id    bigint      NOT NULL REFERENCES users(id),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, shop_id)
);
CREATE INDEX idx_user_shops_shop ON user_shops (shop_id, user_id);

-- 单号由数据库序列生成，杜绝应用层拼接导致并发重复
CREATE SEQUENCE order_no_seq AS bigint;

CREATE TABLE orders (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL REFERENCES shops(id),
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  -- 单号日期按 UTC+8，与业务日口径一致；不依赖数据库会话时区
  order_no         text        NOT NULL DEFAULT
                     ('SO' || to_char(now() AT TIME ZONE 'Asia/Shanghai', 'YYYYMMDD') || '-' ||
                      lpad(nextval('order_no_seq')::text, 10, '0')),
  status           text        NOT NULL DEFAULT 'draft',
  member_id        bigint      REFERENCES members(id),
  shift_id         bigint,
  operator_id      bigint      NOT NULL,
  -- 单一币种 CNY（ADR-0004）：金额列不带 currency，不做币种校验与换算
  total_amount     numeric(14,2) NOT NULL DEFAULT 0,
  discount_amount  numeric(14,2) NOT NULL DEFAULT 0,
  paid_amount      numeric(14,2) NOT NULL DEFAULT 0,
  version          integer     NOT NULL DEFAULT 0,
  deleted_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_orders_shop_no    UNIQUE (shop_id, order_no),      -- 店内唯一
  CONSTRAINT uq_orders_public_id  UNIQUE (public_id),               -- 全局唯一
  CONSTRAINT uq_orders_shop_id_id UNIQUE (shop_id, id),             -- 供复合外键引用
  CONSTRAINT ck_orders_status     CHECK (status IN
              ('draft','open','pending_payment','paid','completed',
               'cancelled','refunding','refunded','partially_refunded')),
  CONSTRAINT ck_orders_amounts    CHECK (total_amount >= 0 AND paid_amount >= 0
                                         AND discount_amount >= 0)
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

CREATE TABLE payments (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  order_id         bigint      NOT NULL,
  status           text        NOT NULL DEFAULT 'pending',
  amount           numeric(14,2) NOT NULL,
  idempotency_key  text        NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_payments_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT uq_payments_idem       UNIQUE (shop_id, idempotency_key),
  CONSTRAINT fk_payments_order      FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_payments_status     CHECK (status IN ('pending','succeeded','failed'))
);
CREATE INDEX idx_payments_shop_created ON payments (shop_id, created_at DESC);

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

CREATE TABLE idempotency_keys (
  scope          text        NOT NULL,
  key            text        NOT NULL,
  request_hash   bytea       NOT NULL,
  status         text        NOT NULL DEFAULT 'in_progress',
  response_code  integer,
  response_body  jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz NOT NULL,
  PRIMARY KEY (scope, key)
);
CREATE INDEX idx_idem_expiry ON idempotency_keys (expires_at);

CREATE TABLE outbox_events (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id        bigint      NOT NULL,
  event_type     text        NOT NULL,
  aggregate_type text        NOT NULL,
  aggregate_id   bigint      NOT NULL,
  payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  published_at   timestamptz,
  attempts       integer     NOT NULL DEFAULT 0
);
CREATE INDEX idx_outbox_unpublished ON outbox_events (occurred_at, id)
  WHERE published_at IS NULL;

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

**为什么单号这样设计**：单号由数据库序列生成 + `(shop_id, order_no)` 唯一约束双保险。任何「应用层用日期 + 门店 ID + 计数器拼字符串」的做法，在并发下都会重复，且没有数据库兜底；这类缺陷一旦产生脏数据，事后补唯一约束会失败。日期部分显式写 `AT TIME ZONE 'Asia/Shanghai'`，与业务日一致，也不随部署环境的会话时区漂移。

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
| 待派发事件 | `outbox_events (occurred_at, id) WHERE published_at IS NULL` | 部分索引，随投递完成自动缩小 |
| 幂等清理 | `idempotency_keys (expires_at)` | 定期清理过期键 |

**验收要求**：每个列表与报表查询在集成测试中跑 `EXPLAIN (ANALYZE, BUFFERS)` 并存档；新增查询若在大表上出现 `Seq Scan`，PR 不予合并。索引一律可回滚，`CREATE INDEX CONCURRENTLY` 的迁移必须标记 `-- +goose NO TRANSACTION`（并发建索引不能在事务块内执行）。

## 6. 迁移与版本管理

1. 迁移文件放 `migrations/`，命名 `<version>_<name>.sql`，由 **goose** 执行；**atlas** 在 CI 中比对期望 schema 与实际 schema，检测漂移。
2. 每个迁移必须有可执行的 `Down`；禁止手工改库，禁止在应用启动时隐式建表。
3. 破坏性变更走 **expand → backfill → contract** 三步：先加可空列/新表 → 回填与双读 → 再删旧列，任何一步都可单独回滚。
4. 加唯一约束前必须先在预发布环境跑重复数据检查，否则迁移会中途失败。
5. 大表加索引用 `CONCURRENTLY`，失败会留下 `INVALID` 索引，需有清理脚本。

## 7. 容量与保留策略（ADR-0004：暂不归档）

- **业务数据在线全量保留**：单据、支付、余额与库存流水、审计日志不设保留年限、不导出历史归档、不做冷热分离，也不提供「归档数据专用查询」路径。原 OQ-02 的「在线 24 个月 + 归档」假设已废弃。
- **不做分区**：PG 声明式分区要求唯一约束包含分区键，会与「`(shop_id, order_no)` 店内唯一」及复合外键设计冲突；在没有归档需求时，分区只增加迁移与维护复杂度。容量靠索引、预聚合、keyset 分页与流式导出扛住。
- **运行期数据仍要清理**（这不是业务归档）：`idempotency_keys` 过期键（24 h）、已投递且超期的 `outbox_events`、导出任务的临时文件；未投递事件永不删除。业务单据与流水**不参与**任何定期删除。
- **监控与复审**：表/索引体积、大表 `Seq Scan` 比例、慢查询数、库体积是必配指标；容量阈值在 R1 压测后写入运维清单。达到阈值时**另立 ADR** 再评估分区或归档，不在代码里悄悄加保留窗口。
- 汇总表 `daily_shop_summary` 是**派生数据**：允许从流水全量重算，重算结果必须与源一致；这是报表可信度的基础，与归档无关。
- **不设范围外表**：不做离线收银与打印（ADR-0004），因此不存在本地待同步单据、补传队列、同步水位、打印任务/模板表；`export_jobs` 只服务于文件导出。

## 8. 数据层测试

| 层次 | 内容 |
|---|---|
| 迁移测试 | 在真实 PG 18.7 容器里跑 `up` → `down` → `up`，断言无残留对象 |
| 约束测试 | 并发插入相同单号断言唯一约束生效；插入跨店明细断言复合外键拒绝 |
| 精度测试 | 大额与分摊场景断言 `numeric` 无舍入漂移；断言金额列不存在 `currency` 字段与浮点类型 |
| 业务日测试 | 在 UTC+8 日界（UTC 16:00）前后各插入一条，断言归入正确的 `business_date`，且单号日期与之一致 |
| 隔离测试 | 用两个门店、两个用户交叉请求，断言任一响应不含对方门店数据 |
| 计划测试 | 关键查询 `EXPLAIN` 存档，断言不出现大表 `Seq Scan` |

集成测试用 `testcontainers-go` 起真实 PG，不 mock 数据访问层（ADR-0002 阻断项 ⑨）。

---

下一册：[03 接口设计](03-api-design.md)。

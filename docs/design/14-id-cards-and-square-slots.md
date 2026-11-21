# 14 · ID 卡持有与方形服务格子（ADR-0017）

本册只新增「卡片资源 + 看板格子展示」契约，不改变 ADR-0012 的判定链 / 联动 / 自动状态流转，也不改变 ADR-0015 的 8 桶状态配色。迁移 `00008_id_cards.sql`，schema 版本 **8**。全文只用通用术语。

## 1. 术语与隔离

| 通用术语 | 落点 | 隔离类别 |
|---|---|---|
| ID 卡 Card | `id_cards` | B（站点业务数据） |
| 持有 Holding | `card_holdings` | B |
| 持卡人 Holder | `staff`（只读引用） | B |

`id_cards` 与 `card_holdings` 都带 `shop_id`；所有端点挂在 `/api/v1/shops/{shopId}/...` 下并显式带 `X-Shop-Id`，缺失 `400 SHOP_CONTEXT_REQUIRED`，与路径不符 `403 PERMISSION_DENIED`（ADR-0006）。

## 2. 数据模型

```sql
CREATE TABLE id_cards (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  code       text        NOT NULL,              -- 店内卡号
  name       text        NOT NULL,              -- 卡名称
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_ic_public_id UNIQUE (public_id),
  CONSTRAINT uq_ic_shop_code UNIQUE (shop_id, code),
  CONSTRAINT uq_ic_shop_id   UNIQUE (shop_id, id),
  CONSTRAINT ck_ic_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_ic_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_ic_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_ic_shop_status ON id_cards (shop_id, status, code);

CREATE TABLE card_holdings (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id    bigint      NOT NULL,
  card_id    bigint      NOT NULL,
  staff_id   bigint      NOT NULL,
  held_from  timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  release_reason text,
  CONSTRAINT fk_ch_card FOREIGN KEY (shop_id, card_id)
    REFERENCES id_cards (shop_id, id),
  CONSTRAINT fk_ch_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id)
);
-- PostgreSQL 不允许表级 UNIQUE 约束带 WHERE，故用 partial unique index 表达
-- 「一卡一人 / 一人一卡」，并发领取由这两个索引兜底。
CREATE UNIQUE INDEX uq_ch_active_card ON card_holdings (shop_id, card_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX uq_ch_active_staff ON card_holdings (shop_id, staff_id) WHERE released_at IS NULL;
CREATE INDEX idx_ch_shop_staff ON card_holdings (shop_id, staff_id, held_from DESC);
```

- 一卡一人、一人一卡都由 partial unique index 在 DB 层保证；并发领取靠唯一约束兜底，不必引入额外 advisory lock。
- `card_holdings` 保留完整历史（多人先后持有），`released_at IS NULL` 即当前持有。
- 无 `duration_minutes` 列；剩余分钟来自尚未落地的订单域，本期恒 `null`。

## 3. 端点清单（门店作用域）

| Method | Path | 权限 | 说明 |
|---|---|---|---|
| GET | `/shops/{shopId}/cards` | `card.read` | 卡片列表 + 当前持有人（含状态筛选） |
| POST | `/shops/{shopId}/cards` | `card.write` | 建档（卡号 + 名称） |
| PATCH | `/shops/{shopId}/cards/{cardId}` | `card.write` | 改名 / 生效失效，带 `version` |
| POST | `/shops/{shopId}/cards/{cardId}/hold` | `card.write` | 领取：`{staff_id, reason?}` |
| POST | `/shops/{shopId}/cards/{cardId}/release` | `card.write` | 释放：`{reason?}` |

响应投影（扁平，无实体图）：

```jsonc
// GET /cards
{ "data": [{
  "public_id": "...", "code": "C-001", "name": "一号卡", "status": "active",
  "version": 0,
  "holder": { "public_id": "...", "display_name": "张三", "held_from": "..." } | null
}]}

// GET /scheduling/board — 每位成员新增
"id_card": { "public_id": "...", "code": "C-001", "name": "一号卡" } | null,
"service_remaining_minutes": null

// GET /staff — StaffSummary 新增同样两个字段
```

`GET /cards` 一次查卡、一次查当前持有（按 `shop_id` + `card_id IN (...)`），纯函数折叠，不逐卡发请求。

## 4. 状态与错误码

- 卡状态：`active` / `inactive`；领取只允许 `active`。
- 释放不限卡状态；释放后 `released_at = now()`、`release_reason` 记录原因（可空）。
- 错误码（全部 400，除注明）：
  - `CARD_INACTIVE`（400）已失效卡被领取
  - `CARD_HELD`（409）卡已被他人持有
  - `STAFF_ALREADY_HOLDS_CARD`（409）该服务员已持卡
  - `CARD_IN_USE`（409）持有的卡被置为失效
  - `CARD_NOT_FOUND`（404）、`STAFF_NOT_FOUND`（404）、`VERSION_CONFLICT`（409）
- 写端点幂等：命中 `Idempotency-Key` 返回首次结果，不重复写流水 / 审计。

并发领取流程：

```
POST .../cards/{cardId}/hold  (Idempotency-Key)
  ├─ 幂等键命中 → 返回首次结果
  ├─ BEGIN
  ├─ SELECT id_cards ... FOR UPDATE          -- 锁卡档案，串行化同一张卡
  ├─ 校验 status='active'
  ├─ INSERT card_holdings (...)              -- partial unique 兜底一人一卡
  ├─ append audit_logs(target_type='card')
  └─ COMMIT；唯一约束冲突 → 409 对应错误码
```

## 5. 前端约定（porch-console）

- `/board` 位次卡、`/queues` 矩阵卡：`aspect-ratio: 1 / 1`，四行内容（姓名 / 状态 pill / 剩余分钟 / ID 卡名），状态色沿用 `bucketOf(state)` 的 8 桶 CSS 变量。
- 剩余分钟为 `null` 时显示「—」并加 `title="服务时长来自订单，暂未接入"`，**不显示 0、不做本地倒计时**。
- 未持卡显示「未绑卡」灰字；持卡显示卡名称 + `code`。
- `/queues` 泳道卡与人员视图不改变形状，仅在 meta 区增加 ID 卡名称。
- 新增 `/cards` 页面：卡片列表（卡号 / 名称 / 状态 / 持卡人）、建档、改名、生效失效、领取 / 释放；操作入口同时挂到服务人员卡片的点击菜单（复用 `StaffActionProvider` 的菜单契约扩展）。
- 权限：`card.write` 才显示建档 / 领取 / 释放等写操作。

## 6. 明确不做

- 不校验「开单必须持卡」，不在派单时自动绑卡（派单与卡片是两条独立流程）。
- 不做时长列、不做订单读取、不做倒计时。
- 不做卡片组 / 类型 / 押金 / 挂失 / 补卡 / 跨店转移。
- 不做卡片分页与搜索（与队列同级量级，前端本地过滤即可）。

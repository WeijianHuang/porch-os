# 17 · 班次模板与一人一班

本分册是 [ADR-0025](../decisions/0025-staff-shift-templates.md) 的设计落地。班次模板是门店每天复用的时间定义（早 / 中 / 晚等），与订单域 `shifts` 的营业班次实例（一次开班事件、全店同时最多一个 open）是**两个概念、两张表、两套端点**，互不读写。

## 1. 领域模型

- `ShiftTemplate`（店内 B 类）：`public_id`、`shop_id`、`name`（店内唯一）、`sort_order`、`start_time`/`end_time`（墙上时间 `HH:MM`）、`check_in_lead_minutes`（`0..1440`，默认 0）、`next_day_flag`（布尔，默认 false）、`version`。
- `Staff` 增量 `shift_template_id`（可空标量外键），投影 `shift_template: {public_id,name,start_time,end_time} | null`。
- 基数：`shift_templates` 1:N `staff`；一名服务人员**至多**属于一个班次模板，重新分配即替换，可清空；一个班次模板可有多名成员。
- 不变量由数据库与事务共同保证：复合外键 `(shop_id, shift_template_id)` 禁止跨店；分配端点全量替换且写审计；删除模板先解绑成员。

## 2. 表与索引（迁移 `00014`，schema 13 → 14）

```sql
CREATE TABLE shift_templates (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  name        text        NOT NULL,
  sort_order  integer     NOT NULL DEFAULT 0,
  start_time  time        NOT NULL,
  end_time    time        NOT NULL,
  check_in_lead_minutes integer NOT NULL DEFAULT 0,
  next_day_flag boolean   NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  version     integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_st_public_id UNIQUE (public_id),
  CONSTRAINT uq_st_shop_name  UNIQUE (shop_id, name),
  CONSTRAINT uq_st_shop_id    UNIQUE (shop_id, id),
  CONSTRAINT ck_st_name  CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_st_lead  CHECK (check_in_lead_minutes BETWEEN 0 AND 1440)
);
CREATE INDEX idx_st_shop_order ON shift_templates (shop_id, sort_order, name);
ALTER TABLE staff
  ADD COLUMN shift_template_id bigint,
  ADD CONSTRAINT fk_staff_shift_template FOREIGN KEY (shop_id, shift_template_id)
    REFERENCES shift_templates (shop_id, id);
CREATE INDEX idx_staff_shop_shift_template ON staff (shop_id, shift_template_id)
  WHERE shift_template_id IS NOT NULL;
```

`down` 顺序：先删 `idx_staff_shop_shift_template`、约束与列，再删 `idx_st_shop_order` 与表。`EXPECTED_SCHEMA_VERSION` 由 13 升为 14。

## 3. 端点

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/api/v1/shops/{shopId}/shift-templates` | `queue.read` | 本店模板，按 `sort_order, name` |
| POST | `/shift-templates` | `queue.write` | `{name,start_time,end_time,check_in_lead_minutes?,next_day_flag?,sort_order?}` |
| PATCH | `/shift-templates/{templateId}` | `queue.write` | 同字段增量 + `version` |
| DELETE | `/shift-templates/{templateId}` | `queue.write` | 同事务解绑成员后删除 |
| PUT | `/shift-templates/{templateId}/staff` | `queue.write` | `{staff_public_ids:string[],version}` 全量替换 |
| GET | `/staff`、`/staff/{staffId}` | `queue.read` | 增量返回 `shift_template` |

所有单店端点要求 `X-Shop-Id`；写操作要求 `Idempotency-Key`；错误码 `SHIFT_TEMPLATE_NOT_FOUND`（404）、`VERSION_CONFLICT`（409）、`CONFLICT`（重名 409）、`INVALID_ARGUMENT`（时间 / 分钟数 400）。

## 4. 流程

1. 新建：校验 `HH:MM` 与分钟范围 → 插入 → 写 `audit_logs`（`target_type='shift_template'`）。
2. 编辑：取现记录 → 合并增量 → 带 `version` 更新 → 审计。
3. 分配：解析全量服务人员 public_id（必须本店）→ 校验模板 `version` → 对本班次旧成员置空 → 对新成员赋值（自动覆盖其旧班次）→ 一条审计含前后成员列表。
4. 删除：记录当前成员 → 置空 `staff.shift_template_id` → 删除模板 → 一条审计含被解绑成员。

## 5. 边界

不做打卡流水、迟到判定、头牌 / 排位 / 绩效、排班日历、自动生成营业班次实例、班次级权限、跨店复用。时间统一走 UTC+8 业务日基线（ADR-0004），模板 `time` 列只是墙上时间，不引入门店时区字段。前端页面与契约见 `porch-console` 前端 ADR-0009。

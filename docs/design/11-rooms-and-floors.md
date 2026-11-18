# 前期系统架构详细设计 · 11 房间与楼层

> 上游：[ADR-0002](../decisions/0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](../decisions/0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0006](../decisions/0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0008](../decisions/0008-design-freeze-and-nq-resolutions.md)（设计冻结）、[ADR-0011](../decisions/0011-rooms-and-floors.md)（第三次受控扩展：房间与楼层）。本册与 `00`–`10` 同为契约基线。

## 1. 需求与范围

门店可维护多个房间；每个房间归属单一门店，并带一个**楼层**属性用于区分与分组；房间有房间号（门店内唯一）与名称；房间可启停。楼层是房间上的标量属性，不是实体；按楼层分区展示由前端完成。

| 在范围内 | 不在范围内 |
|---|---|
| 房间定义（房间号 / 名称 / 楼层 / 启停）的增删改查 | `floors` 表与楼层 CRUD |
| 楼层作为房间整数属性（`-5..200`，可负表示地下） | 楼层自定义名称 / 平面图 / 排序权重 |
| 列表按 `floor, code` 升序返回，前端按楼层分组 | 房间占用 / 预订 / 清洁状态机 |
| 房间号门店内唯一、版本乐观锁、幂等写 | 房间与队列 / 服务人员 / 单据绑定 |
| 房间管理前端页面（按楼层分区的卡片） | 容纳人数 / 面积 / 设备属性、按楼层统计看板 |

## 2. 领域模型（概念层）

- **Room（房间）**：B 类门店实体。字段：`public_id`、`shop_id`、`floor`（整数，`-5..200`）、`code`（房间号，店内唯一）、`name`（房间名称，非空）、`status`（`active` / `inactive`）、`version`、`created_at`、`updated_at`。

不变量（领域纯函数可脱库单测，见 `internal/domain/room`）：

1. `floor` 在 `-5..200` 之间；
2. `code`、`name` 去空白后非空；
3. 同一门店内 `code` 不重复（数据库唯一约束兜底）；
4. `status` 仅 `active` / `inactive`；
5. 楼层不是实体：无 `floors` 表、无 `floor_id`，可见楼层由现有房间的 `floor` 值推导；
6. `PATCH` 至少改一个字段，`version` 必填且匹配，否则 `409 VERSION_CONFLICT`。

前端纯函数（`src/shop/roomGroup.ts`）：`groupByFloor(rooms)` 按 `floor` 降序分组、组内按 `code` 升序；`floorLabel(floor)` 仅做展示格式化（`1`→`1F`、`-1`→`B1`、`0`→`GF`），不回写后端。

## 3. 数据库设计

迁移 `00006_rooms.sql`（结构 + 权限点），`EXPECTED_SCHEMA_VERSION` 为 `6`。

```sql
CREATE TABLE rooms (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  floor      integer     NOT NULL,
  code       text        NOT NULL,
  name       text        NOT NULL,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_rooms_public_id UNIQUE (public_id),
  CONSTRAINT uq_rooms_shop_code UNIQUE (shop_id, code),
  CONSTRAINT ck_rooms_floor  CHECK (floor BETWEEN -5 AND 200),
  CONSTRAINT ck_rooms_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_rooms_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_rooms_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_rooms_shop_floor_code ON rooms (shop_id, floor, code);
```

- 读查询以 `shop_id` 前导命中索引；`ORDER BY floor ASC, code ASC` 直接命中 `idx_rooms_shop_floor_code`，无额外排序。
- `floor` 是标量列，无外键，因此不涉及复合外键所需的 `(shop_id, id)` 引用键。
- 不建视图、不用 RLS、不加 `tenant_id`（ADR-0002 / 0003）。

## 4. 接口契约

`{shopId}` / `{roomId}` 均为 `public_id` 字符串。所有端点带 `X-Shop-Id`；写操作带 `Idempotency-Key`；响应沿用统一 `{data, meta, page?}` 信封。

| 方法与路径 | 说明 | 状态码 |
|---|---|---|
| `GET /api/v1/shops/{shopId}/rooms` | 房间列表，按 `floor, code` 升序 | 200 |
| `POST /api/v1/shops/{shopId}/rooms` | 新建：`{floor, code, name}` | 201 |
| `GET /api/v1/shops/{shopId}/rooms/{roomId}` | 房间详情 | 200 / 404 |
| `PATCH /api/v1/shops/{shopId}/rooms/{roomId}` | 修改：`{floor?, code?, name?, status?, version}` | 200 / 400 / 404 / 409 |
| `DELETE /api/v1/shops/{shopId}/rooms/{roomId}` | 删除房间 | 204 / 404 |

错误码复用冻结表（`design/03` §4），**不新增错误码**：越店 / 无权限 `403 PERMISSION_DENIED`、缺 `X-Shop-Id` `400 SHOP_CONTEXT_REQUIRED`、不存在 `404 NOT_FOUND`、房间号冲突 `409 CONFLICT`、楼层越界或名称空白 `400 INVALID_ARGUMENT`、版本冲突 `409 VERSION_CONFLICT`。

扁平 DTO（无嵌套实体图）：

- `Room`：`public_id / floor / code / name / status / version / created_at / updated_at`。
- 列表与详情同构（房间无子集合），不嵌楼层实体、不嵌队列 / 人员。

## 5. 事务与幂等

- 写操作走 `IdempotentWrite`（业务写入与幂等记录同事务提交，DP-07）；同幂等键重放返回首次结果。
- 版本号 `version` 用于 `PATCH` 乐观锁：`UPDATE ... WHERE id=$1 AND shop_id=$2 AND version=$6`，影响 0 行 → `409 VERSION_CONFLICT`。
- 唯一约束 `(shop_id, code)` 捕获房间号重复 → `409 CONFLICT`；`floor` 越界 / 空白字段由领域纯函数前置校验 → `400 INVALID_ARGUMENT`。
- 删除房间无级联依赖（本期不与其它表关联），单条 `DELETE ... WHERE id AND shop_id`。

## 6. 关键流程

**F-14 房间加载与楼层分组**：进入房间页 → 带 `X-Shop-Id` 拉 `GET /shops/{shopId}/rooms`（按 `floor, code`）→ 前端 `groupByFloor` 按 `floor` 降序分组、组内按 `code` 升序 → 每个楼层分区显示楼层标签与房间卡片。切换门店按 ADR-0006 取消在途、失效 `shop-scope` 缓存并重拉。

**F-15 房间新建 / 编辑 / 启停**：填写楼层、房间号、名称 → `POST` / `PATCH`（带 `version`）→ 409 时提示冲突并重取详情；成功后失效本店 `rooms` 查询键。启停为 `PATCH {status, version}`，停用房间仍在列表弱化展示。

## 7. 可观测与测试

- 指标沿用 `design/07` 的通用 HTTP / DB 指标，不新增房间专属指标。
- 单测（后端）：楼层范围校验、房间号 / 名称空白校验、`status` 合法性；前端：楼层分组顺序、`floorLabel` 格式化、停用房间保留。
- 集成测试（PG 18.7）：一店两房间同号被拒、不同店同号允许、楼层越界被拒、升级 / 降级、`version` 乐观锁、迁移 up/down/up 与 v5→v6。
- 冒烟：登录后进入房间页、建房间（楼层 + 房间号 + 名称）、编辑楼层与名称、启停、删除、旧 `version` 修改 409、越店请求 403、缺 header 400。

## 8. 通用化边界

只用通用词 `room` / `floor` / `code`；不含占用状态、预订、清洁、容纳人数、面积、设备、业绩等业务规则。与既有冻结契约一致：单币种 CNY、UTC+8（`timestamptz` / UTC 传输）、不归档、不打印、不离线、无门店组、无 tenant、当前门店请求期上下文。

返回 [00 总览](00-overview.md) 查看完整分册地图与已关闭的 OQ / NQ 清单。

# ADR-0011: 房间与楼层

- 状态：已接受（2026-11-19）
- 决策问题：在已冻结的 R1 契约上新增一个通用门店领域——**房间（room）**，并以**楼层（floor）**作为房间的分组维度。明确房间归属、楼层语义与取值范围、唯一性范围、端点、权限与明确不做的范围，使冻结后的第三次受控扩展有可追溯的契约来源
- 影响范围：新增 `design/11-rooms-and-floors.md`、一张表（`rooms`）、R1 端点扩展、两个权限点、迁移 `00006`；修订 `design/00` / `02` / `03`、`ARCHITECTURE.md`、README / CHANGELOG；同步前端 `porch-console` 的房间页与 ADR-0003
- 上游：[ADR-0001](0001-postgresql-18.md)（PG 18.7）、[ADR-0002](0002-go-backend-stack.md)（SQL 优先、无实体图）、[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0006](0006-user-shops-and-current-shop.md)（当前门店请求期上下文）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0009](0009-queues-and-staff.md)（首次受控扩展的范式）

## 背景

用户要求「添加房间功能，房间需要区分楼层」。这是 ADR-0008 冻结设计后的第三个新增领域需求。按冻结后变更规则，schema、端点清单与唯一性范围的改动必须**先有 ADR / 设计来源**，本 ADR 即该来源；它是冻结后的**第三次受控扩展**，不推翻 ADR-0001–0010 的任何条款。

「房间」被理解为门店内可被标识、可被启停的通用空间单元（包间、房间、台位均可复用此模型），不含预订、占用状态机；本 ADR 原定为「不与业务单据绑定」，该边界已由 [ADR-0022](0022-order-card-binding-and-companion-groups.md) 修订为「订单可绑一个房间」（仅存 ID，不引入占用状态机）。「楼层」被理解为房间的一个**分组 / 排序维度**，不是需要独立维护生命周期的实体。

### 理解澄清（本轮最容易被误读的四处）

| 说法 | 准确含义 |
|---|---|
| 「房间需要区分楼层」 | `rooms.floor` 是房间的**整数属性**，`-1` 表示地下一层、`0` 表示地面层、`1..n` 表示地上楼层；列表按 `floor` 分组展示。楼层**不是**独立表，不提供楼层的增删改查 |
| 「房间」的最小含义 | `rooms.code`（房间号，门店内唯一）与 `rooms.name`（房间名称，非空）都必填；前者用于定位，后者用于展示。不引入容纳人数、面积、图腾等属性 |
| 「归属」 | `shops` 1:N `rooms`，房间归属**单一 `shop_id`**（B 类门店业务数据），所有单店端点必须显式带 `X-Shop-Id`，由 `user_shops` 实时求交；不支持跨店房间 |
| 「区分」的展示责任 | 后端只负责按 `floor ASC, code ASC` 返回扁平列表；「按楼层分区展示」是**前端纯函数分组**（`groupByFloor`），不新增分组端点、不在响应里嵌楼层实体图 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 楼层建模 | `rooms.floor` 整数字段 | 独立 `floors` 表 + `rooms.floor_id` 外键 | 需求只要「区分 / 分组」，房间自身带楼层号即可排序分组；独立表引入楼层增删改查与空楼层处理，超出通用底座范围，须另立 ADR |
| 楼层类型 | `integer`（可负） | `text`（如 `1F` / `B1`） | 文本楼层排序需额外规则（B1 与 1F 的先后），且无法做数值范围校验；整数天然有序、可 `floor BETWEEN` 校验，展示层的 `B1` / `1F` 标签由前端格式化 |
| 房间号唯一范围 | `(shop_id, code)` 门店内唯一 | `(shop_id, floor, code)` 楼层内唯一 | 与 `staff.code`、`queues.seq` 的「店内唯一」惯例一致；避免同店出现两个 101 造成定位歧义。楼层只是分组，不参与身份 |
| 房间 ↔ 楼层关系 | 房间单表带 `floor` | 房间 ↔ 楼层多对多 | 一个房间只属于一个楼层，不存在跨层房间；多对多无意义且违反最小概念原则 |
| 容纳人数等属性 | 本期不建 | `rooms.capacity` / `area` 等列 | 需求未提出；属性一旦建表就要定义校验与展示，先保持最小面，后续确需再立 ADR |
| 房间占用状态 | 本期不建 | `rooms.status` 扩展 `occupied` / `cleaning` 状态机 | 属于行业运营规则，与「不打印 / 不离线 / 无排队状态机」的通用化边界一致；`status` 只保留 `active` / `inactive` 启停语义 |

## 决策

### 1. 领域与基数

- `shops` **1:N** `rooms`：一个门店多个房间，房间必属单一门店；不支持跨店复用（与 ADR-0003 单店隔离一致）。
- `rooms.floor` 是房间上的**标量属性**，不是实体、不是外键；不存在 `floors` 表，也不存在 `rooms.floor_id`。
- 房间号 `code` 在**门店内**唯一；`name` 仅用于展示，不参与唯一性。

### 2. 表结构（PG 18.7）

| 表 | 类别 | 关键列 | 唯一性范围 |
|---|---|---|---|
| `rooms` | B | `public_id`、`shop_id`、`floor`、`code`、`name`、`status`、`version` | `(shop_id, code)`、`public_id` |

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

- 读查询以 `shop_id` 前导命中索引；`ORDER BY floor ASC, code ASC` 直接利用该索引，无需额外排序步骤。
- 不建视图、不用 RLS、不加 `tenant_id`（ADR-0002 / 0003）；`floor` 不是外键，因此不涉及复合外键的 `(shop_id, id)` 引用键。

### 3. 端点（R1 扩展）

全部为单店资源，路径含 `{shopId}`（`public_id`），必须显式带 `X-Shop-Id`；写操作要求 `Idempotency-Key`。`{roomId}` 为房间 `public_id`。

| 模块 | 方法与路径 | 说明 |
|---|---|---|
| room | `GET / POST /api/v1/shops/{shopId}/rooms` | 房间列表（按 `floor, code` 升序）/ 新建 |
| room | `GET / PATCH / DELETE /api/v1/shops/{shopId}/rooms/{roomId}` | 房间详情 / 修改 / 删除 |

- `POST` 请求体 `{floor, code, name}`；`PATCH` 请求体 `{floor?, code?, name?, status?, version}`，至少提供一个可改字段，`version` 必填，`409 VERSION_CONFLICT`。
- 错误码复用冻结表（`design/03` §4），**不新增错误码**：缺 `X-Shop-Id` → `400 SHOP_CONTEXT_REQUIRED`、越店 → `403 PERMISSION_DENIED`、不存在 → `404 NOT_FOUND`、房间号冲突 → `409 CONFLICT`、楼层越界 / 名称空白 → `400 INVALID_ARGUMENT`、版本冲突 → `409 VERSION_CONFLICT`。
- 权限点新增 `room.read` / `room.write`，`admin` 与 `manager` 全给、`staff` 只读；迁移 `00006_rooms.sql`。沿用既有权限模型，不新增角色。

### 4. 楼层语义与展示

- `floor` 整数：`-1` = 地下一层、`0` = 地面层、`1..n` = 地上楼层；取值 `-5..200`（`ck_rooms_floor`）。这个范围只是数据校验边界，**不代表**门店真实楼层数，也不按门店配置。
- 后端**不**提供楼层清单 / 楼层 CRUD 端点；可见楼层完全由该店现有房间的 `floor` 值推导（前端从列表分组）。
- 前端 `groupByFloor` 为纯函数：按 `floor` 降序（地上高楼层在前，`-1` 地下在后）分组，组内按 `code` 升序；楼层标签展示由前端格式化（如 `floor=1` → `1F`、`floor=-1` → `B1`、`floor=0` → `GF`），格式化只影响展示、不回写后端。
- 房间停用（`status=inactive`）后仍出现在列表中，前端弱化展示，不静默隐藏。

### 5. 前端契约

- 侧边栏新增「房间管理」入口（路由 `/rooms`）；房间页按楼层分区展示房间卡片，每个分区标注楼层与房间数。
- 新建 / 编辑通过表单完成（楼层用数字输入，房间号与名称必填）；支持启停切换与删除。
- 仍遵循 ADR-0006：所有请求带当前门店 `X-Shop-Id`，切换门店取消在途请求、失效 `shop-scope` 缓存、重拉权限；房间列表查询键含 `(user_id 隐含于 token, current_shop_id, 'rooms')`。

### 6. 不引入的概念

不建 `floors` 表、楼层 CRUD、房间占用 / 预订 / 清洁状态机、房间与队列 / 服务人员的绑定、容纳人数 / 面积 / 设备属性、按楼层的统计看板。**房间与订单的绑定原属本条，已由 ADR-0022 修订并接受**（`orders.room_id`，复合外键锁同店、提交结账前必填、删除被引用房间返回 `409 ROOM_IN_USE`）；仍不做房间占用状态机与按房间筛选订单。其余任一需求须另立 ADR 并评估脱钩边界。

## 理由

1. **需求逐字落实**：「房间」落到 `rooms` 表，「区分楼层」落到 `floor` 字段 + 前端分组，两条需求都有明确的表、约束、端点与展示对应，不含未答项。
2. **沿用冻结契约的隔离模型**：房间带 `shop_id`，房间号店内唯一，不给 tenant、不给分组、不给跨店复用，与 ADR-0003 / 0009 完全同构。
3. **楼层用标量而非实体**：分组维度用整数字段即可排序与校验，避免为「区分」引入一整套楼层生命周期，符合最小概念与 SQL-first。
4. **复用既有横切能力**：`X-Shop-Id`、`Idempotency-Key`、`version` 乐观锁、`room.read/write` 权限点、统一错误码与信封全部沿用，不扩大协议面。

## 后果与限制

- **修订 [ADR-0008](0008-design-freeze-and-nq-resolutions.md) 的 R1 首批端点清单**：追加房间端点，作为冻结后的**第三次受控扩展**；ADR-0008 其余条款、运行基线与验收标准不变。
- `EXPECTED_SCHEMA_VERSION` 由 `5` 升为 `6`；迁移 `00006_rooms.sql` 必须提供可执行 `Down`。
- `design/02` 表清单新增 `rooms`；`design/03` 端点与 DTO 清单新增房间组；`design/00` 变更规则注明第三次受控扩展；`design/11` 为本 ADR 的契约分册。
- 楼层取值 `-5..200` 是数据校验边界，不是业务配置；若门店需要自定义楼层名称 / 顺序，须新 ADR 把 `floor` 从标量升级为关联实体。
- 房间号在门店内唯一；同一房间号不能出现在两个楼层。若业务确需「不同楼层同号」，须新 ADR 把唯一范围改为 `(shop_id, floor, code)`。
- **公开仓库脱钩约束不变**：只用 `room` / `floor` / `code` 等通用词，不出现任何内部项目名或行业专属词。

## 复审条件

- 需要楼层独立属性（名称、平面图、排序权重）或楼层增删改查；
- 需要房间占用 / 预订 / 清洁状态机或与单据 / 队列绑定；
- 需要容纳人数 / 面积 / 设备等房间属性；
- 房间号改为楼层内唯一，或需要跨店复用房间；
- 上游 ADR-0002 / 0003 / 0008 / 0009 任一被修订且影响本 ADR 的隔离或扩展规则。

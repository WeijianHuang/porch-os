# ADR-0026 · 服务人员工号可编辑与别名字段

- 状态：Accepted（2026-11-27）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0015](0015-staff-state-and-position-controls.md) / [ADR-0023](0023-service-item-qualifications-and-line-staff.md) / [ADR-0024](0024-staff-categories-and-assisted-order-entry.md) / [ADR-0025](0025-staff-shift-templates.md)（服务人员基线）
- 影响分册：修订 `design/01` §12、`design/02` §11、`design/03` §9、`design/10` §11、`design/00` 变更规则、`ARCHITECTURE.md`、README / CHANGELOG 与 `porch-console` CHANGELOG
- 迁移：新增 `migrations/00015_staff_code_alias.sql`；`EXPECTED_SCHEMA_VERSION` 由 14 → **15**

## 背景

用户要求：「服务员编辑可以编辑工号，再加上别名字段」。当前 `PATCH /shops/{shopId}/staff/{staffId}` 只接受 `display_name` / `status` / `duty_status` / `category_public_id`，工号建档后不可改；`staff` 表只有 `code` 与 `display_name`，没有别名。服务人员详情页需要展示别名，编辑页需要同时修改工号与别名。

按 ADR-0008，schema 与端点契约变化必须先有 ADR / 设计来源；本 ADR 即该来源。它是冻结后的**第十八次受控扩展**，不推翻 ADR-0001–0025 的任何条款：隔离仍只有 `shop_id` 一层，服务人员仍是单店雇员，`public_id` 仍是稳定的对外标识，资质 / 分类 / 班次 / 调度状态 / 订单金额规则全部不变。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| 「编辑工号」 | 在既有 `PATCH /staff/{staffId}` 上增量接受 `code`，**仅改 `staff.code` 文本**；`public_id` 与内部 `id` 不变，外键关联、订单明细快照、审计历史继续指向同一人 |
| 工号唯一性 | 沿用迁移 `00003` 的 `uq_staff_shop_code (shop_id, code)`：**店内唯一，跨店可重名**。不用 `public_id` 当工号，也不新建全局唯一约束 |
| 「别名」 | 新增 `staff.alias` 可空文本，用于展示与检索的第二个名称；非唯一、非必填、不参与身份识别、不进 JWT、不授予权限 |
| 别名与姓名 | `display_name` 仍是主名称与订单明细快照来源（`staff_name_snapshot`）；`alias` 只是辅助标签，不参与计价、报表分组、授权或跨店清分 |
| 是否允许建档时填别名 | 允许。`POST /staff` 同时接受可选 `alias`，避免必须先建档再补录 |
| 是否需要新权限点 | **不需要**。沿用 `queue.read` / `queue.write`；写操作继续带 `Idempotency-Key` 与审计 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 编辑工号的端点 | 扩展既有 `PATCH /staff/{staffId}` | 新增 `PUT /staff/{staffId}/code` | 同一个实体的一次资料编辑应保持一个端点与一个 `version`，拆开会引入并发中间态 |
| 工号唯一性时点 | DB 唯一约束 + 409 `STAFF_CODE_CONFLICT` | 只在前端查重 | 并发编辑必须靠数据库唯一约束兜底，否则会写入重复工号 |
| 别名存储 | `staff.alias` 独立列 | 复用 `display_name` 写「姓名(别名)」 | 拼接会污染主名称与订单快照，且无法单独搜索 / 清空 |
| 别名唯一性 | 不唯一 | 店内唯一 | 别名是展示标签，重名是正常需求；唯一约束会误伤（例如同一人的不同称呼） |
| 别名长度 | 0–60 字符（Unicode 字符数） | 不限制 | 与 `display_name` 一致，防止卡片与弹窗被超长文本撑破 |

## 决策

### 1. 数据模型（迁移 `00015`，schema 14 → 15）

```sql
ALTER TABLE staff ADD COLUMN alias text NOT NULL DEFAULT '';
ALTER TABLE staff ADD CONSTRAINT ck_staff_alias
  CHECK (character_length(btrim(alias)) <= 60);
```

- `alias` 默认空串表示未填；写入前在应用层 `TrimSpace`，空串与纯空白都归一为 `''`。
- `code` 列与 `uq_staff_shop_code (shop_id, code)` 约束不变，只开放通过 PATCH 修改。
- `public_id` 不可变；编辑工号不会改变任何外键或历史引用。
- `Down`：`DROP CONSTRAINT ck_staff_alias` + `DROP COLUMN alias`，无需回填。

### 2. API 契约

- `POST /staff` 请求体：`{code, display_name, alias?}`；`alias` 省略等同空串。
- `PATCH /staff/{staffId}` 请求体增量增加两个可选字段：

```json
{
  "code": "S102",
  "alias": "小王",
  "display_name": "王五",
  "status": "active",
  "duty_status": "on",
  "category_public_id": "…",
  "version": 7
}
```

- 任一字段可选，但至少给一个；`version` 仍是必填乐观锁，冲突返 `409 VERSION_CONFLICT`。
- `GET /staff` 列表与 `GET /staff/{staffId}` 详情在 `Staff` 投影增量返回 `alias: string`（空串表示未填）。列表排序仍按 `display_name, code`，不按别名。
- `code` 编辑后，服务人员卡片、开单选人列表与详情页立即显示新值；已有订单明细的 `staff_name_snapshot` 不回写。

### 3. 校验与错误

| 场景 | 结果 |
|---|---|
| `code` 为空或纯空白 | `400 INVALID_ARGUMENT`（沿用 `ValidateStaff` 的 `ErrBadCode`） |
| 新工号在本店已被他人占用 | `409 STAFF_CODE_CONFLICT`，由 `uq_staff_shop_code` 唯一冲突映射，消息「工号在本店已存在」 |
| 别名超过 60 个 Unicode 字符 | `400 INVALID_ARGUMENT`；DB CHECK 作为最后一道防线 |
| 跨店改工号 | `403 PERMISSION_DENIED`（门店上下文仍由 `X-Shop-Id` + `user_shops` 决定） |
| 版本过期 | `409 VERSION_CONFLICT`，重读后重试 |

新增 `STAFF_CODE_CONFLICT`（409）只在 `FromPG` 中按约束名 `uq_staff_shop_code` 精确映射；其他唯一约束仍返回通用 `CONFLICT`，避免错误码泛化。

### 4. 前端边界

- 编辑弹窗：工号、别名、姓名均可编辑；工号重复时展示服务端错误，不清空表单。
- 建档弹窗：增加可选别名；详情弹窗展示别名（未填显示「—」）。
- 服务人员卡片的姓名/工号搜索同时匹配 `alias`；别名不单独作为筛选维度。
- 不改变卡片上「详情 / 编辑 / 删除 / 分类 / 服务项」操作行，也不改卡片操作菜单。

## 影响与验证

- 受影响文件：`migrations/00015`、`internal/domain/queue`（纯函数 + 单测）、`internal/adapter/postgres/queues.go` / `staff_commands.go`、`internal/adapter/http/handlers_queues.go`、`internal/platform/errors/errors.go`、`internal/domain/queue/queue_test.go`、`.env.example` / `scripts/test.sh` / `README.md` / `docs/*`、前端 `api/types.ts` / `pages/StaffPage.tsx` / `CHANGELOG.md`。
- 验证：迁移 `14 → 15` 与 down-to-14 → up 可逆；Go build / vet / test 全绿；前端 typecheck / lint / test / build 全绿；API 冒烟覆盖改名 + 改别名、重复工号 409、版本冲突 409、详情投影；浏览器实测编辑 / 详情 / 搜索与 1440 宽布局。

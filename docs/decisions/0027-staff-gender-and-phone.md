# ADR-0027 · 服务人员性别与手机号字段，以及编辑弹窗重排

- 状态：Accepted（2026-11-27）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0015](0015-staff-state-and-position-controls.md) / [ADR-0023](0023-service-item-qualifications-and-line-staff.md) / [ADR-0024](0024-staff-categories-and-assisted-order-entry.md) / [ADR-0025](0025-staff-shift-templates.md) / [ADR-0026](0026-editable-staff-code-and-alias.md)（服务人员基线）
- 影响分册：修订 `design/01` §13、`design/02` §12、`design/03` §10、`design/10` §12、`design/00` 变更规则、`ARCHITECTURE.md`、README / CHANGELOG 与 `porch-console` CHANGELOG
- 迁移：新增 `migrations/00016_staff_gender_phone.sql`；`EXPECTED_SCHEMA_VERSION` 由 15 → **16**

## 背景

用户要求：「再添加性别和手机号码字段，编辑页面进行布局排序一下」。当前 `staff` 表已有 `code` / `alias` / `display_name` / `status` / `duty_status` / `category_id` / `shift_template_id`，但没有性别与联系方式；编辑弹窗把这些字段按历史追加顺序纵向平铺，字段一多就难读。

按 ADR-0008，schema 与端点契约变化必须先有 ADR / 设计来源；本 ADR 即该来源。它是冻结后的**第十九次受控扩展**，不推翻 ADR-0001–0026 的任何条款：隔离仍只有 `shop_id` 一层，服务人员仍是单店雇员，`public_id` 仍是稳定对外标识，工号店内唯一，资质 / 分类 / 班次 / 调度状态 / 订单金额规则全部不变。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| 「性别」 | `staff.gender` 三值文本：`unknown`（未填，默认值）、`male`、`female`。不建 ENUM / 字典表，沿用本项目的 `text + CHECK` 约定 |
| 「手机号码」 | `staff.phone` 可选文本，仅作联系信息。非唯一、非必填、不参与身份、授权、资质、分类、班次、计价或报表分组 |
| 是否建档时就能填 | 可以。`POST /staff` 与 `PATCH /staff/{staffId}` 都接受两个新字段；PATCH 为增量更新，省略即保持原值 |
| 是否发短信 / 通知 | **不做**。本期没有短信集成、验证码或提醒任务，手机号只是档案字段 |
| 是否脱敏展示 | 本期不做脱敏：字段只在 `queue.read` 授权的店内资料页可见，不进公开端点、日志或导出 |
| 是否需要新权限点 | **不需要**。沿用 `queue.read` / `queue.write`；写操作继续带 `Idempotency-Key` 与审计 |
| 「编辑页面进行布局排序」 | 纯 UI 调整：把编辑弹窗按「基本信息 / 联系信息 / 任职设置」分区并改为两列栅格，同步调整新建弹窗与详情弹窗的字段顺序；不改任何端点或状态机 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 性别存储 | `text` + `CHECK (gender IN ('unknown','male','female'))` | PG `ENUM` 类型 | 项目既有约定是不建 ENUM；枚举值变更需要 `ALTER TYPE`，且下游 sqlc / Go 映射更麻烦 |
| 未填性别 | 显式 `unknown` 哨兵值，列 `NOT NULL DEFAULT 'unknown'` | `NULL` 表示未填 | 避免三值逻辑渗进 Go 投影与前端渲染；已有行迁移后自动落到 `unknown` |
| 手机号唯一性 | 不唯一 | 店内唯一 | 联系方式重复是正常情况（例如家庭成员同号），唯一约束会误伤建档 |
| 手机号校验 | 应用层允许空或 6–15 位数字（可含 `+`、空格、`-`、`()`），DB CHECK 只限长度 | 绑死中国大陆 11 位号段 | 公开仓只保留通用多店底座，不写死任何地区号段；长度下限仍能挡住明显误输 |
| 编辑弹窗布局 | 分区标题 + 两列栅格 | 继续纵向平铺 | 字段从 6 个增到 8 个，单列会把弹窗撑得过长；两列在 520px 宽下无横向溢出 |

## 决策

### 1. 数据模型（迁移 `00016`，schema 15 → 16）

```sql
ALTER TABLE staff
  ADD COLUMN gender text NOT NULL DEFAULT 'unknown',
  ADD COLUMN phone  text NOT NULL DEFAULT '',
  ADD CONSTRAINT ck_staff_gender CHECK (gender IN ('unknown','male','female')),
  ADD CONSTRAINT ck_staff_phone  CHECK (character_length(btrim(phone)) <= 32);
```

- `phone` 默认空串表示未填；写入前在应用层 `TrimSpace`，空串与纯空白都归一为 `''`。
- 两列都是 B 类（店内资料），不新增索引：本期不按性别或手机号检索，列表仍按 `display_name, code` 排序。
- `Down`：`DROP CONSTRAINT` + `DROP COLUMN`，无需回填。

### 2. API 契约

- `POST /staff` 请求体增量接受两个可选字段：

```json
{ "code": "S001", "display_name": "张三", "gender": "male", "phone": "13800138000" }
```

- `PATCH /staff/{staffId}` 请求体增量接受两个可选字段：

```json
{
  "gender": "female",
  "phone": "+86 138 0013 8000",
  "version": 7
}
```

- 任一字段可省略；省略即保持原值（PATCH 语义），但仍要求至少给一个可写字段，`version` 必填，冲突返 `409 VERSION_CONFLICT`。
- `GET /staff` 列表与 `GET /staff/{staffId}` 详情在 `Staff` 投影增量返回 `gender: string` 与 `phone: string`（空串表示未填手机号）。
- 新增字段不影响订单明细 `staff_name_snapshot`、审计历史或任何外键；`public_id` 与内部 `id` 不变。

### 3. 校验与错误

| 场景 | 结果 |
|---|---|
| `gender` 不是 `unknown` / `male` / `female` | `400 INVALID_ARGUMENT`（新增纯函数 `ValidateGender` 的 `ErrBadGender`）；DB CHECK 兜底 |
| `phone` 含字母或其他非法字符 | `400 INVALID_ARGUMENT`（`ErrBadPhone`） |
| `phone` 数字个数少于 6 或多于 15 | `400 INVALID_ARGUMENT`（`ErrBadPhone`）；DB CHECK 额外限制 32 字符 |
| `phone` 为空或纯空白 | 合法，归一为 `''` |
| 跨店读写 | `403 PERMISSION_DENIED`（门店上下文仍由 `X-Shop-Id` + `user_shops` 决定） |
| 版本过期 | `409 VERSION_CONFLICT`，重读后重试 |

两类校验错误都沿用现有 `validationError → INVALID_ARGUMENT(400)` 映射，**不新增平台错误码**。

### 4. 前端边界（`porch-console`）

- 编辑弹窗按三区重排，顺序固定：
  1. **基本信息**：工号、姓名（第一行）；别名、性别（第二行）。
  2. **联系信息**：手机号（第三行，半宽）。
  3. **任职设置**：状态、在岗状态（第四行）；分类（第五行，半宽）。
- 新建弹窗沿用同一分区与顺序；详情弹窗插入「性别」「手机号」并同步顺序。
- 服务人员卡片：`unknown` 不显示性别，其他值在姓名旁显示小标记；手机号非空时追加到副标题，超长由 CSS 省略。
- 列表搜索仍只匹配姓名 / 工号 / 别名；手机号不参与搜索，避免把联系信息变成检索维度。
- 「详情 / 编辑 / 删除 / 分类 / 服务项」操作行与调度菜单保持不变。

## 影响与验证

- 受影响文件：`migrations/00016`、`internal/domain/queue/queue.go` / `queue_test.go`、`internal/adapter/postgres/queues.go` / `staff_commands.go`、`internal/adapter/http/handlers_queues.go`、`.env.example` / `scripts/test.sh` / `README.md` / `docs/*`、前端 `api/types.ts` / `pages/StaffPage.tsx` / `styles.css` / `CHANGELOG.md`。
- 验证：迁移 `15 → 16` 与 down-to-15 → up 可逆；Go build / vet / test 全绿；前端 typecheck / lint / test / build 全绿；API 冒烟覆盖新建带两字段、PATCH 增量更新、非法性别 / 手机号 400、版本冲突 409、详情投影；浏览器实测编辑弹窗重排、详情展示与 1440 宽无横向溢出。

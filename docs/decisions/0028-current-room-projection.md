# ADR-0028 · 服务人员当前房间投影与紧凑卡片展示

- 状态：Accepted（2026-11-28）
- 关联：[ADR-0003](0003-shop-level-isolation.md)（只有 `shop_id` 一层隔离）、[ADR-0008](0008-design-freeze-and-nq-resolutions.md)（冻结后先改设计再改代码）、[ADR-0015](0015-staff-state-and-position-controls.md) / [ADR-0017](0017-id-cards-and-square-slots.md) / [ADR-0022](0022-order-card-binding-and-companion-groups.md) / [ADR-0023](0023-service-item-qualifications-and-line-staff.md)（订单/人员基线）
- 影响分册：仅 API 投影增量；`design/01` §13 与 `design/10` §12 需补一行说明，schema 版本不变
- 迁移：**无**

## 背景

用户在首页队列的紧凑值视图中提出：「卡片右上角显的比较空，还需要加一个房间号的信息」。经澄清，目标是**队列内服务人员格**的右上角显示该人员当前所在房间号，而不是房间卡片本身。

现有 `GET /staff` 投影返回状态、ID 卡、队列位次、分类、班次、资质，但不返回房间。房间与人员的关联已经存在：`order_lines.staff_id` 指向实际执行人（ADR-0023），`orders.room_id` 指向房间（ADR-0022），无需新增表或列。

按 ADR-0008，API 契约变化必须先有 ADR；本 ADR 是冻结后的**第二十次受控扩展**，不改动 schema、唯一性、隔离条件、状态机、错误码、端点清单或运行基线数值。

## 理解澄清

| 说法 | 准确含义 |
|---|---|
| 「房间号」 | 房间的 `rooms.code`，例如 `201`；同时带 `public_id` / `floor` / `name`，与订单投影 `OrderRoomRef` 同形 |
| 「当前所在」 | 该服务人员被指派为执行人的**最近一张未结束订单**所绑定的房间；不是常驻工位，不写回 `staff` |
| 「右上角」 | 仅紧凑值视图：状态文字已隐藏，右上角留空，用于放房间号；默认视图保持状态胶囊，不额外加房间号 |
| 是否需要新权限点 | **不需要**。沿用 `queue.read`，数据仍是本店 B 类信息 |
| 是否需要新端点 | **不需要**。只扩展已有的 `GET /staff` 列表与详情投影 |

## 备选方案

| 议题 | 方案 A（采用） | 方案 B | 不选 B 的原因 |
|---|---|---|---|
| 房间数据来源 | 从 `order_lines → orders → rooms` 现算 | 给 `staff` 加 `current_room_id` 列 | 常驻列会在订单结束后变陈旧，且需要写路径维护；本需求只需读时投影 |
| 多张在途订单 | 按订单 `created_at DESC, id DESC` 取唯一一张 | 返回数组 | 卡片右上角只放得下一个值；数组会引入优先级歧义 |
| 结束判定 | `orders.status IN ('draft','open','pending_payment')` | 只看 `paid` | 已支付订单不再是「当前服务」，不应占位 |
| 前端呈现 | 紧凑格右上角小徽标显示 `code` | 替换状态胶囊 | 用户要求颜色继续表达状态；状态文字去掉后不能再把颜色语义换成房间 |

## 决策

### 1. 后端投影

- `StaffSummary` 增量字段：`CurrentRoom *OrderRoomRef \`json:"current_room"\``，无房间时返回 `null`。
- 新增查询 `CurrentRoomsByStaff(ctx, q, shopID)`，一次查出本店所有人员的最近房间，与 `CurrentCardByStaff` 一样在内存中按 `public_id` 折叠（DA-01，无 N+1）：

```sql
SELECT DISTINCT ON (ol.staff_id)
       s.public_id::text,
       r.public_id::text, r.floor, r.code, r.name
FROM order_lines ol
JOIN orders o ON o.shop_id = ol.shop_id AND o.id = ol.order_id
JOIN rooms r ON r.shop_id = o.shop_id AND r.id = o.room_id
JOIN staff s ON s.shop_id = ol.shop_id AND s.id = ol.staff_id
WHERE ol.shop_id = $1
  AND ol.staff_id IS NOT NULL
  AND o.status IN ('draft','open','pending_payment')
  AND o.room_id IS NOT NULL
ORDER BY ol.staff_id, o.created_at DESC, o.id DESC
```

- 查询带 `shop_id` 限制与三处复合外键同店条件，遵守 ADR-0003；不新增索引，复用 `idx_order_lines_shop_staff` 与 `idx_orders_shop_room`。
- `GET /staff` 列表与 `GET /staff/{staffId}` 详情在各自现有投影上增量返回 `current_room`，不新增端点，不改变任何写操作。

### 2. 前端展示（`porch-console`）

- `StaffSummary` 类型增量 `current_room?: OrderRoomRef | null`。
- 仅当「仅显示值」开关开启（紧凑视图）时，在服务人员格右上角显示房间号徽标 `current_room.code`；无房间时显示 `—`，`title` 给出房间名。
- 默认视图不显示房间号，避免与状态胶囊争抢右上角；颜色仍按 ADR-0015 / ADR-0028 的状态色表表达状态。
- 房间号不参与搜索、筛选、排序或导出，只作即时信息。

## 影响与验证

- 受影响文件：`internal/adapter/postgres/staff_commands.go`、前端 `src/api/types.ts` / `src/pages/HomePage.tsx` / `src/styles.css`。
- 验证：Go build / vet / test 全绿；前端 typecheck / lint / test / build 全绿；API 冒烟确认 `current_room` 在无订单时为 `null`、有在途订单时返回房间投影；浏览器实测紧凑格右上角房间号、默认视图不受影响、1440 / 1024 无横向溢出。

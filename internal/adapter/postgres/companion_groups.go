package postgres

import (
	"context"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/WeijianHuang/porch-os/internal/domain/companion"
	"github.com/WeijianHuang/porch-os/internal/domain/money"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// Companion groups (ADR-0022 §5). A group is an orchestration view over
// multiple already-card-bound orders: it stores no amount, is not a payment
// subject, and never changes an order's card/room binding.

var (
	errGroupNotFound    = apierrors.New("GROUP_NOT_FOUND", "同行组不存在")
	errGroupNotOpen     = apierrors.New("GROUP_NOT_OPEN", "同行组当前不可修改")
	errGroupUnsettled   = apierrors.New("GROUP_HAS_UNSETTLED_MEMBERS", "组内仍有未结清订单，不能关闭")
	errGroupHasPaid     = apierrors.New("GROUP_HAS_PAID_MEMBERS", "组内已有成功支付，只能关闭、不能取消")
	errMemberNotInGroup = apierrors.New("MEMBER_NOT_IN_GROUP", "订单不是该同行组的活跃成员")
	errMemberUnchanged  = apierrors.New("MEMBER_UNCHANGED", "订单已在该同行组中")
)

type CompanionGroup struct {
	PublicID     string            `json:"public_id"`
	Status       string            `json:"status"`
	Version      int               `json:"version"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Members      []CompanionMember `json:"members,omitempty"`
	PayableTotal string            `json:"payable_total,omitempty"`
	PaidTotal    string            `json:"paid_total,omitempty"`
	ID           int64             `json:"-"`
}

// CompanionMember is a flat per-order projection with ADR-0014 derived
// amounts. payable = total − paid; refunded/refundable are informational.
type CompanionMember struct {
	OrderPublicID    string    `json:"order_public_id"`
	OrderNo          string    `json:"order_no"`
	Status           string    `json:"status"`
	TotalAmount      string    `json:"total_amount"`
	DiscountAmount   string    `json:"discount_amount"`
	PaidAmount       string    `json:"paid_amount"`
	RefundedAmount   string    `json:"refunded_amount"`
	RefundableAmount string    `json:"refundable_amount"`
	PayableAmount    string    `json:"payable_amount"`
	JoinedAt         time.Time `json:"joined_at"`
}

func (v CompanionGroup) GetCursor() (time.Time, int64) { return v.CreatedAt, v.ID }

const groupCols = `public_id::text, status, version, created_at, updated_at, id`

func scanGroup(row pgx.Row) (CompanionGroup, error) {
	var v CompanionGroup
	err := row.Scan(&v.PublicID, &v.Status, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID)
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, err
}

// CreateCompanionGroup creates an empty or pre-populated open group.
func (s *Store) CreateCompanionGroup(ctx context.Context, tx pgx.Tx, shopID, actorID int64, orderPubs []string) (CompanionGroup, error) {
	var id int64
	var pub string
	if err := tx.QueryRow(ctx, `
		INSERT INTO companion_groups (shop_id, created_by)
		VALUES ($1,$2) RETURNING id, public_id::text`, shopID, actorID).Scan(&id, &pub); err != nil {
		return CompanionGroup{}, err
	}
	for _, orderPub := range orderPubs {
		if err := s.addGroupMember(ctx, tx, shopID, id, orderPub); err != nil {
			return CompanionGroup{}, err
		}
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "companion_group_created", pub,
		map[string]any{"order_count": len(orderPubs)}); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOutbox(ctx, tx, shopID, "companion_group", pub, "companion_group.created", nil); err != nil {
		return CompanionGroup{}, err
	}
	return s.companionGroupDetail(ctx, tx, shopID, pub)
}

// ListCompanionGroups is a keyset list with an optional status filter.
func (s *Store) ListCompanionGroups(ctx context.Context, q DBTX, shopID int64, limit int, cursorTime *time.Time, cursorID int64, status string) ([]CompanionGroup, Page, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(groupCols).From("companion_groups").
		Where("shop_id = ?", shopID).
		OrderBy("created_at DESC", "id DESC").Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)
	if status != "" {
		b = b.Where("status = ?", status)
	}
	if cursorTime != nil && cursorID > 0 {
		b = b.Where(squirrel.Expr("(created_at, id) < (?, ?)", *cursorTime, cursorID))
	}
	sqlText, args, err := b.ToSql()
	if err != nil {
		return nil, Page{}, err
	}
	rows, err := q.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	out := make([]CompanionGroup, 0, limit+1)
	for rows.Next() {
		v, err := scanGroup(rows)
		if err != nil {
			return nil, Page{}, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, Page{}, err
	}
	page := Page{}
	if len(out) > limit {
		page.HasMore = true
		out = out[:limit]
	}
	return out, page, nil
}

// CompanionGroupDetail returns the group plus every active member and the
// read-only aggregated totals.
func (s *Store) CompanionGroupDetail(ctx context.Context, q DBTX, shopID int64, groupPub string) (CompanionGroup, error) {
	if q == nil {
		q = s.Pool
	}
	return s.companionGroupDetail(ctx, q, shopID, groupPub)
}

func (s *Store) companionGroupDetail(ctx context.Context, q DBTX, shopID int64, groupPub string) (CompanionGroup, error) {
	v, err := scanGroup(q.QueryRow(ctx,
		`SELECT `+groupCols+` FROM companion_groups WHERE shop_id=$1 AND public_id=$2::uuid`,
		shopID, groupPub))
	if err != nil {
		if err == pgx.ErrNoRows {
			return v, errGroupNotFound
		}
		return v, err
	}
	rows, err := q.Query(ctx, `
		SELECT o.public_id::text, o.order_no, o.status, o.total_amount::text, o.discount_amount::text,
		       COALESCE(p.paid,0)::text, COALESCE(r.refunded,0)::text, m.joined_at
		FROM companion_group_members m
		JOIN orders o ON o.id=m.order_id AND o.shop_id=m.shop_id
		LEFT JOIN LATERAL (
		  SELECT SUM(amount) AS paid FROM payments
		  WHERE shop_id=o.shop_id AND order_id=o.id AND kind='sale' AND status='succeeded'
		) p ON true
		LEFT JOIN LATERAL (
		  SELECT SUM(amount) AS refunded FROM refunds
		  WHERE shop_id=o.shop_id AND order_id=o.id AND kind='sale' AND status='succeeded'
		) r ON true
		WHERE m.shop_id=$1 AND m.group_id=$2 AND m.left_at IS NULL
		ORDER BY m.joined_at, o.id`, shopID, v.ID)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	v.Members = make([]CompanionMember, 0)
	payableTotal, paidTotal := money.Zero(), money.Zero()
	for rows.Next() {
		var m CompanionMember
		if err := rows.Scan(&m.OrderPublicID, &m.OrderNo, &m.Status, &m.TotalAmount,
			&m.DiscountAmount, &m.PaidAmount, &m.RefundedAmount, &m.JoinedAt); err != nil {
			return v, err
		}
		m.JoinedAt = utc(m.JoinedAt)
		total, _ := money.FromString(m.TotalAmount)
		paid, _ := money.FromString(m.PaidAmount)
		refunded, _ := money.FromString(m.RefundedAmount)
		payable := total.Sub(paid)
		if payable.IsNegative() {
			payable = money.Zero()
		}
		m.PayableAmount = payable.String()
		m.RefundableAmount = paid.Sub(refunded).String()
		v.Members = append(v.Members, m)
		payableTotal = payableTotal.Add(payable)
		paidTotal = paidTotal.Add(paid)
	}
	if err := rows.Err(); err != nil {
		return v, err
	}
	v.PayableTotal, v.PaidTotal = payableTotal.String(), paidTotal.String()
	return v, nil
}

// JoinCompanionGroup adds one or more orders to an open group. An order already
// active in another group is moved in the same transaction; an emptied source
// group is closed atomically.
func (s *Store) JoinCompanionGroup(ctx context.Context, tx pgx.Tx, shopID, actorID int64, groupPub string, orderPubs []string, version int) (CompanionGroup, error) {
	id, status, current, err := lockGroup(ctx, tx, shopID, groupPub)
	if err != nil {
		return CompanionGroup{}, err
	}
	if err := companion.CanJoin(companion.Status(status)); err != nil {
		return CompanionGroup{}, errGroupNotOpen
	}
	if version != current {
		return CompanionGroup{}, errVersion
	}
	for _, orderPub := range orderPubs {
		if err := s.addGroupMember(ctx, tx, shopID, id, orderPub); err != nil {
			return CompanionGroup{}, err
		}
		if err := appendOrderAudit(ctx, tx, shopID, actorID, "companion_group_member_added", groupPub,
			map[string]any{"order_id": orderPub}); err != nil {
			return CompanionGroup{}, err
		}
		if err := appendOutbox(ctx, tx, shopID, "companion_group", groupPub,
			"companion_group.member_added", map[string]any{"order_id": orderPub}); err != nil {
			return CompanionGroup{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE companion_groups SET version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id); err != nil {
		return CompanionGroup{}, err
	}
	return s.companionGroupDetail(ctx, tx, shopID, groupPub)
}

// addGroupMember validates an unsettled order, moves it out of a previous open
// group if needed, and inserts the active membership row.
func (s *Store) addGroupMember(ctx context.Context, tx pgx.Tx, shopID, groupID int64, orderPub string) error {
	var orderID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT id, status FROM orders
		WHERE shop_id=$1 AND public_id=$2::uuid AND deleted_at IS NULL`,
		shopID, orderPub).Scan(&orderID, &status); err != nil {
		if err == pgx.ErrNoRows {
			return apierrors.NotFound
		}
		return err
	}
	if !isEditableOrderStatus(status) {
		return errOrderSettled
	}
	var paid bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM payments
		               WHERE shop_id=$1 AND order_id=$2 AND status='succeeded')`,
		shopID, orderID).Scan(&paid); err != nil {
		return err
	}
	if paid {
		return errOrderSettled
	}

	var memberID, oldGroupID int64
	err := tx.QueryRow(ctx, `
		SELECT m.id, m.group_id FROM companion_group_members m
		WHERE m.shop_id=$1 AND m.order_id=$2 AND m.left_at IS NULL`,
		shopID, orderID).Scan(&memberID, &oldGroupID)
	switch {
	case err == nil && oldGroupID == groupID:
		return errMemberUnchanged
	case err == nil:
		if _, err := tx.Exec(ctx, `
			UPDATE companion_group_members SET left_at=now()
			WHERE shop_id=$1 AND id=$2 AND left_at IS NULL`, shopID, memberID); err != nil {
			return err
		}
		var remaining int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM companion_group_members
			WHERE shop_id=$1 AND group_id=$2 AND left_at IS NULL`,
			shopID, oldGroupID).Scan(&remaining); err != nil {
			return err
		}
		if remaining == 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE companion_groups SET status='closed', version=version+1, updated_at=now()
				WHERE shop_id=$1 AND id=$2 AND status='open'`, shopID, oldGroupID); err != nil {
				return err
			}
		}
	case err != nil && err != pgx.ErrNoRows:
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO companion_group_members (shop_id, group_id, order_id, joined_at)
		VALUES ($1,$2,$3,now())`, shopID, groupID, orderID)
	if err != nil {
		if apierrors.FromPG(err).Code == "CONFLICT" {
			return errMemberUnchanged
		}
		return err
	}
	return nil
}

// LeaveCompanionGroup splits an order out by setting left_at; the order keeps
// its card/room binding, lines and amounts untouched.
func (s *Store) LeaveCompanionGroup(ctx context.Context, tx pgx.Tx, shopID, actorID int64, groupPub, orderPub string, version int) (CompanionGroup, error) {
	id, status, current, err := lockGroup(ctx, tx, shopID, groupPub)
	if err != nil {
		return CompanionGroup{}, err
	}
	if err := companion.CanLeave(companion.Status(status)); err != nil {
		return CompanionGroup{}, errGroupNotOpen
	}
	if version != current {
		return CompanionGroup{}, errVersion
	}
	tag, err := tx.Exec(ctx, `
		UPDATE companion_group_members m SET left_at=now()
		WHERE m.shop_id=$1 AND m.group_id=$2 AND m.left_at IS NULL
		  AND m.order_id=(SELECT id FROM orders WHERE shop_id=$1 AND public_id=$3::uuid)`,
		shopID, id, orderPub)
	if err != nil {
		return CompanionGroup{}, err
	}
	if tag.RowsAffected() == 0 {
		return CompanionGroup{}, errMemberNotInGroup
	}
	if err := s.closeIfEmpty(ctx, tx, shopID, id); err != nil {
		return CompanionGroup{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE companion_groups SET version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "companion_group_member_removed", groupPub,
		map[string]any{"order_id": orderPub}); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOutbox(ctx, tx, shopID, "companion_group", groupPub,
		"companion_group.member_removed", map[string]any{"order_id": orderPub}); err != nil {
		return CompanionGroup{}, err
	}
	return s.companionGroupDetail(ctx, tx, shopID, groupPub)
}

// CloseCompanionGroup closes a group once every active member is settled (or
// cancelled) or the group is empty.
func (s *Store) CloseCompanionGroup(ctx context.Context, tx pgx.Tx, shopID, actorID int64, groupPub string, version int) (CompanionGroup, error) {
	id, status, current, err := lockGroup(ctx, tx, shopID, groupPub)
	if err != nil {
		return CompanionGroup{}, err
	}
	if version != current {
		return CompanionGroup{}, errVersion
	}
	active, unsettled, err := groupMemberCounts(ctx, tx, shopID, id)
	if err != nil {
		return CompanionGroup{}, err
	}
	if err := companion.CanClose(companion.Status(status), active, unsettled); err != nil {
		if err == companion.ErrNotOpen {
			return CompanionGroup{}, errGroupNotOpen
		}
		// Group-level refusal: some member is unsettled. Reporting ORDER_SETTLED
		// here would tell the cashier the opposite of what happened.
		return CompanionGroup{}, errGroupUnsettled
	}
	if _, err := tx.Exec(ctx, `
		UPDATE companion_groups SET status='closed', version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "companion_group_closed", groupPub, nil); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOutbox(ctx, tx, shopID, "companion_group", groupPub,
		"companion_group.closed", nil); err != nil {
		return CompanionGroup{}, err
	}
	return s.companionGroupDetail(ctx, tx, shopID, groupPub)
}

// CancelCompanionGroup cancels an open group only when no member carries a
// successful payment.
func (s *Store) CancelCompanionGroup(ctx context.Context, tx pgx.Tx, shopID, actorID int64, groupPub string, version int) (CompanionGroup, error) {
	id, status, current, err := lockGroup(ctx, tx, shopID, groupPub)
	if err != nil {
		return CompanionGroup{}, err
	}
	if version != current {
		return CompanionGroup{}, errVersion
	}
	var paidMembers int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(DISTINCT o.id)
		FROM companion_group_members m
		JOIN orders o ON o.id=m.order_id AND o.shop_id=m.shop_id
		JOIN payments p ON p.order_id=o.id AND p.shop_id=o.shop_id
		               AND p.kind='sale' AND p.status='succeeded'
		WHERE m.shop_id=$1 AND m.group_id=$2 AND m.left_at IS NULL`,
		shopID, id).Scan(&paidMembers); err != nil {
		return CompanionGroup{}, err
	}
	if err := companion.CanCancel(companion.Status(status), paidMembers); err != nil {
		if err == companion.ErrNotOpen {
			return CompanionGroup{}, errGroupNotOpen
		}
		return CompanionGroup{}, errGroupHasPaid
	}
	if _, err := tx.Exec(ctx, `
		UPDATE companion_groups SET status='cancelled', version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2`, shopID, id); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "companion_group_cancelled", groupPub, nil); err != nil {
		return CompanionGroup{}, err
	}
	if err := appendOutbox(ctx, tx, shopID, "companion_group", groupPub,
		"companion_group.cancelled", nil); err != nil {
		return CompanionGroup{}, err
	}
	return s.companionGroupDetail(ctx, tx, shopID, groupPub)
}

func lockGroup(ctx context.Context, tx pgx.Tx, shopID int64, groupPub string) (int64, string, int, error) {
	var id int64
	var status string
	var version int
	err := tx.QueryRow(ctx, `
		SELECT id, status, version FROM companion_groups
		WHERE shop_id=$1 AND public_id=$2::uuid FOR UPDATE`, shopID, groupPub).
		Scan(&id, &status, &version)
	if err == pgx.ErrNoRows {
		return 0, "", 0, errGroupNotFound
	}
	return id, status, version, err
}

// groupMemberCounts returns active members and those still unsettled. A
// cancelled member no longer holds the group open.
func groupMemberCounts(ctx context.Context, tx pgx.Tx, shopID, groupID int64) (int, int, error) {
	var active, unsettled int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE o.status NOT IN
		         ('paid','completed','partially_refunded','refunded','cancelled'))
		FROM companion_group_members m
		JOIN orders o ON o.id=m.order_id AND o.shop_id=m.shop_id
		WHERE m.shop_id=$1 AND m.group_id=$2 AND m.left_at IS NULL`,
		shopID, groupID).Scan(&active, &unsettled); err != nil {
		return 0, 0, err
	}
	return active, unsettled, nil
}

func (s *Store) closeIfEmpty(ctx context.Context, tx pgx.Tx, shopID, groupID int64) error {
	var remaining int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM companion_group_members
		WHERE shop_id=$1 AND group_id=$2 AND left_at IS NULL`,
		shopID, groupID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		_, err := tx.Exec(ctx, `
			UPDATE companion_groups SET status='closed', version=version+1, updated_at=now()
			WHERE shop_id=$1 AND id=$2 AND status='open'`, shopID, groupID)
		return err
	}
	return nil
}

package postgres

import (
	"context"
	"time"

	"github.com/WeijianHuang/porch-os/internal/domain/money"
	domorder "github.com/WeijianHuang/porch-os/internal/domain/order"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

type Refund struct {
	PublicID   string      `json:"public_id"`
	RefundNo   string      `json:"refund_no"`
	Kind       string      `json:"kind"`
	PaymentID  string      `json:"payment_id"`
	Status     string      `json:"status"`
	Amount     string      `json:"amount"`
	Reason     string      `json:"reason,omitempty"`
	Entries    []PayMethod `json:"refund_entries"`
	Refundable string      `json:"refundable_amount"`
	CreatedAt  time.Time   `json:"created_at"`
	ID         int64       `json:"-"`
}

// refundRequest is the shared validated input. refund_entries are explicit:
// the system only validates and never auto-allocates (ADR-0014 / 0016).
type refundRequest struct {
	Amount  string
	Reason  string
	Entries []PayMethod
}

// validateRefundEntries checks Σ entries == amount and method whitelist.
func validateRefundEntries(amount string, entries []PayMethod) (money.Money, []money.Money, error) {
	total, err := money.FromString(amount)
	if err != nil || !total.IsPositive() || len(entries) == 0 {
		return money.Zero(), nil, apierrorsInvalid
	}
	sum := money.Zero()
	amounts := make([]money.Money, 0, len(entries))
	for _, e := range entries {
		if e.Method != "cash" && e.Method != "balance" && e.Method != "card" {
			return money.Zero(), nil, apierrorsInvalid
		}
		m, err := money.FromString(e.Amount)
		if err != nil || !m.IsPositive() {
			return money.Zero(), nil, apierrorsInvalid
		}
		sum = sum.Add(m)
		amounts = append(amounts, m)
	}
	if sum.String() != total.String() {
		return money.Zero(), nil, errUnprocessable
	}
	return total, amounts, nil
}

var apierrorsInvalid = apierrors.InvalidArgument

// channelAudit sums already-succeeded refunds per method for one payment,
// under a row lock on the payment held by the caller's transaction.
func channelAudit(ctx context.Context, tx pgx.Tx, shopID, paymentID int64) (map[string]money.Money, money.Money, error) {
	rows, err := tx.Query(ctx, `
		SELECT re.method, SUM(re.amount)::text
		FROM refund_entries re JOIN refunds r
		  ON r.shop_id=re.shop_id AND r.id=re.refund_id
		WHERE re.shop_id=$1 AND r.payment_id=$2 AND r.status='succeeded'
		GROUP BY re.method`, shopID, paymentID)
	if err != nil {
		return nil, money.Zero(), err
	}
	defer rows.Close()
	out := make(map[string]money.Money)
	total := money.Zero()
	for rows.Next() {
		var method, amt string
		if err := rows.Scan(&method, &amt); err != nil {
			return nil, money.Zero(), err
		}
		m, err := money.FromString(amt)
		if err != nil {
			return nil, money.Zero(), err
		}
		out[method] = m
		total = total.Add(m)
	}
	return out, total, rows.Err()
}

// SaleRefund runs design/06 F-02 in one transaction. The order must be in a
// refundable state; the payment's succeeded entries are the channel source;
// cumulative totals per payment and per (payment, method) are checked under a
// row lock. It never modifies the original order amounts or lines.
func (s *Store) SaleRefund(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, orderPub, paymentPub, amount, reason string, entries []PayMethod, shiftIDRaw string) (Refund, error) {
	var out Refund
	total, amounts, err := validateRefundEntries(amount, entries)
	if err != nil {
		return out, err
	}
	order, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return out, err
	}
	if err := domorder.CanOrderTransition(order.Status, domorder.StatusPartiallyRefunded); err != nil {
		return out, errInvalidState
	}
	// Lock the payment row and verify it is the succeeded sale payment of this
	// order in this shop (composite isolation; no cross-shop refund).
	payment, err := s.PaymentByPublic(ctx, tx, shopID, paymentPub)
	if err != nil {
		return out, err
	}
	var payStatus string
	var lockID int64
	err = tx.QueryRow(ctx, `
		SELECT id, status FROM payments
		WHERE shop_id=$1 AND id=$2 AND kind='sale' AND status='succeeded' FOR UPDATE`,
		shopID, payment.ID).Scan(&lockID, &payStatus)
	if err != nil {
		return out, errInvalidState
	}
	if !payment.OrderID.Valid || payment.OrderID.Int64 != order.ID {
		return out, errInvalidState
	}

	// Cumulative refund <= paid, under the same lock.
	paid, refunded, err := s.OrderDerived(ctx, tx, shopID, order.ID)
	if err != nil {
		return out, err
	}
	refundable := paid.Sub(refunded)
	if total.Decimal().GreaterThan(refundable.Decimal()) {
		return out, errConflict
	}

	// Per-channel capacity from the original payment entries.
	capacity := make(map[string]money.Money)
	origSum := money.Zero()
	for _, e := range payment.Entries {
		m, err := money.FromString(e.Amount)
		if err != nil {
			return out, err
		}
		capacity[e.Method] = m
		origSum = origSum.Add(m)
	}
	if origSum.String() != payment.Amount.String() {
		return out, errConflict
	}
	byMethod, _, err := channelAudit(ctx, tx, shopID, payment.ID)
	if err != nil {
		return out, err
	}
	for i, e := range entries {
		base, ok := capacity[e.Method]
		if !ok {
			return out, errUnprocessable // method not in original payment
		}
		used := byMethod[e.Method]
		used = used.Add(amounts[i])
		if used.Decimal().GreaterThan(base.Decimal()) {
			return out, errConflict // no auto re-allocation
		}
	}

	refundNo, err := s.nextRefundNo(ctx, tx, shopID)
	if err != nil {
		return out, err
	}
	var refundID int64
	var shiftID any
	if shiftIDRaw != "" {
		shiftID = shiftIDRaw
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO refunds (shop_id,refund_no,kind,payment_id,order_id,shift_id,operator_id,amount,status,reason)
		VALUES ($1,$2,'sale',$3,$4,$5,$6,$7,'succeeded',$8)
		RETURNING id, public_id::text, created_at`,
		shopID, refundNo, payment.ID, order.ID, shiftID, operatorID, amount, reason).
		Scan(&refundID, &out.PublicID, &out.CreatedAt)
	if err != nil {
		return out, err
	}
	out.CreatedAt = utc(out.CreatedAt)
	for i, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO refund_entries (shop_id,refund_id,seq,method,amount)
			VALUES ($1,$2,$3,$4,$5::numeric)`, shopID, refundID, i+1, e.Method, e.Amount); err != nil {
			return out, err
		}
	}

	// Refunding a balance-funded sale credits the member balance back via an
	// append-only ledger row (the reverse of the payment deduction).
	var balanceBack money.Money
	for i, e := range entries {
		if e.Method == "balance" {
			balanceBack = balanceBack.Add(amounts[i])
		}
	}
	if balanceBack.IsPositive() && payment.MemberID.Valid {
		var after string
		if err := tx.QueryRow(ctx, `
			UPDATE member_balances SET balance=balance+$3::numeric, version=version+1, updated_at=now()
			WHERE shop_id=$1 AND member_id=$2 RETURNING balance::text`,
			shopID, payment.MemberID.Int64, balanceBack.String()).Scan(&after); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO balance_ledger (shop_id,member_id,delta,balance_after,reason,ref_type,ref_id)
			VALUES ($1,$2,$3::numeric,$4::numeric,'refund','refund',$5)`,
			shopID, payment.MemberID.Int64, balanceBack.String(), after, out.PublicID); err != nil {
			return out, err
		}
	}

	// Order status marker: fully refunded when the new balance is zero.
	newRefunded := refunded.Add(total)
	fully := newRefunded.String() == paid.String()
	newStatus, err := domorder.RefundOrderStatus(order.Status, fully)
	if err != nil {
		return out, err
	}
	if err := s.SetOrderRefundStatus(ctx, tx, shopID, order.ID, newStatus); err != nil {
		return out, err
	}
	if err := appendOrderAudit(ctx, tx, shopID, operatorID, "order_refunded", order.PublicID, nil); err != nil {
		return out, err
	}
	if err := appendOutbox(ctx, tx, shopID, "refund", out.PublicID, "refund.succeeded", nil); err != nil {
		return out, err
	}
	if err := appendOutbox(ctx, tx, shopID, "shop", "", "shop.summary_dirty", nil); err != nil {
		return out, err
	}
	out.Kind, out.Status, out.Amount, out.Reason, out.PaymentID = "sale", "succeeded", amount, reason, paymentPub
	out.Entries = entries
	remaining := paid.Sub(newRefunded)
	if remaining.IsNegative() {
		remaining = money.Zero()
	}
	out.Refundable = remaining.String()
	return out, nil
}

// TopupRefund runs design/06 F-03 refund row in one transaction: validate the
// source is a succeeded topup of this shop and member, check channel capacity,
// insert refund + entries, conditionally debit the member balance (insufficient
// => whole refund rejected, no partial), and write the reverse ledger row.
func (s *Store) TopupRefund(ctx context.Context, tx pgx.Tx, shopID, operatorID, memberID int64, paymentPub, amount, reason string, entries []PayMethod, shiftIDRaw string) (Refund, error) {
	var out Refund
	total, amounts, err := validateRefundEntries(amount, entries)
	if err != nil {
		return out, err
	}
	payment, err := s.PaymentByPublic(ctx, tx, shopID, paymentPub)
	if err != nil {
		return out, err
	}
	if payment.Kind != "topup" || !payment.MemberID.Valid || payment.MemberID.Int64 != memberID {
		return out, errInvalidState
	}
	var payStatus string
	err = tx.QueryRow(ctx, `
		SELECT status FROM payments
		WHERE shop_id=$1 AND id=$2 AND kind='topup' AND status='succeeded' FOR UPDATE`,
		shopID, payment.ID).Scan(&payStatus)
	if err != nil {
		return out, errInvalidState
	}

	// Cumulative topup refund <= original credited amount.
	var refundedText string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount),0)::text FROM refunds
		WHERE shop_id=$1 AND payment_id=$2 AND kind='topup' AND status='succeeded'`,
		shopID, payment.ID).Scan(&refundedText); err != nil {
		return out, err
	}
	refunded, err := money.FromString(refundedText)
	if err != nil {
		return out, err
	}
	refundable := payment.Amount.Sub(refunded)
	if total.Decimal().GreaterThan(refundable.Decimal()) {
		return out, errConflict
	}

	capacity := make(map[string]money.Money)
	for _, e := range payment.Entries {
		m, err := money.FromString(e.Amount)
		if err != nil {
			return out, err
		}
		capacity[e.Method] = m
	}
	byMethod, _, err := channelAudit(ctx, tx, shopID, payment.ID)
	if err != nil {
		return out, err
	}
	for i, e := range entries {
		base, ok := capacity[e.Method]
		if !ok {
			return out, errUnprocessable
		}
		if byMethod[e.Method].Add(amounts[i]).Decimal().GreaterThan(base.Decimal()) {
			return out, errConflict
		}
	}

	refundNo, err := s.nextRefundNo(ctx, tx, shopID)
	if err != nil {
		return out, err
	}
	var refundID int64
	var shiftID any
	if shiftIDRaw != "" {
		shiftID = shiftIDRaw
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO refunds (shop_id,refund_no,kind,payment_id,member_id,shift_id,operator_id,amount,status,reason)
		VALUES ($1,$2,'topup',$3,$4,$5,$6,$7,'succeeded',$8)
		RETURNING id, public_id::text, created_at`,
		shopID, refundNo, payment.ID, memberID, shiftID, operatorID, amount, reason).
		Scan(&refundID, &out.PublicID, &out.CreatedAt)
	if err != nil {
		return out, err
	}
	out.CreatedAt = utc(out.CreatedAt)
	for i, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO refund_entries (shop_id,refund_id,seq,method,amount)
			VALUES ($1,$2,$3,$4,$5::numeric)`, shopID, refundID, i+1, e.Method, e.Amount); err != nil {
			return out, err
		}
	}

	// Conditional balance rollback: insufficient => 409, whole tx rolls back.
	var after string
	err = tx.QueryRow(ctx, `
		UPDATE member_balances SET balance=balance-$3::numeric, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND member_id=$2 AND balance >= $3::numeric
		RETURNING balance::text`, shopID, memberID, amount).Scan(&after)
	if err != nil {
		return out, errInsufficient
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO balance_ledger (shop_id,member_id,delta,balance_after,reason,ref_type,ref_id)
		VALUES ($1,$2,$3::numeric,$4::numeric,'topup_refund','refund',$5)`,
		shopID, memberID, "-"+amount, after, out.PublicID); err != nil {
		return out, err
	}

	if err := appendOrderAudit(ctx, tx, shopID, operatorID, "topup_refunded", out.PublicID, nil); err != nil {
		return out, err
	}
	if err := appendOutbox(ctx, tx, shopID, "refund", out.PublicID, "refund.succeeded", nil); err != nil {
		return out, err
	}
	out.Kind, out.Status, out.Amount, out.Reason, out.PaymentID = "topup", "succeeded", amount, reason, paymentPub
	out.Entries = entries
	remaining := payment.Amount.Sub(refunded.Add(total))
	if remaining.IsNegative() {
		remaining = money.Zero()
	}
	out.Refundable = remaining.String()
	return out, nil
}

// nextRefundNo uses the same database sequence as order numbers, with an RF
// prefix; uniqueness is DB-backed and the date follows business_date.
func (s *Store) nextRefundNo(ctx context.Context, tx pgx.Tx, shopID int64) (string, error) {
	var no string
	err := tx.QueryRow(ctx, `
		SELECT 'RF' || to_char(business_date(now(), $1),'YYYYMMDD') || '-'
		       || lpad(nextval('order_no_seq')::text, 10, '0')`, shopID).Scan(&no)
	return no, err
}

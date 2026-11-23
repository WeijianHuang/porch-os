package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/WeijianHuang/porch-os/internal/domain/money"
	domorder "github.com/WeijianHuang/porch-os/internal/domain/order"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// PayMethod is one real funding channel of a combined payment. Coupon is a
// discount, never a method (ADR-0016); the handler whitelists the values.
type PayMethod struct {
	Method string `json:"method" binding:"required"`
	Amount string `json:"amount" binding:"required"`
}

type Payment struct {
	PublicID  string      `json:"public_id"`
	Kind      string      `json:"kind"`
	OrderID   *string     `json:"order_id"`
	MemberID  *string     `json:"member_id"`
	Status    string      `json:"status"`
	Amount    string      `json:"amount"`
	Entries   []PayMethod `json:"entries"`
	CreatedAt time.Time   `json:"created_at"`
	ID        int64       `json:"-"`
}

// PaymentForRefund is the immutable source payment with its entry breakdown,
// used by both sale and topup refund validation.
type PaymentForRefund struct {
	ID       int64
	PublicID string
	Kind     string
	OrderID  sql.NullInt64
	MemberID sql.NullInt64
	Amount   money.Money
	Entries  []PayMethod
	ShopID   int64
}

// PaymentByPublic resolves a payment public_id inside the shop.
func (s *Store) PaymentByPublic(ctx context.Context, q DBTX, shopID int64, publicID string) (PaymentForRefund, error) {
	if q == nil {
		q = s.Pool
	}
	var p PaymentForRefund
	var amt string
	err := q.QueryRow(ctx, `
		SELECT id, public_id::text, kind, order_id, member_id, amount::text
		FROM payments WHERE shop_id=$1 AND public_id=$2::uuid`, shopID, publicID).
		Scan(&p.ID, &p.PublicID, &p.Kind, &p.OrderID, &p.MemberID, &amt)
	if err != nil {
		return p, err
	}
	p.ShopID = shopID
	p.Amount, err = money.FromString(amt)
	if err != nil {
		return p, err
	}
	rows, err := q.Query(ctx, `
		SELECT method, amount::text FROM payment_entries
		WHERE shop_id=$1 AND payment_id=$2 ORDER BY seq`, shopID, p.ID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Entries = make([]PayMethod, 0)
	for rows.Next() {
		var e PayMethod
		if err := rows.Scan(&e.Method, &e.Amount); err != nil {
			return p, err
		}
		p.Entries = append(p.Entries, e)
	}
	return p, rows.Err()
}

// PayOrder executes design/06 F-01 stage D in one transaction: row-lock the
// order with optimistic version, validate the state machine and the combined
// amount, insert the payment + entries, conditionally deduct member balance,
// move to paid WITHOUT writing paid_amount, and record audit + outbox events.
func (s *Store) PayOrder(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, orderPub, idemKey, shiftIDRaw string, entries []PayMethod) (Payment, Order, error) {
	var out Payment
	var order Order

	// a. Row lock with shop + version guard (version read first via detail).
	order, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return out, order, err
	}
	var status, totalText string
	var version int
	var memberID sql.NullInt64
	if err := tx.QueryRow(ctx, `
		SELECT status, total_amount::text, version, member_id FROM orders
		WHERE shop_id=$1 AND id=$2 FOR UPDATE`, shopID, order.ID).
		Scan(&status, &totalText, &version, &memberID); err != nil {
		return out, order, err
	}
	// b. State machine.
	if err := domorder.CanOrderTransition(status, domorder.StatusPaid); err != nil {
		return out, order, errInvalidState
	}
	// c. Combined methods sum must equal the immutable payable snapshot.
	payable, err := money.FromString(totalText)
	if err != nil {
		return out, order, err
	}
	sum := money.Zero()
	balanceAmount := money.Zero()
	for _, e := range entries {
		m, err := money.FromString(e.Amount)
		if err != nil || !m.IsPositive() {
			return out, order, apierrors.InvalidArgument
		}
		if e.Method == "balance" {
			balanceAmount = balanceAmount.Add(m)
		}
		sum = sum.Add(m)
	}
	if sum.String() != payable.String() || len(entries) == 0 {
		return out, order, errUnprocessable
	}
	// Balance payment requires the order to carry a member.
	if balanceAmount.IsPositive() && !memberID.Valid {
		return out, order, errUnprocessable
	}

	// d. Insert payment + entries. sale must carry order_id; status succeeded.
	var payID int64
	var shiftID any
	if shiftIDRaw != "" {
		shiftID = shiftIDRaw
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO payments (shop_id,kind,order_id,shift_id,operator_id,status,amount,idempotency_key)
		VALUES ($1,'sale',$2,$3,$4,'succeeded',$5,$6) RETURNING id, public_id::text`,
		shopID, order.ID, shiftID, operatorID, payable.String(), idemKey).Scan(&payID, &out.PublicID)
	if err != nil {
		return out, order, err
	}
	for i, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_entries (shop_id,payment_id,seq,method,amount)
			VALUES ($1,$2,$3,$4,$5::numeric)`, shopID, payID, i+1, e.Method, e.Amount); err != nil {
			return out, order, err
		}
	}

	// e. Balance deduction: conditional update never writes a negative balance.
	if balanceAmount.IsPositive() {
		var after string
		err := tx.QueryRow(ctx, `
			UPDATE member_balances SET balance=balance-$3::numeric, version=version+1, updated_at=now()
			WHERE shop_id=$1 AND member_id=$2 AND balance >= $3::numeric
			RETURNING balance::text`, shopID, memberID.Int64, balanceAmount.String()).Scan(&after)
		if err != nil {
			return out, order, errInsufficient
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO balance_ledger (shop_id,member_id,delta,balance_after,reason,ref_type,ref_id)
			VALUES ($1,$2,$3::numeric,$4::numeric,'payment','payment',$5)`,
			shopID, memberID.Int64, "-"+balanceAmount.String(), after, out.PublicID); err != nil {
			return out, order, err
		}
	}

	// g. Move to paid; NO paid_amount write (ADR-0014).
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status='paid', version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND status='pending_payment'`, shopID, order.ID)
	if err != nil {
		return out, order, err
	}
	if tag.RowsAffected() == 0 {
		return out, order, errVersion
	}
	// ADR-0022: settlement releases both the order-side card binding and the
	// staff holding in the same transaction. No-op when no card was bound.
	if err := releaseOrderCard(ctx, tx, shopID, operatorID, order.ID, order.PublicID, "settled"); err != nil {
		return out, order, err
	}

	if err := appendOrderAudit(ctx, tx, shopID, operatorID, "order_paid", order.PublicID, nil); err != nil {
		return out, order, err
	}
	if err := appendOutbox(ctx, tx, shopID, "order", order.PublicID, "order.paid", nil); err != nil {
		return out, order, err
	}
	if err := appendOutbox(ctx, tx, shopID, "payment", "", "payment.succeeded", nil); err != nil {
		return out, order, err
	}
	if err := appendOutbox(ctx, tx, shopID, "shop", "", "shop.summary_dirty", nil); err != nil {
		return out, order, err
	}

	// Kind/status/amount are fixed by this write; read them back in-tx.
	err = tx.QueryRow(ctx, `
		SELECT kind, status, amount::text, created_at FROM payments WHERE id=$1`, payID).
		Scan(&out.Kind, &out.Status, &out.Amount, &out.CreatedAt)
	if err != nil {
		return out, order, err
	}
	out.CreatedAt = utc(out.CreatedAt)
	out.Entries = entries
	order, err = s.OrderByPublic(ctx, tx, shopID, orderPub)
	return out, order, err
}

// TopupPayment records a member topup as kind='topup' with no order_id, in the
// same transaction as the balance credit. operator is the logged-in user.
func (s *Store) TopupPayment(ctx context.Context, tx pgx.Tx, shopID, memberID, operatorID int64, amount string, idemKey, shiftIDRaw string, entries []PayMethod) (Payment, error) {
	var out Payment
	if _, err := tx.Exec(ctx, `
		INSERT INTO member_balances (shop_id,member_id,balance) VALUES ($1,$2,0)
		ON CONFLICT (shop_id,member_id) DO NOTHING`, shopID, memberID); err != nil {
		return out, err
	}
	var after string
	if err := tx.QueryRow(ctx, `
		UPDATE member_balances SET balance=balance+$3::numeric, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND member_id=$2 RETURNING balance::text`, shopID, memberID, amount).Scan(&after); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO balance_ledger (shop_id,member_id,delta,balance_after,reason,ref_type,ref_id)
		VALUES ($1,$2,$3::numeric,$4::numeric,'topup','topup','')`, shopID, memberID, amount, after); err != nil {
		return out, err
	}
	var shiftID any
	if shiftIDRaw != "" {
		shiftID = shiftIDRaw
	}
	var payID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO payments (shop_id,kind,member_id,shift_id,operator_id,status,amount,idempotency_key)
		VALUES ($1,'topup',$2,$3,$4,'succeeded',$5,$6) RETURNING id, public_id::text`,
		shopID, memberID, shiftID, operatorID, amount, idemKey).Scan(&payID, &out.PublicID)
	if err != nil {
		return out, err
	}
	for i, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_entries (shop_id,payment_id,seq,method,amount)
			VALUES ($1,$2,$3,$4,$5::numeric)`, shopID, payID, i+1, e.Method, e.Amount); err != nil {
			return out, err
		}
	}
	if err := tx.QueryRow(ctx, `
		SELECT kind, status, amount::text, created_at FROM payments WHERE id=$1`, payID).
		Scan(&out.Kind, &out.Status, &out.Amount, &out.CreatedAt); err != nil {
		return out, err
	}
	out.CreatedAt = utc(out.CreatedAt)
	out.Entries = entries
	if err := appendOutbox(ctx, tx, shopID, "payment", out.PublicID, "payment.succeeded", nil); err != nil {
		return out, err
	}
	return out, nil
}

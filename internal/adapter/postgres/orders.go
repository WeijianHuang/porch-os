package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/WeijianHuang/porch-os/internal/domain/money"
	domorder "github.com/WeijianHuang/porch-os/internal/domain/order"
	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Order / OrderLine are flat, scenario-specific DTOs (DA-01): the detail
// projection carries its lines but no entity graph, and derived amounts are
// computed from immutable records on read, never persisted (ADR-0014).
type Order struct {
	PublicID         string            `json:"public_id"`
	OrderNo          string            `json:"order_no"`
	Status           string            `json:"status"`
	MemberID         *string           `json:"member_id"`
	ShiftID          *string           `json:"shift_id"`
	OperatorID       int64             `json:"operator_id"`
	TotalAmount      string            `json:"total_amount"`
	DiscountAmount   string            `json:"discount_amount"`
	Version          int               `json:"version"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Card             *OrderCardRef     `json:"card"`
	CardBinding      *OrderCardBinding `json:"card_binding_history,omitempty"`
	Room             *OrderRoomRef     `json:"room"`
	ID               int64             `json:"-"`
	Lines            []OrderLine       `json:"lines,omitempty"`
	PaidAmount       string            `json:"paid_amount,omitempty"`
	RefundedAmount   string            `json:"refunded_amount,omitempty"`
	RefundableAmount string            `json:"refundable_amount,omitempty"`
}

// OrderCardRef is the active card binding projection. A released binding is
// returned as Card=nil plus CardBinding, so callers never mistake a historical
// card for a currently held one (ADR-0022 §1).
type OrderCardRef struct {
	PublicID string `json:"public_id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
}

type OrderCardBinding struct {
	Card       OrderCardRef `json:"card"`
	ReleasedAt time.Time    `json:"released_at"`
	Reason     string       `json:"reason"`
}

// OrderRoomRef is read from the current rooms row; ADR-0022 deliberately does
// not snapshot room code/name on the order.
type OrderRoomRef struct {
	PublicID string `json:"public_id"`
	Floor    int    `json:"floor"`
	Code     string `json:"code"`
	Name     string `json:"name"`
}

type OrderLine struct {
	LineNo           int    `json:"line_no"`
	CatalogItemID    string `json:"catalog_item_id"`
	ItemNameSnapshot string `json:"item_name_snapshot"`
	Quantity         string `json:"quantity"`
	UnitPrice        string `json:"unit_price"`
	Amount           string `json:"amount"`
}

// GetCursor enables keyset pagination for ListOrders. It is exported so the
// http layer's pageResponse type assertion can see it across packages; an
// unexported method can never satisfy an interface declared in another package.
func (v Order) GetCursor() (time.Time, int64) { return v.CreatedAt, v.ID }

const orderCols = `public_id::text, order_no, status,
  (SELECT public_id::text FROM members WHERE members.id=orders.member_id),
  (SELECT public_id::text FROM shifts WHERE shifts.id=orders.shift_id),
  operator_id, total_amount::text, discount_amount::text, version, created_at, updated_at, id,
  card_released_at, card_release_reason,
  (SELECT public_id::text FROM id_cards WHERE id_cards.id=orders.card_id),
  (SELECT code FROM id_cards WHERE id_cards.id=orders.card_id),
  (SELECT name FROM id_cards WHERE id_cards.id=orders.card_id),
  (SELECT public_id::text FROM rooms WHERE rooms.id=orders.room_id),
  (SELECT floor FROM rooms WHERE rooms.id=orders.room_id),
  (SELECT code FROM rooms WHERE rooms.id=orders.room_id),
  (SELECT name FROM rooms WHERE rooms.id=orders.room_id)`

func scanOrder(row pgx.Row) (Order, error) {
	var v Order
	var member, shift sql.NullString
	var releasedAt sql.NullTime
	var releaseReason sql.NullString
	var cardPub, cardCode, cardName sql.NullString
	var roomPub, roomCode, roomName sql.NullString
	var roomFloor sql.NullInt64
	err := row.Scan(&v.PublicID, &v.OrderNo, &v.Status, &member, &shift, &v.OperatorID,
		&v.TotalAmount, &v.DiscountAmount, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.ID,
		&releasedAt, &releaseReason, &cardPub, &cardCode, &cardName,
		&roomPub, &roomFloor, &roomCode, &roomName)
	if err != nil {
		return v, err
	}
	if member.Valid {
		v.MemberID = &member.String
	}
	if shift.Valid {
		v.ShiftID = &shift.String
	}
	// cardId is non-null whenever cardPub is; a released binding is projected
	// as history only, so a settled order never advertises a usable card.
	if cardPub.Valid {
		ref := OrderCardRef{PublicID: cardPub.String, Code: cardCode.String, Name: cardName.String}
		if releasedAt.Valid {
			v.CardBinding = &OrderCardBinding{
				Card:       ref,
				ReleasedAt: utc(releasedAt.Time),
				Reason:     releaseReason.String,
			}
		} else {
			v.Card = &ref
		}
	}
	if roomPub.Valid {
		v.Room = &OrderRoomRef{
			PublicID: roomPub.String,
			Floor:    int(roomFloor.Int64),
			Code:     roomCode.String,
			Name:     roomName.String,
		}
	}
	v.CreatedAt, v.UpdatedAt = utc(v.CreatedAt), utc(v.UpdatedAt)
	return v, nil
}

var (
	errInvalidState  = apierrors.New("PRECONDITION_FAILED", "单据状态不允许该操作")
	errVersion       = apierrors.New("VERSION_CONFLICT", "并发冲突，请重读后重试")
	errUnprocessable = apierrors.New("UNPROCESSABLE", "金额校验不通过")
	errConflict      = apierrors.New("CONFLICT", "业务规则冲突")
	errInsufficient  = apierrors.New("INSUFFICIENT_BALANCE", "余额不足，整笔拒绝")
)

// PriceLineInput is one full-cart line of a preview request (ADR-0019 §2).
// catalog_item_id is the item's public_id; the client never supplies a price.
type PriceLineInput struct {
	CatalogItemID string `json:"catalog_item_id"`
	Quantity      string `json:"quantity"`
}

// PricePreview is the read-only quote shared with submit. It resolves each
// item's effective price from the shop override (整价替换) or base price,
// rejects unavailable items, and allocates merged adjustments to lines.
// Nothing is written: no order, no number, no transaction, no idempotency.
func (s *Store) PricePreview(ctx context.Context, shopID int64, in []PriceLineInput, adjustments []money.Money) (domorder.QuoteResult, error) {
	var result domorder.QuoteResult
	if len(in) == 0 {
		return result, apierrors.InvalidArgument
	}
	qlines := make([]domorder.QuoteLine, 0, len(in))
	meta := make([]domorder.QuoteLineResult, 0, len(in))
	for i, l := range in {
		if l.CatalogItemID == "" {
			return result, apierrors.InvalidArgument
		}
		qty, err := decimal.NewFromString(l.Quantity)
		if err != nil || !qty.IsPositive() {
			return result, apierrors.InvalidArgument
		}
		// Effective price and availability: an override row wins over the
		// global base price / active flag (ADR-0019 §2). One query per line,
		// no N+1 into other aggregates, and no write of any kind.
		var price, name string
		var available bool
		err = s.Pool.QueryRow(ctx, `
			SELECT c.name, COALESCE(o.price, c.base_price)::text,
			       COALESCE(o.available, c.active)
			FROM catalog_items c
			LEFT JOIN shop_catalog_overrides o
			  ON o.catalog_item_id=c.id AND o.shop_id=$1
			WHERE c.public_id=$2::uuid`, shopID, l.CatalogItemID).
			Scan(&name, &price, &available)
		if err != nil {
			return result, err
		}
		if !available {
			return result, apierrors.WithDetails(apierrors.InvalidArgument,
				apierrors.Detail{Field: "lines[" + strconv.Itoa(i) + "]", Reason: "item_unavailable"})
		}
		unit, err := money.FromString(price)
		if err != nil {
			return result, err
		}
		amount, err := domorder.LineAmount(unit, qty)
		if err != nil {
			return result, apierrors.InvalidArgument
		}
		qlines = append(qlines, domorder.QuoteLine{LineNo: i + 1, Amount: amount})
		meta = append(meta, domorder.QuoteLineResult{
			LineNo:           i + 1,
			CatalogItemID:    l.CatalogItemID,
			ItemNameSnapshot: name,
			Quantity:         qty.String(),
		})
	}
	result, err := domorder.Quote(qlines, adjustments)
	if err != nil {
		return result, errUnprocessable
	}
	for i := range result.Lines {
		result.Lines[i].CatalogItemID = meta[i].CatalogItemID
		result.Lines[i].ItemNameSnapshot = meta[i].ItemNameSnapshot
		result.Lines[i].Quantity = meta[i].Quantity
	}
	return result, nil
}

// OrderByPublic resolves a public_id to its internal id within a shop.
func (s *Store) OrderByPublic(ctx context.Context, q DBTX, shopID int64, publicID string) (Order, error) {
	if q == nil {
		q = s.Pool
	}
	v, err := scanOrder(q.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE shop_id=$1 AND public_id=$2::uuid AND deleted_at IS NULL`, shopID, publicID))
	return v, err
}

// OrderDetail returns the order plus its immutable lines and derived amounts
// (paid / refunded / refundable) aggregated from succeeded records.
func (s *Store) OrderDetail(ctx context.Context, q DBTX, shopID int64, publicID string) (Order, error) {
	if q == nil {
		q = s.Pool
	}
	v, err := s.OrderByPublic(ctx, q, shopID, publicID)
	if err != nil {
		return v, err
	}
	rows, err := q.Query(ctx, `
		SELECT line_no, catalog_item_public::text, item_name_snapshot, quantity::text, unit_price::text, amount::text
		FROM (SELECT l.line_no, c.public_id AS catalog_item_public, l.item_name_snapshot,
		             l.quantity, l.unit_price, l.amount
		      FROM order_lines l JOIN catalog_items c ON c.id=l.catalog_item_id
		      WHERE l.shop_id=$1 AND l.order_id=$2) x
		ORDER BY line_no`, shopID, v.ID)
	if err != nil {
		return v, err
	}
	v.Lines = make([]OrderLine, 0)
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.LineNo, &l.CatalogItemID, &l.ItemNameSnapshot, &l.Quantity, &l.UnitPrice, &l.Amount); err != nil {
			rows.Close()
			return v, err
		}
		v.Lines = append(v.Lines, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return v, err
	}
	rows.Close()
	paid, refunded, err := s.OrderDerived(ctx, q, shopID, v.ID)
	if err != nil {
		return v, err
	}
	d := domorder.DeriveAmounts(paid, refunded)
	v.PaidAmount, v.RefundedAmount, v.RefundableAmount = d.Paid.String(), d.Refunded.String(), d.Refundable.String()
	return v, nil
}

// OrderDerived computes paid - refunded from succeeded rows only, and filters
// refunds to kind='sale' so a topup refund never reduces an order's balance.
func (s *Store) OrderDerived(ctx context.Context, q DBTX, shopID, orderID int64) (money.Money, money.Money, error) {
	if q == nil {
		q = s.Pool
	}
	var paidText, refundedText string
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount),0)::text FROM payments
		WHERE shop_id=$1 AND order_id=$2 AND kind='sale' AND status='succeeded'`, shopID, orderID).Scan(&paidText); err != nil {
		return money.Zero(), money.Zero(), err
	}
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount),0)::text FROM refunds
		WHERE shop_id=$1 AND order_id=$2 AND kind='sale' AND status='succeeded'`, shopID, orderID).Scan(&refundedText); err != nil {
		return money.Zero(), money.Zero(), err
	}
	paid, err := money.FromString(paidText)
	if err != nil {
		return money.Zero(), money.Zero(), err
	}
	refunded, err := money.FromString(refundedText)
	return paid, refunded, err
}

// ListOrders is a keyset list; status is an optional whitelisted filter.
func (s *Store) ListOrders(ctx context.Context, q DBTX, shopID int64, limit int, cursorTime *time.Time, cursorID int64, status string) ([]Order, Page, error) {
	if q == nil {
		q = s.Pool
	}
	b := squirrel.Select(orderCols).From("orders").
		Where("shop_id = ?", shopID).Where("deleted_at IS NULL").
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
	out := make([]Order, 0, limit+1)
	for rows.Next() {
		v, err := scanOrder(rows)
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

// CreateOrder writes an order in draft with an explicit next_order_no value.
func (s *Store) CreateOrder(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, memberID *int64, shiftID *int64) (Order, error) {
	var orderNo string
	if err := tx.QueryRow(ctx, `SELECT next_order_no($1)`, shopID).Scan(&orderNo); err != nil {
		return Order{}, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO orders (shop_id, order_no, status, member_id, shift_id, operator_id)
		VALUES ($1,$2,'draft',$3,$4,$5) RETURNING id`, shopID, orderNo, memberID, shiftID, operatorID).Scan(&id); err != nil {
		return Order{}, err
	}
	v, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE id=$1`, id))
	if err != nil {
		return v, err
	}
	if err := appendOrderAudit(ctx, tx, shopID, operatorID, "order_created", v.PublicID, nil); err != nil {
		return v, err
	}
	return v, appendOutbox(ctx, tx, shopID, "order", v.PublicID, "order.created", nil)
}

// AddOrderLine snapshots the catalog item name and effective (overridden)
// price at order time. draft → open is the only allowed state move here.
func (s *Store) AddOrderLine(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, orderPub, itemPub, quantity string) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if err := domorder.CanOrderTransition(v.Status, domorder.StatusOpen); err != nil {
		return v, errInvalidState
	}
	qty, err := decimal.NewFromString(quantity)
	if err != nil || !qty.IsPositive() {
		return v, apierrors.InvalidArgument
	}
	var itemID int64
	var name, price string
	err = tx.QueryRow(ctx, `
		SELECT c.id, c.name, COALESCE(o.price, c.base_price)::text
		FROM catalog_items c
		LEFT JOIN shop_catalog_overrides o
		  ON o.catalog_item_id=c.id AND o.shop_id=$1 AND o.available
		WHERE c.public_id=$2::uuid`, shopID, itemPub).Scan(&itemID, &name, &price)
	if err != nil {
		return v, err
	}
	unitPrice, err := money.FromString(price)
	if err != nil {
		return v, err
	}
	amount, err := domorder.LineAmount(unitPrice, qty)
	if err != nil {
		return v, apierrors.InvalidArgument
	}
	var next int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(line_no),0)+1 FROM order_lines WHERE shop_id=$1 AND order_id=$2`, shopID, v.ID).Scan(&next); err != nil {
		return v, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status='open', version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3 AND status='draft'`, shopID, v.ID, v.Version)
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() == 0 {
		return v, errVersion
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_lines (shop_id,order_id,line_no,catalog_item_id,item_name_snapshot,quantity,unit_price,amount)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, shopID, v.ID, next, itemID, name, qty.String(), unitPrice.String(), amount.String()); err != nil {
		return v, err
	}
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// RemoveOrderLine deletes a draft/open line and renumbers the rest densely.
func (s *Store) RemoveOrderLine(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, orderPub string, lineNo int) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if v.Status != domorder.StatusOpen && v.Status != domorder.StatusDraft {
		return v, errInvalidState
	}
	tag, err := tx.Exec(ctx, `DELETE FROM order_lines WHERE shop_id=$1 AND order_id=$2 AND line_no=$3`, shopID, v.ID, lineNo)
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() == 0 {
		return v, apierrors.NotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE order_lines SET line_no=line_no-1
		WHERE shop_id=$1 AND order_id=$2 AND line_no>$3`, shopID, v.ID, lineNo); err != nil {
		return v, err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET version=version+1, updated_at=now() WHERE shop_id=$1 AND id=$2`, shopID, v.ID); err != nil {
		return v, err
	}
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// Adjustment is a whole-order discount entry in the structured form defined
// by ADR-0020: type (coupon/promotion/rounding/manual/other), a bill title,
// an optional long description and a coupon source_id (coupon only). It is
// persisted in order_price_adjustments (ADR-0021) and allocated to lines by
// the Quote pure function (ADR-0019).
type Adjustment struct {
	Seq         int    `json:"seq"`
	Type        string `json:"type"`
	Amount      string `json:"amount"`
	SourceID    *int64 `json:"source_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// ValidateAdjustment enforces the ADR-0020 shape: type is one of five values,
// amount is non-negative, title is non-empty, and source_id is present only
// for coupon (and then must be a positive internal id). Coupon existence is
// NOT checked here — the coupon domain does not exist yet.
func ValidateAdjustment(a Adjustment) error {
	switch a.Type {
	case "coupon", "promotion", "rounding", "manual", "other":
	default:
		return apierrors.InvalidArgument
	}
	if strings.TrimSpace(a.Title) == "" {
		return apierrors.InvalidArgument
	}
	if a.SourceID != nil && *a.SourceID <= 0 {
		return apierrors.InvalidArgument
	}
	if a.Type == "coupon" && a.SourceID == nil {
		return apierrors.InvalidArgument
	}
	if a.Type != "coupon" && a.SourceID != nil {
		return apierrors.InvalidArgument
	}
	return nil
}

// SubmitOrder re-prices the order: recomputes the line total, validates the
// client-supplied expected amount, allocates discounts by line amount and
// moves open → pending_payment in one transaction.
func (s *Store) SubmitOrder(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, orderPub string, version int, adjustments []Adjustment, expectedTotal string) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if err := domorder.CanOrderTransition(v.Status, domorder.StatusPendingPayment); err != nil {
		return v, errInvalidState
	}
	if version != v.Version {
		return v, errVersion
	}
	// ADR-0022: submit requires an active card binding and a bound room.
	if err := submitContextCheck(ctx, tx, shopID, v.ID); err != nil {
		return v, err
	}
	// Read line snapshots for pricing (source of truth, never client totals).
	rows, err := tx.Query(ctx, `
		SELECT line_no, amount::text FROM order_lines
		WHERE shop_id=$1 AND order_id=$2 ORDER BY line_no`, shopID, v.ID)
	if err != nil {
		return v, err
	}
	type lineAmount struct {
		no  int
		amt money.Money
	}
	var lines []lineAmount
	lineTotal := money.Zero()
	for rows.Next() {
		var no int
		var amt string
		if err := rows.Scan(&no, &amt); err != nil {
			rows.Close()
			return v, err
		}
		m, err := money.FromString(amt)
		if err != nil {
			rows.Close()
			return v, err
		}
		lines = append(lines, lineAmount{no: no, amt: m})
		lineTotal = lineTotal.Add(m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return v, err
	}
	rows.Close()
	if len(lines) == 0 {
		return v, errUnprocessable
	}

	discount := money.Zero()
	for _, a := range adjustments {
		if a.Seq <= 0 || a.Amount == "" {
			return v, apierrors.InvalidArgument
		}
		m, err := money.FromString(a.Amount)
		if err != nil || m.IsNegative() {
			return v, apierrors.InvalidArgument
		}
		discount = discount.Add(m)
	}
	total := lineTotal.Sub(discount)
	if total.IsNegative() {
		return v, errUnprocessable
	}
	clientTotal, err := money.FromString(expectedTotal)
	if err != nil || clientTotal.String() != total.String() {
		return v, errUnprocessable
	}

	// Persist adjustments at order level (ADR-0020/0021): reason is structured
	// as type + title + description, with source_id reserved for coupons.
	for _, a := range adjustments {
		if a.Seq <= 0 {
			return v, apierrors.InvalidArgument
		}
		if err := ValidateAdjustment(a); err != nil {
			return v, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_price_adjustments
			  (shop_id,order_id,seq,type,amount,source_id,title,description)
			VALUES ($1,$2,$3,$4,$5::numeric,$6,$7,$8)`,
			shopID, v.ID, a.Seq, a.Type, a.Amount, a.SourceID, a.Title, a.Description); err != nil {
			return v, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status='pending_payment', total_amount=$4::numeric,
		  discount_amount=$5::numeric, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3 AND status='open'`, shopID, v.ID, v.Version, total.String(), discount.String())
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() == 0 {
		return v, errVersion
	}
	if err := appendOrderAudit(ctx, tx, shopID, operatorID, "order_submitted", v.PublicID, nil); err != nil {
		return v, err
	}
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// CancelOrder moves an unpaid order to cancelled (whichever state it is in).
func (s *Store) CancelOrder(ctx context.Context, tx pgx.Tx, shopID, operatorID int64, orderPub string, version int) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if err := domorder.CanOrderTransition(v.Status, domorder.StatusCancelled); err != nil {
		return v, errInvalidState
	}
	if version != v.Version {
		return v, errVersion
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status='cancelled', version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3`, shopID, v.ID, version)
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() == 0 {
		return v, errVersion
	}
	// Cancelling an unpaid order must not leave the card locked (ADR-0022).
	if err := releaseOrderCard(ctx, tx, shopID, operatorID, v.ID, v.PublicID, "cancelled"); err != nil {
		return v, err
	}
	if err := appendOrderAudit(ctx, tx, shopID, operatorID, "order_cancelled", v.PublicID, nil); err != nil {
		return v, err
	}
	if err := appendOutbox(ctx, tx, shopID, "order", v.PublicID, "order.cancelled", nil); err != nil {
		return v, err
	}
	// Re-read inside the transaction so the response carries status=cancelled.
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// SetOrderRefundStatus updates the coarse refund marker inside the refund
// transaction; amounts always come from the records (ADR-0014).
func (s *Store) SetOrderRefundStatus(ctx context.Context, tx pgx.Tx, shopID, orderID int64, status string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status=$3, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND status IN ('paid','completed','partially_refunded')`,
		shopID, orderID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errInvalidState
	}
	return nil
}

// appendOrderAudit writes one operator action. actor_id is the logged-in user.
func appendOrderAudit(ctx context.Context, tx pgx.Tx, shopID, actorID int64, action, targetPub string, payload map[string]any) error {
	b, err := jsonBytes(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (shop_id,actor_id,action,target_type,target_id,payload,created_at)
		VALUES ($1,$2,$3,'order',$4,$5,now())`, shopID, actorID, action, targetPub, b)
	return err
}

// appendOutbox records an event in the same transaction as the business write.
func appendOutbox(ctx context.Context, tx pgx.Tx, shopID int64, aggType, aggID, eventType string, payload map[string]any) error {
	b, err := jsonBytes(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (shop_id,aggregate_type,aggregate_id,event_type,payload,created_at)
		VALUES ($1,$2,$3,$4,$5,now())`, shopID, aggType, aggID, eventType, b)
	return err
}

// jsonBytes is shared by order/payment/refund stores in this package.
func jsonBytes(v map[string]any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}

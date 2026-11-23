package postgres

import (
	"context"
	"database/sql"
	"time"

	apierrors "github.com/WeijianHuang/porch-os/internal/platform/errors"
	"github.com/jackc/pgx/v5"
)

// Order context bindings (ADR-0022): one active card and one room per order.
// A room may be referenced by many active orders at once (several guests in
// one room); there is intentionally no room-level exclusivity check.

var (
	errOrderCardRequired = apierrors.New("ORDER_CARD_REQUIRED", "提交前必须先绑定有效的 ID 卡")
	errOrderRoomRequired = apierrors.New("ORDER_ROOM_REQUIRED", "提交前必须先绑定房间")
	errCardNotHeld       = apierrors.New("CARD_NOT_HELD", "该 ID 卡当前无人持有")
	errCardBoundToOrder  = apierrors.New("CARD_BOUND_TO_ORDER", "该 ID 卡已被另一张活跃订单占用")
	errRoomInactive      = apierrors.New("ROOM_INACTIVE", "该房间已停用，不能绑定")
	errRoomNotFound      = apierrors.New("ROOM_NOT_FOUND", "房间不存在")
	errOrderSettled      = apierrors.New("ORDER_SETTLED", "订单已结算，不能再修改卡、房间或分组")
	errStateUnchanged    = apierrors.New("STATE_UNCHANGED", "目标状态与当前状态一致")
)

func isEditableOrderStatus(status string) bool {
	switch status {
	case "draft", "open", "pending_payment":
		return true
	default:
		return false
	}
}

// assertOrderEditable rejects any order that is already settled or carries a
// successful payment. Context bindings and group membership share this rule.
func assertOrderEditable(ctx context.Context, tx pgx.Tx, shopID, orderID int64, status string) error {
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
	return nil
}

// BindOrderCard binds or replaces the order's single card. The card row is
// locked so concurrent binds of the same card serialise; uq_orders_active_card
// is the final guard.
func (s *Store) BindOrderCard(ctx context.Context, tx pgx.Tx, shopID, actorID int64, orderPub, cardPub string, version int) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if version != v.Version {
		return v, errVersion
	}
	if err := assertOrderEditable(ctx, tx, shopID, v.ID, v.Status); err != nil {
		return v, err
	}

	var cardID int64
	var cardStatus string
	if err := tx.QueryRow(ctx, `
		SELECT id, status FROM id_cards
		WHERE shop_id=$1 AND public_id=$2::uuid FOR UPDATE`, shopID, cardPub).
		Scan(&cardID, &cardStatus); err != nil {
		if err == pgx.ErrNoRows {
			return v, apierrors.New("CARD_NOT_FOUND", "ID 卡不存在")
		}
		return v, err
	}
	if cardStatus != "active" {
		return v, apierrors.New("CARD_INACTIVE", "ID 卡已失效")
	}
	var held bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM card_holdings
		               WHERE shop_id=$1 AND card_id=$2 AND released_at IS NULL)`,
		shopID, cardID).Scan(&held); err != nil {
		return v, err
	}
	if !held {
		return v, errCardNotHeld
	}
	var bound bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM orders
		               WHERE shop_id=$1 AND card_id=$2 AND id<>$3
		                 AND card_released_at IS NULL AND deleted_at IS NULL)`,
		shopID, cardID, v.ID).Scan(&bound); err != nil {
		return v, err
	}
	if bound {
		return v, errCardBoundToOrder
	}

	// Record which card (if any) is being replaced. The order keeps only the
	// latest binding column, so audit/outbox carry the old card for traceability.
	var oldCardPub sql.NullString
	if err := tx.QueryRow(ctx, `
		SELECT c.public_id::text FROM orders o
		LEFT JOIN id_cards c ON c.id=o.card_id AND c.shop_id=o.shop_id
		WHERE o.shop_id=$1 AND o.id=$2`, shopID, v.ID).Scan(&oldCardPub); err != nil {
		return v, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE orders SET card_id=$4, card_released_at=NULL, card_release_reason=NULL,
		  version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3
		  AND status IN ('draft','open','pending_payment')`,
		shopID, v.ID, version, cardID)
	if err != nil {
		return v, mapOrderContextErr(err)
	}
	if tag.RowsAffected() == 0 {
		return v, errVersion
	}
	if oldCardPub.Valid && oldCardPub.String != cardPub {
		if err := appendOrderAudit(ctx, tx, shopID, actorID, "order_card_replaced", v.PublicID,
			map[string]any{"from_card_id": oldCardPub.String, "to_card_id": cardPub}); err != nil {
			return v, err
		}
		if err := appendOutbox(ctx, tx, shopID, "order", v.PublicID, "order.card_replaced",
			map[string]any{"from_card_id": oldCardPub.String, "to_card_id": cardPub}); err != nil {
			return v, err
		}
		return s.OrderByPublic(ctx, tx, shopID, orderPub)
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "order_card_bound", v.PublicID,
		map[string]any{"card_id": cardPub}); err != nil {
		return v, err
	}
	if err := appendOutbox(ctx, tx, shopID, "order", v.PublicID, "order.card_bound",
		map[string]any{"card_id": cardPub}); err != nil {
		return v, err
	}
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// UnbindOrderCard releases only the order-side binding (reason=unbound). The
// staff holding is untouched: a manual unbind is not a return of the card.
func (s *Store) UnbindOrderCard(ctx context.Context, tx pgx.Tx, shopID, actorID int64, orderPub string, version int) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if version != v.Version {
		return v, errVersion
	}
	if err := assertOrderEditable(ctx, tx, shopID, v.ID, v.Status); err != nil {
		return v, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET card_released_at=now(), card_release_reason='unbound',
		  version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3
		  AND card_id IS NOT NULL AND card_released_at IS NULL`,
		shopID, v.ID, version)
	if err != nil {
		return v, err
	}
	if tag.RowsAffected() == 0 {
		return v, errStateUnchanged
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "order_card_unbound", v.PublicID, nil); err != nil {
		return v, err
	}
	if err := appendOutbox(ctx, tx, shopID, "order", v.PublicID, "order.card_unbound", nil); err != nil {
		return v, err
	}
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// BindOrderRoom binds or replaces the order's room. Multiple active orders may
// share the same room; no exclusivity is enforced by design.
func (s *Store) BindOrderRoom(ctx context.Context, tx pgx.Tx, shopID, actorID int64, orderPub, roomPub string, version int) (Order, error) {
	v, err := s.OrderByPublic(ctx, tx, shopID, orderPub)
	if err != nil {
		return v, err
	}
	if version != v.Version {
		return v, errVersion
	}
	if err := assertOrderEditable(ctx, tx, shopID, v.ID, v.Status); err != nil {
		return v, err
	}
	var roomID int64
	var roomStatus string
	if err := tx.QueryRow(ctx, `
		SELECT id, status FROM rooms
		WHERE shop_id=$1 AND public_id=$2::uuid`, shopID, roomPub).
		Scan(&roomID, &roomStatus); err != nil {
		if err == pgx.ErrNoRows {
			return v, errRoomNotFound
		}
		return v, err
	}
	if roomStatus != "active" {
		return v, errRoomInactive
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET room_id=$4, version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND version=$3
		  AND status IN ('draft','open','pending_payment')`,
		shopID, v.ID, version, roomID)
	if err != nil {
		return v, mapOrderContextErr(err)
	}
	if tag.RowsAffected() == 0 {
		return v, errVersion
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "order_room_bound", v.PublicID,
		map[string]any{"room_id": roomPub}); err != nil {
		return v, err
	}
	if err := appendOutbox(ctx, tx, shopID, "order", v.PublicID, "order.room_bound",
		map[string]any{"room_id": roomPub}); err != nil {
		return v, err
	}
	return s.OrderByPublic(ctx, tx, shopID, orderPub)
}

// releaseOrderCard is called in the settlement/cancel transaction. It releases
// both layers: the order-side binding (settled/cancelled) and the card's
// current holding (order_settled/order_cancelled). Conditional updates make a
// replayed payment idempotent; nothing is written when the card was already
// released or never bound.
func releaseOrderCard(ctx context.Context, tx pgx.Tx, shopID, actorID, orderID int64, orderPub, reason string) error {
	releaseReason := "order_" + reason
	var cardID int64
	err := tx.QueryRow(ctx, `
		UPDATE orders SET card_released_at=now(), card_release_reason=$3,
		  version=version+1, updated_at=now()
		WHERE shop_id=$1 AND id=$2 AND card_id IS NOT NULL AND card_released_at IS NULL
		RETURNING card_id`, shopID, orderID, reason).Scan(&cardID)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var cardPub string
	if err := tx.QueryRow(ctx, `SELECT public_id::text FROM id_cards WHERE shop_id=$1 AND id=$2`,
		shopID, cardID).Scan(&cardPub); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE card_holdings SET released_at=now(), release_reason=$3
		WHERE shop_id=$1 AND card_id=$2 AND released_at IS NULL`,
		shopID, cardID, releaseReason)
	if err != nil {
		return err
	}
	if err := appendOrderAudit(ctx, tx, shopID, actorID, "order_card_released", orderPub,
		map[string]any{"card_id": cardPub, "reason": reason}); err != nil {
		return err
	}
	if err := appendOutbox(ctx, tx, shopID, "order", orderPub, "order.card_released",
		map[string]any{"card_id": cardPub, "reason": reason}); err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err := appendCardAudit(ctx, tx, shopID, actorID, "card_released", cardPub,
			map[string]any{"reason": releaseReason, "order_id": orderPub}, nowUTC()); err != nil {
			return err
		}
		if err := appendOutbox(ctx, tx, shopID, "card", cardPub, "card.hold_released",
			map[string]any{"reason": releaseReason, "order_id": orderPub}); err != nil {
			return err
		}
	}
	return nil
}

// SubmitOrder requires an active card binding and a room before moving to
// pending_payment (ADR-0022 §2/§3). Missing context is a 422, not a silent
// default.
func submitContextCheck(ctx context.Context, tx pgx.Tx, shopID, orderID int64) error {
	var cardOK bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM orders o
		  JOIN id_cards c ON c.id=o.card_id AND c.shop_id=o.shop_id
		  WHERE o.shop_id=$1 AND o.id=$2
		    AND o.card_id IS NOT NULL AND o.card_released_at IS NULL
		    AND c.status='active')`, shopID, orderID).Scan(&cardOK); err != nil {
		return err
	}
	if !cardOK {
		return errOrderCardRequired
	}
	var roomID sql.NullInt64
	if err := tx.QueryRow(ctx, `SELECT room_id FROM orders WHERE shop_id=$1 AND id=$2`,
		shopID, orderID).Scan(&roomID); err != nil {
		return err
	}
	if !roomID.Valid {
		return errOrderRoomRequired
	}
	return nil
}

func mapOrderContextErr(err error) error {
	if err == nil {
		return nil
	}
	// 23505 from uq_orders_active_card means another active order already owns
	// the card. 23503 covers a card/room deleted between validation and write.
	if pgErr := apierrors.FromPG(err); pgErr.Code == "CONFLICT" {
		return errCardBoundToOrder
	}
	return err
}

func nowUTC() time.Time { return time.Now().UTC() }

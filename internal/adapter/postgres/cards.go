package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/WeijianHuang/porch-os/internal/domain/card"
)

// CardRef is the projection embedded into board members and staff summaries
// (ADR-0017 §3). It is deliberately small: no status, no history, no version.
type CardRef struct {
	PublicID string `json:"public_id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
}

// Card is the full shop-local card row plus its current holder, if any.
type Card struct {
	CardRef
	Status     string          `json:"status"`
	Version    int             `json:"version"`
	Holder     *CardHolder     `json:"holder"`
	BoundOrder *CardBoundOrder `json:"bound_order"`
}

// CardBoundOrder is the active order currently occupying a card (ADR-0022).
// Released bindings are excluded, so a settled order never shows here.
type CardBoundOrder struct {
	PublicID string `json:"public_id"`
	OrderNo  string `json:"order_no"`
	Status   string `json:"status"`
}

type CardHolder struct {
	PublicID    string    `json:"public_id"`
	DisplayName string    `json:"display_name"`
	HeldFrom    time.Time `json:"held_from"`
}

// CardInput is the create payload. Update uses the same struct plus version.
type CardInput struct {
	Code    string
	Name    string
	Status  string
	Version int
	ActorID int64
}

type HoldInput struct {
	CardPub  string
	StaffPub string
	Reason   string
	ActorID  int64
}

const cardSelect = `
	SELECT c.public_id::text, c.code, c.name, c.status, c.version
	FROM id_cards c`

// ListCards returns every card in the shop with its current holder, folded in
// two queries (DA-01): no per-card lookup.
func (s *Store) ListCards(ctx context.Context, q DBTX, shopID int64) ([]Card, error) {
	if q == nil {
		q = s.Pool
	}
	rows, err := q.Query(ctx, cardSelect+` WHERE c.shop_id=$1 ORDER BY c.code`, shopID)
	if err != nil {
		return nil, err
	}
	out := make([]Card, 0)
	for rows.Next() {
		var v Card
		if err := rows.Scan(&v.PublicID, &v.Code, &v.Name, &v.Status, &v.Version); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	holders, err := cardHoldersByID(ctx, q, shopID, nil)
	if err != nil {
		return nil, err
	}
	bound, err := cardBoundOrders(ctx, q, shopID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if h, ok := holders[out[i].PublicID]; ok {
			out[i].Holder = &h
		}
		if b, ok := bound[out[i].PublicID]; ok {
			out[i].BoundOrder = &b
		}
	}
	return out, nil
}

// cardBoundOrders folds active order occupancy into a map keyed by card
// public id. One card has at most one active binding (partial unique index).
func cardBoundOrders(ctx context.Context, q DBTX, shopID int64) (map[string]CardBoundOrder, error) {
	rows, err := q.Query(ctx, `
		SELECT c.public_id::text, o.public_id::text, o.order_no, o.status
		FROM orders o
		JOIN id_cards c ON c.id=o.card_id AND c.shop_id=o.shop_id
		WHERE o.shop_id=$1 AND o.card_released_at IS NULL AND o.deleted_at IS NULL`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]CardBoundOrder)
	for rows.Next() {
		var cardPub string
		var b CardBoundOrder
		if err := rows.Scan(&cardPub, &b.PublicID, &b.OrderNo, &b.Status); err != nil {
			return nil, err
		}
		out[cardPub] = b
	}
	return out, rows.Err()
}

// cardHoldersByID folds current holdings into a map keyed by card public id.
// staffPub is the key; caller may pass a card public-id filter for a single
// card lookup (nil = all cards in the shop).
func cardHoldersByID(ctx context.Context, q DBTX, shopID int64, filter map[string]bool) (map[string]CardHolder, error) {
	rows, err := q.Query(ctx, `
		SELECT c.public_id::text, st.public_id::text, st.display_name, h.held_from
		FROM card_holdings h
		JOIN id_cards c ON c.id = h.card_id AND c.shop_id = h.shop_id
		JOIN staff st ON st.id = h.staff_id AND st.shop_id = h.shop_id
		WHERE h.shop_id=$1 AND h.released_at IS NULL`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]CardHolder)
	for rows.Next() {
		var cardPub, staffPub, name string
		var held time.Time
		if err := rows.Scan(&cardPub, &staffPub, &name, &held); err != nil {
			return nil, err
		}
		if filter != nil && !filter[cardPub] {
			continue
		}
		out[cardPub] = CardHolder{PublicID: staffPub, DisplayName: name, HeldFrom: held}
	}
	return out, rows.Err()
}

// CurrentCardByStaff returns the card each staff member currently holds,
// keyed by staff public id. Used to project id_card into the board and /staff
// in one extra query instead of one per card (DA-01).
func CurrentCardByStaff(ctx context.Context, q DBTX, shopID int64) (map[string]CardRef, error) {
	rows, err := q.Query(ctx, `
		SELECT st.public_id::text, c.public_id::text, c.code, c.name
		FROM card_holdings h
		JOIN staff st ON st.id = h.staff_id AND st.shop_id = h.shop_id
		JOIN id_cards c ON c.id = h.card_id AND c.shop_id = h.shop_id
		WHERE h.shop_id=$1 AND h.released_at IS NULL`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]CardRef)
	for rows.Next() {
		var staffPub, cardPub, code, name string
		if err := rows.Scan(&staffPub, &cardPub, &code, &name); err != nil {
			return nil, err
		}
		out[staffPub] = CardRef{PublicID: cardPub, Code: code, Name: name}
	}
	return out, rows.Err()
}

// CreateCard inserts a shop-local card. Duplicate code maps to a conflict.
func (s *Store) CreateCard(ctx context.Context, tx pgx.Tx, shopID int64, in CardInput) (Card, error) {
	var out Card
	status := card.StatusActive
	if in.Status != "" {
		status = card.Status(in.Status)
	}
	if !card.IsValidStatus(status) {
		return out, schedErr("INVALID_ARGUMENT", "status must be active or inactive")
	}
	if err := validateCardFields(in.Code, in.Name); err != nil {
		return out, err
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO id_cards (shop_id, code, name, status)
		VALUES ($1,$2,$3,$4)
		RETURNING public_id::text, code, name, status, version`,
		shopID, in.Code, in.Name, string(status)).Scan(
		&out.PublicID, &out.Code, &out.Name, &out.Status, &out.Version)
	if err != nil {
		return out, mapCardErr(err)
	}
	if err := appendCardAudit(ctx, tx, shopID, in.ActorID, "card_created", out.PublicID,
		map[string]any{"code": out.Code, "name": out.Name}, time.Now()); err != nil {
		return out, err
	}
	return out, nil
}

// UpdateCard changes name and/or status with optimistic locking. Deactivating
// a held card is rejected before the write so the caller gets CARD_IN_USE.
func (s *Store) UpdateCard(ctx context.Context, tx pgx.Tx, shopID int64, cardPub string, in CardInput) (Card, error) {
	var out Card
	var id int64
	var status string
	var version int
	if err := tx.QueryRow(ctx, `
		SELECT id, status, version FROM id_cards WHERE shop_id=$1 AND public_id=$2::uuid
		FOR UPDATE`, shopID, cardPub).Scan(&id, &status, &version); err != nil {
		return out, mapCardErr(err)
	}
	if in.Version != version {
		return out, schedErr("VERSION_CONFLICT", "card was changed by another request")
	}
	if in.Status != "" && !card.IsValidStatus(card.Status(in.Status)) {
		return out, schedErr("INVALID_ARGUMENT", "status must be active or inactive")
	}
	if in.Status == string(card.StatusInactive) {
		var held bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM card_holdings
			               WHERE shop_id=$1 AND card_id=$2 AND released_at IS NULL)`,
			shopID, id).Scan(&held); err != nil {
			return out, err
		}
		if err := card.CanDeactivate(held); err != nil {
			return out, cardErr(err)
		}
	}
	name := in.Name
	if name == "" {
		name = out.Name
	}
	if name == "" {
		if err := tx.QueryRow(ctx, `SELECT name FROM id_cards WHERE id=$1`, id).Scan(&name); err != nil {
			return out, err
		}
	}
	newStatus := status
	if in.Status != "" {
		newStatus = in.Status
	}
	if err := tx.QueryRow(ctx, `
		UPDATE id_cards SET name=$1, status=$2, version=version+1, updated_at=now()
		WHERE shop_id=$3 AND id=$4
		RETURNING public_id::text, code, name, status, version`,
		name, newStatus, shopID, id).Scan(
		&out.PublicID, &out.Code, &out.Name, &out.Status, &out.Version); err != nil {
		return out, mapCardErr(err)
	}
	if err := appendCardAudit(ctx, tx, shopID, in.ActorID, "card_updated", cardPub,
		map[string]any{"name": out.Name, "status": out.Status}, time.Now()); err != nil {
		return out, err
	}
	return out, nil
}

// HoldCard gives an active, free card to a staff member who holds no card.
// FOR UPDATE on the card serialises concurrent holds of the same card; the
// partial unique indexes are the final guard for both invariants.
func (s *Store) HoldCard(ctx context.Context, tx pgx.Tx, shopID int64, in HoldInput) (Card, error) {
	var out Card
	var id int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT id, status FROM id_cards WHERE shop_id=$1 AND public_id=$2::uuid
		FOR UPDATE`, shopID, in.CardPub).Scan(&id, &status); err != nil {
		return out, mapCardErr(err)
	}
	var held, staffHas bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM card_holdings
		               WHERE shop_id=$1 AND card_id=$2 AND released_at IS NULL)`,
		shopID, id).Scan(&held); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM card_holdings
		               WHERE shop_id=$1 AND staff_id=(SELECT id FROM staff
		                 WHERE shop_id=$1 AND public_id=$2::uuid) AND released_at IS NULL)`,
		shopID, in.StaffPub).Scan(&staffHas); err != nil {
		return out, err
	}
	if err := card.CanHold(card.Status(status), held, staffHas); err != nil {
		return out, cardErr(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO card_holdings (shop_id, card_id, staff_id, held_from)
		VALUES ($1,$2,(SELECT id FROM staff WHERE shop_id=$1 AND public_id=$3::uuid),now())`,
		shopID, id, in.StaffPub); err != nil {
		return out, mapCardErr(err)
	}
	if err := tx.QueryRow(ctx, cardSelect+` WHERE c.shop_id=$1 AND c.id=$2`,
		shopID, id).Scan(&out.PublicID, &out.Code, &out.Name, &out.Status, &out.Version); err != nil {
		return out, err
	}
	holders, err := cardHoldersByID(ctx, tx, shopID, map[string]bool{out.PublicID: true})
	if err != nil {
		return out, err
	}
	if h, ok := holders[out.PublicID]; ok {
		out.Holder = &h
	}
	if err := appendCardAudit(ctx, tx, shopID, in.ActorID, "card_held", out.PublicID,
		map[string]any{"staff_id": in.StaffPub, "reason": in.Reason}, time.Now()); err != nil {
		return out, err
	}
	return out, nil
}

// ReleaseCard closes the current holding, if any. Releasing a free card is a
// no-op so a double click never fails the operator.
func (s *Store) ReleaseCard(ctx context.Context, tx pgx.Tx, shopID int64, cardPub, reason string, actorID int64) (Card, error) {
	var out Card
	var id int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM id_cards WHERE shop_id=$1 AND public_id=$2::uuid
		FOR UPDATE`, shopID, cardPub).Scan(&id); err != nil {
		return out, mapCardErr(err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE card_holdings SET released_at=now(), release_reason=$1
		WHERE shop_id=$2 AND card_id=$3 AND released_at IS NULL`,
		reason, shopID, id)
	if err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, cardSelect+` WHERE c.shop_id=$1 AND c.id=$2`,
		shopID, id).Scan(&out.PublicID, &out.Code, &out.Name, &out.Status, &out.Version); err != nil {
		return out, err
	}
	if tag.RowsAffected() > 0 {
		if err := appendCardAudit(ctx, tx, shopID, actorID, "card_released", cardPub,
			map[string]any{"reason": reason}, time.Now()); err != nil {
			return out, err
		}
	}
	return out, nil
}

func validateCardFields(code, name string) error {
	if code == "" {
		return schedErr("INVALID_ARGUMENT", "code is required")
	}
	if name == "" {
		return schedErr("INVALID_ARGUMENT", "name is required")
	}
	return nil
}

func cardErr(err error) error {
	switch {
	case errors.Is(err, card.ErrInactive):
		return schedErr("CARD_INACTIVE", "card is inactive")
	case errors.Is(err, card.ErrCardHeld):
		return schedErr("CARD_HELD", "card is held by someone else")
	case errors.Is(err, card.ErrStaffHasCard):
		return schedErr("STAFF_ALREADY_HOLDS_CARD", "staff member already holds a card")
	case errors.Is(err, card.ErrCardInUse):
		return schedErr("CARD_IN_USE", "release the card before deactivating it")
	default:
		return schedErr("INVALID_ARGUMENT", "%s", err.Error())
	}
}

func mapCardErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return schedErr("CARD_NOT_FOUND", "card not found")
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			if pg.ConstraintName == "uq_ch_active_staff" {
				return schedErr("STAFF_ALREADY_HOLDS_CARD", "staff member already holds a card")
			}
			return schedErr("CARD_HELD", "card is held by someone else")
		case "23503":
			return schedErr("STAFF_NOT_FOUND", "staff member not found in this shop")
		}
	}
	return err
}

func appendCardAudit(ctx context.Context, tx pgx.Tx, shopID, actorID int64, action, cardPub string, payload map[string]any, at time.Time) error {
	if actorID == 0 {
		return schedErr("AUDIT_ACTOR_MISSING", "no actor for audit")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (shop_id,actor_id,action,target_type,target_id,payload,created_at)
		VALUES ($1,$2,$3,'card',$4,$5::jsonb,$6)`,
		shopID, actorID, action, cardPub, string(b), at)
	return err
}

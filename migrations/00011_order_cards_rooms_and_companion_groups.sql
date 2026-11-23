-- +goose Up
-- Order context bindings and companion groups (ADR-0022, design/01 §2/§4,
-- design/02 §2/§3, design/06 F-09).
--
-- 1. An order binds at most one ID card and at most one room. Both are
--    nullable while the order is a draft and become mandatory at submit.
--    Card occupancy is tracked by card_released_at, so settlement/cancel can
--    release the card while keeping card_id for history. A partial unique
--    index guarantees one card is bound to at most one active order.
-- 2. A room can be referenced by many orders at the same time (several guests
--    in one room); there is deliberately NO room-level unique index.
-- 3. Companion groups gather multiple already-card-bound orders for a shared
--    checkout. The group stores no money and is not a payment subject; it only
--    holds membership and orchestration state.
--
-- Every new table/column carries shop_id; composite FKs pin rows to the same
-- shop, continuing ADR-0003. Amounts stay numeric(14,2), timestamps timestamptz.

-- Referenced key for the orders.room_id composite FK. rooms (00006) only had
-- (shop_id, code) and public_id, so add (shop_id, id) first.
ALTER TABLE rooms
  ADD CONSTRAINT uq_rooms_shop_id_id UNIQUE (shop_id, id);

ALTER TABLE orders
  ADD COLUMN card_id             bigint,
  ADD COLUMN card_released_at    timestamptz,
  ADD COLUMN card_release_reason text,
  ADD COLUMN room_id             bigint,
  ADD CONSTRAINT fk_orders_card FOREIGN KEY (shop_id, card_id)
    REFERENCES id_cards (shop_id, id),
  ADD CONSTRAINT fk_orders_room FOREIGN KEY (shop_id, room_id)
    REFERENCES rooms (shop_id, id),
  -- Released columns are meaningful only together, and only when a card was
  -- ever bound. An active binding has both release columns NULL.
  ADD CONSTRAINT ck_orders_card_release CHECK (
    (card_id IS NULL AND card_released_at IS NULL AND card_release_reason IS NULL)
    OR (card_id IS NOT NULL AND card_released_at IS NULL AND card_release_reason IS NULL)
    OR (card_id IS NOT NULL AND card_released_at IS NOT NULL AND card_release_reason IS NOT NULL)
  );

-- One card occupies at most one active order. Released rows drop out of the
-- index, so the same card can be bound to the next order after settlement.
CREATE UNIQUE INDEX uq_orders_active_card ON orders (shop_id, card_id)
  WHERE card_id IS NOT NULL AND card_released_at IS NULL;
CREATE INDEX idx_orders_shop_card ON orders (shop_id, card_id);
CREATE INDEX idx_orders_shop_room ON orders (shop_id, room_id);

-- Companion group: a checkout orchestration view over multiple orders. It
-- stores no amount and is not a payment subject (ADR-0022 §6).
CREATE TABLE companion_groups (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  status     text        NOT NULL DEFAULT 'open',
  version    integer     NOT NULL DEFAULT 0,
  created_by bigint      NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_cg_public_id  UNIQUE (public_id),
  CONSTRAINT uq_cg_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT ck_cg_status CHECK (status IN ('open','closed','cancelled'))
);
CREATE INDEX idx_cg_shop_status ON companion_groups (shop_id, status, created_at DESC);

-- Membership history: a member leaves by left_at, never by deleting the row,
-- so "which group was this order in" stays answerable. At most one active
-- membership per order.
CREATE TABLE companion_group_members (
  id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id   bigint      NOT NULL,
  group_id  bigint      NOT NULL,
  order_id  bigint      NOT NULL,
  joined_at timestamptz NOT NULL DEFAULT now(),
  left_at   timestamptz,
  CONSTRAINT fk_cgm_group FOREIGN KEY (shop_id, group_id)
    REFERENCES companion_groups (shop_id, id),
  CONSTRAINT fk_cgm_order FOREIGN KEY (shop_id, order_id)
    REFERENCES orders (shop_id, id),
  CONSTRAINT ck_cgm_span CHECK (left_at IS NULL OR left_at >= joined_at)
);
CREATE UNIQUE INDEX uq_cgm_active_order ON companion_group_members (shop_id, order_id)
  WHERE left_at IS NULL;
CREATE INDEX idx_cgm_shop_group ON companion_group_members (shop_id, group_id)
  WHERE left_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_cgm_shop_group;
DROP INDEX IF EXISTS uq_cgm_active_order;
DROP TABLE IF EXISTS companion_group_members;
DROP INDEX IF EXISTS idx_cg_shop_status;
DROP TABLE IF EXISTS companion_groups;
DROP INDEX IF EXISTS idx_orders_shop_room;
DROP INDEX IF EXISTS idx_orders_shop_card;
DROP INDEX IF EXISTS uq_orders_active_card;
ALTER TABLE orders
  DROP CONSTRAINT IF EXISTS ck_orders_card_release,
  DROP CONSTRAINT IF EXISTS fk_orders_room,
  DROP CONSTRAINT IF EXISTS fk_orders_card,
  DROP COLUMN IF EXISTS room_id,
  DROP COLUMN IF EXISTS card_release_reason,
  DROP COLUMN IF EXISTS card_released_at,
  DROP COLUMN IF EXISTS card_id;
ALTER TABLE rooms DROP CONSTRAINT IF EXISTS uq_rooms_shop_id_id;

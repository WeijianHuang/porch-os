-- +goose Up
-- ID cards and their holding history (ADR-0017 / design/14). A card is a
-- shop-scoped resource held by at most one staff member at a time, and a
-- staff member holds at most one card. Both invariants are enforced by
-- partial unique indexes so concurrent holds cannot be double-booked.
-- Generic multi-shop concept only; no industry rules, no tenant layer.

CREATE TABLE id_cards (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  code       text        NOT NULL,
  name       text        NOT NULL,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_ic_public_id UNIQUE (public_id),
  CONSTRAINT uq_ic_shop_code UNIQUE (shop_id, code),
  CONSTRAINT uq_ic_shop_id   UNIQUE (shop_id, id),
  CONSTRAINT ck_ic_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_ic_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_ic_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_ic_shop_status ON id_cards (shop_id, status, code);

CREATE TABLE card_holdings (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id       bigint      NOT NULL,
  card_id       bigint      NOT NULL,
  staff_id      bigint      NOT NULL,
  held_from     timestamptz NOT NULL DEFAULT now(),
  released_at   timestamptz,
  release_reason text,
  CONSTRAINT fk_ch_card  FOREIGN KEY (shop_id, card_id)
    REFERENCES id_cards (shop_id, id),
  CONSTRAINT fk_ch_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id),
  CONSTRAINT ck_ch_release CHECK (released_at IS NULL OR released_at >= held_from)
);
-- Partial unique indexes: one card has at most one active holder, and one
-- staff member holds at most one card. Expressed as indexes because PostgreSQL
-- does not allow a WHERE clause on a table-level UNIQUE constraint.
CREATE UNIQUE INDEX uq_ch_active_card ON card_holdings (shop_id, card_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX uq_ch_active_staff ON card_holdings (shop_id, staff_id) WHERE released_at IS NULL;
CREATE INDEX idx_ch_shop_staff ON card_holdings (shop_id, staff_id, held_from DESC);
CREATE INDEX idx_ch_shop_card  ON card_holdings (shop_id, card_id, held_from DESC);

-- Permission points (ADR-0017 / design/14): admin+manager full, staff read-only.
INSERT INTO permissions (code, name) VALUES
  ('card.read',  '查看 ID 卡'),
  ('card.write', '维护 ID 卡与持有关系')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code IN ('admin','manager') AND p.code IN ('card.read','card.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code = 'staff' AND p.code = 'card.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions WHERE permission_id IN (SELECT id FROM permissions WHERE code IN ('card.read','card.write'));
DELETE FROM permissions WHERE code IN ('card.read','card.write');
DROP INDEX IF EXISTS idx_ch_shop_card;
DROP INDEX IF EXISTS idx_ch_shop_staff;
DROP TABLE IF EXISTS card_holdings;
DROP INDEX IF EXISTS idx_ic_shop_status;
DROP TABLE IF EXISTS id_cards;

-- +goose Up
-- Rooms and floors (ADR-0011 / design/11). Generic multi-shop concept only;
-- no industry rules, no tenant layer. Isolation stays a single shop_id layer.
-- A room belongs to one shop, has a shop-local room code and a floor number.
-- Floor is a scalar grouping dimension, not an entity (no floors table).

CREATE TABLE rooms (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  floor      integer     NOT NULL,
  code       text        NOT NULL,
  name       text        NOT NULL,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_rooms_public_id UNIQUE (public_id),
  CONSTRAINT uq_rooms_shop_code UNIQUE (shop_id, code),
  CONSTRAINT ck_rooms_floor  CHECK (floor BETWEEN -5 AND 200),
  CONSTRAINT ck_rooms_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_rooms_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_rooms_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_rooms_shop_floor_code ON rooms (shop_id, floor, code);

-- Generic room permission points (ADR-0011 / design/11).
INSERT INTO permissions (code, name) VALUES
  ('room.read', '查看房间'),
  ('room.write', '维护房间')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code IN ('admin','manager') AND p.code IN ('room.read','room.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code = 'staff' AND p.code = 'room.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions WHERE permission_id IN (SELECT id FROM permissions WHERE code IN ('room.read','room.write'));
DELETE FROM permissions WHERE code IN ('room.read','room.write');
DROP INDEX IF EXISTS idx_rooms_shop_floor_code;
DROP TABLE IF EXISTS rooms;

-- +goose Up
-- Queues and staff (ADR-0009 / design/10). Generic multi-shop concepts only;
-- no industry rules, no tenant layer. Isolation stays a single shop_id layer.
-- A queue belongs to one shop and has a shop-local sequence number and name.
-- A staff member belongs to one shop and may serve in many queues of that shop.

CREATE TABLE queues (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  seq        integer     NOT NULL,
  name       text        NOT NULL,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_queues_public_id UNIQUE (public_id),
  CONSTRAINT uq_queues_shop_seq  UNIQUE (shop_id, seq),
  CONSTRAINT uq_queues_shop_name UNIQUE (shop_id, name),
  CONSTRAINT uq_queues_shop_id   UNIQUE (shop_id, id),
  CONSTRAINT ck_queues_seq    CHECK (seq > 0),
  CONSTRAINT ck_queues_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_queues_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_queues_shop_order ON queues (shop_id, seq);

CREATE TABLE staff (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  shop_id      bigint      NOT NULL REFERENCES shops(id),
  code         text        NOT NULL,
  display_name text        NOT NULL,
  status       text        NOT NULL DEFAULT 'active',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  version      integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_staff_public_id UNIQUE (public_id),
  CONSTRAINT uq_staff_shop_code UNIQUE (shop_id, code),
  CONSTRAINT uq_staff_shop_id   UNIQUE (shop_id, id),
  CONSTRAINT ck_staff_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_staff_name   CHECK (length(btrim(display_name)) > 0),
  CONSTRAINT ck_staff_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_staff_shop_name ON staff (shop_id, display_name);

-- Composite foreign keys keep both ends in the same shop at the database layer.
-- It is impossible to attach a staff member from shop A to a queue of shop B.
CREATE TABLE queue_staff (
  shop_id   bigint      NOT NULL,
  queue_id  bigint      NOT NULL,
  staff_id  bigint      NOT NULL,
  joined_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (queue_id, staff_id),
  CONSTRAINT fk_queue_staff_queue FOREIGN KEY (shop_id, queue_id)
              REFERENCES queues (shop_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_queue_staff_staff FOREIGN KEY (shop_id, staff_id)
              REFERENCES staff (shop_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_queue_staff_shop_staff ON queue_staff (shop_id, staff_id, queue_id);

-- +goose Down
DROP TABLE IF EXISTS queue_staff;
DROP TABLE IF EXISTS staff;
DROP TABLE IF EXISTS queues;

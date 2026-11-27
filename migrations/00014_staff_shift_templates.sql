-- +goose Up
-- ADR-0025 / design/17: shop-scoped shift templates (reusable HH:MM time
-- definitions) and a single optional template per staff member. This is NOT
-- the order-domain `shifts` table (one open business shift per shop); the two
-- concepts stay in separate tables with separate endpoints.
-- Isolation stays a single shop_id layer; the composite FK pins same-shop.

CREATE TABLE shift_templates (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  name        text        NOT NULL,
  sort_order  integer     NOT NULL DEFAULT 0,
  start_time  time        NOT NULL,
  end_time    time        NOT NULL,
  check_in_lead_minutes integer NOT NULL DEFAULT 0,
  next_day_flag boolean   NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  version     integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_st_public_id UNIQUE (public_id),
  CONSTRAINT uq_st_shop_name  UNIQUE (shop_id, name),
  CONSTRAINT uq_st_shop_id    UNIQUE (shop_id, id),
  CONSTRAINT ck_st_name CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_st_lead CHECK (check_in_lead_minutes BETWEEN 0 AND 1440)
);
CREATE INDEX idx_st_shop_order ON shift_templates (shop_id, sort_order, name);

ALTER TABLE staff
  ADD COLUMN shift_template_id bigint,
  ADD CONSTRAINT fk_staff_shift_template FOREIGN KEY (shop_id, shift_template_id)
    REFERENCES shift_templates (shop_id, id);
CREATE INDEX idx_staff_shop_shift_template ON staff (shop_id, shift_template_id)
  WHERE shift_template_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_staff_shop_shift_template;
ALTER TABLE staff DROP CONSTRAINT IF EXISTS fk_staff_shift_template;
ALTER TABLE staff DROP COLUMN IF EXISTS shift_template_id;

DROP INDEX IF EXISTS idx_st_shop_order;
DROP TABLE IF EXISTS shift_templates;

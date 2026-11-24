-- +goose Up
-- ADR-0024 / design/10: shop-scoped staff categories (one per staff member)
-- and a nullable grouping column on the global catalog. Both are display /
-- filtering dimensions only; they grant no capability and change no pricing.
-- Isolation stays a single shop_id layer.

CREATE TABLE staff_categories (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  code       text        NOT NULL,
  name       text        NOT NULL,
  sort_order integer     NOT NULL DEFAULT 0,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_sc_public_id UNIQUE (public_id),
  CONSTRAINT uq_sc_shop_code  UNIQUE (shop_id, code),
  CONSTRAINT uq_sc_shop_name  UNIQUE (shop_id, name),
  CONSTRAINT uq_sc_shop_id    UNIQUE (shop_id, id),
  CONSTRAINT ck_sc_code   CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_sc_name   CHECK (length(btrim(name)) > 0),
  CONSTRAINT ck_sc_status CHECK (status IN ('active','inactive'))
);
CREATE INDEX idx_sc_shop_order ON staff_categories (shop_id, status, sort_order, code);

ALTER TABLE staff
  ADD COLUMN category_id bigint,
  ADD CONSTRAINT fk_staff_category FOREIGN KEY (shop_id, category_id)
    REFERENCES staff_categories (shop_id, id);
CREATE INDEX idx_staff_shop_category ON staff (shop_id, category_id)
  WHERE category_id IS NOT NULL;

ALTER TABLE catalog_items
  ADD COLUMN category_id bigint REFERENCES catalog_categories(id);
CREATE INDEX idx_catalog_items_category ON catalog_items (category_id)
  WHERE category_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_catalog_items_category;
ALTER TABLE catalog_items DROP COLUMN IF EXISTS category_id;

DROP INDEX IF EXISTS idx_staff_shop_category;
ALTER TABLE staff DROP CONSTRAINT IF EXISTS fk_staff_category;
ALTER TABLE staff DROP COLUMN IF EXISTS category_id;

DROP INDEX IF EXISTS idx_sc_shop_order;
DROP TABLE IF EXISTS staff_categories;

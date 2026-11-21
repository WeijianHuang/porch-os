-- +goose Up
-- ADR-0018 / design/15: nominal service duration on catalog items. Minutes,
-- 1..1440, only for type='service'; products must keep NULL. CHECK is the DB
-- backstop so direct writes cannot bypass the API. Global catalog (class A),
-- no shop_id; per-shop overrides still cover price/availability only.
-- Existing product rows have NULL and already satisfy the new constraint.

ALTER TABLE catalog_items
  ADD COLUMN duration_minutes integer,
  ADD CONSTRAINT ck_catalog_items_duration CHECK (
    (type = 'product' AND duration_minutes IS NULL) OR
    (type = 'service' AND duration_minutes BETWEEN 1 AND 1440)
  );

-- +goose Down
ALTER TABLE catalog_items
  DROP CONSTRAINT IF EXISTS ck_catalog_items_duration,
  DROP COLUMN IF EXISTS duration_minutes;

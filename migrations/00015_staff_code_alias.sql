-- +goose Up
-- ADR-0026 / design/02 §11: editable staff code and an optional display alias.
-- The shop-scoped code uniqueness from 00003 stays unchanged; only the write
-- path becomes editable. Alias is a display/search label, never an identity.

ALTER TABLE staff
  ADD COLUMN alias text NOT NULL DEFAULT '',
  ADD CONSTRAINT ck_staff_alias CHECK (character_length(btrim(alias)) <= 60);

-- +goose Down
ALTER TABLE staff DROP CONSTRAINT IF EXISTS ck_staff_alias;
ALTER TABLE staff DROP COLUMN IF EXISTS alias;

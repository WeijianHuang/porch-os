-- +goose Up
-- ADR-0027 / design/02 §12: optional gender and contact phone on staff.
-- Gender uses an explicit 'unknown' sentinel (never NULL) and a CHECK instead
-- of a PG ENUM. Phone is a non-unique contact label, not an identity or a
-- notification channel; application-layer format checks run before this CHECK.

ALTER TABLE staff
  ADD COLUMN gender text NOT NULL DEFAULT 'unknown',
  ADD COLUMN phone  text NOT NULL DEFAULT '',
  ADD CONSTRAINT ck_staff_gender CHECK (gender IN ('unknown','male','female')),
  ADD CONSTRAINT ck_staff_phone  CHECK (character_length(btrim(phone)) <= 32);

-- +goose Down
ALTER TABLE staff DROP CONSTRAINT IF EXISTS ck_staff_phone;
ALTER TABLE staff DROP CONSTRAINT IF EXISTS ck_staff_gender;
ALTER TABLE staff DROP COLUMN IF EXISTS phone;
ALTER TABLE staff DROP COLUMN IF EXISTS gender;

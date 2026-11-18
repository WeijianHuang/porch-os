-- +goose Up
-- ADR-0010: current duty state on staff and explicit queue-local ordering.
-- Backfill existing assignments so positions are dense 1..n per queue.

ALTER TABLE staff
  ADD COLUMN duty_status text NOT NULL DEFAULT 'off',
  ADD CONSTRAINT ck_staff_duty CHECK (duty_status IN ('on','off'));

ALTER TABLE queue_staff ADD COLUMN position integer;

UPDATE queue_staff qs
SET position = ranked.rn
FROM (
  SELECT queue_id, staff_id,
         row_number() OVER (PARTITION BY queue_id ORDER BY joined_at, staff_id) AS rn
  FROM queue_staff
) ranked
WHERE qs.queue_id = ranked.queue_id AND qs.staff_id = ranked.staff_id;

ALTER TABLE queue_staff
  ALTER COLUMN position SET NOT NULL,
  ADD CONSTRAINT uq_queue_staff_position UNIQUE (queue_id, position);

CREATE INDEX idx_queue_staff_queue_position ON queue_staff (shop_id, queue_id, position);

-- +goose Down
DROP INDEX IF EXISTS idx_queue_staff_queue_position;
ALTER TABLE queue_staff DROP CONSTRAINT IF EXISTS uq_queue_staff_position;
ALTER TABLE queue_staff DROP COLUMN IF EXISTS position;
ALTER TABLE staff DROP CONSTRAINT IF EXISTS ck_staff_duty;
ALTER TABLE staff DROP COLUMN IF EXISTS duty_status;

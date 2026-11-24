-- +goose Up
-- ADR-0023 / design/02: service-item qualifications and per-line executor staff.
--
-- Qualification is a shop-scoped M:N between a staff member (B class, one shop)
-- and a service catalog item (A class, global). The association row carries
-- shop_id and a composite FK to staff(shop_id, id), so a member of shop A can
-- never be qualified through shop B. catalog_items.type='service' cannot be
-- expressed as a cross-table CHECK; the API validates it on grant, assignment
-- and submit (with data-layer tests covering direct writes).
--
-- Order lines carry the actual executor of a service line. One service line has
-- at most one staff member; product lines carry none. The name snapshot keeps
-- historical orders readable after the staff row is deleted (DeleteStaff clears
-- staff_id in the same transaction but never touches the snapshot).
--
-- No amounts, payments, refunds, card bindings or companion groups change.

CREATE TABLE staff_service_items (
  shop_id         bigint      NOT NULL,
  staff_id        bigint      NOT NULL,
  catalog_item_id bigint      NOT NULL REFERENCES catalog_items(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (staff_id, catalog_item_id),
  CONSTRAINT fk_staff_service_items_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_staff_service_items_shop_item ON staff_service_items (shop_id, catalog_item_id);

ALTER TABLE order_lines
  ADD COLUMN staff_id            bigint,
  ADD COLUMN staff_name_snapshot text,
  ADD CONSTRAINT fk_order_lines_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id),
  ADD CONSTRAINT ck_order_lines_staff_snapshot CHECK (
    staff_id IS NULL OR staff_name_snapshot IS NOT NULL
  );
CREATE INDEX idx_order_lines_shop_staff ON order_lines (shop_id, staff_id)
  WHERE staff_id IS NOT NULL;

-- +goose Down
-- Reverse dependency order: child index/constraints/columns first, then table.
DROP TABLE IF EXISTS staff_service_items;
DROP INDEX IF EXISTS idx_order_lines_shop_staff;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS fk_order_lines_staff;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS ck_order_lines_staff_snapshot;
ALTER TABLE order_lines DROP COLUMN IF EXISTS staff_id;
ALTER TABLE order_lines DROP COLUMN IF EXISTS staff_name_snapshot;

-- +goose Up
-- Order domain: orders, lines, price adjustments, payments, refunds and shifts
-- (ADR-0013 / 0014 / 0016 / 0019, design/01 §2/§5, design/02 §3, design/06 F-01..F-03).
-- Four independent aggregates: Order (Lines + PriceAdjustment), Payment
-- (PaymentEntry), Refund (RefundEntry), Shift. Structures hold IDs only; no
-- entity graph and no ORM. Composite foreign keys pin every child row to its
-- parent's shop_id, so cross-shop attachment is impossible at the DB layer.
--
-- Money is numeric(14,2) with a single CNY currency (no currency column,
-- no float). Timestamps are timestamptz stored in UTC; session TimeZone is
-- fixed to UTC by the pool. Business day and order numbers go through the
-- shared business_date()/next_order_no() functions below (UTC+8 plus the
-- shop's business-day cutoff), matching daily reports, shifts and summaries.
--
-- Amounts derived from immutable records (paid/refunded/refundable) are NOT
-- stored on orders. Coupons are discounts in order_price_adjustments, never a
-- payment channel; method CHECK lists only real funds (cash/balance/card).
-- Shifts allow at most one open shift per shop via a partial unique index.

-- Business-day sequence for order numbers. The sequence supplies the numeric
-- part; business_date() supplies the date part so order_no and daily reports
-- stay on the same business-day basis across cutoff boundaries.
CREATE SEQUENCE order_no_seq AS bigint;

-- Business day: baseline UTC+8 plus the shop's cutoff offset from
-- shop_settings (key='business_day', value->>'cutoff_minutes', default 0).
-- Does not depend on session TimeZone; shops may configure only the cutoff,
-- never the baseline timezone (ADR-0004 / ADR-0014).
-- +goose StatementBegin
CREATE FUNCTION business_date(p_at timestamptz, p_shop_id bigint) RETURNS date
LANGUAGE sql STABLE AS $$
  SELECT ((p_at AT TIME ZONE 'Asia/Shanghai')
          - COALESCE(
              (SELECT (value->>'cutoff_minutes')::integer
               FROM shop_settings
               WHERE shop_id = p_shop_id AND key = 'business_day'), 0) * interval '1 minute'
         )::date;
$$;
-- +goose StatementEnd

-- Order number: date from business_date(), sequence from order_no_seq.
-- orders.order_no has NO literal DEFAULT: INSERT must call next_order_no()
-- explicitly, so a forgotten call fails with NOT NULL instead of splitting
-- the business-day basis (ADR-0014).
-- +goose StatementBegin
CREATE FUNCTION next_order_no(p_shop_id bigint) RETURNS text
LANGUAGE sql AS $$
  SELECT 'SO' || to_char(business_date(now(), p_shop_id), 'YYYYMMDD') || '-'
         || lpad(nextval('order_no_seq')::text, 10, '0');
$$;
-- +goose StatementEnd

-- Order aggregate root. total_amount/discount_amount are the immutable
-- payable snapshot: total = SUM(lines) - SUM(adjustments). No paid_amount
-- column: paid/refunded/refundable are derived from succeeded records.
CREATE TABLE orders (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL REFERENCES shops(id),
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  order_no         text        NOT NULL,
  status           text        NOT NULL DEFAULT 'draft',
  member_id        bigint      REFERENCES members(id),
  shift_id         bigint,
  operator_id      bigint      NOT NULL,
  total_amount     numeric(14,2) NOT NULL DEFAULT 0,
  discount_amount  numeric(14,2) NOT NULL DEFAULT 0,
  version          integer     NOT NULL DEFAULT 0,
  deleted_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_orders_shop_no    UNIQUE (shop_id, order_no),
  CONSTRAINT uq_orders_public_id  UNIQUE (public_id),
  CONSTRAINT uq_orders_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT ck_orders_status     CHECK (status IN
              ('draft','open','pending_payment','paid','completed',
               'cancelled','refunded','partially_refunded')),
  CONSTRAINT ck_orders_amounts    CHECK (total_amount >= 0 AND discount_amount >= 0)
);
CREATE INDEX idx_orders_shop_created ON orders (shop_id, created_at DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX idx_orders_shop_status_created ON orders (shop_id, status, created_at DESC);

CREATE TABLE order_lines (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id              bigint      NOT NULL,
  order_id             bigint      NOT NULL,
  line_no              integer     NOT NULL,
  catalog_item_id      bigint      NOT NULL REFERENCES catalog_items(id),
  item_name_snapshot   text        NOT NULL,
  quantity             numeric(14,3) NOT NULL,
  unit_price           numeric(14,2) NOT NULL,
  amount               numeric(14,2) NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_order_lines_shop_order_line UNIQUE (shop_id, order_id, line_no),
  CONSTRAINT fk_order_lines_order FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_order_lines_amount CHECK (amount >= 0)
);
CREATE INDEX idx_order_lines_shop_order ON order_lines (shop_id, order_id);

-- Whole-order adjustments: the reason is structured (ADR-0020) instead of a
-- free-text reason. type distinguishes coupon / promotion / rounding / manual
-- / other; title is the short label for the customer bill, description holds
-- the long reason. source_id is a coupon-instance ID held as a plain bigint
-- (no FK yet: the coupon table does not exist); the CHECK pins that only a
-- coupon carries one. A composite FK (shop_id, source_id) must be added when
-- the coupon domain lands. Coupons are discounts and never payment entries
-- (ADR-0016). Allocation to lines is done by the pricing pure function.
CREATE TABLE order_price_adjustments (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id      bigint        NOT NULL,
  order_id     bigint        NOT NULL,
  seq          integer       NOT NULL,
  type         text          NOT NULL,
  amount       numeric(14,2) NOT NULL,
  source_id    bigint,
  title        text          NOT NULL,
  description  text,
  created_at   timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT uq_order_price_adjustments_shop_order_seq UNIQUE (shop_id, order_id, seq),
  CONSTRAINT fk_order_price_adjustments_order FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_opa_type CHECK (type IN ('coupon','promotion','rounding','manual','other')),
  CONSTRAINT ck_opa_type_source CHECK (
    (type = 'coupon' AND source_id IS NOT NULL) OR
    (type <> 'coupon' AND source_id IS NULL)
  ),
  CONSTRAINT ck_opa_amount CHECK (amount >= 0),
  CONSTRAINT ck_opa_title CHECK (length(btrim(title)) > 0)
);
CREATE INDEX idx_order_price_adjustments_shop_order ON order_price_adjustments (shop_id, order_id);

-- Payment is an independent aggregate: kind distinguishes sale/topup/other,
-- order_id is nullable and the CHECK pins the combinations (ADR-0013).
-- shift_id/operator_id record the action's own attribution, held as IDs only.
CREATE TABLE payments (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  kind             text        NOT NULL DEFAULT 'sale',
  order_id         bigint,
  member_id        bigint,
  shift_id         bigint,
  operator_id      bigint      NOT NULL,
  status           text        NOT NULL DEFAULT 'pending',
  amount           numeric(14,2) NOT NULL,
  idempotency_key  text        NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_payments_shop_id_id UNIQUE (shop_id, id),
  CONSTRAINT uq_payments_idem       UNIQUE (shop_id, idempotency_key),
  CONSTRAINT fk_payments_order      FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_payments_status     CHECK (status IN ('pending','succeeded','failed')),
  CONSTRAINT ck_payments_kind       CHECK (kind IN ('sale','topup','other')),
  CONSTRAINT ck_payments_kind_ref   CHECK (
    (kind = 'sale'  AND order_id IS NOT NULL) OR
    (kind = 'topup' AND order_id IS NULL AND member_id IS NOT NULL) OR
    (kind = 'other' AND order_id IS NULL)
  ),
  CONSTRAINT ck_payments_amount     CHECK (amount >= 0)
);
CREATE INDEX idx_payments_shop_created  ON payments (shop_id, created_at DESC);
CREATE INDEX idx_payments_shop_shift    ON payments (shop_id, shift_id, created_at DESC);
CREATE INDEX idx_payments_shop_operator ON payments (shop_id, operator_id, created_at DESC);
CREATE INDEX idx_payments_shop_order    ON payments (shop_id, order_id);

-- One payment can be split across real funding channels; the sum equals
-- payments.amount. Coupon is NOT a channel (ADR-0016).
CREATE TABLE payment_entries (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint      NOT NULL,
  payment_id  bigint      NOT NULL,
  seq         integer     NOT NULL,
  method      text        NOT NULL,
  amount      numeric(14,2) NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_payment_entries_shop_payment_seq UNIQUE (shop_id, payment_id, seq),
  CONSTRAINT fk_payment_entries_payment FOREIGN KEY (shop_id, payment_id)
              REFERENCES payments (shop_id, id),
  CONSTRAINT ck_payment_entries_method CHECK (method IN ('cash','balance','card')),
  CONSTRAINT ck_payment_entries_amount CHECK (amount >= 0)
);
CREATE INDEX idx_payment_entries_shop_payment ON payment_entries (shop_id, payment_id);

-- Refund kind mirrors the original payment (sale/topup); order_id is nullable
-- so topup refunds can attach to a member instead (ADR-0016). shift_id /
-- operator_id are this refund action's own attribution, not inherited from the order.
-- Cross-shop refunds are impossible: both composite FKs pin shop_id.
CREATE TABLE refunds (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id          bigint      NOT NULL,
  public_id        uuid        NOT NULL DEFAULT uuidv7(),
  refund_no        text        NOT NULL,
  kind             text        NOT NULL,
  payment_id       bigint      NOT NULL,
  order_id         bigint,
  member_id        bigint,
  shift_id         bigint,
  operator_id      bigint      NOT NULL,
  amount           numeric(14,2) NOT NULL,
  status           text        NOT NULL DEFAULT 'pending',
  reason           text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_refunds_shop_no     UNIQUE (shop_id, refund_no),
  CONSTRAINT uq_refunds_public_id   UNIQUE (public_id),
  CONSTRAINT uq_refunds_shop_id_id  UNIQUE (shop_id, id),
  CONSTRAINT fk_refunds_payment     FOREIGN KEY (shop_id, payment_id)
              REFERENCES payments (shop_id, id),
  CONSTRAINT fk_refunds_order       FOREIGN KEY (shop_id, order_id)
              REFERENCES orders (shop_id, id),
  CONSTRAINT ck_refunds_status      CHECK (status IN ('pending','succeeded','failed')),
  CONSTRAINT ck_refunds_amount      CHECK (amount > 0),
  CONSTRAINT ck_refunds_kind        CHECK (kind IN ('sale','topup')),
  CONSTRAINT ck_refunds_kind_ref    CHECK (
    (kind = 'sale'  AND order_id IS NOT NULL AND member_id IS NULL) OR
    (kind = 'topup' AND order_id IS NULL AND member_id IS NOT NULL)
  )
);
CREATE INDEX idx_refunds_shop_created  ON refunds (shop_id, created_at DESC);
CREATE INDEX idx_refunds_shop_shift    ON refunds (shop_id, shift_id, created_at DESC);
CREATE INDEX idx_refunds_shop_operator ON refunds (shop_id, operator_id, created_at DESC);
CREATE INDEX idx_refunds_shop_payment  ON refunds (shop_id, payment_id);
CREATE INDEX idx_refunds_shop_member   ON refunds (shop_id, member_id, created_at DESC);

-- Refund channel detail is explicitly supplied by the request and validated
-- (never auto-allocated) by the use case (ADR-0014/0016). Sum of entries must
-- equal refunds.amount; per-channel cumulative refund <= original payment.
CREATE TABLE refund_entries (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint        NOT NULL,
  refund_id   bigint        NOT NULL,
  seq         integer       NOT NULL,
  method      text          NOT NULL,
  amount      numeric(14,2) NOT NULL,
  created_at  timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT uq_refund_entries_shop_refund_seq UNIQUE (shop_id, refund_id, seq),
  CONSTRAINT fk_refund_entries_refund FOREIGN KEY (shop_id, refund_id)
              REFERENCES refunds (shop_id, id),
  CONSTRAINT ck_refund_entries_method CHECK (method IN ('cash','balance','card')),
  CONSTRAINT ck_refund_entries_amount CHECK (amount > 0)
);
CREATE INDEX idx_refund_entries_shop_refund ON refund_entries (shop_id, refund_id);

-- Cashier shift. Partial unique index: at most one open shift per shop.
CREATE TABLE shifts (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  shift_no    text        NOT NULL,
  status      text        NOT NULL DEFAULT 'open',
  opened_at   timestamptz NOT NULL DEFAULT now(),
  closed_at   timestamptz,
  CONSTRAINT uq_shifts_shop_no UNIQUE (shop_id, shift_no),
  CONSTRAINT ck_shifts_status  CHECK (status IN ('open','closing','closed'))
);
CREATE UNIQUE INDEX uq_shifts_one_open_per_shop ON shifts (shop_id) WHERE status = 'open';

-- Permission points (ADR-0019 §1): order.read to all roles (so staff can
-- price orders), order.write and member.topup.write to manager + admin only.
-- Same INSERT pattern as 00004 / 00007 / 00008.
INSERT INTO permissions (code, name) VALUES
  ('order.read',          '查看订单与试价'),
  ('order.write',         '维护订单、收款与退款'),
  ('member.topup.write',  '会员充值与充值退款')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code IN ('staff','manager','admin') AND p.code = 'order.read'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code IN ('manager','admin') AND p.code IN ('order.write','member.topup.write')
ON CONFLICT DO NOTHING;

-- +goose Down
-- Reverse dependency order: children before parents, functions/sequence last.
DELETE FROM role_permissions WHERE permission_id IN (
  SELECT id FROM permissions WHERE code IN ('order.read','order.write','member.topup.write'));
DELETE FROM permissions WHERE code IN ('order.read','order.write','member.topup.write');
DROP INDEX IF EXISTS uq_shifts_one_open_per_shop;
DROP TABLE IF EXISTS shifts;
DROP INDEX IF EXISTS idx_refund_entries_shop_refund;
DROP TABLE IF EXISTS refund_entries;
DROP INDEX IF EXISTS idx_refunds_shop_member;
DROP INDEX IF EXISTS idx_refunds_shop_payment;
DROP INDEX IF EXISTS idx_refunds_shop_operator;
DROP INDEX IF EXISTS idx_refunds_shop_shift;
DROP INDEX IF EXISTS idx_refunds_shop_created;
DROP TABLE IF EXISTS refunds;
DROP INDEX IF EXISTS idx_payment_entries_shop_payment;
DROP TABLE IF EXISTS payment_entries;
DROP INDEX IF EXISTS idx_payments_shop_order;
DROP INDEX IF EXISTS idx_payments_shop_operator;
DROP INDEX IF EXISTS idx_payments_shop_shift;
DROP INDEX IF EXISTS idx_payments_shop_created;
DROP TABLE IF EXISTS payments;
DROP INDEX IF EXISTS idx_order_price_adjustments_shop_order;
DROP TABLE IF EXISTS order_price_adjustments;
DROP INDEX IF EXISTS idx_order_lines_shop_order;
DROP TABLE IF EXISTS order_lines;
DROP INDEX IF EXISTS idx_orders_shop_status_created;
DROP INDEX IF EXISTS idx_orders_shop_created;
DROP TABLE IF EXISTS orders;
DROP FUNCTION IF EXISTS next_order_no(bigint);
DROP FUNCTION IF EXISTS business_date(timestamptz, bigint);
DROP SEQUENCE IF EXISTS order_no_seq;

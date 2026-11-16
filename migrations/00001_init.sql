-- +goose Up
-- R1 core schema. PostgreSQL 18.7 only; no views, no triggers, no ENUM types.
-- Isolation is a single shop_id layer (ADR-0003); user_shops is the sole
-- source of visible shops and is many-to-many (ADR-0006).

CREATE TABLE users (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  login         text        NOT NULL,
  password_hash text        NOT NULL,
  status        text        NOT NULL DEFAULT 'active',
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  version       integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_users_login UNIQUE (login),
  CONSTRAINT ck_users_status CHECK (status IN ('active','disabled'))
);

CREATE TABLE roles (
  id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code  text NOT NULL,
  name  text NOT NULL,
  CONSTRAINT uq_roles_code UNIQUE (code)
);

CREATE TABLE permissions (
  id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code  text NOT NULL,
  name  text NOT NULL,
  CONSTRAINT uq_permissions_code UNIQUE (code)
);

CREATE TABLE role_permissions (
  role_id       bigint NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_id bigint NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
  PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE user_roles (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id bigint NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, role_id)
);

CREATE TABLE shops (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  code       text        NOT NULL,
  name       text        NOT NULL,
  status     text        NOT NULL DEFAULT 'active',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version    integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_shops_code UNIQUE (code),
  CONSTRAINT uq_shops_public_id UNIQUE (public_id),
  CONSTRAINT ck_shops_status CHECK (status IN ('active','inactive'))
);

-- No timezone column: the baseline timezone is a global constant (ADR-0004).
CREATE TABLE shop_settings (
  shop_id    bigint      NOT NULL REFERENCES shops(id),
  key        text        NOT NULL,
  value      jsonb       NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (shop_id, key)
);

-- One account may see many shops; one shop may be visible to many accounts.
-- No users.shop_id, no shops.owner_user_id, no current_shop table (ADR-0006).
CREATE TABLE user_shops (
  user_id    bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  shop_id    bigint      NOT NULL REFERENCES shops(id) ON DELETE CASCADE,
  active     boolean     NOT NULL DEFAULT true,
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, shop_id)
);
CREATE INDEX idx_user_shops_shop ON user_shops (shop_id, user_id);

CREATE TABLE refresh_tokens (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash  text        NOT NULL,
  expires_at  timestamptz NOT NULL,
  revoked_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_refresh_tokens_hash UNIQUE (token_hash)
);
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens (user_id) WHERE revoked_at IS NULL;

CREATE TABLE audit_logs (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  shop_id     bigint, -- NULL for account-level events; not an isolation key
  actor_id    bigint      NOT NULL,
  action      text        NOT NULL,
  target_type text        NOT NULL,
  target_id   text,
  payload     jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_audit_logs_public_id UNIQUE (public_id)
);
CREATE INDEX idx_audit_logs_created ON audit_logs (created_at);

CREATE TABLE catalog_categories (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid        NOT NULL DEFAULT uuidv7(),
  parent_id  bigint REFERENCES catalog_categories(id),
  code       text        NOT NULL,
  name       text        NOT NULL,
  sort_order integer     NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_catalog_categories_public_id UNIQUE (public_id),
  CONSTRAINT uq_catalog_categories_code UNIQUE NULLS NOT DISTINCT (parent_id, code)
);

CREATE TABLE catalog_items (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id  uuid          NOT NULL DEFAULT uuidv7(),
  sku        text          NOT NULL,
  name       text          NOT NULL,
  type       text          NOT NULL DEFAULT 'product',
  base_price numeric(14,2) NOT NULL DEFAULT 0,
  attributes jsonb         NOT NULL DEFAULT '{}'::jsonb,
  active     boolean       NOT NULL DEFAULT true,
  created_at timestamptz   NOT NULL DEFAULT now(),
  updated_at timestamptz   NOT NULL DEFAULT now(),
  version    integer       NOT NULL DEFAULT 0,
  CONSTRAINT uq_catalog_items_sku UNIQUE (sku),
  CONSTRAINT uq_catalog_items_public_id UNIQUE (public_id),
  CONSTRAINT ck_catalog_items_type CHECK (type IN ('product','service')),
  CONSTRAINT ck_catalog_items_price CHECK (base_price >= 0)
);

CREATE TABLE shop_catalog_overrides (
  shop_id         bigint        NOT NULL REFERENCES shops(id),
  catalog_item_id bigint        NOT NULL REFERENCES catalog_items(id),
  price           numeric(14,2) NOT NULL,
  available       boolean       NOT NULL DEFAULT true,
  updated_at      timestamptz   NOT NULL DEFAULT now(),
  PRIMARY KEY (shop_id, catalog_item_id),
  CONSTRAINT ck_shop_catalog_overrides_price CHECK (price >= 0)
);

CREATE TABLE members (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  phone        text        NOT NULL,
  display_name text        NOT NULL,
  status       text        NOT NULL DEFAULT 'active',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  version      integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_members_phone UNIQUE (phone),
  CONSTRAINT uq_members_public_id UNIQUE (public_id),
  CONSTRAINT ck_members_status CHECK (status IN ('active','disabled'))
);

CREATE TABLE member_balances (
  shop_id    bigint        NOT NULL REFERENCES shops(id),
  member_id  bigint        NOT NULL REFERENCES members(id),
  balance    numeric(14,2) NOT NULL DEFAULT 0,
  version    integer       NOT NULL DEFAULT 0,
  updated_at timestamptz   NOT NULL DEFAULT now(),
  PRIMARY KEY (shop_id, member_id),
  CONSTRAINT ck_member_balances_nonneg CHECK (balance >= 0)
);

CREATE TABLE balance_ledger (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id       bigint        NOT NULL,
  public_id     uuid          NOT NULL DEFAULT uuidv7(),
  member_id     bigint        NOT NULL,
  delta         numeric(14,2) NOT NULL,
  balance_after numeric(14,2) NOT NULL,
  reason        text          NOT NULL,
  ref_type      text,
  ref_id        text,
  created_at    timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT uq_balance_ledger_shop_public UNIQUE (shop_id, public_id),
  CONSTRAINT fk_balance_ledger_balance FOREIGN KEY (shop_id, member_id)
              REFERENCES member_balances (shop_id, member_id)
);
CREATE INDEX idx_balance_ledger_shop_member ON balance_ledger (shop_id, member_id, created_at DESC);

-- Business writes and their idempotency record commit in the same transaction.
CREATE TABLE idempotency_keys (
  scope           text        NOT NULL,
  key             text        NOT NULL,
  request_hash    bytea       NOT NULL,
  state           text        NOT NULL DEFAULT 'in_progress',
  response_status integer,
  response_body   jsonb,
  expires_at      timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
  created_at      timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,
  PRIMARY KEY (scope, key),
  CONSTRAINT ck_idem_state CHECK (state IN ('in_progress','succeeded','failed'))
);
CREATE INDEX idx_idem_expiry ON idempotency_keys (expires_at) WHERE state <> 'succeeded';

CREATE TABLE outbox_events (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id        bigint,
  aggregate_type text        NOT NULL,
  aggregate_id   text        NOT NULL,
  event_type     text        NOT NULL,
  payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
  attempts       integer     NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  dispatched_at  timestamptz
);
CREATE INDEX idx_outbox_pending ON outbox_events (created_at) WHERE dispatched_at IS NULL;

CREATE TABLE dead_letters (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_id    bigint      NOT NULL,
  error_text  text        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- Audit-cleanup run trace. Deliberately not an audit_logs row (ADR-0008).
CREATE TABLE audit_log_cleanup_runs (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  run_key        text        NOT NULL,
  attempt        integer     NOT NULL DEFAULT 1,
  retention_days integer     NOT NULL,
  cutoff_at      timestamptz NOT NULL,
  started_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz,
  status         text        NOT NULL DEFAULT 'running',
  deleted_rows   bigint      NOT NULL DEFAULT 0,
  batches        integer     NOT NULL DEFAULT 0,
  error_message  text,
  CONSTRAINT uq_audit_cleanup_runs_key_attempt UNIQUE (run_key, attempt),
  CONSTRAINT ck_audit_cleanup_runs_status CHECK (status IN ('running','succeeded','failed')),
  CONSTRAINT ck_audit_cleanup_runs_retention CHECK (retention_days BETWEEN 1 AND 3650)
);
CREATE INDEX idx_audit_cleanup_runs_started ON audit_log_cleanup_runs (started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log_cleanup_runs;
DROP TABLE IF EXISTS dead_letters;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS balance_ledger;
DROP TABLE IF EXISTS member_balances;
DROP TABLE IF EXISTS members;
DROP TABLE IF EXISTS shop_catalog_overrides;
DROP TABLE IF EXISTS catalog_items;
DROP TABLE IF EXISTS catalog_categories;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS user_shops;
DROP TABLE IF EXISTS shop_settings;
DROP TABLE IF EXISTS shops;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS users;

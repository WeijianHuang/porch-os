-- +goose Up
-- ADR-0012 / design/12: multi-queue scheduling engine, integrated into this
-- repository (not a standalone project). Generic, industry-agnostic terms
-- only. Site = shops/shop_id, Executor = staff, Position = queue_staff.position.
-- Isolation stays a single shop_id layer. No industry vocabulary appears in
-- any identifier, comment or constraint (C-10).

-- ---- 1. Executor state on staff (reuses the existing people file) ----------
ALTER TABLE staff
  ADD COLUMN executor_state text NOT NULL DEFAULT 'standby',
  ADD COLUMN stop_rotation   boolean NOT NULL DEFAULT false,
  ADD COLUMN time_reservations jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN primary_queue_id bigint,
  ADD CONSTRAINT ck_staff_executor_state CHECK (executor_state IN
    ('standby','available','executing','designated_occupied','pinned',
     'time_reserved','overtime','disabled','leave','off_duty','cross_group_support'));

-- ---- 2. Per-queue pin and position source on memberships ------------------
ALTER TABLE queue_staff
  ADD COLUMN pin_type text NOT NULL DEFAULT 'none',
  ADD COLUMN pin_effective_from timestamptz,
  ADD COLUMN pin_expires_at timestamptz,
  ADD COLUMN source text NOT NULL DEFAULT 'auto',
  ADD CONSTRAINT ck_qs_pin_type CHECK (pin_type IN ('none','permanent','timed','conditional')),
  ADD CONSTRAINT ck_qs_source CHECK (source IN ('auto','manual','pin','restored','inherited'));
CREATE INDEX idx_queue_staff_pin ON queue_staff (shop_id, queue_id, pin_type)
  WHERE pin_type <> 'none';

-- ---- 3. 8-step decision chain rules ---------------------------------------
CREATE TABLE scheduling_rules (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  shop_id      bigint      NOT NULL REFERENCES shops(id),
  type         text        NOT NULL,
  ord          integer     NOT NULL,
  enabled      boolean     NOT NULL DEFAULT true,
  scope        text        NOT NULL DEFAULT 'site',
  scope_id     text        NOT NULL DEFAULT '',
  on_hit       text        NOT NULL DEFAULT 'hold',
  time_window  jsonb,      -- configurable local time range; NULL = no window
  assignments  jsonb       NOT NULL DEFAULT '[]'::jsonb, -- methods that allow rotation
  from_group   text,
  to_group     text,
  no_rotation  boolean     NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  version      integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_sr_public_id UNIQUE (public_id),
  CONSTRAINT uq_sr_shop_scope UNIQUE (shop_id, type, scope, scope_id),
  CONSTRAINT ck_sr_type CHECK (type IN
    ('executor_stopped_rotation','rotation_master_switch',
     'assignment_method_allows_rotation','task_type_no_rotation',
     'overtime_no_rotation','time_reserved','group_a_to_b','group_b_to_a')),
  CONSTRAINT ck_sr_scope CHECK (scope IN ('executor','queue','task_type','site','global')),
  CONSTRAINT ck_sr_on_hit CHECK (on_hit IN ('rotate','hold')),
  CONSTRAINT ck_sr_ord CHECK (ord > 0)
);
CREATE INDEX idx_sr_shop_order ON scheduling_rules (shop_id, ord);

-- ---- 4. Linkage policy per (site, primary queue) --------------------------
CREATE TABLE scheduling_policies (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id       uuid        NOT NULL DEFAULT uuidv7(),
  shop_id         bigint      NOT NULL,
  primary_queue_id bigint     NOT NULL,
  mode            text        NOT NULL DEFAULT 'independent',
  allowlist       jsonb       NOT NULL DEFAULT '[]'::jsonb,
  exclude         jsonb       NOT NULL DEFAULT '[]'::jsonb,
  grp             text,
  action_map      jsonb       NOT NULL DEFAULT '{}'::jsonb,
  insert_at       jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  version         integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_sp_public_id UNIQUE (public_id),
  CONSTRAINT uq_sp_shop_primary UNIQUE (shop_id, primary_queue_id),
  CONSTRAINT fk_sp_queue FOREIGN KEY (shop_id, primary_queue_id)
    REFERENCES queues (shop_id, id) ON DELETE CASCADE,
  CONSTRAINT ck_sp_mode CHECK (mode IN
    ('independent','all','same_group','allowlist','exclude','frozen'))
);

-- ---- 5. Task types ---------------------------------------------------------
CREATE TABLE scheduling_task_types (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id   uuid        NOT NULL DEFAULT uuidv7(),
  shop_id     bigint      NOT NULL REFERENCES shops(id),
  code        text        NOT NULL,
  name        text        NOT NULL,
  grp         text,
  no_rotation boolean     NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  version     integer     NOT NULL DEFAULT 0,
  CONSTRAINT uq_stt_public_id UNIQUE (public_id),
  CONSTRAINT uq_stt_shop_code UNIQUE (shop_id, code),
  CONSTRAINT ck_stt_code CHECK (length(btrim(code)) > 0),
  CONSTRAINT ck_stt_name CHECK (length(btrim(name)) > 0)
);

-- ---- 6. Dispatch events (idempotency anchor, decision summary) ------------
CREATE TABLE scheduling_events (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  public_id    uuid        NOT NULL DEFAULT uuidv7(),
  shop_id      bigint      NOT NULL,
  event_id     text        NOT NULL,
  shift_id     text,
  staff_id     bigint      NOT NULL,
  source_queue_id bigint   NOT NULL,
  task_type_id bigint,
  method       text        NOT NULL DEFAULT 'rotational',
  region       text,
  decision     text        NOT NULL,
  hit_rule     text,
  explain      text,
  operator     text        NOT NULL DEFAULT '',
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_se_public_id UNIQUE (public_id),
  CONSTRAINT uq_se_shop_event UNIQUE (shop_id, event_id),
  CONSTRAINT fk_se_staff FOREIGN KEY (shop_id, staff_id)
    REFERENCES staff (shop_id, id),
  CONSTRAINT fk_se_queue FOREIGN KEY (shop_id, source_queue_id)
    REFERENCES queues (shop_id, id),
  CONSTRAINT ck_se_method CHECK (method IN
    ('rotational','designated','reassigned','delayed','selected','called')),
  CONSTRAINT ck_se_decision CHECK (decision IN ('rotate','hold'))
);
CREATE INDEX idx_se_shop_time ON scheduling_events (shop_id, occurred_at DESC);

-- ---- 7. Per-event before/after position snapshots -------------------------
CREATE TABLE scheduling_snapshots (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  shop_id    bigint      NOT NULL,
  event_id   text        NOT NULL,
  before     jsonb       NOT NULL DEFAULT '{}'::jsonb,
  after      jsonb       NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_ss_shop_event UNIQUE (shop_id, event_id),
  CONSTRAINT fk_ss_event FOREIGN KEY (shop_id, event_id)
    REFERENCES scheduling_events (shop_id, event_id) ON DELETE CASCADE
);

-- ---- 8. Permission points --------------------------------------------------
INSERT INTO permissions (code, name) VALUES
  ('scheduling.read', '查看调度看板与规则'),
  ('scheduling.write', '维护调度规则与派单')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code IN ('admin','manager') AND p.code IN ('scheduling.read','scheduling.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.code = 'staff' AND p.code = 'scheduling.read'
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions WHERE permission_id IN (SELECT id FROM permissions WHERE code IN ('scheduling.read','scheduling.write'));
DELETE FROM permissions WHERE code IN ('scheduling.read','scheduling.write');
DROP TABLE IF EXISTS scheduling_snapshots;
DROP TABLE IF EXISTS scheduling_events;
DROP TABLE IF EXISTS scheduling_task_types;
DROP TABLE IF EXISTS scheduling_policies;
DROP TABLE IF EXISTS scheduling_rules;
DROP INDEX IF EXISTS idx_queue_staff_pin;
ALTER TABLE queue_staff
  DROP CONSTRAINT IF EXISTS ck_qs_source,
  DROP CONSTRAINT IF EXISTS ck_qs_pin_type,
  DROP COLUMN IF EXISTS source,
  DROP COLUMN IF EXISTS pin_expires_at,
  DROP COLUMN IF EXISTS pin_effective_from,
  DROP COLUMN IF EXISTS pin_type;
ALTER TABLE staff
  DROP CONSTRAINT IF EXISTS ck_staff_executor_state,
  DROP COLUMN IF EXISTS primary_queue_id,
  DROP COLUMN IF EXISTS time_reservations,
  DROP COLUMN IF EXISTS stop_rotation,
  DROP COLUMN IF EXISTS executor_state;

-- WSMS-Gateway initial schema (production reference).
-- The server uses GORM AutoMigrate for dev; this file is the hand-authored equivalent
-- for controlled deploys. Enums are modeled as text + CHECK (amendment F10) so values
-- can be added without the ALTER TYPE ... ADD VALUE transaction pitfall.

CREATE TABLE IF NOT EXISTS clients (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS ix_clients_deleted ON clients(deleted_at);

CREATE TABLE IF NOT EXISTS api_keys (
    id                 uuid PRIMARY KEY,
    client_id          uuid NOT NULL REFERENCES clients(id),
    prefix             text NOT NULL,
    hash               text NOT NULL,               -- argon2id(secret)
    scopes             text NOT NULL,
    signing_secret_enc bytea,                        -- F3: separate, encrypted signing secret
    active             boolean NOT NULL DEFAULT true,
    last_used_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    deleted_at         timestamptz
);
CREATE INDEX IF NOT EXISTS ix_api_keys_prefix ON api_keys(prefix);

CREATE TABLE IF NOT EXISTS devices (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    status       text NOT NULL DEFAULT 'ENROLLED'
                   CHECK (status IN ('ENROLLED','ONLINE','OFFLINE','DISABLED')),
    last_seen_at timestamptz,
    app_version  text,
    push_token   text,
    secret_hash  text,
    wake_misses  int NOT NULL DEFAULT 0,             -- F6
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);

CREATE TABLE IF NOT EXISTS sims (
    id              uuid PRIMARY KEY,
    device_id       uuid NOT NULL REFERENCES devices(id),
    slot            int NOT NULL,
    subscription_id int NOT NULL,                    -- Android-local, not a cross-system key
    operator        text NOT NULL DEFAULT 'UNKNOWN'
                      CHECK (operator IN ('TELKOMSEL','INDOSAT','XL','AXIS','TRI','SMARTFREN','UNKNOWN')),
    operator_locked boolean NOT NULL DEFAULT false,
    msisdn          text,
    status          text NOT NULL DEFAULT 'UNKNOWN'
                      CHECK (status IN ('UNKNOWN','READY','ABSENT','DISABLED','QUOTA_EXCEEDED','COOLDOWN')),
    daily_quota     int NOT NULL DEFAULT 200,        -- segments/day
    sent_today      int NOT NULL DEFAULT 0,          -- segments (single unit — F2)
    sent_window     int NOT NULL DEFAULT 0,          -- segments in the rate window (F2)
    min_gap_ms      int NOT NULL DEFAULT 8000,
    health_score    int NOT NULL DEFAULT 100,
    cooldown_until  timestamptz,
    last_used_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);
CREATE INDEX IF NOT EXISTS ix_sims_device ON sims(device_id);
CREATE INDEX IF NOT EXISTS ix_sims_operator ON sims(operator);

CREATE TABLE IF NOT EXISTS messages (
    id                 uuid PRIMARY KEY,
    client_id          uuid NOT NULL REFERENCES clients(id),
    target_msisdn      text NOT NULL,               -- canonical +62
    target_operator    text NOT NULL DEFAULT 'UNKNOWN',
    body               text NOT NULL,
    encoding           text NOT NULL CHECK (encoding IN ('GSM7','UCS2')),
    segments           int NOT NULL,
    status             text NOT NULL DEFAULT 'QUEUED'
                         CHECK (status IN ('QUEUED','ROUTING','DISPATCHED','AWAITING_ACK','SENT',
                                           'SENT_UNCONFIRMED','DELIVERED','FAILED','EXPIRED','CANCELLED')),
    routing_policy     text NOT NULL DEFAULT 'ON_NET_PREF'
                         CHECK (routing_policy IN ('ON_NET_PREF','ON_NET_STRICT','ANY')),
    assigned_sim_id    uuid,
    assigned_device_id uuid,
    assigned_operator  text,
    dedup_key          text,
    payload_hash       text,                         -- F12
    attempts           int NOT NULL DEFAULT 0,
    max_attempts       int NOT NULL DEFAULT 3,
    callback_url       text,
    last_error         text,
    expires_at         timestamptz NOT NULL,
    dispatched_at      timestamptz,
    meta               jsonb,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_messages_status ON messages(status);
CREATE INDEX IF NOT EXISTS ix_messages_client ON messages(client_id);
CREATE INDEX IF NOT EXISTS ix_messages_expires ON messages(expires_at);
-- Per-client idempotency (F12 pairs with payload_hash checked in the app layer).
CREATE UNIQUE INDEX IF NOT EXISTS ux_messages_client_dedup
    ON messages(client_id, dedup_key) WHERE dedup_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS message_events (
    id         uuid PRIMARY KEY,
    message_id uuid NOT NULL REFERENCES messages(id),
    event_type text NOT NULL,
    detail     jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_events_message ON message_events(message_id);
CREATE INDEX IF NOT EXISTS ix_events_time ON message_events(created_at);

-- Server-side sent ledger (F1): existence of a row blocks cross-device re-send.
CREATE TABLE IF NOT EXISTS message_sends (
    message_id    uuid PRIMARY KEY,
    sim_id        uuid NOT NULL,
    device_id     uuid NOT NULL,
    dispatched_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS operator_prefixes (
    prefix   text PRIMARY KEY,
    operator text NOT NULL
);

CREATE TABLE IF NOT EXISTS enrollment_tokens (
    id         uuid PRIMARY KEY,
    token_hash text NOT NULL,
    label      text,
    used       boolean NOT NULL DEFAULT false,
    device_id  uuid,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_enroll_hash ON enrollment_tokens(token_hash);

-- Admin console (docs/07 §8). GORM pluralizes these to admin_users / admin_audits.
CREATE TABLE IF NOT EXISTS admin_users (
    id            uuid PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL DEFAULT 'readonly'
                    CHECK (role IN ('owner','operator','support','readonly')),
    active        boolean NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);

CREATE TABLE IF NOT EXISTS admin_audits (
    id          uuid PRIMARY KEY,
    actor       text NOT NULL,
    actor_role  text,
    action      text NOT NULL,
    target_type text,
    target_id   text,
    reason      text,
    before      jsonb,
    after       jsonb,
    source_ip   text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_admin_audits_action ON admin_audits(action);
CREATE INDEX IF NOT EXISTS ix_admin_audits_time ON admin_audits(created_at);

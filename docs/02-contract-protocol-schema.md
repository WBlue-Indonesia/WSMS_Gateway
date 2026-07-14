# 02 — Contract, Protocol & Schema (Canonical Interface Contract)

> **Status:** Normative. This document is the Single Source of Truth for every wire
> format, database field, enum value, and state transition in WSMS-Gateway. The Go
> backend (Gin + GORM + Postgres), the Flutter/Android sender app, and any client
> integrating the REST API MUST conform to exactly what is written here. If another
> doc disagrees with this one, **this one wins** until amended.

**Interpretation:** The key words MUST, MUST NOT, SHOULD, SHOULD NOT, MAY are used per
RFC 2119. "Server" = the Go backend. "Device" = an owned Android phone running the
Flutter sender app. "Client" = an external system calling the REST API to submit sends.

---

## Table of Contents

- [0. Foundations — canonical types & conventions](#0-foundations)
- [A. Data Model (Postgres + GORM)](#a-data-model)
  - [A.1 Global conventions](#a1-global-conventions)
  - [A.2 Enums (the canonical vocabulary)](#a2-enums)
  - [A.3 `clients` & `api_keys`](#a3-clients--api_keys)
  - [A.4 `devices`](#a4-devices)
  - [A.5 `sims`](#a5-sims)
  - [A.6 `messages`](#a6-messages)
  - [A.7 `message_events`](#a7-message_events)
  - [A.8 `operator_prefixes`](#a8-operator_prefixes)
  - [A.9 `enrollment_tokens`](#a9-enrollment_tokens)
  - [A.10 ER diagram](#a10-er-diagram)
- [B. Client-facing REST API](#b-client-facing-rest-api)
- [C. Device ↔ Server WebSocket protocol](#c-device--server-websocket-protocol)
- [D. Message status state machine](#d-message-status-state-machine)
- [E. Idempotency & no-double-send guarantee (normative)](#e-idempotency)

---

<a name="0-foundations"></a>

## 0. Foundations — canonical types & conventions

These primitive types appear in the DB, the REST API, and the WS protocol. Define once, reuse everywhere.

### 0.1 Identifiers

| Entity | Type | Format | Notes |
|---|---|---|---|
| `message_id` | `UUIDv7` (string) | `018f9b2a-...` | Time-ordered UUID. Server-assigned, globally unique, sortable by creation time. |
| `event_id` | `UUIDv7` | | One per row in `message_events`. |
| `device_id` | `UUID` (string) | | Server-assigned at enrollment. |
| `sim_id` | `UUID` (string) | | Server-assigned. **Never** the Android `subscription_id` — that is unstable across reboots/SIM swaps. |
| `client_id` | `UUID` | | |
| `frame.id` | `ULID` | `01J8...` | Per-WS-frame id for transport correlation/ack. Client/device-generated, unique per connection lifetime. |
| `dedup_key` | string ≤ 128 | client-chosen | Idempotency key, unique **per client**. |

> **Rule:** `subscription_id` (Android `SubscriptionInfo.getSubscriptionId()`) is an
> integer that is only meaningful *on a given device at a given moment*. It MUST NOT be
> used as a cross-system key. The server addresses a SIM by `sim_id`; the send command
> carries the current `sim_subscription_id` only so the device knows which local
> `SmsManager` to bind.

### 0.2 Timestamps

All timestamps are **RFC 3339 / ISO 8601 in UTC with millisecond precision**, e.g.
`2026-07-14T09:12:33.482Z`. Stored as `timestamptz` in Postgres. The literal field name
`ts` on WS frames is Unix epoch **milliseconds** (integer) for cheap ordering on-device;
REST bodies use the RFC 3339 string form.

### 0.3 MSISDN canonical form

The **canonical form is E.164 with leading `+`**: `+628123456789`.

Normalization (server-side, before any prefix match or DB write) — applied to both target
numbers and SIM-reported numbers:

```text
normalize(raw):
  strip spaces, dashes, parentheses, dots
  if starts with "+62"  -> keep as-is
  if starts with "62"   -> prepend "+"            ("628.." -> "+628..")
  if starts with "0"    -> replace leading "0" with "+62"   ("0812.." -> "+62812..")
  if starts with "8"    -> prepend "+62"          (bare national, "812.." -> "+62812..")
  else -> reject as INVALID_MSISDN
  validate: ^\+62\d{8,13}$   (Indonesian mobile: +62 followed by 8..13 digits)
```

For **prefix→operator** matching, derive the `08xx` national form from canonical:
`local = "0" + msisdn[3:]` → first 4 chars are the routing prefix.

### 0.4 Operator enum

The closed vocabulary (see [A.2](#a2-enums)):

```
TELKOMSEL | INDOSAT | XL | AXIS | TRI | SMARTFREN | UNKNOWN
```

`UNKNOWN` is used when a prefix does not match the table (new/ported ranges).
Because Indonesia has **no full mobile MNP**, prefix→operator is a strong heuristic, not a
guarantee — this is the entire reason [routing fallback](#d-message-status-state-machine)
exists. `INDOSAT` and `TRI` are kept **distinct** despite the 2022 Indosat Ooredoo
Hutchison merger, because their networks/prefixes remain operationally separate.

### 0.5 Encoding & segmentation

```
GSM7  -> 160 chars single / 153 per part when multipart
UCS2  -> 70  chars single / 67  per part when multipart
```

The **server** computes `encoding` and `segments` at submit time and stores them
(clients MUST NOT be trusted to compute cost):

```text
encoding = GSM7 if every rune ∈ GSM 03.38 basic+extension set, else UCS2
  (GSM-7 extension chars: ^ { } \ [ ] ~ | € each count as 2 septets)
segments:
  GSM7: len<=160 -> 1 ; else ceil(len_septets / 153)
  UCS2: len<=70  -> 1 ; else ceil(len_utf16units / 67)
```

`segments` drives per-SIM quota accounting and cost reporting.

### 0.6 Money / cost

Cost is **out of scope for this contract** beyond recording `segments`. Doc `05-*`
(billing) computes cost from `segments × operator tariff`. This doc guarantees `segments`
is present and correct on every `message`.

---

<a name="a-data-model"></a>

## A. Data Model (Postgres + GORM)

<a name="a1-global-conventions"></a>

### A.1 Global conventions

- Postgres 15+. Extensions: `pgcrypto` (for `gen_random_uuid()` fallback) — but UUIDv7 is
  generated in Go (`github.com/google/uuid` v1.6 `NewV7`) so IDs are time-sortable.
- Every table has `created_at timestamptz NOT NULL DEFAULT now()`, `updated_at timestamptz NOT NULL DEFAULT now()`.
- Soft delete via GORM `gorm.DeletedAt` (`deleted_at timestamptz NULL`, indexed) **only**
  on config-lifecycle tables (`clients`, `api_keys`, `devices`, `sims`). Operational
  tables (`messages`, `message_events`) are **never** soft-deleted; they are retained then
  hard-purged by a retention job.
- Enums are stored as Postgres native `ENUM` types (`CREATE TYPE ...`). In Go they are
  `string`-based typed constants with a `Valid()` method. Native enums give DB-level
  integrity; adding a value is `ALTER TYPE ... ADD VALUE`.
- Monetary/quota counters are plain `int` (segments), reset by a scheduled job (see `sims`).
- JSONB columns use `datatypes.JSON` (`gorm.io/datatypes`).

Shared base struct:

```go
type Base struct {
    CreatedAt time.Time `gorm:"not null;default:now()" json:"created_at"`
    UpdatedAt time.Time `gorm:"not null;default:now()" json:"updated_at"`
}

type SoftBase struct {
    Base
    DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
```

<a name="a2-enums"></a>

### A.2 Enums (the canonical vocabulary)

```sql
CREATE TYPE operator_t AS ENUM
  ('TELKOMSEL','INDOSAT','XL','AXIS','TRI','SMARTFREN','UNKNOWN');

CREATE TYPE encoding_t AS ENUM ('GSM7','UCS2');

CREATE TYPE device_status_t AS ENUM
  ('ENROLLED','ONLINE','OFFLINE','DISABLED');

CREATE TYPE sim_status_t AS ENUM
  ('UNKNOWN','READY','ABSENT','DISABLED','QUOTA_EXCEEDED','COOLDOWN');

CREATE TYPE message_status_t AS ENUM
  ('QUEUED','ROUTING','DISPATCHED','SENT','DELIVERED','FAILED','EXPIRED','CANCELLED');

CREATE TYPE routing_policy_t AS ENUM
  ('ON_NET_PREFERRED','ON_NET_STRICT','ANY','PINNED');

CREATE TYPE failure_reason_t AS ENUM (
  -- routing/server side
  'NO_ONLINE_SIM','NO_MATCHING_OPERATOR_SIM','DEVICE_OFFLINE',
  'QUOTA_EXCEEDED','RATE_LIMITED','EXPIRED_TTL','CANCELLED',
  'REJECTED_BY_DEVICE','INVALID_MSISDN','ROUTE_EXHAUSTED',
  -- device / Android SmsManager side (SENT PendingIntent)
  'GENERIC_FAILURE','NO_SERVICE','NULL_PDU','RADIO_OFF','SIM_ABSENT',
  'LIMIT_EXCEEDED','FDN_CHECK_FAILURE','SHORT_CODE_NOT_ALLOWED',
  -- delivery report (DELIVERED PendingIntent / SMS-STATUS-REPORT)
  'DELIVERY_PERMANENT_ERROR','DELIVERY_TEMP_ERROR',
  'NONE','UNKNOWN'
);

CREATE TYPE event_type_t AS ENUM (
  'CREATED','ROUTED','REROUTED','DISPATCH','SEND_ACCEPTED','SEND_REJECTED',
  'SENT','DELIVERED','FAILED','EXPIRED','CANCELLED','RETRY_SCHEDULED','WEBHOOK_SENT'
);
```

Go mirror (pattern — one shown, all follow it):

```go
type MessageStatus string

const (
    StatusQueued     MessageStatus = "QUEUED"
    StatusRouting    MessageStatus = "ROUTING"
    StatusDispatched MessageStatus = "DISPATCHED"
    StatusSent       MessageStatus = "SENT"
    StatusDelivered  MessageStatus = "DELIVERED"
    StatusFailed     MessageStatus = "FAILED"
    StatusExpired    MessageStatus = "EXPIRED"
    StatusCancelled  MessageStatus = "CANCELLED"
)

func (s MessageStatus) Terminal() bool {
    switch s {
    case StatusDelivered, StatusFailed, StatusExpired, StatusCancelled:
        return true
    }
    return false
}
```

**Android result-code → `failure_reason_t` mapping** (device computes, server stores; the
canonical translation lives in the app but is normative here):

| Android constant (SENT PendingIntent `resultCode`) | `failure_reason_t` |
|---|---|
| `Activity.RESULT_OK` | `NONE` (→ status `SENT`) |
| `SmsManager.RESULT_ERROR_GENERIC_FAILURE` | `GENERIC_FAILURE` |
| `SmsManager.RESULT_ERROR_NO_SERVICE` | `NO_SERVICE` |
| `SmsManager.RESULT_ERROR_NULL_PDU` | `NULL_PDU` |
| `SmsManager.RESULT_ERROR_RADIO_OFF` | `RADIO_OFF` |
| `SmsManager.RESULT_ERROR_LIMIT_EXCEEDED` | `LIMIT_EXCEEDED` |
| `SmsManager.RESULT_ERROR_FDN_CHECK_FAILURE` | `FDN_CHECK_FAILURE` |
| `SmsManager.RESULT_ERROR_SHORT_CODE_NOT_ALLOWED` / `_NEVER_ALLOWED` | `SHORT_CODE_NOT_ALLOWED` |
| (no active subscription / SIM removed pre-send) | `SIM_ABSENT` |

| DELIVERED PendingIntent (SMS-STATUS-REPORT TP-Status) | `failure_reason_t` |
|---|---|
| status `0x00` (delivered) | `NONE` (→ status `DELIVERED`) |
| status `0x20–0x3F` (temporary) | `DELIVERY_TEMP_ERROR` |
| status `0x40–0x7F` (permanent) | `DELIVERY_PERMANENT_ERROR` |

<a name="a3-clients--api_keys"></a>

### A.3 `clients` & `api_keys`

A **client** is an integrating system/tenant. An **api_key** is a credential belonging to a
client. Separating them lets a client rotate keys without losing config/webhook history.

```go
type Client struct {
    ID            uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
    Name          string         `gorm:"not null" json:"name"`
    WebhookURL    *string        `gorm:"" json:"webhook_url,omitempty"`
    // HMAC secret used to sign outbound webhooks; stored encrypted at rest (KMS/age).
    WebhookSecret []byte         `gorm:"type:bytea" json:"-"`
    // Global submit rate limit for this client (messages/sec, token-bucket).
    RateLimitPerSec int          `gorm:"not null;default:5" json:"rate_limit_per_sec"`
    Status        string         `gorm:"type:text;not null;default:'active'" json:"status"` // active|suspended
    SoftBase
}

type APIKey struct {
    ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
    ClientID   uuid.UUID  `gorm:"type:uuid;not null;index" json:"client_id"`
    // Public, non-secret prefix shown in dashboards/logs, e.g. "wsms_live_a1b2c3d4".
    KeyID      string     `gorm:"uniqueIndex;not null" json:"key_id"`
    // Argon2id hash of the secret half. Raw secret is shown to the user exactly once.
    SecretHash string     `gorm:"not null" json:"-"`
    Scopes     pq.StringArray `gorm:"type:text[];not null" json:"scopes"` // see below
    LastUsedAt *time.Time `json:"last_used_at,omitempty"`
    ExpiresAt  *time.Time `json:"expires_at,omitempty"`
    RevokedAt  *time.Time `json:"revoked_at,omitempty"`
    SoftBase
}
```

**Scopes:** `messages:write`, `messages:read`, `devices:read`, `sims:read`, `admin`.

**API key wire format** (opaque bearer token): `wsms_<env>_<keyid>.<secret>` e.g.
`wsms_live_a1b2c3d4.9f7e...`. The server splits on the first `.`; looks up `KeyID`
(`wsms_live_a1b2c3d4`), then verifies `secret` against `SecretHash` (Argon2id, constant-time).

SQL notes:

```sql
CREATE UNIQUE INDEX uq_api_keys_keyid ON api_keys(key_id) WHERE deleted_at IS NULL;
CREATE INDEX ix_api_keys_client ON api_keys(client_id);
```

<a name="a4-devices"></a>

### A.4 `devices`

One row per owned Android phone.

```go
type Device struct {
    ID          uuid.UUID       `gorm:"type:uuid;primaryKey" json:"id"`
    // Human label, e.g. "HP-A". Unique, admin-assigned.
    DeviceKey   string          `gorm:"uniqueIndex;not null" json:"device_key"`
    Platform    string          `gorm:"type:text;not null;default:'android'" json:"platform"` // ALWAYS android — see note
    Model       string          `json:"model"`
    OSVersion   string          `json:"os_version"`
    AppVersion  string          `json:"app_version"`
    Status      string          `gorm:"type:device_status_t;not null;default:'ENROLLED'" json:"status"`
    // Long-lived credential hash used to authenticate the WS runtime connection.
    SecretHash  string          `gorm:"not null" json:"-"`
    // FCM registration token — used to WAKE a killed process (high-priority data msg).
    PushToken   *string         `json:"push_token,omitempty"`
    LastSeenAt  *time.Time      `gorm:"index" json:"last_seen_at,omitempty"`
    // Current live WS session id; null when offline. Guards single-connection rule.
    SessionID   *string         `json:"session_id,omitempty"`
    // {"battery_pct":83,"charging":true,"doze":false,"ignoring_batt_opt":true}
    Health      datatypes.JSON  `gorm:"type:jsonb" json:"health"`
    SoftBase
}
```

> **Hard fact — Android only.** `Platform` exists for schema hygiene but the fleet is
> **Android exclusively**. **iOS cannot send SMS programmatically**; there is no public API
> to do so. An iOS build of the app could only *monitor* — it can never be a sender. The
> server MUST reject enrollment where `platform != 'android'` for a sender role.

<a name="a5-sims"></a>

### A.5 `sims`

Two rows per dual-SIM device (one per physical slot with an active subscription). This is the
routing target.

```go
type SIM struct {
    ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
    DeviceID       uuid.UUID  `gorm:"type:uuid;not null;index" json:"device_id"`
    // Physical slot 0/1 (SubscriptionInfo.getSimSlotIndex()). Stable within a device.
    SlotIndex      int        `gorm:"not null" json:"slot_index"`
    // Android SubscriptionInfo.getSubscriptionId(). UNSTABLE across reboot/SIM swap;
    // refreshed on every sim_report; used only to bind the on-device SmsManager.
    SubscriptionID int        `gorm:"not null" json:"subscription_id"`
    Operator       string     `gorm:"type:operator_t;not null;default:'UNKNOWN'" json:"operator"`
    CarrierName    string     `json:"carrier_name"`   // SubscriptionInfo.getCarrierName()
    MCC            string     `gorm:"type:char(3)" json:"mcc"` // "510" for Indonesia
    MNC            string     `gorm:"type:varchar(3)" json:"mnc"` // 10=Telkomsel,01=Indosat,11=XL,89=3,09=Smartfren...
    // Often NULL — Android rarely exposes the SIM's own number (needs READ_PHONE_NUMBERS,
    // and carriers may not provision it). Populated when available.
    MSISDN         *string    `gorm:"index" json:"msisdn,omitempty"`
    ICCID          *string    `json:"iccid,omitempty"`
    Status         string     `gorm:"type:sim_status_t;not null;default:'UNKNOWN'" json:"status"`
    // Admin master switch — a DISABLED sim is never routed to regardless of health.
    Enabled        bool       `gorm:"not null;default:true" json:"enabled"`
    // Anti-ban hygiene (see doc 08 legal/deliverability). Per-SIM daily cap in *segments*.
    DailyQuota     int        `gorm:"not null;default:200" json:"daily_quota"`
    SentToday      int        `gorm:"not null;default:0" json:"sent_today"`         // reset 00:00 Asia/Jakarta
    // Rolling short-window counter for pacing (e.g. last 60s), maintained in Redis + snapshotted.
    SentWindow     int        `gorm:"not null;default:0" json:"sent_window"`
    LastSentAt     *time.Time `json:"last_sent_at,omitempty"`
    // {"signal_dbm":-89,"radio":"LTE","roaming":false}
    Health         datatypes.JSON `gorm:"type:jsonb" json:"health"`
    LastReportAt   *time.Time `json:"last_report_at,omitempty"`
    SoftBase
}
```

SQL notes / constraints:

```sql
-- one SIM row per physical slot per device
CREATE UNIQUE INDEX uq_sim_device_slot ON sims(device_id, slot_index) WHERE deleted_at IS NULL;
-- current subscription id must be unique within a device (guards stale duplicates)
CREATE UNIQUE INDEX uq_sim_device_sub  ON sims(device_id, subscription_id) WHERE deleted_at IS NULL;
-- routing hot-path index: find READY, enabled SIMs on a given operator with quota left
CREATE INDEX ix_sim_routing ON sims(operator, status, enabled)
  WHERE status = 'READY' AND enabled = true;
```

**SentToday reset & quota:** a cron (`00:00 Asia/Jakarta`) sets `sent_today = 0`. When
`sent_today + msg.segments > daily_quota`, the SIM is skipped for routing and its status
flips to `QUOTA_EXCEEDED` until reset. `COOLDOWN` is a transient status the pacing engine
sets between sends (human-like jitter).

<a name="a6-messages"></a>

### A.6 `messages`

The core operational record. One row per submitted send.

```go
type Message struct {
    ID             uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"` // UUIDv7
    ClientID       uuid.UUID  `gorm:"type:uuid;not null;index" json:"client_id"`
    // Client idempotency key; unique per client. Null allowed (server treats as non-idempotent).
    DedupKey       *string    `gorm:"index" json:"dedup_key,omitempty"`

    TargetMSISDN   string     `gorm:"not null;index" json:"target_msisdn"`     // canonical +62...
    TargetOperator string     `gorm:"type:operator_t;not null" json:"target_operator"`

    Body           string     `gorm:"not null" json:"body"`
    Encoding       string     `gorm:"type:encoding_t;not null" json:"encoding"`
    Segments       int        `gorm:"not null" json:"segments"`

    Status         string     `gorm:"type:message_status_t;not null;default:'QUEUED';index" json:"status"`
    RoutingPolicy  string     `gorm:"type:routing_policy_t;not null;default:'ON_NET_PREFERRED'" json:"routing_policy"`

    // Client pin (RoutingPolicy=PINNED) OR server's chosen SIM. Null until routed.
    RequestedSIMID *uuid.UUID `gorm:"type:uuid" json:"requested_sim_id,omitempty"`
    AssignedSIMID  *uuid.UUID `gorm:"type:uuid;index" json:"assigned_sim_id,omitempty"`
    AssignedDevID  *uuid.UUID `gorm:"type:uuid;index" json:"assigned_device_id,omitempty"`

    Attempts       int        `gorm:"not null;default:0" json:"attempts"`
    MaxAttempts    int        `gorm:"not null;default:3" json:"max_attempts"`
    Priority       int        `gorm:"not null;default:5" json:"priority"` // 1=highest .. 9=lowest

    CallbackURL    *string    `json:"callback_url,omitempty"` // per-message webhook override
    ScheduledAt    *time.Time `gorm:"index" json:"scheduled_at,omitempty"` // null=send now
    ExpiresAt      time.Time  `gorm:"not null;index" json:"expires_at"`    // TTL; EXPIRED past this

    LastReason     string     `gorm:"type:failure_reason_t;not null;default:'NONE'" json:"last_reason"`
    LastDetail     *string    `json:"last_detail,omitempty"`

    // Lifecycle timestamps (nullable until reached)
    DispatchedAt   *time.Time `json:"dispatched_at,omitempty"`
    SentAt         *time.Time `json:"sent_at,omitempty"`
    DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
    TerminalAt     *time.Time `json:"terminal_at,omitempty"`

    Metadata       datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"` // free-form client tags
    Base
}
```

SQL notes:

```sql
-- idempotency: one message per (client, dedup_key)
CREATE UNIQUE INDEX uq_msg_client_dedup ON messages(client_id, dedup_key)
  WHERE dedup_key IS NOT NULL;
-- worker queue scan: pick actionable messages by status, priority, schedule
CREATE INDEX ix_msg_queue ON messages(status, priority, scheduled_at)
  WHERE status IN ('QUEUED','ROUTING');
-- expiry sweeper
CREATE INDEX ix_msg_expiry ON messages(expires_at) WHERE status NOT IN
  ('DELIVERED','FAILED','EXPIRED','CANCELLED');
```

**Default TTL:** `expires_at = created_at + 6h` unless the client overrides via `ttl_seconds`.

<a name="a7-message_events"></a>

### A.7 `message_events`

Append-only audit log of every state change and every device report. This is what powers
delivery debugging and webhook replay. **Never updated, never soft-deleted.**

```go
type MessageEvent struct {
    ID         uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"` // UUIDv7 -> ordered
    MessageID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"message_id"`
    EventType  string     `gorm:"type:event_type_t;not null" json:"event_type"`
    FromStatus *string    `gorm:"type:message_status_t" json:"from_status,omitempty"`
    ToStatus   *string    `gorm:"type:message_status_t" json:"to_status,omitempty"`
    SIMID      *uuid.UUID `gorm:"type:uuid" json:"sim_id,omitempty"`
    DeviceID   *uuid.UUID `gorm:"type:uuid" json:"device_id,omitempty"`
    Attempt    int        `gorm:"not null;default:0" json:"attempt"`
    Reason     string     `gorm:"type:failure_reason_t;not null;default:'NONE'" json:"reason"`
    // Verbatim device payload for forensics: raw Android resultCode, part index, etc.
    Raw        datatypes.JSON `gorm:"type:jsonb" json:"raw,omitempty"`
    Detail     *string    `json:"detail,omitempty"`
    CreatedAt  time.Time  `gorm:"not null;default:now();index" json:"created_at"`
}
```

```sql
CREATE INDEX ix_events_msg_time ON message_events(message_id, created_at);
```

<a name="a8-operator_prefixes"></a>

### A.8 `operator_prefixes`

Code table driving prefix→operator resolution. In DB (not hardcoded) so ranges can be added
without a redeploy. Loaded into an in-memory map at boot and on change (LISTEN/NOTIFY or TTL refresh).

```go
type OperatorPrefix struct {
    ID       uint   `gorm:"primaryKey" json:"id"`
    Prefix   string `gorm:"uniqueIndex;not null" json:"prefix"` // national 08xx form, e.g. "0812"
    Operator string `gorm:"type:operator_t;not null" json:"operator"`
    Note     string `json:"note,omitempty"`
    Active   bool   `gorm:"not null;default:true" json:"active"`
    Base
}
```

Seed (normative — matches the locked prefix fact sheet):

```sql
INSERT INTO operator_prefixes (prefix, operator) VALUES
 ('0811','TELKOMSEL'),('0812','TELKOMSEL'),('0813','TELKOMSEL'),
 ('0821','TELKOMSEL'),('0822','TELKOMSEL'),('0823','TELKOMSEL'),
 ('0851','TELKOMSEL'),('0852','TELKOMSEL'),('0853','TELKOMSEL'),
 ('0814','INDOSAT'),('0815','INDOSAT'),('0816','INDOSAT'),
 ('0855','INDOSAT'),('0856','INDOSAT'),('0857','INDOSAT'),('0858','INDOSAT'),
 ('0817','XL'),('0818','XL'),('0819','XL'),('0859','XL'),('0877','XL'),('0878','XL'),
 ('0831','AXIS'),('0832','AXIS'),('0833','AXIS'),('0838','AXIS'),
 ('0895','TRI'),('0896','TRI'),('0897','TRI'),('0898','TRI'),('0899','TRI'),
 ('0881','SMARTFREN'),('0882','SMARTFREN'),('0883','SMARTFREN'),('0884','SMARTFREN'),
 ('0885','SMARTFREN'),('0886','SMARTFREN'),('0887','SMARTFREN'),('0888','SMARTFREN'),('0889','SMARTFREN');
```

Resolution: `resolve(msisdn) → local="0"+msisdn[3:]; op = table[local[:4]] ?? UNKNOWN`.
Note XL/Axis share owner but are kept distinct operators; Axis SIMs are on-net for Axis
targets only (routing treats them as separate operators — conservative and correct for cost).

<a name="a9-enrollment_tokens"></a>

### A.9 `enrollment_tokens`

One-time (or limited-use) provisioning secrets an operator generates in the admin UI, then
types/scans into a fresh phone to bind it. Prevents anyone from registering a rogue sender.

```go
type EnrollmentToken struct {
    ID        uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
    TokenHash string     `gorm:"not null" json:"-"`      // sha256 of the raw token
    Label     string     `json:"label"`                  // intended device, e.g. "HP-D"
    MaxUses   int        `gorm:"not null;default:1" json:"max_uses"`
    Uses      int        `gorm:"not null;default:0" json:"uses"`
    ExpiresAt time.Time  `gorm:"not null" json:"expires_at"`
    Base
}
```

<a name="a10-er-diagram"></a>

### A.10 ER diagram

```mermaid
erDiagram
    clients        ||--o{ api_keys       : has
    clients        ||--o{ messages       : submits
    devices        ||--o{ sims           : contains
    sims           ||--o{ messages       : "assigned_sim_id"
    devices        ||--o{ messages       : "assigned_device_id"
    messages       ||--o{ message_events : logs
    operator_prefixes ||..|| messages    : "resolves target_operator"
    enrollment_tokens ||..o{ devices     : "binds at enroll"

    clients {
        uuid id PK
        text name
        text webhook_url
        int  rate_limit_per_sec
    }
    devices {
        uuid id PK
        text device_key UK
        device_status_t status
        text push_token
        uuid session_id
    }
    sims {
        uuid id PK
        uuid device_id FK
        int  slot_index
        int  subscription_id
        operator_t operator
        sim_status_t status
        int  daily_quota
        int  sent_today
    }
    messages {
        uuid id PK
        uuid client_id FK
        text dedup_key
        text target_msisdn
        operator_t target_operator
        encoding_t encoding
        int  segments
        message_status_t status
        uuid assigned_sim_id FK
        int  attempts
    }
    message_events {
        uuid id PK
        uuid message_id FK
        event_type_t event_type
        failure_reason_t reason
    }
```

---

<a name="b-client-facing-rest-api"></a>

## B. Client-facing REST API

Base URL: `https://gw.example.id`. All paths versioned under `/v1`. `Content-Type:
application/json; charset=utf-8`. All 4xx/5xx use the [standard error envelope](#b1-conventions).

<a name="b1-conventions"></a>

### B.1 Auth & conventions

- **Auth:** `Authorization: Bearer wsms_live_<keyid>.<secret>` on every request. Missing/bad
  → `401`. Valid key without required scope → `403`.
- **Idempotency:** `Idempotency-Key: <string≤128>` header on `POST /v1/messages`. Maps to
  `messages.dedup_key`. Re-submitting the same key returns the **original** message
  (HTTP `200` with `"idempotent_replay": true`) instead of creating a duplicate.
- **Rate limit:** token bucket per client (`clients.rate_limit_per_sec`). On exhaustion →
  `429` with headers `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `Retry-After`.
- **Pagination:** cursor-based. `?limit=50&cursor=<opaque>`; response includes
  `"next_cursor"` (null when done).
- **Error envelope:**

```json
{
  "error": {
    "code": "INVALID_MSISDN",
    "message": "target_msisdn '+1202...' is not an Indonesian mobile number",
    "request_id": "req_01J8...",
    "details": { "field": "target_msisdn" }
  }
}
```

Error `code` values: `UNAUTHENTICATED`, `FORBIDDEN`, `INVALID_MSISDN`, `VALIDATION_ERROR`,
`BODY_TOO_LONG`, `RATE_LIMITED`, `NOT_FOUND`, `CONFLICT`, `NO_ROUTE_AVAILABLE`,
`INTERNAL`.

### B.2 `POST /v1/messages` — submit one

Request:

```http
POST /v1/messages
Authorization: Bearer wsms_live_a1b2c3d4.9f7e...
Idempotency-Key: order-88213-otp
Content-Type: application/json

{
  "to": "0812-3456-7890",
  "body": "Kode OTP Anda: 448192. Berlaku 5 menit.",
  "routing_policy": "ON_NET_PREFERRED",
  "ttl_seconds": 900,
  "priority": 2,
  "callback_url": "https://client.example/webhooks/sms",
  "metadata": { "order_id": "88213", "type": "otp" }
}
```

Field rules:

| Field | Req | Notes |
|---|---|---|
| `to` | ✔ | Any accepted form; server normalizes to `+62…`. Rejects non-mobile → `INVALID_MSISDN`. |
| `body` | ✔ | 1..1600 chars (10 segments hard cap). Server computes `encoding`+`segments`. |
| `routing_policy` | ✖ | `ON_NET_PREFERRED` (default) \| `ON_NET_STRICT` \| `ANY` \| `PINNED`. |
| `pinned_sim_id` | cond | Required iff `routing_policy=PINNED`. |
| `ttl_seconds` | ✖ | 60..86400. Default 21600 (6h). |
| `priority` | ✖ | 1..9, default 5. |
| `scheduled_at` | ✖ | RFC 3339 future time; defer send. |
| `callback_url` | ✖ | Per-message webhook override. |
| `metadata` | ✖ | Arbitrary JSON object ≤ 4KB. |

Response `202 Accepted`:

```json
{
  "id": "018f9b2a-7c31-7e44-9a10-2b6f0c9d1e00",
  "status": "QUEUED",
  "to": "+6281234567890",
  "target_operator": "TELKOMSEL",
  "encoding": "GSM7",
  "segments": 1,
  "routing_policy": "ON_NET_PREFERRED",
  "expires_at": "2026-07-14T09:27:33.482Z",
  "created_at": "2026-07-14T09:12:33.482Z",
  "idempotent_replay": false
}
```

- `ON_NET_STRICT` with no matching-operator SIM online → the message is still accepted
  (`202`, `QUEUED`) but will terminate `FAILED / NO_MATCHING_OPERATOR_SIM` if none appears
  before TTL. Use `ON_NET_PREFERRED` to allow random-SIM fallback (the default behavior the
  owner asked for).

### B.3 `POST /v1/messages/batch` — submit many

Request: `{ "messages": [ {..single body..}, ... ] }` (max 500). Per-item `idempotency_key`
allowed inside each object. Response `200` multi-status:

```json
{
  "results": [
    { "index": 0, "status": "accepted", "id": "018f9b2a-...", "segments": 1 },
    { "index": 1, "status": "rejected", "error": { "code": "INVALID_MSISDN",
        "message": "..." } }
  ],
  "accepted": 1,
  "rejected": 1
}
```

Batch is **not** atomic — each item is evaluated independently.

### B.4 `GET /v1/messages/:id`

```json
{
  "id": "018f9b2a-...",
  "status": "DELIVERED",
  "to": "+6281234567890",
  "target_operator": "TELKOMSEL",
  "encoding": "GSM7",
  "segments": 1,
  "routing_policy": "ON_NET_PREFERRED",
  "assigned_sim_id": "3f0c-...",
  "assigned_device_id": "9a11-...",
  "assigned_operator": "TELKOMSEL",
  "on_net": true,
  "attempts": 1,
  "last_reason": "NONE",
  "timeline": {
    "created_at":    "2026-07-14T09:12:33.482Z",
    "dispatched_at": "2026-07-14T09:12:34.001Z",
    "sent_at":       "2026-07-14T09:12:36.221Z",
    "delivered_at":  "2026-07-14T09:12:41.880Z",
    "terminal_at":   "2026-07-14T09:12:41.880Z"
  },
  "metadata": { "order_id": "88213", "type": "otp" }
}
```

`?include=events` appends the full `message_events` list (the audit trail from A.7).

### B.5 `GET /v1/messages` — list

Filters: `status`, `to`, `operator`, `from`/`to` (time range), `metadata.key=val`. Cursor paginated.

### B.6 `POST /v1/messages/:id/cancel`

Cancels a message **only** if not yet terminal and not yet confirmed left the phone.
Allowed from `QUEUED`, `ROUTING`, `DISPATCHED` (best-effort: server sends a WS `cancel` and
races the device). Not allowed from `SENT`/`DELIVERED` (already gone) → `409 CONFLICT`.
Success → `200 { "id":..., "status":"CANCELLED" }`.

### B.7 Fleet visibility

- `GET /v1/devices` → list with live status, last_seen, battery, sim summary.
- `GET /v1/devices/:id` → device + its sims.
- `GET /v1/sims` → all sims; filter `?operator=TELKOMSEL&status=READY`. Includes
  `sent_today`, `daily_quota`, `on_net_ready` booleans — how a client checks capacity per operator.

```json
// GET /v1/sims?operator=TELKOMSEL
{
  "sims": [
    { "id":"3f0c-...", "device_key":"HP-A", "slot_index":0, "operator":"TELKOMSEL",
      "status":"READY", "enabled":true, "sent_today":42, "daily_quota":200,
      "carrier_name":"Telkomsel", "last_sent_at":"2026-07-14T09:12:36Z" }
  ],
  "next_cursor": null
}
```

### B.8 Health

- `GET /v1/healthz` — liveness, always `200 {"ok":true}` if process up.
- `GET /v1/readyz` — readiness; `200` iff DB reachable AND ≥1 device ONLINE AND ≥1 SIM READY;
  else `503` with `{"ready":false,"reasons":["no_online_device"]}`.
- `GET /v1/stats` (scope `messages:read`) — counts by status + per-operator SIM capacity snapshot.

### B.9 Webhooks (status callbacks)

Server POSTs to `callback_url` (per-message) or `clients.webhook_url` (default) on **every
status transition** the client cares about. Delivery is at-least-once with retries
(exp backoff, `Retry-After` honored, max 24h then drop → surfaced in `message_events` as `WEBHOOK_SENT`/failed).

```http
POST https://client.example/webhooks/sms
X-WSMS-Event: message.delivered
X-WSMS-Delivery: whd_01J8...
X-WSMS-Signature: t=1752483161,v1=6f3b...hexHMAC
Content-Type: application/json

{
  "event": "message.delivered",
  "id": "018f9b2a-...",
  "status": "DELIVERED",
  "previous_status": "SENT",
  "to": "+6281234567890",
  "target_operator": "TELKOMSEL",
  "assigned_operator": "TELKOMSEL",
  "on_net": true,
  "segments": 1,
  "reason": "NONE",
  "attempt": 1,
  "occurred_at": "2026-07-14T09:12:41.880Z",
  "metadata": { "order_id": "88213" }
}
```

Event names: `message.routed`, `message.dispatched`, `message.sent`, `message.delivered`,
`message.failed`, `message.expired`, `message.cancelled`. Clients MAY subscribe to a subset
(configured on the client record); by default only terminal events + `sent` are delivered.

**Signature verification (normative):**
`signed_payload = "{t}." + raw_body`; `expected = hex(HMAC_SHA256(client.webhook_secret, signed_payload))`;
compare `expected` to `v1` in constant time; reject if `|now - t| > 300s` (replay guard).

---

<a name="c-device--server-websocket-protocol"></a>

## C. Device ↔ Server WebSocket protocol

Transport: **WSS** at `wss://gw.example.id/v1/device/ws`. One persistent connection per
device, held open by an Android **foreground service** (ongoing notification). Because Doze
and app-kill will still tear this down, the server ALSO holds each device's **FCM token** and
sends a high-priority *data* message (`{"wsms":"wake"}`) to revive a dead process, which then
reconnects. The app prompts for battery-optimization exemption at enroll.

### C.1 Envelope (every frame, both directions)

```json
{ "type": "<frame_type>", "id": "<ULID>", "ts": 1752483153482, "data": { } }
```

- `type` — frame type (tables below).
- `id` — ULID unique within the connection; used to correlate acks.
- `ts` — epoch **ms** on the sender's clock.
- `data` — type-specific payload (may be `{}`).

Text frames, UTF-8 JSON. Max frame 64 KiB. Ping/pong: use protocol frames below **and**
WS-level ping every 20s (server-initiated) with 60s read deadline.

### C.2 Enrollment (one-time, over REST)

Before the WS ever opens, a fresh phone binds itself:

```http
POST /v1/devices/enroll
Content-Type: application/json

{
  "enroll_token": "ENR-7Q2X-9F4K",
  "device_key": "HP-D",
  "model": "Samsung SM-A155F",
  "os_version": "Android 14",
  "app_version": "1.0.0",
  "fcm_token": "e0j...:APA91b..."
}
```

Server validates `enroll_token` (hash match, not expired, uses<max_uses), creates/updates the
`devices` row, and returns a **long-lived device secret** used for WS auth:

```json
{
  "device_id": "9a11-...",
  "device_secret": "dsec_live_9a11.f83b...",
  "ws_url": "wss://gw.example.id/v1/device/ws",
  "config": { "heartbeat_sec": 25, "ack_timeout_sec": 15 }
}
```

The app stores `device_secret` in Android Keystore-backed encrypted storage. Losing it →
re-enroll with a new token.

### C.3 Runtime handshake

```mermaid
sequenceDiagram
    participant D as Device (Android)
    participant S as Server

    D->>S: WSS connect (Authorization: Bearer dsec_live_9a11.f83b...)
    Note over S: verify device_secret hash, check no other live session
    S-->>D: {type:"welcome", data:{session_id, config, resume_ok}}
    D->>S: {type:"hello", data:{device_id, app_version, battery, ignoring_batt_opt}}
    D->>S: {type:"sim_report", data:{sims:[...]}}
    S-->>D: {type:"ack", data:{ref:<sim_report.id>, status:"ok"}}
    S->>D: {type:"config", data:{per_sim_rate, jitter_ms, quotas}}
    loop every heartbeat_sec
        D->>S: {type:"heartbeat", data:{battery, sims_health}}
        S-->>D: {type:"ack", data:{ref:...}}
    end
```

**Single-connection rule:** if a second WS opens for the same `device_id`, the server closes
the older one (`close code 4001 "superseded"`) and updates `devices.session_id`. This
prevents split delivery. Auth failure → `close 4401`. Disabled device → `close 4403`.

### C.4 Frame catalog — Device → Server

| `type` | Purpose | Needs ack? |
|---|---|---|
| `hello` | Post-connect identity + capabilities | server `welcome` already sent; no |
| `sim_report` | Full current SIM inventory (authoritative) | yes (`ack`) |
| `heartbeat` | Liveness + battery + coarse sim health | yes (`ack`) |
| `send_ack` | Accept/reject a `send_command` | is itself the ack for the command |
| `delivery_report` | SENT / DELIVERED / FAILED lifecycle from Android PendingIntents | yes (`ack`) |
| `status` | Async device state change (sim added/removed, radio off) | yes (`ack`) |
| `ack` | Transport ack for a server-initiated frame (config/cancel/ping) | n/a |

**`sim_report`** — the device is the source of truth for its SIMs. Server reconciles into
`sims` (upsert by `device_id`+`slot_index`; refresh `subscription_id`, `operator`,
`carrier_name`, `mcc/mnc`, `msisdn` if present). Operator is derived by the server from
`mcc/mnc` first, then confirmed/overridden by `msisdn` prefix if a number is known.

```json
{
  "type": "sim_report",
  "id": "01J8ZQ...",
  "ts": 1752483150000,
  "data": {
    "sims": [
      { "slot_index": 0, "subscription_id": 3, "carrier_name": "Telkomsel",
        "mcc": "510", "mnc": "10", "msisdn": "+6281234500001",
        "iccid": "8962...", "state": "READY",
        "health": { "signal_dbm": -85, "radio": "LTE", "roaming": false } },
      { "slot_index": 1, "subscription_id": 5, "carrier_name": "XL Axiata",
        "mcc": "510", "mnc": "11", "msisdn": null,
        "iccid": "8962...", "state": "READY",
        "health": { "signal_dbm": -97, "radio": "LTE", "roaming": false } }
    ]
  }
}
```

`state` ∈ `READY|ABSENT|DISABLED|UNKNOWN`. A slot present in the device but missing from a
later `sim_report` → server marks that `sim.status=ABSENT`.

**`send_ack`** — device's immediate accept/reject of a dispatched job:

```json
{
  "type": "send_ack",
  "id": "01J8ZR...",
  "ts": 1752483154200,
  "data": {
    "ref": "01J8ZQ_CMD_9c1",          // the send_command frame id
    "message_id": "018f9b2a-...",
    "result": "accepted",              // accepted | rejected | duplicate
    "reason": "NONE",                  // failure_reason_t when rejected
    "sim_subscription_id": 3
  }
}
```

- `accepted` → server: `DISPATCHED`.
- `rejected` (e.g. `SIM_ABSENT`, `RADIO_OFF`, `QUOTA_EXCEEDED`) → server re-routes (new attempt).
- `duplicate` → device already has this `message_id`; server treats as accepted and awaits
  the (re-sent) `delivery_report`. **This is the anti-double-send handshake** (see [E](#e-idempotency)).

**`delivery_report`** — one or more per message, reflecting Android PendingIntent callbacks:

```json
{
  "type": "delivery_report",
  "id": "01J8ZS...",
  "ts": 1752483156221,
  "data": {
    "message_id": "018f9b2a-...",
    "sim_subscription_id": 3,
    "phase": "SENT",                   // SENT | DELIVERED | FAILED
    "reason": "NONE",                  // failure_reason_t
    "android_result_code": -1,         // raw Activity.RESULT_OK == -1
    "parts_total": 1,
    "parts_ok": 1,
    "occurred_at": "2026-07-14T09:12:36.221Z"
  }
}
```

- `phase:"SENT"` → message `SENT`, set `sent_at`, increment `sim.sent_today += segments`.
- `phase:"DELIVERED"` → message `DELIVERED` (terminal).
- `phase:"FAILED"` → server decides retry vs terminal (see state machine). `reason` carries
  the mapped Android code.

For multipart, the device aggregates per-part PendingIntents and emits ONE `SENT` when all
parts report OK (or `FAILED` on first hard failure), then ONE `DELIVERED` when all parts'
status reports arrive. `parts_ok/parts_total` expose partials.

**`status`** — spontaneous device change (SIM hot-swap, airplane mode, battery critical):

```json
{ "type":"status", "id":"01J8...", "ts":1752483200000,
  "data": { "kind":"sim_removed", "slot_index":1, "detail":"user ejected SIM" } }
```

`kind` ∈ `sim_added|sim_removed|radio_off|radio_on|battery_low|batt_opt_revoked|shutting_down`.

### C.5 Frame catalog — Server → Device

| `type` | Purpose | Device response |
|---|---|---|
| `welcome` | Handshake accept + session config | (none) |
| `send_command` | Instruct a specific SIM to send one message | `send_ack` then `delivery_report`(s) |
| `cancel` | Abort a not-yet-sent message | `ack` + (report if already sent) |
| `config` | Push runtime knobs (rate limits, jitter, heartbeat) | `ack` |
| `ping` | App-level keepalive/probe | `ack` |
| `ack` | Transport ack for a device frame | (none) |

**`send_command`** — THE core instruction. Targets exactly one SIM on one device:

```json
{
  "type": "send_command",
  "id": "01J8ZQ_CMD_9c1",
  "ts": 1752483153900,
  "data": {
    "message_id": "018f9b2a-...",         // idempotency key on the device
    "attempt": 1,
    "sim_subscription_id": 3,             // which local SmsManager to bind
    "sim_id": "3f0c-...",                 // server sim id (for the device's local mapping)
    "to": "+6281234567890",
    "body": "Kode OTP Anda: 448192. Berlaku 5 menit.",
    "encoding": "GSM7",
    "segments": 1,
    "request_delivery_report": true,      // register DELIVERED PendingIntent
    "expires_at": "2026-07-14T09:27:33.482Z",
    "pacing": { "min_gap_ms": 4000, "jitter_ms": 2500 }  // human-like spacing on this SIM
  }
}
```

Device MUST: check its local idempotency ledger for `message_id` first; if unseen, insert then
send via `SmsManager.getSmsManagerForSubscriptionId(sim_subscription_id)` (multipart via
`divideMessage`+`sendMultipartTextMessage` when `segments>1`), registering SENT (+DELIVERED
if requested) PendingIntents. If `message_id` already seen → respond `send_ack:duplicate`
and re-emit last known report; **do not send again**.

**`cancel`:**

```json
{ "type":"cancel", "id":"01J8...", "ts":..., "data":{ "message_id":"018f9b2a-..." } }
```

Device: if not yet handed to `SmsManager` → drop, `ack{cancelled:true}`. If already sent →
`ack{cancelled:false}` and the normal `delivery_report` still flows (server keeps `SENT`).

**`config`:**

```json
{ "type":"config", "id":"01J8...", "ts":...,
  "data": { "heartbeat_sec":25, "ack_timeout_sec":15,
            "per_sim": [ {"sim_id":"3f0c-...","daily_quota":200,"min_gap_ms":4000} ] } }
```

### C.6 ACK, timeout, redelivery semantics (normative)

**Two ack layers — do not conflate:**

1. **Transport ack** (`ack` frame, `data.ref = <frame.id>`): confirms *receipt* of a frame.
   Used for `sim_report`, `heartbeat`, `status`, `delivery_report` (device→server) and
   `config`, `cancel`, `ping` (server→device).
2. **Application ack** (`send_ack`): confirms the device *accepted the send job*. It doubles
   as the transport ack for `send_command` — the server does not additionally `ack` a command.

**Timeouts & redelivery:**

| From | Waits for | Timeout | On timeout |
|---|---|---|---|
| server, after `send_command` | `send_ack` | `ack_timeout_sec` (15s) | resend **same** command (same `message_id`, **new** frame `id`), up to 3 transport redeliveries; device dedups so no double-send |
| server, after `send_ack:accepted` | `delivery_report(SENT)` | `send_wait_sec` (60s) | keep `DISPATCHED`; on device reconnect re-query via `status`; do NOT reroute (SMS may already be gone) — let TTL decide |
| device, after `heartbeat` | `ack` | 10s | log; keep connection; escalate to reconnect after 3 misses |

**Reconnect resume:** on reconnect the device re-sends `sim_report` and any `delivery_report`s
the server hasn't `ack`'d (a small on-device outbox of un-acked reports). The server, for each
`DISPATCHED` message assigned to this device with no terminal report, does **not** re-issue a
new `send_command` unless the device explicitly reports `duplicate`/absence — it relies on the
device's idempotency ledger to resolve state.

---

<a name="d-message-status-state-machine"></a>

## D. Message status state machine

```mermaid
stateDiagram-v2
    [*] --> QUEUED : POST /v1/messages (validated, persisted)

    QUEUED --> ROUTING : worker picks up (scheduled_at reached)
    QUEUED --> CANCELLED : client cancel
    QUEUED --> EXPIRED : now > expires_at

    ROUTING --> DISPATCHED : SIM chosen + send_command delivered + send_ack=accepted
    ROUTING --> QUEUED : no eligible SIM now (backoff, retry later)\nON_NET_PREFERRED falls back to random SIM
    ROUTING --> FAILED : ON_NET_STRICT & no matching SIM before TTL\n(NO_MATCHING_OPERATOR_SIM) / ROUTE_EXHAUSTED
    ROUTING --> CANCELLED : client cancel
    ROUTING --> EXPIRED : now > expires_at

    DISPATCHED --> SENT : delivery_report phase=SENT (RESULT_OK)
    DISPATCHED --> ROUTING : send_ack=rejected OR delivery_report FAILED\nwith NOT-SENT reason (RADIO_OFF/NO_SERVICE/SIM_ABSENT)\n& attempts < max_attempts  [reroute, attempts++]
    DISPATCHED --> FAILED : rejected/failed & attempts >= max_attempts (ROUTE_EXHAUSTED)
    DISPATCHED --> CANCELLED : cancel wins the race (not yet handed to SmsManager)
    DISPATCHED --> EXPIRED : now > expires_at with no SENT

    SENT --> DELIVERED : delivery_report phase=DELIVERED (status 0x00)
    SENT --> FAILED : delivery_report DELIVERY_PERMANENT_ERROR
    SENT --> SENT : DELIVERY_TEMP_ERROR (network still trying)
    SENT --> EXPIRED : no DELIVERED before delivery_wait TTL\n(kept as SENT-but-unconfirmed if carrier gives no report)

    DELIVERED --> [*]
    FAILED --> [*]
    EXPIRED --> [*]
    CANCELLED --> [*]
```

**Retry loop — the critical safety rule.** The **only** back-edge that re-selects a
different SIM/device is `DISPATCHED → ROUTING`, and it fires **exclusively** when the server
has positive evidence the SMS did **not** leave the phone:

- `send_ack.result = "rejected"`, or
- `delivery_report.phase = "FAILED"` with a **NOT-SENT** reason: `RADIO_OFF`, `NO_SERVICE`,
  `SIM_ABSENT`, `NULL_PDU`, `QUOTA_EXCEEDED`, `RATE_LIMITED`.

If the outcome is ambiguous (command delivered but no `send_ack`, or device went offline
mid-flight), the server keeps the message on the **same** `message_id`/SIM and relies on the
device idempotency ledger — it MUST NOT reroute to a different SIM, because the first SIM may
have already sent. Ambiguous messages resolve when the device reconnects and re-reports, or
they hit `EXPIRED`. This is how "retry" never becomes "double-send".

Each attempt uses backoff `min(30s * 2^(attempt-1), 10m)` with ±20% jitter and is capped by
`max_attempts` (default 3) and `expires_at`. Every transition writes a `message_events` row.

**Routing decision (invoked on `QUEUED → ROUTING`)** — pseudocode:

```text
route(msg):
  op = msg.target_operator
  candidates = sims WHERE status=READY AND enabled AND
               sent_today + msg.segments <= daily_quota AND device.status=ONLINE
  match = candidates WHERE operator == op          // on-net (cheaper/free)
  switch msg.routing_policy:
    PINNED:            pool = candidates WHERE sim_id == msg.requested_sim_id
    ON_NET_STRICT:     pool = match
    ON_NET_PREFERRED:  pool = match if non-empty else candidates   // FALLBACK: random SIM
    ANY:               pool = candidates
  if pool empty:
     if strict/pinned and TTL not near: back to QUEUED (retry later)
     else: FAILED(NO_MATCHING_OPERATOR_SIM | NO_ONLINE_SIM)
  sim = pick(pool)   // least-loaded-then-random: min(sent_window), tie-break random
  assign msg.sim/device; send send_command; -> DISPATCHED on accepted
```

Fallback (owner's requirement) is exactly the `ON_NET_PREFERRED` branch: prefer a SIM on the
target's operator; if none is online, pick a **random** available SIM. `on_net=true` in
API/webhook payloads records whether the actual send was on-net.

---

<a name="e-idempotency"></a>

## E. Idempotency & no-double-send guarantee (normative)

Three independent guards; all MUST be implemented:

1. **Submit dedup (client → server).** `Idempotency-Key`/`dedup_key` unique per client
   (`uq_msg_client_dedup`). Re-submitting returns the original message. Prevents the client
   accidentally creating two messages.

2. **Command idempotency (server → device).** `send_command.message_id` is the device-side
   idempotency key. The device keeps a local ledger:

   ```
   outbox(message_id PK, sim_subscription_id, phase, last_report_json, created_at)
   ```

   On `send_command`: `INSERT ... ON CONFLICT(message_id) DO NOTHING`. If the row is new →
   send once. If it already exists → reply `send_ack:duplicate` and re-emit `last_report_json`;
   **never call `SmsManager` twice for the same `message_id`.** Because the server always
   redelivers with the **same** `message_id` (only the frame `id` changes), transport
   redelivery after a lost `send_ack` is safe.

3. **Reroute guard (server).** A different SIM/device is chosen **only** from states with
   positive not-sent evidence (see [D](#d-message-status-state-machine)). Ambiguity → wait,
   never duplicate.

Together these ensure: a re-submitted request, a re-delivered command, a dropped ack, and a
mid-flight reconnect all converge to **exactly one SMS** per message (or zero + a terminal
`FAILED/EXPIRED`), never two.

---

## Appendix — legal & deliverability note (carried into the contract)

Bulk/A2P SMS sent from ordinary consumer SIMs is a **grey route** that violates carrier ToS
and Indonesian A2P rules. Carriers detect high-volume P2P-shaped traffic and **block/ban
SIMs**. This contract bakes in the *hygiene* mitigations — per-SIM `daily_quota`,
`sent_today`/`sent_window` accounting, `pacing.min_gap_ms`+`jitter_ms`, SIM rotation via the
routing pool, and `COOLDOWN`/`QUOTA_EXCEEDED` sim states — framed strictly as **ToS-compliance
and deliverability**, not detection-evasion. For sustained or higher volume, the owner SHOULD
move to a licensed A2P / official sender channel. The ban risk is real and MUST be surfaced to
the owner, not hidden behind the abstraction.

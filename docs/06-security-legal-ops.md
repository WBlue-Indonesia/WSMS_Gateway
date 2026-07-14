# 06 — Security, Legal & Operations

> **Status:** Normative for security posture and ops; **advisory + risk-disclosure** for
> the legal section. This document builds strictly on **[02 — Contract, Protocol & Schema](#)**
> (the canonical interface contract). Where this doc names a field, enum, endpoint, frame,
> or close code, it means the one defined in doc 02 — nothing here redefines the wire format
> or schema. Terms: **Server** = Go backend (Gin + GORM + Postgres). **Device** = an owned
> Android phone running the Flutter/Android sender app. **Client** = an external system
> calling the REST API. **Owner** = WBlue, the party that owns the phone fleet and holds the
> SIM subscriber accounts.

RFC 2119 keywords (MUST / SHOULD / MAY) carry their usual meaning.

---

## Table of contents

- [1. Security](#1-security)
  - [1.1 Threat model & trust boundaries](#11-threat-model--trust-boundaries)
  - [1.2 Transport security — TLS/WSS everywhere](#12-transport-security)
  - [1.3 Client API authentication (API keys)](#13-client-api-authentication)
  - [1.4 Optional inbound request signing (HMAC)](#14-optional-inbound-request-signing)
  - [1.5 Device enrollment, secrets & rotation](#15-device-enrollment-secrets--rotation)
  - [1.6 Authorization matrix](#16-authorization-matrix)
  - [1.7 Rate limiting & public-API abuse prevention](#17-rate-limiting--abuse-prevention)
  - [1.8 Secrets handling](#18-secrets-handling)
  - [1.9 Audit log](#19-audit-log)
- [2. Legal & compliance reality (READ THIS)](#2-legal--compliance-reality)
- [3. Operations & deployment](#3-operations--deployment)
  - [3.1 Deployment topology](#31-deployment-topology)
  - [3.2 Docker Compose](#32-docker-compose)
  - [3.3 Environment configuration](#33-environment-configuration)
  - [3.4 Prometheus metrics](#34-prometheus-metrics)
  - [3.5 Structured logging](#35-structured-logging)
  - [3.6 Alerting](#36-alerting)
  - [3.7 Backups & retention](#37-backups--retention)
- [4. Testing strategy](#4-testing-strategy)
  - [4.1 Unit tests — routing & encoding](#41-unit-tests)
  - [4.2 Simulated-device harness](#42-simulated-device-harness)
  - [4.3 Integration & no-double-send chaos tests](#43-integration--chaos-tests)
  - [4.4 Load test](#44-load-test)
- [5. Phased rollout roadmap](#5-phased-rollout-roadmap)
- [6. Cost & deliverability model](#6-cost--deliverability-model)

---

<a name="1-security"></a>

## 1. Security

### 1.1 Threat model & trust boundaries

**Assets, ranked by blast radius:**

| Asset | Why it matters | Where it lives |
|---|---|---|
| Message bodies (OTPs, PII) | Account-takeover fuel if leaked; regulated personal data | `messages.body`, `message_events`, logs |
| SIM fleet subscriber identities | Tied to a real NIK (Indonesian SIM registration); a ban attaches to a person | Physical phones; `sims` |
| Client API secrets | Impersonation → send SMS / read others' messages | `api_keys.secret_hash` |
| Device secrets | Rogue device could accept jobs / spoof reports | `devices.secret_hash` |
| Enrollment tokens | Register a rogue sender into the fleet | `enrollment_tokens.token_hash` |
| Webhook secrets | Forge status callbacks to clients | `clients.webhook_secret` (encrypted) |

**Adversaries & primary mitigations:**

| Adversary | Goal | Primary control |
|---|---|---|
| External attacker (internet) | Send free SMS, exfiltrate OTPs | API-key auth + scopes + TLS + rate limit |
| Network MITM | Read/alter traffic | TLS 1.3, WSS, cert pinning on app |
| Rogue/stolen device | Join fleet, siphon jobs | Enrollment token + device secret + single-connection rule + `DISABLED` kill switch |
| Compromised client key | Abuse one tenant's quota | Per-key scopes, revocation, per-client rate limit, anomaly auto-suspend |
| Malicious insider | Dump message bodies | Least-privilege DB roles, audit log, retention purge, PII redaction in logs |
| Carrier abuse-detection | Ban SIMs | **Not a security control** — see [§2](#2-legal--compliance-reality) |

**Trust boundaries:**

```mermaid
flowchart LR
    subgraph Internet
      C[Client systems]
      D[Android devices]
    end
    subgraph Edge["TLS termination (Caddy/Traefik)"]
      RP[Reverse proxy\nTLS 1.3 + HSTS]
    end
    subgraph Private["Private network (no public ingress)"]
      GW[Go Gateway\nGin]
      PG[(Postgres)]
      RS[(Redis)]
      PR[Prometheus]
    end
    FCM[[FCM push]]

    C -- HTTPS /v1 + Bearer --> RP
    D -- WSS /v1/device/ws + dsec --> RP
    RP --> GW
    GW --> PG
    GW --> RS
    GW -- wake --> FCM
    FCM -. high-prio data .-> D
    PR -- scrape /metrics --> GW
```

**Boundary rules:**
- The Go gateway, Postgres, Redis, and Prometheus MUST NOT be exposed to the public internet.
  Only the reverse proxy listens publicly (443).
- Postgres and Redis MUST require auth and MUST NOT bind `0.0.0.0` outside the Docker network.
- `/metrics` MUST NOT be publicly reachable (scraped over the private network, or protected
  by a bearer token / mTLS).

---

<a name="12-transport-security"></a>

### 1.2 Transport security — TLS/WSS everywhere

- **Public REST is HTTPS-only; device WS is WSS-only.** Plain `http://` / `ws://` MUST be
  refused (redirect 308 → HTTPS for browsers; hard-close for the device WS).
- **TLS 1.3 preferred, 1.2 minimum.** Disable TLS ≤ 1.1, RC4, 3DES, and CBC-only suites.
- **HSTS:** `Strict-Transport-Security: max-age=63072000; includeSubDomains` on all responses.
- **Certificates:** automatic issuance/renewal via the reverse proxy (Caddy ACME or
  Traefik + Let's Encrypt). Alert 21 days before expiry (metric in [§3.6](#36-alerting)).
- **Certificate pinning (app):** the Android app SHOULD pin the server's leaf/intermediate
  SPKI. Because renewal rotates the leaf, pin the **intermediate** or ship **two** pins
  (current + next) so renewal never bricks the fleet. Provide an out-of-band pin-update path.
- **WSS keepalive** already defined by the contract (server WS ping every 20s, 60s read
  deadline) — this doubles as a dead-connection detector, not just transport hygiene.

---

<a name="13-client-api-authentication"></a>

### 1.3 Client API authentication (API keys)

The contract fixes the scheme: `Authorization: Bearer wsms_<env>_<keyid>.<secret>`, the
server splits on the first `.`, looks up `api_keys.key_id`, and verifies `secret` against
`secret_hash` with **Argon2id** in constant time. This section defines the **lifecycle** ops
around that.

**Argon2id parameters (baseline, tune to hardware):**

```
memory = 64 MiB, iterations = 3, parallelism = 2, salt = 16B random, key = 32B
```

Store as an encoded PHC string so params can evolve; re-hash on next successful auth if the
stored params are below current policy.

**Issuance:**
1. Admin (scope `admin`) calls `POST /v1/admin/clients/:id/keys` with requested `scopes` and
   optional `expires_at`.
2. Server generates `key_id` (`wsms_live_<rand8>`) + a 32-byte random `secret`, stores only
   `secret_hash`, returns the **full token exactly once**. It is never retrievable again.
3. `key_id` is safe to show in dashboards/logs; `secret` is not.

**Rotation (zero-downtime):**
- Keys are additive. To rotate, issue a **new** key, deploy it to the client, then set
  `revoked_at` on the old one. Multiple live keys per client are allowed — that overlap
  window is the whole point.
- `last_used_at` (updated best-effort, async) lets an operator confirm the old key is idle
  before revoking.
- Recommend a rotation cadence of ≤ 90 days for high-traffic clients; enforce softly via a
  dashboard warning, not a hard cutoff that breaks sends.

**Revocation & expiry:**
- `revoked_at != NULL` or `expires_at < now` → `401 UNAUTHENTICATED`. Checked on every request.
- Revocation MUST take effect immediately (no positive auth cache longer than a few seconds;
  a short negative/positive cache keyed on `key_id` with ≤ 5s TTL is acceptable).

**Brute-force resistance:** constant-time compare is mandatory; also apply a per-`key_id` and
per-source-IP failed-auth throttle (e.g. 10 failures/min → 1-minute lockout) to blunt
credential-stuffing. Never reveal whether it was the `key_id` or the `secret` that was wrong.

---

<a name="14-optional-inbound-request-signing"></a>

### 1.4 Optional inbound request signing (HMAC) — hardening extension

Bearer auth from [§1.3](#13-client-api-authentication) is the **normative** mechanism. For
high-assurance clients (bearer token could leak in a proxy log), the server MAY additionally
require an HMAC signature over the request body. This is a *defense-in-depth extension*, not a
replacement, and it deliberately mirrors the **outbound webhook** scheme already in the
contract so both directions share one algorithm and one mental model.

- Enabled per-key via a `require_signing` flag on the client record (admin-set).
- Header on `POST /v1/messages` and `/batch`:
  `X-WSMS-Signature: t=<unix_seconds>,v1=<hex HMAC_SHA256(api_secret, "{t}." + raw_body)>`
- Server recomputes with the same `api_secret` used for bearer auth, constant-time compares,
  and rejects if `|now − t| > 300s` (replay window, same as webhooks).
- Combined with the mandatory `Idempotency-Key`, a captured-and-replayed request within the
  window still collapses to the original message (submit dedup), so signing + idempotency
  together defeat replay.

> Rationale for symmetry: clients already implement HMAC-SHA256 verification for inbound
> webhooks; reusing it for outbound signing means one library, one test suite.

---

<a name="15-device-enrollment-secrets--rotation"></a>

### 1.5 Device enrollment, secrets & rotation

This hardens the flow the contract already defines (`enrollment_tokens`,
`POST /v1/devices/enroll`, `device_secret`).

**Token issuance (admin → phone):**
1. Operator generates a token in the admin UI. Server stores `sha256(raw_token)` in
   `enrollment_tokens.token_hash` with `label`, `max_uses` (default 1), `expires_at`
   (recommend ≤ 24h). The raw `ENR-XXXX-XXXX` is shown once and typed/QR-scanned into the
   fresh phone.
2. Tokens are **short-lived and single-use by default**. A leaked expired/consumed token is
   worthless.

**Enrollment → device secret:**
- On `POST /v1/devices/enroll` the server validates: hash match, not expired,
  `uses < max_uses`; increments `uses`; creates/updates the `devices` row; and returns a
  **long-lived** `device_secret` (`dsec_live_<device_id>.<secret>`), storing only its
  Argon2id hash in `devices.secret_hash`.
- **Android-only enforcement (contract hard fact):** enrollment MUST reject `platform != "android"`
  for a sender role. iOS cannot send SMS programmatically; an iOS build could only *monitor*.
- The app stores `device_secret` in **Android Keystore-backed encrypted storage**
  (`EncryptedSharedPreferences` / Keystore-wrapped key). It never leaves the device and is
  never logged.

**WS auth & the single-connection rule:**
- The device authenticates the WSS handshake with `Authorization: Bearer dsec_live_...`.
- A second live connection for the same `device_id` closes the older one with WS close
  **4001 "superseded"** and updates `devices.session_id`. Bad secret → **4401**. A device
  whose `status = DISABLED` → **4403** and is refused. These close codes are the fleet kill
  switch.

**Device secret rotation & compromise response:**
- **Rotate:** admin issues a fresh enrollment token; the phone re-enrolls, obtaining a new
  `device_secret`; the old secret becomes dead once its `secret_hash` is overwritten.
- **Compromise / lost or stolen phone:** set `devices.status = DISABLED`. The next handshake
  (or the current one, force-closed) gets 4403. Because SIMs are addressed by server-side
  `sim_id`, disabling the device also removes its SIMs from the routing pool immediately.
- **Never** ship a device secret through analytics, crash reporters, or backups.

```mermaid
sequenceDiagram
    participant Admin
    participant S as Server
    participant P as Fresh phone
    Admin->>S: create enrollment token (label HP-D, max_uses 1, ttl 24h)
    S-->>Admin: ENR-7Q2X-9F4K  (shown once)
    Admin->>P: type / scan token
    P->>S: POST /v1/devices/enroll {enroll_token, device_key, fcm_token, platform:android}
    S->>S: verify hash, not expired, uses<max_uses; uses++
    S-->>P: {device_id, device_secret dsec_live_..., ws_url, config}
    P->>P: store device_secret in Android Keystore
    P->>S: WSS connect (Bearer dsec_live_...)
    Note over S: on 2nd session → close 4001 old; bad secret → 4401; disabled → 4403
```

---

<a name="16-authorization-matrix"></a>

### 1.6 Authorization matrix

Scopes are the contract's closed set: `messages:write`, `messages:read`, `devices:read`,
`sims:read`, `admin`. Enforced after authentication; missing scope → `403 FORBIDDEN`.

| Endpoint | Required scope |
|---|---|
| `POST /v1/messages`, `/messages/batch`, `/messages/:id/cancel` | `messages:write` |
| `GET /v1/messages`, `/messages/:id` | `messages:read` |
| `GET /v1/devices`, `/devices/:id` | `devices:read` |
| `GET /v1/sims` | `sims:read` |
| `GET /v1/stats` | `messages:read` |
| `GET /v1/healthz`, `/readyz` | none (unauthenticated liveness) |
| `POST /v1/devices/enroll` | enrollment token (not an API key) |
| `POST /v1/admin/*` (issue/revoke keys, enroll tokens, enable/disable SIM/device, quotas) | `admin` |
| `wss /v1/device/ws` | device secret (not an API key) |

Principle of least privilege: an OTP-sending integration gets `messages:write` only. A
monitoring dashboard gets `messages:read` + `devices:read` + `sims:read`. `admin` is for
operators, issued sparingly, and SHOULD be signing-required ([§1.4](#14-optional-inbound-request-signing)).

---

<a name="17-rate-limiting--abuse-prevention"></a>

### 1.7 Rate limiting & public-API abuse prevention

Layered limits — the first that trips wins:

1. **Per-client submit rate** (contract): token bucket at `clients.rate_limit_per_sec`.
   Exhaustion → `429` with `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `Retry-After`.
   Counters live in Redis so the limit is correct across multiple gateway replicas.
2. **Body & batch caps** (contract): `body` 1..1600 chars, hard cap **10 segments**;
   batch ≤ **500** items; `metadata` ≤ 4 KB. Oversize → `BODY_TOO_LONG` / `VALIDATION_ERROR`.
3. **Per-SIM daily quota & pacing** (contract): `sims.daily_quota` in segments,
   `sent_today` accounting, `pacing.min_gap_ms` + `jitter_ms`, `QUOTA_EXCEEDED` / `COOLDOWN`
   statuses. This is the fleet-protection layer, not a client-facing limit — but it caps how
   much any client can push through in aggregate.
4. **Fleet capacity guard:** `POST /v1/messages` still returns `202 QUEUED` even when no SIM
   is free (the message waits for TTL), but `GET /v1/readyz` flips to `503` and `GET /v1/sims`
   exposes `on_net_ready` so clients can back off. `NO_ROUTE_AVAILABLE` is surfaced when a
   strict/pinned policy provably cannot be served.
5. **Anomaly auto-suspend:** a background monitor flips `clients.status = 'suspended'` (→ all
   submits `403`) on abuse signals: sustained `429` saturation, a failure ratio > 50% over
   the last 200 sends (spraying dead numbers), or a submit-rate spike > 10× the client's
   trailing hour. Suspension is logged to the audit log and requires an `admin` unsuspend.
6. **Network hygiene:** optional per-client IP allowlist; global connection/read timeouts;
   request-body size limit at the proxy; `X-Request-ID` propagated end to end.

> These are **application-hygiene** limits (fair use, fleet protection, cost control). They
> are explicitly **not** carrier-detection-evasion — see [§2](#2-legal--compliance-reality).

---

<a name="18-secrets-handling"></a>

### 1.8 Secrets handling

| Secret | At rest | In transit | Rotation |
|---|---|---|---|
| API key secret | Argon2id hash only | TLS | new key + revoke old ([§1.3](#13-client-api-authentication)) |
| Device secret | Argon2id hash only; raw in Android Keystore | TLS/WSS | re-enroll ([§1.5](#15-device-enrollment-secrets--rotation)) |
| Enrollment token | `sha256` hash only | shown once | single-use + short TTL |
| Webhook secret | **Encrypted** (`bytea`, KMS/age envelope) | never sent to client after creation | re-issue + dual-verify window |
| DB / Redis / FCM creds | Docker/host secret store or `.env` (0600), never in image | private network | manual, documented in runbook |
| TLS private key | reverse-proxy volume, `0600` | n/a | ACME auto-renew |

**Rules:**
- No secret is ever written to logs, traces, error messages, crash reports, or metrics labels.
- Environment variables carry infra secrets; application-issued secrets (API/device) live only
  hashed in the DB. For managed deployments prefer Docker secrets / a KMS over a bare `.env`.
- `WebhookSecret` is stored encrypted at rest per the contract; the encryption key comes from
  the environment (`WSMS_ENCRYPTION_KEY`, 32B) and is itself never committed.
- Argon2/HMAC key material and the encryption key rotate via envelope encryption so a rotation
  does not require re-hashing every row.

---

<a name="19-audit-log"></a>

### 1.9 Audit log

Two complementary, append-only trails:

1. **Operational trail — `message_events`** (contract A.7). Every status transition and every
   device report, never updated, never soft-deleted, purged only by the retention job. Powers
   delivery debugging and webhook replay (`WEBHOOK_SENT` events included).
2. **Administrative trail — `admin_audit`** (ops-owned table, additive to the contract; it
   records *config* actions the contract's operational tables don't). One row per privileged
   action:

```
admin_audit(
  id uuidv7 PK, actor text, actor_key_id text, action text,
  target_type text, target_id text, before jsonb, after jsonb,
  source_ip inet, request_id text, created_at timestamptz )
```

Logged actions: API key issue/revoke, enrollment-token create, device enroll/disable,
SIM enable/disable, quota change, client suspend/unsuspend, webhook-secret rotate. `before`/
`after` capture the diff. Never store raw secrets — store `key_id`/`device_id` references.
Retain admin audit longer than operational data (recommend 1 year) for accountability.

---

<a name="2-legal--compliance-reality"></a>

## 2. Legal & compliance reality (READ THIS)

> ### ⚠️ Plain-language disclosure — do not skip, do not soften
>
> **Sending application-to-person (A2P) SMS — OTPs, notifications, marketing — from ordinary
> consumer SIM cards is a "grey route." It violates the carriers' terms of service and
> Indonesia's A2P telecom rules. Carriers actively detect this traffic and BLOCK or
> permanently BAN the SIMs. This risk cannot be engineered away. It can only be reduced,
> disclosed, and accepted — or avoided by using a licensed A2P channel.**
>
> Everything this system does to "pace," "rotate," and "quota" SIMs is **deliverability and
> fleet-longevity hygiene and courtesy to the network — it is NOT a shield against bans, and
> it is NOT an attempt to evade detection.** If volume grows, the correct answer is a licensed
> A2P provider, not more SIMs.

### 2.1 What exactly is the violation

- **Carrier ToS.** Consumer/prepaid plans are for person-to-person use. Bulk, automated, or
  commercial messaging from them breaches the subscriber agreement. The carrier's remedy is
  suspension or termination of the number — no notice required.
- **Indonesian A2P regime.** Legitimate A2P/masking SMS (OTP, transactional, promo) is meant
  to flow through **registered sender IDs / SMS masking / official A2P aggregators** contracted
  with the operators, under the telecom regulator (Komdigi/Kominfo). Injecting that same
  traffic through consumer SIMs bypasses that regime.
- **SIM registration ties the risk to a real person.** Indonesian prepaid SIMs are registered
  to a national ID (NIK) and family card (KK), with a cap (commonly **3 numbers per NIK per
  operator**). A ban is therefore not "lose a SIM and buy another anonymously" — it attaches to
  the **registered owner's identity** and constrains how large the fleet can even get.

### 2.2 How carriers detect it (stated so the owner understands the risk, not to evade it)

Carriers fingerprint A2P-shaped traffic on consumer SIMs: high one-to-many fan-out, bursty
volume, near-identical message templates, unusually short inter-message gaps, low/absent
inbound (no real conversations), and known grey-route device signatures. When flagged, the SIM
is throttled, then blocked, sometimes permanently, and sometimes the whole subscriber account.

### 2.3 Who bears the risk

| Party | Exposure |
|---|---|
| **Owner (WBlue)** | **Primary.** SIM suspension/blacklist tied to the registered NIK; loss of the fleet; possible account-level termination; regulatory exposure for operating an unlicensed A2P route; cost of re-provisioning. |
| **Platform operator (whoever runs the gateway)** | Operational: silent delivery failure as SIMs die; support burden; reputational risk if used for spam. |
| **Clients** | Deliverability: their OTPs/notifications silently stop arriving when SIMs are banned. This is why the API surfaces `on_net`, `NO_ROUTE_AVAILABLE`, and per-operator capacity. |
| **End recipients** | Receive OTPs from rotating unknown personal numbers — lower trust, higher phishing confusion. |

The owner MUST acknowledge, in writing, that **they hold the ban and regulatory risk** before
production use. A sign-off checklist is in [§2.6](#26-owner-sign-off).

### 2.4 When to switch to a licensed A2P provider

Move (or route the high-volume/regulated slice) to a **licensed A2P / SMS-masking aggregator**
when any of these is true:

- Sustained volume beyond a few hundred messages/day across the fleet, or growth is expected.
- OTP / transactional traffic where **delivery must be reliable** (a banned SIM = failed logins).
- You need a **branded sender ID** (masking) rather than a rotating personal MSISDN.
- Marketing/promotional content at any meaningful scale.
- Any contractual or regulatory requirement for auditable, compliant delivery.

The consumer-SIM gateway is appropriate only for **small, low-volume, internal, or
best-effort** use where occasional SIM loss is tolerable and the owner has accepted the risk.
Design the client integration so the **same submit API can front a licensed provider later** —
the gateway becomes one route among several (off-ramp is a Phase 4 milestone, [§5](#5-phased-rollout-roadmap)).

### 2.5 Compliant-usage guidance (deliverability + doing right by recipients)

Independent of the grey-route issue, follow legitimate-messaging norms — they reduce spam
complaints (a major ban trigger) and keep you on the right side of consumer-protection rules:

- **Opt-in only.** Send only to recipients who requested it (their own OTP, an alert they
  subscribed to). No purchased lists, no cold blasts.
- **Transactional over promotional.** Prefer OTP/notification content; avoid marketing over
  consumer SIMs entirely.
- **Identify the sender** in the body and honor **STOP/UNSUB** opt-outs; maintain a suppression
  list and never send to a number that opted out.
- **Low volume, respect quiet hours** and recipient DND expectations.
- **No unlawful content** (fraud, phishing, illegal promotion) — obvious, and it is the fastest
  route to both bans and legal liability.

### 2.6 Data protection (PDP)

Message bodies routinely contain personal data and secrets (OTPs). Under Indonesia's Personal
Data Protection Law (UU 27/2022):

- **Minimize retention.** Operational tables (`messages`, `message_events`) are retained then
  hard-purged by the retention job (contract A.1); set a short window (default **30 days**,
  configurable) — an OTP has no reason to live in a DB for months.
- **Restrict access.** Least-privilege DB roles; message bodies never in application logs
  ([§3.5](#35-structured-logging)); admin access audited ([§1.9](#19-audit-log)).
- **Encrypt in transit and at rest** (TLS everywhere; disk-level encryption on the DB volume;
  encrypted backups).
- Have a lawful basis (consent/contract) for processing recipient numbers, and a breach-response
  runbook.

### 2.7 Owner sign-off

Before Phase 1 production traffic, capture an explicit acknowledgement (store it in
`admin_audit`):

- [ ] I understand A2P over consumer SIMs violates carrier ToS and Indonesian A2P rules.
- [ ] I accept that SIMs (registered to my NIK) may be blocked or permanently banned, with no
      notice, and that hygiene features do not prevent this.
- [ ] I accept the operational and regulatory risk sits with me (the owner).
- [ ] I will keep volume low, opt-in, and transactional, and honor opt-outs.
- [ ] I will migrate high-volume / critical traffic to a licensed A2P provider.

---

<a name="3-operations--deployment"></a>

## 3. Operations & deployment

### 3.1 Deployment topology

Single-host Docker Compose is sufficient for a 3–10 phone fleet. Components:

```mermaid
flowchart TB
    subgraph host["Single VM / host"]
      caddy[Caddy / Traefik\nTLS 443]
      gw[wsms-gateway\nGin + WS hub]
      pg[(Postgres 15)]
      rs[(Redis 7)]
      prom[Prometheus]
      graf[Grafana]
      am[Alertmanager]
    end
    caddy --> gw
    gw --> pg
    gw --> rs
    prom --> gw
    prom --> am
    graf --> prom
    gw -. FCM wake .-> fcm[[Google FCM]]
```

- **Redis** holds the token-bucket rate-limit counters and the per-SIM `sent_window` rolling
  counter (contract A.5), snapshotted to Postgres. It is a performance cache, not a source of
  truth — Postgres remains authoritative.
- The gateway is a single binary containing the REST API, the device WS hub, the routing
  worker, the retry/expiry sweepers, the webhook dispatcher, and the metrics endpoint. It can
  run 1 replica for this scale; the design keeps counters in Redis so a 2nd replica is possible
  later.

### 3.2 Docker Compose

```yaml
# docker-compose.yml
name: wsms-gateway

x-logging: &logging
  logging:
    driver: json-file
    options: { max-size: "10m", max-file: "5" }

services:
  caddy:
    image: caddy:2
    restart: unless-stopped
    ports: ["80:80", "443:443"]
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data           # ACME certs
      - caddy_config:/config
    depends_on: [gateway]
    <<: *logging

  gateway:
    build: { context: ., dockerfile: Dockerfile }
    restart: unless-stopped
    env_file: [.env]
    expose: ["8080", "9090"]       # 8080 API/WS, 9090 /metrics — never published to host
    depends_on:
      postgres: { condition: service_healthy }
      redis:    { condition: service_healthy }
    healthcheck:
      test: ["CMD", "/wsms", "healthcheck"]   # hits GET /v1/healthz internally
      interval: 15s
      timeout: 3s
      retries: 5
    <<: *logging

  postgres:
    image: postgres:15
    restart: unless-stopped
    environment:
      POSTGRES_DB: wsms
      POSTGRES_USER: wsms
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_password
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./deploy/pg-backup:/backup
    secrets: [pg_password]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U wsms -d wsms"]
      interval: 10s
      timeout: 5s
      retries: 5
    <<: *logging

  redis:
    image: redis:7
    restart: unless-stopped
    command: ["redis-server", "--requirepass", "${REDIS_PASSWORD}", "--appendonly", "yes"]
    volumes: [redisdata:/data]
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "${REDIS_PASSWORD}", "ping"]
      interval: 10s
      timeout: 3s
      retries: 5
    <<: *logging

  prometheus:
    image: prom/prometheus:latest
    restart: unless-stopped
    volumes:
      - ./deploy/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - ./deploy/alerts.yml:/etc/prometheus/alerts.yml:ro
      - promdata:/prometheus
    expose: ["9091"]
    <<: *logging

  alertmanager:
    image: prom/alertmanager:latest
    restart: unless-stopped
    volumes: ["./deploy/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro"]
    expose: ["9093"]
    <<: *logging

  grafana:
    image: grafana/grafana:latest
    restart: unless-stopped
    environment:
      GF_SECURITY_ADMIN_PASSWORD__FILE: /run/secrets/grafana_admin
    volumes: [grafanadata:/var/lib/grafana]
    secrets: [grafana_admin]
    expose: ["3000"]              # publish behind the proxy or an SSH tunnel only
    <<: *logging

secrets:
  pg_password:   { file: ./secrets/pg_password }
  grafana_admin: { file: ./secrets/grafana_admin }

volumes:
  pgdata: {}
  redisdata: {}
  promdata: {}
  grafanadata: {}
  caddy_data: {}
  caddy_config: {}
```

```
# deploy/Caddyfile
gw.example.id {
    encode gzip
    header Strict-Transport-Security "max-age=63072000; includeSubDomains"
    reverse_proxy gateway:8080        # WSS upgrades pass through transparently
}
```

Multi-stage `Dockerfile` (sketch): build the static Go binary (`CGO_ENABLED=0`), copy into a
`gcr.io/distroless/static` or `scratch` image, run as non-root. Run GORM `AutoMigrate` (or,
preferably, versioned migrations via `goose`/`golang-migrate`) on boot behind a leader lock.

### 3.3 Environment configuration

| Var | Example | Purpose |
|---|---|---|
| `WSMS_ENV` | `live` | Env tag; drives `wsms_<env>_` key prefix. |
| `WSMS_HTTP_ADDR` | `:8080` | REST + WS listener (behind proxy). |
| `WSMS_METRICS_ADDR` | `:9090` | Prometheus `/metrics` (private). |
| `DATABASE_URL` | `postgres://wsms:***@postgres:5432/wsms?sslmode=disable` | Postgres DSN (TLS on if DB is remote). |
| `REDIS_URL` | `redis://:***@redis:6379/0` | Rate-limit + pacing counters. |
| `WSMS_ENCRYPTION_KEY` | 32-byte base64 | Envelope key for `webhook_secret` at rest. |
| `WSMS_ARGON2_MEM_MIB` / `_ITER` / `_PAR` | `64` / `3` / `2` | Password-hash tuning. |
| `WSMS_MSG_TTL_DEFAULT_SEC` | `21600` | Default 6h TTL (contract). |
| `WSMS_MSG_RETENTION_DAYS` | `30` | Purge window for `messages`/`message_events`. |
| `WSMS_SIM_DAILY_QUOTA_DEFAULT` | `200` | Default per-SIM segment cap. |
| `WSMS_PACING_MIN_GAP_MS` / `_JITTER_MS` | `4000` / `2500` | Default per-SIM pacing. |
| `WSMS_QUOTA_TZ` | `Asia/Jakarta` | Daily `sent_today` reset zone (contract). |
| `WSMS_WS_PING_SEC` / `_READ_DEADLINE_SEC` | `20` / `60` | WS keepalive (contract). |
| `WSMS_ACK_TIMEOUT_SEC` / `_SEND_WAIT_SEC` | `15` / `60` | Command ack / SENT wait (contract C.6). |
| `FCM_CREDENTIALS_JSON` | path/secret | Service-account for the wake push. |
| `WSMS_LOG_LEVEL` / `_LOG_FORMAT` | `info` / `json` | Structured logging. |
| `WSMS_WEBHOOK_MAX_RETRY_HOURS` | `24` | Webhook retry ceiling (contract B.9). |

### 3.4 Prometheus metrics

Exposed on the private `/metrics`. Names use a `wsms_` prefix. The four the owner explicitly
asked for are called out.

**Messages / queue:**
```
wsms_messages_submitted_total{client, operator, result}      counter  # accepted|rejected
wsms_message_transitions_total{from, to, reason}             counter  # every state edge
wsms_queue_depth{status}                                     gauge    # ★ QUEUED/ROUTING/DISPATCHED backlog
wsms_dispatch_latency_seconds{operator, on_net}              histogram# QUEUED→DISPATCHED
wsms_e2e_latency_seconds{operator, on_net}                   histogram# submit→SENT and submit→DELIVERED (le buckets)
wsms_reroutes_total{reason}                                  counter
wsms_messages_terminal_total{status, reason}                 counter  # DELIVERED/FAILED/EXPIRED/CANCELLED
```

**Per-SIM / fleet (★ per-SIM send rate, ★ delivery success, ★ device online):**
```
wsms_sim_sent_segments_total{sim_id, device_key, operator, on_net}  counter
  # ★ per-SIM send RATE via rate(wsms_sim_sent_segments_total[5m])
wsms_sim_sent_today{sim_id, operator}                        gauge
wsms_sim_daily_quota{sim_id}                                 gauge
wsms_sims_ready{operator}                                    gauge    # READY & enabled & quota-left, by operator
wsms_devices_online                                          gauge    # ★ device online count
wsms_ws_connections                                          gauge
wsms_delivery_success_ratio{operator}                        gauge
  # ★ delivery success % = delivered / (delivered + failed), computed by an exporter recording rule
wsms_sim_failures_total{sim_id, reason}                      counter  # spikes here ⇒ possible SIM ban
```

**Transport / API / webhooks:**
```
wsms_http_requests_total{route, method, code}                counter
wsms_http_request_duration_seconds{route}                    histogram
wsms_rate_limited_total{client}                              counter
wsms_ws_frames_total{type, dir}                              counter  # dir=in|out
wsms_webhook_delivery_total{result}                          counter  # ok|retry|dropped
wsms_webhook_queue_depth                                     gauge
wsms_build_info{version, commit}                             gauge=1
wsms_tls_cert_expiry_seconds                                 gauge    # from proxy exporter
```

A Prometheus **recording rule** derives the success ratio and per-operator send rate so
Grafana panels and alerts read a single stable series:

```yaml
# excerpt: deploy/prometheus.yml recording rules
- record: wsms:delivery_success_ratio:5m
  expr: |
    sum by (operator) (rate(wsms_messages_terminal_total{status="DELIVERED"}[15m]))
    /
    clamp_min(sum by (operator) (
      rate(wsms_messages_terminal_total{status=~"DELIVERED|FAILED"}[15m])), 1e-9)
```

### 3.5 Structured logging

- **JSON logs** (Go `log/slog` JSON handler or zerolog), one event per line, level-gated.
- **Correlation fields on every line where relevant:** `request_id`, `client_id`, `message_id`,
  `device_id`, `sim_id`, `frame_id`, `attempt`, `event_type`. This makes a single message's
  whole life greppable across REST → routing → WS → webhook.
- **PII/secret redaction is mandatory.** Never log `messages.body`, OTP contents, API/device
  secrets, or full MSISDNs. Log `body_len`, `segments`, `encoding`, and a **masked** number
  (`+62812****7890`) instead. This aligns with the PDP obligations in [§2.6](#26-data-protection-pdp).
- **Levels:** `error` = actionable failure; `warn` = reroute/quota/rate-limit/ban-suspect;
  `info` = state transitions + admin actions; `debug` = frame-level trace (off in prod).
- Ship logs to the Docker json-file driver (rotated, as configured) or forward to Loki/ELK for
  a fleet with more devices.

### 3.6 Alerting

Prometheus rules → Alertmanager → (Telegram/Slack/email). Concrete rules:

| Alert | Condition | Severity | Meaning / action |
|---|---|---|---|
| `NoDeviceOnline` | `wsms_devices_online == 0` for 2m | page | Fleet is dark; no sends possible. |
| `ReadyzFailing` | `probe readyz != 200` for 3m | page | DB down or no READY SIM. |
| `LowDeliverySuccess` | `wsms:delivery_success_ratio:5m < 0.8` for 15m | page | Deliverability dropping — **possible SIM bans**. |
| `SimBanSuspected` | `rate(wsms_sim_failures_total{reason=~"NO_SERVICE\|GENERIC_FAILURE"}[10m]) > k` on one `sim_id` | warn→page | A specific SIM is likely blocked; disable & investigate. |
| `QueueBacklog` | `wsms_queue_depth{status="QUEUED"} > 500` for 5m | warn | Capacity < demand; back-pressure clients. |
| `FleetQuotaExhausted` | `sum(wsms_sims_ready) == 0` for 5m | warn | All SIMs quota-capped/cooldown. |
| `WebhookBacklog` | `wsms_webhook_queue_depth > 200` for 10m | warn | Client endpoints failing. |
| `HighRerouteRate` | `rate(wsms_reroutes_total[10m])` above baseline | info | Routing churn; radio/SIM instability. |
| `CertExpirySoon` | `wsms_tls_cert_expiry_seconds < 21d` | warn | ACME renewal stuck. |
| `GatewayDown` | `up{job="wsms"} == 0` for 1m | page | Process/host down. |

`LowDeliverySuccess` + `SimBanSuspected` are the early-warning system for the [§2](#2-legal--compliance-reality)
ban reality — they turn "SIMs silently dying" into a page.

### 3.7 Backups & retention

- **Postgres:** nightly `pg_dump` (encrypted with `age`/GPG) to `./deploy/pg-backup`, plus
  **WAL archiving / continuous archiving** for point-in-time recovery if the fleet grows.
  Copy backups off-host (object storage). Keep 7 daily + 4 weekly.
- **Restore drills:** a documented, *tested* restore into a scratch DB at least quarterly — an
  untested backup is not a backup.
- **Redis:** `appendonly yes` (already set). Redis is a cache; loss means recomputing counters,
  not data loss — acceptable.
- **Retention purge (contract A.1):** a scheduled job hard-deletes `messages` and
  `message_events` older than `WSMS_MSG_RETENTION_DAYS`. Run **after** the nightly backup so a
  purged message still exists in one prior backup, then falls out of the backup rotation too —
  meeting PDP minimization.
- **Config-lifecycle tables** (`clients`, `api_keys`, `devices`, `sims`) use soft delete and
  are retained; they carry no message content.
- **Secrets are not in backups in plaintext** — `webhook_secret` is stored encrypted, password
  columns are hashes, so a DB dump leak does not expose usable credentials.

**Runbook one-liners** (keep in `deploy/RUNBOOK.md`): enroll a device, rotate an API key,
disable a suspected-banned SIM (`admin` → SIM `enabled=false`), drain a device before
maintenance (set `DISABLED`, wait for in-flight `DISPATCHED` to terminate/EXPIRE), restore DB.

---

<a name="4-testing-strategy"></a>

## 4. Testing strategy

The golden constraint: **test the whole WS + dispatch loop without sending a single real SMS.**
Real SMS costs money, burns quota, and risks the very bans in [§2](#2-legal--compliance-reality).

### 4.1 Unit tests — routing & encoding

Pure functions from the contract, table-driven Go tests (`_test.go`, `testing` + `testify`):

- **`normalize(raw) → +62…`** (contract 0.3): `"0812-3456-7890"`, `"+62 812..."`, `"62812..."`,
  `"812..."` all → `+6281234567890`; `"+1202..."`, letters, too-short → `INVALID_MSISDN`.
- **`resolve(msisdn) → operator`** (contract 0.4 / A.8): every seeded prefix maps to the right
  `operator_t`; an unseeded prefix → `UNKNOWN`; boundary prefixes (`0851`→TELKOMSEL vs
  `0855`→INDOSAT, `0859`→XL vs `0855`→INDOSAT) are asserted explicitly.
- **`encode(body) → (encoding, segments)`** (contract 0.5): 160 GSM-7 → 1; 161 → 2 (153/part);
  a single `€`/`{` counts 2 septets; any emoji → UCS2, 70 → 1, 71 → 2 (67/part); 10-segment
  hard cap enforced.
- **`route(msg, sims) → pool + pick`** (contract D): the core matrix —

| Policy | Matching SIM online? | Any SIM online? | Expected |
|---|---|---|---|
| `ON_NET_STRICT` | yes | — | pick matching; `on_net=true` |
| `ON_NET_STRICT` | no | yes | pool empty → wait/`FAILED NO_MATCHING_OPERATOR_SIM` |
| `ON_NET_PREFERRED` | yes | — | pick matching; `on_net=true` |
| `ON_NET_PREFERRED` | no | yes | **fallback** random; `on_net=false` |
| `ON_NET_PREFERRED` | no | no | `FAILED NO_ONLINE_SIM` |
| `ANY` | — | yes | pick least-loaded |
| `PINNED` | pinned READY | — | pick pinned; else wait/fail |
| any | matching but `QUOTA_EXCEEDED` | other | quota'd SIM skipped |

  Assert the picker is **least-loaded-then-random** (`min(sent_window)`, random tie-break) and
  that a `DISABLED`/`ABSENT`/quota-capped SIM is never chosen.
- **Idempotency & state machine:** submit dedup (same `dedup_key` → original message returned);
  command dedup semantics; and that the **only** `DISPATCHED→ROUTING` back-edge fires solely on
  positive not-sent evidence (`send_ack rejected` or `delivery_report FAILED` with RADIO_OFF /
  NO_SERVICE / SIM_ABSENT / NULL_PDU / QUOTA_EXCEEDED / RATE_LIMITED), never on ambiguity.

### 4.2 Simulated-device harness

A **virtual device** that speaks the exact WS protocol (contract C) — no phone, no SIM, no SMS.
It is the workhorse for integration and load tests.

**What it does:**
1. Enrolls via `POST /v1/devices/enroll` with a test enrollment token, stores the returned
   `device_secret`.
2. Opens the WSS connection, completes the handshake (`welcome`→`hello`→`sim_report`), and sends
   `heartbeat` on the configured interval.
3. Advertises a **scriptable SIM inventory** (operators, slots, `subscription_id`, quotas).
4. On each `send_command`, applies a **scripted outcome policy** and replies with the correct
   `send_ack` then `delivery_report`(s) — honoring `message_id` idempotency (re-sent command
   with same `message_id` → `send_ack:duplicate`, re-emit last report; **never** "sends" twice).

**Scriptable outcomes** (per SIM or per message pattern):

```
- happy:        send_ack accepted → SENT (RESULT_OK) → DELIVERED (0x00)
- slow:         same, but delays each phase by N ms (tests ack_timeout / send_wait)
- reject:       send_ack rejected reason=RADIO_OFF        (expect reroute, attempts++)
- sent_no_dlr:  SENT then never DELIVERED                 (expect SENT→EXPIRED at TTL)
- fail_hard:    SENT then DELIVERY_PERMANENT_ERROR        (expect SENT→FAILED)
- quota:        send_ack rejected reason=QUOTA_EXCEEDED   (expect skip + reroute)
- flap:         drop the WS mid-command, reconnect, resend un-acked reports (resume path)
- dup_probe:    reply duplicate to a re-delivered command (anti-double-send handshake)
```

**Sketch (Go):**

```go
type VirtualDevice struct {
    key      string
    secret   string
    sims     []SimSpec              // operator, subID, slot, quota
    policy   func(cmd SendCommand) []Frame   // scripted outcome → ordered reply frames
    ledger   map[string]string      // message_id -> last_report_json (idempotency)
}

func (v *VirtualDevice) onSendCommand(cmd SendCommand) []Frame {
    if last, seen := v.ledger[cmd.MessageID]; seen {
        return []Frame{sendAck(cmd, "duplicate"), rawReport(last)} // never "send" twice
    }
    frames := v.policy(cmd)          // e.g. accepted → SENT → DELIVERED
    v.ledger[cmd.MessageID] = frames[len(frames)-1].JSON()
    return frames                    // harness writes these back with realistic timing
}
```

**Companion: app "dry-run" mode.** The real Android app ships a build flag (`WSMS_DRY_RUN`)
that, instead of calling `SmsManager`, logs the intended send and synthesizes the SENT/DELIVERED
PendingIntent callbacks. This lets a **real phone on a real WS** exercise the full stack —
including Doze, foreground-service survival, and FCM wake — without emitting SMS. Pair with the
Android emulator for permission/dual-SIM enumeration testing.

### 4.3 Integration & no-double-send chaos tests

End-to-end against a Compose stack (Postgres + Redis + gateway + N virtual devices):

- **Golden path:** submit → `202 QUEUED` → routed on-net → `DISPATCHED` → `SENT` → `DELIVERED`;
  assert `message_events` timeline, webhook fired with valid HMAC, `sent_today += segments`.
- **Fallback path:** target Telkomsel with only an XL virtual SIM online under `ON_NET_PREFERRED`
  → delivered `on_net=false`; under `ON_NET_STRICT` → `FAILED NO_MATCHING_OPERATOR_SIM`.
- **No-double-send chaos** (the safety property that matters most):
  - Drop the first `send_ack` → server redelivers same `message_id`, new frame id → device
    replies `duplicate` → still **exactly one** synthesized send.
  - Kill the WS after `send_ack:accepted` but before `delivery_report` → server keeps
    `DISPATCHED`, does **not** reroute; on reconnect the device replays the report → resolves.
  - Reconnect mid-flight, re-send the on-device outbox → no duplicate SMS, no duplicate webhook.
  - Assert invariant across every scenario: **per `message_id`, count of "SmsManager.send"
    (synthesized) ∈ {0, 1}** — never 2.
- **Idempotent submit:** same `Idempotency-Key` twice → one `messages` row, second response
  `idempotent_replay=true`.
- **Webhook signature:** client-side verifier accepts a genuine signature and rejects a tampered
  body / stale timestamp (>300s).

### 4.4 Load test

- **REST submit load:** `k6` or `vegeta` drives `POST /v1/messages` at target RPS with a mix of
  operators and policies; assert `429` kicks in exactly at `rate_limit_per_sec`, p99 submit
  latency, and that the queue drains.
- **Dispatch throughput:** spin up 20–50 virtual devices (matching a realistic fleet) with
  `happy`/`slow` policies; push a backlog; measure `wsms_queue_depth` drain rate, dispatch
  latency, and that per-SIM `daily_quota` + `pacing` are respected (no SIM exceeds its cap, gaps
  ≥ `min_gap_ms`).
- **Soak:** run 24h at steady state to catch WS leak, goroutine growth, Redis counter drift, and
  the midnight `Asia/Jakarta` `sent_today` reset.
- **CI wiring:** unit + contract tests on every PR; the Compose integration + chaos suite nightly
  (or on release branches); load/soak on demand. Migrations tested against a fresh Postgres each
  run.

---

<a name="5-phased-rollout-roadmap"></a>

## 5. Phased rollout roadmap

Each phase has an explicit **exit criterion** — you do not advance until it's met. Timeline is
indicative for a small team.

```mermaid
gantt
    title WSMS-Gateway rollout
    dateFormat  YYYY-MM-DD
    axisFormat  %b
    section Foundations
    Phase 0 Foundations      :p0, 2026-07-14, 14d
    section MVP
    Phase 1 MVP single-SIM   :p1, after p0, 21d
    section Routing
    Phase 2 On-net routing   :p2, after p1, 21d
    section Reliability
    Phase 3 Reliability+Obs  :p3, after p2, 28d
    section Scale
    Phase 4 Scale & off-ramp :p4, after p3, 30d
```

**Phase 0 — Foundations (≈2 wk).** Repo, Gin+GORM skeleton, Postgres schema + migrations from
contract A, enum types, TLS via Caddy, CI (unit + lint), Docker Compose up. Owner signs the
[§2.7](#26-owner-sign-off) risk acknowledgement.
*Exit:* migrations apply cleanly; `/healthz` green over HTTPS; one enrollment token issuable.

**Phase 1 — MVP, single device / single SIM (≈3 wk).** REST `POST /v1/messages` +
`GET /v1/messages/:id`; API-key auth + scopes; MSISDN normalize + encoding/segments; device
enroll + WSS handshake; `send_command` → `send_ack` → `delivery_report`; `RoutingPolicy=ANY`
(no operator matching yet); command idempotency ledger on device. Validate on **one real phone,
one SIM, to owner's own number** — lowest possible volume.
*Exit:* an OTP submitted via API arrives on a real handset; status reaches `DELIVERED`;
**exactly one** SMS per message proven by the chaos harness.

**Phase 2 — On-net routing + dual-SIM fleet (≈3 wk).** `operator_prefixes` resolution;
`ON_NET_STRICT` / `ON_NET_PREFERRED` (with random fallback) / `PINNED`; dual-SIM enumeration and
`sim_report` reconciliation; multi-device routing pool; per-SIM `daily_quota` + `sent_today` +
pacing; `GET /v1/sims`, `/devices`. Grow to 3–5 phones.
*Exit:* a Telkomsel target routes to a Telkomsel SIM (`on_net=true`); with none online it falls
back to a random SIM (`on_net=false`); quota-capped SIMs are skipped.

**Phase 3 — Reliability + observability (≈4 wk).** Retry/backoff + the reroute safety rule;
reconnect resume + on-device outbox; foreground service + FCM high-priority wake +
battery-optimization prompt; TTL/expiry sweeper; webhooks with HMAC signing + retries; batch
endpoint; full Prometheus metrics + Grafana dashboards + Alertmanager (incl. `LowDeliverySuccess`
/ `SimBanSuspected`); backups + retention purge; audit log.
*Exit:* survives Doze/app-kill (revives via FCM); no-double-send holds under the full chaos
suite; alerts fire in a killed-device drill; a restore drill succeeds.

**Phase 4 — Scale, hardening & A2P off-ramp (≈4–6 wk).** Grow to 6–10 phones; per-client rate
limits + anomaly auto-suspend; optional inbound request signing + IP allowlist; load/soak tests;
security review; admin UI for keys/devices/SIMs/quotas; and — driven by the [§6](#6-cost--deliverability-model)
model and [§2.4](#24-when-to-switch-to-a-licensed-a2p-provider) triggers — evaluate/integrate a
**licensed A2P provider** as an alternate route behind the same submit API.
*Exit:* documented capacity numbers; passing security review; a working switch to move a traffic
slice onto a licensed route without a client-side API change.

---

<a name="6-cost--deliverability-model"></a>

## 6. Cost & deliverability model

A back-of-envelope model to size the fleet and know when consumer SIMs stop making sense.

### 6.1 Capacity (supply)

Two independent ceilings; **effective capacity = min(quota ceiling, pacing ceiling)** per SIM.

```
Quota ceiling  = daily_quota                       segments / SIM / day     (default 200)
Pacing ceiling = 86400 / (min_gap_ms/1000 + jitter/2000)  sends / SIM / day
```

With `min_gap_ms = 4000`, `jitter_ms = 2500` → avg gap ≈ 5.25 s → ~16,450 sends/day *if*
uncapped. So the **daily quota is the binding constraint** — deliberately, for
[§2](#2-legal--compliance-reality) hygiene. Fleet capacity:

```
Fleet segments/day = Σ over SIMs of daily_quota
```

| Fleet | SIMs | Quota/SIM | Nominal capacity | After ~20% ban/cooldown attrition |
|---|---|---|---|---|
| 3 phones × 2 SIM | 6 | 200 | 1,200 seg/day | ~960 seg/day |
| 5 phones × 2 SIM | 10 | 200 | 2,000 seg/day | ~1,600 seg/day |
| 10 phones × 2 SIM | 20 | 200 | 4,000 seg/day | ~3,200 seg/day |

Note the SIM-registration cap (~3 numbers per NIK per operator) is a hard ceiling on how many
SIMs one identity can even hold — scaling the fleet means more registered identities, not just
more handsets.

### 6.2 Deliverability (yield)

```
delivered ≈ submitted × on_net_hit_rate-adjusted × sim_health × (1 − block_rate)
```

- **On-net vs off-net:** on-net (SIM operator == target operator) is cheapest/most reliable and
  is what routing prefers. Off-net fallback still delivers but at tariff cost and slightly lower
  yield. The `on_net` flag on every message/webhook lets you measure the real mix.
- **SIM health / bans:** the dominant variable. A blocked SIM yields **0%** until replaced.
  Watch `wsms:delivery_success_ratio` and `SimBanSuspected` ([§3.6](#36-alerting)); a sudden
  per-SIM `NO_SERVICE`/`GENERIC_FAILURE` spike is the signature of a block.
- Plan for attrition, not perfection: assume a fraction of SIMs enter `COOLDOWN`/ban at any time
  and size the fleet with headroom.

### 6.3 Cost & break-even vs licensed A2P

```
per-message cost (grey route) ≈ segments × (on_net ? ~0 : off_net_tariff)
                              + amortized(SIM replacement + handset + labor when banned)
per-message cost (licensed A2P) ≈ segments × A2P_price_per_segment    (predictable, no ban risk)
```

The consumer-SIM route looks "free" only if you ignore the amortized cost of banned SIMs and the
operational toil of replacing them and re-enrolling handsets. As volume rises, that hidden cost —
plus the deliverability cliff of a ban wave — crosses the fixed per-segment price of a licensed
A2P provider. **Model the crossover explicitly per the owner's real numbers, and treat it as the
Phase 4 trigger to move traffic onto the licensed route.**

**Bottom line for the owner:** this gateway is engineered to be reliable, observable, and safe
*as software*. It cannot make a grey route compliant or ban-proof. Keep volume low, opt-in, and
transactional; watch the deliverability metrics; and migrate to a licensed A2P provider before
the cost and ban risk outrun the savings.

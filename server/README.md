# WSMS-Gateway — Server

Go (Gin + GORM + Postgres) backend: REST API for submitting SMS, a device
WebSocket hub, and the operator-aware dispatcher. Implements the design in
[`../docs`](../docs) with the amendments from [`../docs/08-amendments.md`](../docs/08-amendments.md)
baked in (no-double-send pinning, atomic quota reserve, `SENT_UNCONFIRMED`, short TTL).

## Layout

```
cmd/wsmsd            entrypoint (wiring + graceful shutdown)
internal/
  config             env config
  models             GORM models + canonical enums
  store              DB open, AutoMigrate, seed prefixes, bootstrap creds
  smstext            SMS encoding + segment counting
  router             operator detection + atomic SIM reserve (the routing engine)
  ws                 WebSocket protocol + hub + connection pumps
  fleet              device/SIM upsert (enroll + sim_report)
  dispatch           claim → route → send → lifecycle (the delivery engine)
  api                REST handlers, API-key auth, device enroll + WS
  admin              server-rendered /admin console (html/template + vendored htmx)
  secret             Argon2id hashing, token generation
migrations           hand-authored SQL (AutoMigrate is used in dev)
```

## Run (dev)

```bash
docker compose up -d db                 # Postgres on :5432
cp .env.example .env
go run ./cmd/wsmsd
```

On first start it prints three **bootstrap credentials** (once) to the logs:

- a client API token `wsms_<prefix>.<secret>` — for calling `/v1/messages`
- a device enrollment token — for pairing the first phone
- the `admin` console login (username `admin` + a random password)

## Admin console

Open **`http://localhost:8080/admin`** and log in with the bootstrap credentials.
Server-rendered (Go `html/template` + vendored htmx, no external assets):

- **Overview** — devices/SIMs online, queue depth, delivery success %, on-net vs
  fallback, per-operator volume + capacity, segments today
- **Messages** — searchable log (MSISDN masked by default), detail drawer with the
  full lifecycle timeline and the routing decision (on-net / fallback); role-gated
  **unmask** writes an audit row
- **Fleet** — per-device presence + per-SIM operator, status, quota bar, health
- **Clients & keys**, **Enrollment** (issue pairing tokens), **API Docs** (rendered
  reference + downloadable `openapi.json`)

Roles (RBAC): `owner` / `operator` / `support` / `readonly`.

## API quickstart

```bash
TOKEN='wsms_....'                       # from the bootstrap log line

# Submit an SMS (server detects operator + segments, queues it)
curl -sX POST localhost:8080/v1/messages \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"to":"0812-3456-7890","message":"Kode OTP: 123456","ttl_seconds":300,"dedup_key":"otp-abc"}'

# Check status (+ lifecycle events)
curl -s "localhost:8080/v1/messages/<id>?include=events" -H "Authorization: Bearer $TOKEN"

# Fleet
curl -s "localhost:8080/v1/sims?on_net_ready=true" -H "Authorization: Bearer $TOKEN"
curl -s localhost:8080/v1/devices -H "Authorization: Bearer $TOKEN"

# Health
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
```

## Device pairing (what the Flutter app does)

```
POST /v1/device/enroll   {token, name, os, model, sims:[{subscription_id,slot,carrier_name}]}
  -> {device_id, device_secret}     # secret shown once

GET  /v1/device/ws
  Authorization: Bearer dev_<device_id>.<device_secret>
  # then: send sim_report + heartbeat, receive send_command, reply send_ack + delivery_report
```

## Tests

```bash
go test ./...     # routing detection/normalization + SMS segmentation
```

## Notes

- `readyz` is coarse (amendment F14): green does not guarantee capacity for a
  specific operator — `ON_NET_STRICT` clients should check `/v1/sims?on_net_ready=true`.
- Consumer-SIM A2P is a carrier grey route — read [`../docs/06-security-legal-ops.md` §2](../docs/06-security-legal-ops.md).

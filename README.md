# WSMS-Gateway

Self-hosted SMS gateway. A **Go** (Gin + GORM + Postgres) backend drives a fleet of
**owned dual-SIM Android phones** running a **Flutter** sender app, connected over a
persistent **WebSocket** (with FCM high-priority push as a wake fallback).

Its defining feature is **operator-aware SIM routing**: detect the target number's
Indonesian operator by prefix and send from a SIM on the **same operator** (on-net =
cheaper), falling back to a random available SIM when no matching SIM is online.

It is a production-grade rework of the idea behind
[`nizwar/nsms_gateway`](https://github.com/nizwar/nsms_gateway) — fixing that project's
broadcast-to-all duplicate sends, lack of routing, fire-and-forget delivery, and missing
auth.

> ⚠️ **Read [`docs/06-security-legal-ops.md` §2](docs/06-security-legal-ops.md) first.**
> Sending bulk/A2P SMS from ordinary consumer SIMs is a grey route that violates carrier
> Terms of Service; carriers detect bulk patterns and **ban SIMs**. This project documents
> that risk honestly and is intended for low-volume, self-owned, opt-in use.

## Documentation

Start with **[`docs/00-overview.md`](docs/00-overview.md)**.

| Doc | Purpose |
|-----|---------|
| [00-overview](docs/00-overview.md) | Vision, architecture, roadmap, risk register — read first |
| [01-architecture-transport](docs/01-architecture-transport.md) | End-to-end architecture + WebSocket/FCM realtime layer |
| [02-contract-protocol-schema](docs/02-contract-protocol-schema.md) | **Canonical SSoT**: DB schema, enums, REST API, WS protocol, state machine |
| [03-routing-engine](docs/03-routing-engine.md) | Operator-aware SIM selection + random fallback |
| [04-go-server](docs/04-go-server.md) | Go backend design (packages, WS hub, dispatch worker) |
| [05-flutter-android-app](docs/05-flutter-android-app.md) | Android sender app (dual-SIM, foreground service, delivery reports) |
| [06-security-legal-ops](docs/06-security-legal-ops.md) | Security, legal reality, deployment, observability, testing, rollout |
| [07-admin-management](docs/07-admin-management.md) | Admin dashboard + integrated API docs portal |
| [08-amendments](docs/08-amendments.md) | **Normative** fixes for the audit findings — supersedes referenced sections |
| [09-audit-findings](docs/09-audit-findings.md) | Adversarial audit record (traceability → 08) |

## Status

Planning / design phase. No implementation code yet — these docs are the blueprint.

## Stack

- **Backend:** Go, Gin, GORM, Postgres, WebSocket, FCM
- **Sender app:** Flutter (Android only — iOS cannot send SMS programmatically), Kotlin platform channel to `SmsManager` / `SubscriptionManager`
- **Admin:** integrated web dashboard + self-hosted OpenAPI docs

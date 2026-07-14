# 09 — Adversarial Audit Findings

> Raw output of the adversarial review pass over docs 00–06. **Resolutions are specified normatively in [08-amendments.md](08-amendments.md)** — each finding `Fn` below maps to a fix there. This doc is the traceability record; 08 is the authority.

## Verdict

The plan is unusually thorough, internally cross-referenced, and honest about the legal/ban reality — the contract-as-SSoT discipline, the state machine, and the three-guard idempotency framing are genuinely good. But it is NOT production-ready as written, and its single most important claim — "exactly one SMS per message, never two" — is broken by a concrete double-send hole in the actual dispatch code (doc 04 §10.3 combined with the device send ordering in doc 05 §8.1). Separately, the anti-ban quota/pacing controls — which the plan itself ranks as the #1 project risk — are not reliably enforced in the implemented flow (the reserve-before-dispatch step from doc 03 §7 is absent from doc 04's worker loop, and the counter semantics contradict across docs), so the one mechanism protecting the SIMs is unsound under concurrency. There are also two design-level impossibilities (inbound HMAC signing against an Argon2id-hashed secret; FCM "reviving" a force-stopped app) and a terminal-state modeling error (SENT→EXPIRED) that will misreport the common Indonesian no-delivery-report case as failure. The legal section is strong and does not understate the ban risk. Fix the double-send path, wire in real quota reservation, and correct the SENT-unconfirmed terminal before any real traffic; the rest are medium/low hardening items.

## Findings

| ID | Sev | Area | Problem (short) | Resolved in |
|----|-----|------|-----------------|-------------|
| F1 | 🔴 blocker | doc 04 §10.3 dispatcher ack-timeout requeue × doc 05 §8.1 send ordering (idempotency) | Double-send hole that defeats the system's core guarantee | §F1 |
| F2 | 🟠 high | doc 03 §7 vs doc 04 §9.2/§10.3/§6.3 (quota + pacing enforcement, anti-ban) | The reserve-before-dispatch step (doc 03 §7.1: per-SIM Redis lock, token bucket, min_gap gate, optimistic sent_today/sent_window increment)  | §F2 |
| F3 | 🟠 high | doc 06 §1.4 (optional inbound HMAC request signing) | Cryptographically impossible as specified | §F3 |
| F4 | 🟡 medium | doc 02 §D state machine (SENT → EXPIRED terminal) | Modeling error that will misreport the COMMON case | §F4 |
| F5 | 🟡 medium | doc 05 §8.1 (device ledger marks ACCEPTED before radio handoff) | Lost-send-without-fallback hole | §F5 |
| F6 | 🟡 medium | doc 01 §7.2 / doc 05 §4 (FCM wake of a killed process) | Overstated reliability claim | §F6 |
| F7 | 🟡 medium | doc 02 §A.6 default TTL / doc 05 OTP use case | Default expires_at = created_at + 6h is dangerous for the stated primary use case (OTPs, 'berlaku 5 menit') | §F7 |
| F8 | 🟡 medium | doc 05 §3.1 / doc 01 §7.1 (foreground-service survival on modern Android) | The claim 'foreground services are not subject to normal background execution limits' is no longer true | §F8 |
| F9 | 🟡 medium | doc 02 §B.6 / §C.5 (cancel race) | POST /messages/:id/cancel returns 200 {status:CANCELLED} best-effort while racing the device, but the device may have already handed the bod | §F9 |
| F10 | 🟡 medium | doc 02 §A.1/§A.2 + doc 04 §5.1 (native enum migrations) | The plan says 'adding an enum value = ALTER TYPE .. | §F10 |
| F11 | ⚪ low | doc 02 §0.6 and §A.5 (broken cross-references) | Cross-doc references that don't match: §0.6 says cost is computed in 'Doc 05-* (billing)', but doc 05 is the Flutter Android app and the cos | §F11 |
| F12 | ⚪ low | doc 02 §B.1 / doc 04 §7.3 (idempotency-key reuse semantics) | Re-submitting the same Idempotency-Key with a DIFFERENT body silently returns the original message (idempotent_replay:true) and drops the ne | §F12 |
| F13 | ⚪ low | doc 02 §B.3 (batch) × §B.1 rate limiting | Batch accepts up to 500 messages per request, but the per-client rate limit is defined as messages/sec (token bucket) without specifying whe | §F13 |
| F14 | ⚪ low | doc 02 §B.8 readyz / ON_NET_STRICT clients | readyz returns 200 if ≥1 device ONLINE and ≥1 SIM READY across ANY operator, but a client doing ON_NET_STRICT (or targeting an operator with | §F14 |
| F15 | ⚪ low | doc 03 §7 / doc 06 §2 (jitter framing vs 'not evasion') | The plan repeatedly and emphatically frames human-like timing JITTER, SIM rotation, and keeping volume under detection thresholds as 'ToS-co | §F15 |

## Detail

### F1 — 🔴 BLOCKER · doc 04 §10.3 dispatcher ack-timeout requeue × doc 05 §8.1 send ordering (idempotency)

**Problem.** Double-send hole that defeats the system's core guarantee. In doc 05 §8.1 the device inserts its ledger row and calls SmsManager (SMS leaves the radio) BEFORE it transmits send_ack. In doc 04 §10.3, if the send_ack is lost and the device then goes offline, the redelivery attempt fails with ErrDeviceOffline and the worker calls requeue() → status back to QUEUED. The message was never in DISPATCHED, so the contract's ambiguity/no-reroute rule (§D/§E) never engages; it is freely re-routed. The device idempotency ledger is PER-DEVICE (on-device SQLite), so re-routing to a different device/SIM — the normal case in a multi-SIM fleet — hits a device with no ledger entry and sends the SMS a SECOND time. The code comment itself rationalizes this incorrectly ('safe to requeue ONLY because no send_ack means the device never confirmed acceptance') — but 'no send_ack' is NOT 'not sent'; the SMS may already be gone. This is exactly flaw F1/F5 the whole design claims to eliminate.

**Prescribed fix.** Treat 'send_command delivered but no send_ack' as AMBIGUOUS, identical to the contract's DISPATCHED-ambiguity case: never re-route to a different SIM. Transition to DISPATCHED (or a new AWAITING_ACK state) as soon as the command is written to the socket, pin the message to that message_id+SIM, and let only the device ledger (on reconnect) or TTL resolve it. Alternatively make the device send send_ack BEFORE handing to SmsManager AND make the server's cross-device protection a server-side sent-ledger keyed on message_id (not just the per-device ledger). Add a chaos test that asserts count(SmsManager.send) ∈ {0,1} specifically for the 'ack lost then device offline then requeue' sequence.

### F2 — 🟠 HIGH · doc 03 §7 vs doc 04 §9.2/§10.3/§6.3 (quota + pacing enforcement, anti-ban)

**Problem.** The reserve-before-dispatch step (doc 03 §7.1: per-SIM Redis lock, token bucket, min_gap gate, optimistic sent_today/sent_window increment) is NOT present in doc 04's implemented flow — Route() (§9.2) just picks and returns, handle() (§10.3) assigns and sends, and sent_today/sent_window are only bumped later by IncSent on the SENT delivery_report (§6.3, §11). With N concurrent workers claiming different messages via SKIP LOCKED, nothing stops several messages being routed to the SAME SIM before any SENT report moves its counter, so daily_quota and min_gap_ms can both be blown past. Since pick() ranks by sent_window (which stays 0 until delayed reports arrive), a burst makes every SIM look 'least loaded' and piles onto one SIM — the precise bulk/fast/even pattern that gets SIMs banned. This is the plan's own #1 risk with its primary control effectively disabled under load. Compounding it, the counter is double-incremented and unit-inconsistent: doc 03 §7.1 does sent_today += segments AND sent_window += 1 at reserve, while doc 04 §6.3 IncSent does sent_today += segments AND sent_window += segments on SENT — so quota advances 2× (or the Redis reserve and Postgres IncSent are separate stores that never reconcile) and sent_window mixes 'count of sends' with 'count of segments'.

**Prescribed fix.** Actually wire the atomic reserve into the dispatch path: increment sent_today (and take the token/min_gap) transactionally at assignment time (single UPDATE ... WHERE sent_today+seg<=daily_quota RETURNING, or the documented Redis per-SIM lock), and roll it back on send_ack:rejected/not-sent. Pick ONE store as authoritative for the counters and reconcile reserve vs SENT so a message is counted exactly once. Define sent_window in a single unit (segments) consistently across docs 02/03/04.

### F3 — 🟠 HIGH · doc 06 §1.4 (optional inbound HMAC request signing)

**Problem.** Cryptographically impossible as specified. §1.4 says the server 'recomputes with the same api_secret used for bearer auth' — but §1.3/§1.8 store ONLY the Argon2id hash of the API secret (one-way). The server therefore cannot compute HMAC_SHA256(api_secret, body) and cannot verify the signature. This directly contradicts the (correct) design choice to store api secrets as hashes, and differs from the webhook secret which is stored reversibly-encrypted precisely so it can be HMAC'd.

**Prescribed fix.** Either drop the feature, or give signing-enabled keys a SEPARATE signing secret stored encrypted-at-rest (like webhook_secret) rather than trying to reuse the bearer secret; make clear the bearer secret hash cannot serve double duty. Update §1.8 secrets table accordingly.

### F4 — 🟡 MEDIUM · doc 02 §D state machine (SENT → EXPIRED terminal)

**Problem.** Modeling error that will misreport the COMMON case. Indonesian carriers frequently never return an SMS-STATUS-REPORT, so 'SENT with no DELIVERED before delivery_wait TTL' is the normal outcome, not an edge case — yet the machine sends it to the terminal EXPIRED state and fires a message.expired webhook. A client that treats expired as failure will resend the OTP over another channel, so the recipient gets the SMS twice — re-introducing duplicates at the application layer, which is the whole thing the system is meant to prevent.

**Prescribed fix.** Add a distinct terminal state (e.g. SENT_UNCONFIRMED / DELIVERY_UNKNOWN) and a message.sent_unconfirmed webhook that explicitly means 'left the phone, no delivery confirmation' — NOT expired/failed. Document that absence of a delivery report is expected on many IDN routes and clients MUST NOT treat it as non-delivery.

### F5 — 🟡 MEDIUM · doc 05 §8.1 (device ledger marks ACCEPTED before radio handoff)

**Problem.** Lost-send-without-fallback hole. insertIfAbsent writes the outbox row (phase ACCEPTED) BEFORE telephony.sendSms actually hands the message to SmsManager. If the app is killed between the ledger insert and the radio handoff, the redelivered send_command sees the ledger row → replies send_ack:duplicate and re-emits lastReport (null) → the server treats it as accepted and waits for a SENT that never comes → the message EXPIREs never sent, and because 'duplicate' counts as accepted the reroute guard prevents any fallback to another SIM. A message the operator believes was accepted is silently dropped.

**Prescribed fix.** Only mark the ledger as committed/'sent' AFTER SmsManager accepts the message (pre-flight OK). Keep a distinct 'pending' phase that, on redelivery, permits a real re-send (still guarded so at most one SmsManager.send actually occurs). If the device can prove it never reached the radio, it must reply rejected (allowing reroute), not duplicate.

### F6 — 🟡 MEDIUM · doc 01 §7.2 / doc 05 §4 (FCM wake of a killed process)

**Problem.** Overstated reliability claim. FCM high-priority data messages canNOT be delivered to an app in the 'stopped state' — i.e. after a user force-stop (Settings → Force stop) or on OEMs that put swipe-away/killed apps into that state — and BOOT_COMPLETED also does not fire for a force-stopped app until it is manually launched once. The survival matrix lists 'process killed / swiped away → wake → startForegroundService' as if FCM always recovers it, conflating OS/memory kills (recoverable) with force-stop (not recoverable). Such a phone becomes a silent dead sender with no automatic recovery; the router will keep trying to wake it and messages sit until TTL.

**Prescribed fix.** Document the force-stop / stopped-state limitation explicitly; detect chronically-unwakeable devices (no reconnect within N wake attempts) and surface an operator alert (‘phone needs manual relaunch’) rather than silently retrying. Do not claim FCM recovers all kills.

### F7 — 🟡 MEDIUM · doc 02 §A.6 default TTL / doc 05 OTP use case

**Problem.** Default expires_at = created_at + 6h is dangerous for the stated primary use case (OTPs, 'berlaku 5 menit'). If the fleet is offline for even 20 minutes and then reconnects, the gateway will happily deliver a long-stale OTP well after it is useless (and after the recipient likely requested a new one), causing confusion and support load. The example request sets ttl_seconds=900, but the DEFAULT is the trap.

**Prescribed fix.** Lower the default TTL substantially (e.g. 5–15 min) or make ttl_seconds required for messages tagged as OTP/transactional; drop messages whose remaining TTL is below a floor at dispatch time instead of sending them.

### F8 — 🟡 MEDIUM · doc 05 §3.1 / doc 01 §7.1 (foreground-service survival on modern Android)

**Problem.** The claim 'foreground services are not subject to normal background execution limits' is no longer true. On Android 15 (API 35) a dataSync FGS is capped at ~6h per 24h then force-stopped via onTimeout; the plan's answer is to declare specialUse, but specialUse is heavily scrutinized and, more importantly, none of the FGS types prevent the aggressive OEM killers (MIUI/ColorOS/FuntouchOS/EMUI) or low-memory kills from tearing down a persistent socket. The reliability of the whole transport rests on a foreground service that modern Android and OEMs actively fight.

**Prescribed fix.** Soften the blanket claim; document the Android 15 dataSync cap and the specialUse gamble honestly. Treat FGS survival as best-effort and lean harder on fast reconnect + wake + presence-driven requeue. Add a per-device 'time since last seen' health metric/alert so operators notice phones that the OS is killing repeatedly.

### F9 — 🟡 MEDIUM · doc 02 §B.6 / §C.5 (cancel race)

**Problem.** POST /messages/:id/cancel returns 200 {status:CANCELLED} best-effort while racing the device, but the device may have already handed the body to SmsManager (SMS already sent). The result is a message reported CANCELLED to the client even though the recipient received it — a status that actively lies about what happened, and could cause the client to resend.

**Prescribed fix.** Do not report CANCELLED optimistically for DISPATCHED messages. Return 202/'cancel_requested' and only move to CANCELLED after the device's cancel ack confirms it was dropped before SmsManager; if the device reports it already sent, keep SENT and surface cancel_failed.

### F10 — 🟡 MEDIUM · doc 02 §A.1/§A.2 + doc 04 §5.1 (native enum migrations)

**Problem.** The plan says 'adding an enum value = ALTER TYPE ... ADD VALUE in a migration' and uses native Postgres ENUMs, but ALTER TYPE ... ADD VALUE cannot be used within the same transaction that adds it, and golang-migrate wraps each migration step in a transaction by default. This will bite the first time an operator, failure_reason, or event_type value is added, and enum values also cannot be removed/reordered — making the failure_reason_t/event_type_t sets (which are large and likely to churn) painful to evolve.

**Prescribed fix.** Either mark the enum-adding migrations as no-transaction (golang-migrate '-- +migrate NoTransaction' equivalent) and split add-value from use-value across two migrations, or model the churny vocabularies (failure_reason_t, event_type_t) as text + CHECK/lookup table instead of native enums. Document the constraint in doc 04 §5.1.

### F11 — ⚪ LOW · doc 02 §0.6 and §A.5 (broken cross-references)

**Problem.** Cross-doc references that don't match: §0.6 says cost is computed in 'Doc 05-* (billing)', but doc 05 is the Flutter Android app and the cost model actually lives in doc 06 §6. §A.5 references 'doc 08 legal/deliverability', but there is no doc 08 — the legal doc is 06. These are exactly the kind of contract↔doc drift that misleads implementers.

**Prescribed fix.** Point cost references to doc 06 §6 and the legal/deliverability references to doc 06 §2; sweep all docs for stale 'doc 05 (billing)'/'doc 08' pointers.

### F12 — ⚪ LOW · doc 02 §B.1 / doc 04 §7.3 (idempotency-key reuse semantics)

**Problem.** Re-submitting the same Idempotency-Key with a DIFFERENT body silently returns the original message (idempotent_replay:true) and drops the new content with no error. A client that reused a key by mistake believes its new message was sent when it was not.

**Prescribed fix.** Store a hash of the request payload alongside dedup_key; if the same key arrives with a different payload, return 409 CONFLICT rather than silently replaying the old message.

### F13 — ⚪ LOW · doc 02 §B.3 (batch) × §B.1 rate limiting

**Problem.** Batch accepts up to 500 messages per request, but the per-client rate limit is defined as messages/sec (token bucket) without specifying whether a 500-item batch consumes 500 tokens or 1. As written a client can push 500 sends in a single request that costs one token, bypassing the ingress backpressure (BP1) that is supposed to protect the fleet.

**Prescribed fix.** Charge the token bucket per contained message (or per segment) for batch submits, and reject/deny-partial when the bucket can't cover the batch; document the accounting explicitly.

### F14 — ⚪ LOW · doc 02 §B.8 readyz / ON_NET_STRICT clients

**Problem.** readyz returns 200 if ≥1 device ONLINE and ≥1 SIM READY across ANY operator, but a client doing ON_NET_STRICT (or targeting an operator with no online SIM) will still fail while readyz is green. readyz is not a usable 'can I send to operator X' signal.

**Prescribed fix.** Keep readyz coarse but document that per-operator capacity must be checked via GET /v1/sims on_net_ready; optionally add per-operator readiness to /v1/stats or a query param on readyz.

### F15 — ⚪ LOW · doc 03 §7 / doc 06 §2 (jitter framing vs 'not evasion')

**Problem.** The plan repeatedly and emphatically frames human-like timing JITTER, SIM rotation, and keeping volume under detection thresholds as 'ToS-compliance/deliverability hygiene, explicitly NOT detection-evasion.' Randomizing inter-send timing specifically to avoid looking like a metronome, and rotating SIMs to spread a bulk pattern, are functionally aimed at not being detected as bulk — the categorical 'not evasion' framing is a bit of a fig leaf. It follows the owner's stated constraint, but an honest audit should note the line is blurrier than the docs assert.

**Prescribed fix.** Keep the controls, but state plainly that pacing/jitter/rotation reduce the CHANCE of triggering bulk-detection (they do not make the route compliant), rather than asserting a clean 'hygiene not evasion' dichotomy; this is more honest to the owner and consistent with §2's otherwise-excellent disclosure.

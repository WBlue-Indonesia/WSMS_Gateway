# WSMS-Gateway — Android Sender App

Flutter (Android-only) app that runs on each owned phone. It holds a persistent
WebSocket to the server, reports its dual-SIM subscriptions, and turns
`send_command` frames into real SMS via `SmsManager` — reporting `send_ack` and
delivery status back. Implements [`../docs/05-flutter-android-app.md`](../docs/05-flutter-android-app.md).

> **iOS is not supported and never will be** — iOS has no API to send SMS
> programmatically. The fleet is Android only.

## Layout

```
lib/
  main.dart              enroll-vs-home routing
  src/
    config.dart          channel names + frame/enum constants (mirror the server)
    storage.dart         secure creds + on-device send ledger (dedup)
    telephony.dart       Dart side of the platform channel (SimInfo, send, events)
    gateway.dart         WebSocket client + send lifecycle + reconnect/heartbeat
    permissions.dart     runtime permission requests
    enroll_screen.dart   pairing UI (server URL + enrollment token)
    home_screen.dart     status, SIMs, live log
android/app/src/main/kotlin/id/wblue/wsms_sender/
    MainActivity.kt      registers the method + event channels
    TelephonyHandler.kt  SubscriptionManager enumeration + dual-SIM SmsManager send
                         + SENT/DELIVERED PendingIntent receivers
```

## The native bridge (the important part)

Channel `wsms/telephony`:

- `listSims()` → `[{subscription_id, slot, carrier_name, mcc, mnc, number}]`
  via `SubscriptionManager.getActiveSubscriptionInfoList()`.
- `sendSms({message_id, subscription_id, target, body})` → binds
  `SmsManager.createForSubscriptionId(subId)` (API 31+, else the deprecated
  `getSmsManagerForSubscriptionId`), `divideMessage` + `sendMultipartTextMessage`.
  Returns `{accepted:true}` once the radio takes it (amendment F5). SENT/DELIVERED
  results arrive via one-shot `PendingIntent` broadcast receivers and are streamed
  on the `wsms/telephony_events` `EventChannel`.

## Send lifecycle (matches the server contract + amendments)

```
send_command → ledger check (dup?) → expiry check (F7) → SmsManager.sendSms
  accepted  → ledger='sent' (AFTER acceptance, F5) → send_ack: accepted
  not taken → send_ack: rejected            (radio never took it; server may reroute)
SENT      receiver → delivery_report: sent
DELIVERED receiver → delivery_report: delivered
failure   receiver → delivery_report: failed
```

## Run

```bash
flutter pub get
flutter run          # on a real dual-SIM Android phone (emulators can't send SMS)
```

Pair: enter the server URL (use `http://10.0.2.2:8080` from an emulator) and a
device enrollment token from the admin, grant SMS + phone permissions.

## Permissions

`SEND_SMS`, `READ_PHONE_STATE`, `READ_PHONE_NUMBERS` (send + SIM enumeration),
`POST_NOTIFICATIONS` + `FOREGROUND_SERVICE(_DATA_SYNC)` (keep-alive),
`RECEIVE_BOOT_COMPLETED` + battery-optimization exemption (survival, best-effort).

## Known next steps

- Move the `Gateway` into a `flutter_foreground_task` isolate so the socket
  survives the app being backgrounded (dependency + manifest already wired).
  Note the Android 15 foreground-service time cap and OEM-killer reality
  (amendment F8) — survival is best-effort; the server's fast-reconnect + wake +
  requeue path is the real guarantee.
- FCM high-priority wake for force-stopped processes has hard limits (F6).

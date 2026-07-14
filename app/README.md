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

## Release build (signed APK)

Release signing reads `android/key.properties` (gitignored). To sign your own
fleet APK, create a keystore + that file (a `key.properties.example` is provided):

```bash
keytool -genkeypair -keystore android/keystore/release.jks -alias wsms \
  -keyalg RSA -keysize 2048 -validity 10000
cp android/key.properties.example android/key.properties   # fill in the passwords
flutter build apk --release
```

If `key.properties` is absent the build falls back to debug signing so `flutter
run` still works. Keep the keystore + `key.properties` secret and back them up —
you need the same key to ship app updates.

Pair: enter the server URL (use `http://10.0.2.2:8080` from an emulator) and a
device enrollment token from the admin, grant SMS + phone permissions.

## Permissions

`SEND_SMS`, `READ_PHONE_STATE`, `READ_PHONE_NUMBERS` (send + SIM enumeration),
`POST_NOTIFICATIONS` + `FOREGROUND_SERVICE(_DATA_SYNC)` (keep-alive),
`RECEIVE_BOOT_COMPLETED` + battery-optimization exemption (survival, best-effort).

## Survival & wake

- **Foreground service** (`foreground.dart`, flutter_foreground_task): started on
  the home screen, holds an ongoing notification + wakelock to keep the process (and
  the main-isolate `Gateway`) alive in the background. Survival is **best-effort**
  (amendment F8): Android 15 caps a dataSync FGS at ~6h/24h and OEM killers can still
  win — the server's fast reconnect + FCM wake + presence-driven requeue is the real
  guarantee.
- **FCM wake** (`push.dart`): on launch the app fetches its FCM token and reports it
  in the `hello` frame; the server sends a high-priority data message to wake this
  device when work is queued for it while it is offline. Force-stopped processes
  cannot be woken (F6) — the server tracks `WakeMisses` and the admin flags such phones.

### Firebase setup (required for FCM wake only)

The repo ships a **placeholder** `android/app/google-services.json` so the project
builds. FCM does nothing until you replace it:

1. Create a Firebase project, add an Android app with package `id.wblue.wsms_sender`.
2. Download the real `google-services.json` into `android/app/`.
3. On the server, set `WSMS_FCM_CREDENTIALS` (service-account JSON) + `WSMS_FCM_PROJECT_ID`.

Without this the app runs fine; the server simply cannot wake an offline device.

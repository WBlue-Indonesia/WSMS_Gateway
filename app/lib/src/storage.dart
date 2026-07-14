import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Persists enrollment credentials and the on-device send ledger (dedup guard).
class Storage {
  static const _storage = FlutterSecureStorage();
  static const _kServerUrl = 'server_url';
  static const _kDeviceId = 'device_id';
  static const _kDeviceSecret = 'device_secret';
  static const _kLedger = 'send_ledger';
  static const _kThemeMode = 'theme_mode';

  // ---- Theme preference: 'system' | 'light' | 'dark' ----
  Future<String?> get themeMode => _storage.read(key: _kThemeMode);
  Future<void> setThemeMode(String mode) => _storage.write(key: _kThemeMode, value: mode);

  Future<bool> get isEnrolled async =>
      (await deviceId) != null && (await deviceSecret) != null;

  Future<String?> get serverUrl => _storage.read(key: _kServerUrl);
  Future<String?> get deviceId => _storage.read(key: _kDeviceId);
  Future<String?> get deviceSecret => _storage.read(key: _kDeviceSecret);

  Future<void> saveEnrollment({
    required String serverUrl,
    required String deviceId,
    required String deviceSecret,
  }) async {
    await _storage.write(key: _kServerUrl, value: serverUrl);
    await _storage.write(key: _kDeviceId, value: deviceId);
    await _storage.write(key: _kDeviceSecret, value: deviceSecret);
  }

  Future<void> clear() async => _storage.deleteAll();

  // ---- Send ledger: message_id -> last known phase (amendment F1/F5 dedup) ----

  Future<Map<String, String>> _readLedger() async {
    final raw = await _storage.read(key: _kLedger);
    if (raw == null || raw.isEmpty) return {};
    final map = jsonDecode(raw) as Map<String, dynamic>;
    return map.map((k, v) => MapEntry(k, v.toString()));
  }

  Future<String?> ledgerPhase(String messageId) async =>
      (await _readLedger())[messageId];

  /// Records a phase only after SmsManager has ACCEPTED the message (F5), so a crash
  /// before the radio handoff does not falsely mark a message as sent.
  Future<void> setLedgerPhase(String messageId, String phase) async {
    final map = await _readLedger();
    map[messageId] = phase;
    await _storage.write(key: _kLedger, value: jsonEncode(map));
  }
}

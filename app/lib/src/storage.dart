import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// UI-side preferences only. Enrollment credentials + the send ledger now live in the
/// native SenderService (SharedPreferences), so they survive with the service.
class Storage {
  static const _storage = FlutterSecureStorage();
  static const _kThemeMode = 'theme_mode';

  Future<String?> get themeMode => _storage.read(key: _kThemeMode);
  Future<void> setThemeMode(String mode) => _storage.write(key: _kThemeMode, value: mode);
}

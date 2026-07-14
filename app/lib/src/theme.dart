import 'package:flutter/material.dart';

/// WBlue brand theme — a single source of truth for light + dark, keyed off the
/// brand blue so the sender app matches the admin console (navy #173483 → cyan).
class AppTheme {
  static const seed = Color(0xFF205BA4);
  static const brandNavy = Color(0xFF173483);
  static const brandCyan = Color(0xFF2A91CA);

  static ThemeData _build(Brightness brightness) {
    final scheme = ColorScheme.fromSeed(seedColor: seed, brightness: brightness);
    final dark = brightness == Brightness.dark;
    final surface = dark ? const Color(0xFF121B30) : Colors.white;
    return ThemeData(
      useMaterial3: true,
      colorScheme: scheme,
      scaffoldBackgroundColor: dark ? const Color(0xFF0A0F1C) : const Color(0xFFEEF2F9),
      appBarTheme: AppBarTheme(
        centerTitle: false,
        scrolledUnderElevation: 0.5,
        backgroundColor: surface,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
      ),
      cardTheme: CardThemeData(
        elevation: 0,
        color: surface,
        clipBehavior: Clip.antiAlias,
        margin: EdgeInsets.zero,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(18),
          side: BorderSide(color: scheme.outlineVariant.withValues(alpha: dark ? 0.7 : 1)),
        ),
      ),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: dark ? const Color(0xFF0E1626) : const Color(0xFFF4F7FC),
        contentPadding: const EdgeInsets.symmetric(horizontal: 14, vertical: 15),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(12),
          borderSide: BorderSide(color: scheme.outlineVariant),
        ),
        enabledBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(12),
          borderSide: BorderSide(color: scheme.outlineVariant),
        ),
      ),
      filledButtonTheme: FilledButtonThemeData(
        style: FilledButton.styleFrom(
          minimumSize: const Size.fromHeight(52),
          textStyle: const TextStyle(fontWeight: FontWeight.w600, fontSize: 15),
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(13)),
        ),
      ),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: OutlinedButton.styleFrom(
          minimumSize: const Size.fromHeight(48),
          shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(13)),
        ),
      ),
      navigationBarTheme: NavigationBarThemeData(
        height: 66,
        backgroundColor: surface,
        elevation: 3,
        labelTextStyle: const WidgetStatePropertyAll(TextStyle(fontSize: 12, fontWeight: FontWeight.w600)),
      ),
      dividerTheme: DividerThemeData(color: scheme.outlineVariant, thickness: 1, space: 1),
      chipTheme: const ChipThemeData(side: BorderSide.none),
    );
  }

  static ThemeData get light => _build(Brightness.light);
  static ThemeData get dark => _build(Brightness.dark);
}

/// Semantic status colors used by SIM/activity chips (theme-aware).
class StatusColors {
  static Color ok(BuildContext c) =>
      Theme.of(c).brightness == Brightness.dark ? const Color(0xFF3FCE7F) : const Color(0xFF12894E);
  static Color bad(BuildContext c) =>
      Theme.of(c).brightness == Brightness.dark ? const Color(0xFFF0715E) : const Color(0xFFD33A2C);
  static Color warn(BuildContext c) =>
      Theme.of(c).brightness == Brightness.dark ? const Color(0xFFE7B352) : const Color(0xFFB9741A);
  static Color muted(BuildContext c) => Theme.of(c).colorScheme.onSurfaceVariant;

  /// Maps a server SIM status (READY/COOLDOWN/...) or connection label to a color.
  static Color forStatus(BuildContext c, String s) {
    switch (s.toUpperCase()) {
      case 'READY':
      case 'ONLINE':
      case 'CONNECTED':
        return ok(c);
      case 'DISABLED':
      case 'ABSENT':
      case 'OFFLINE':
        return bad(c);
      case 'COOLDOWN':
      case 'QUOTA_EXCEEDED':
      case 'CONNECTING':
        return warn(c);
      default:
        return muted(c);
    }
  }
}

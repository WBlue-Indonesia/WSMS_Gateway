import 'package:flutter/material.dart';

import 'storage.dart';

/// App-wide theme mode, persisted across launches. MaterialApp listens to this.
final themeModeNotifier = ValueNotifier<ThemeMode>(ThemeMode.system);

ThemeMode _parse(String? s) => switch (s) {
      'light' => ThemeMode.light,
      'dark' => ThemeMode.dark,
      _ => ThemeMode.system,
    };

Future<void> loadThemeMode() async {
  themeModeNotifier.value = _parse(await Storage().themeMode);
}

/// Flips between light and dark relative to what is currently shown, and persists it.
Future<void> toggleTheme(BuildContext context) async {
  final isDark = Theme.of(context).brightness == Brightness.dark;
  final next = isDark ? ThemeMode.light : ThemeMode.dark;
  themeModeNotifier.value = next;
  await Storage().setThemeMode(next == ThemeMode.dark ? 'dark' : 'light');
}

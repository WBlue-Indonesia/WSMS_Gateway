import 'package:flutter/material.dart';

import 'src/device_bridge.dart';
import 'src/enroll_screen.dart';
import 'src/home_screen.dart';
import 'src/theme.dart';
import 'src/theme_controller.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await loadThemeMode();
  runApp(const WsmsApp());
}

class WsmsApp extends StatelessWidget {
  const WsmsApp({super.key});

  @override
  Widget build(BuildContext context) {
    return ValueListenableBuilder<ThemeMode>(
      valueListenable: themeModeNotifier,
      builder: (_, mode, _) => MaterialApp(
        title: 'WSMS Sender',
        debugShowCheckedModeBanner: false,
        theme: AppTheme.light,
        darkTheme: AppTheme.dark,
        themeMode: mode,
        home: const _Root(),
      ),
    );
  }
}

class _Root extends StatefulWidget {
  const _Root();

  @override
  State<_Root> createState() => _RootState();
}

class _RootState extends State<_Root> {
  bool? _enrolled;

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  Future<void> _refresh() async {
    final ok = await DeviceBridge.isEnrolled();
    if (ok) await DeviceBridge.registerFcm(); // refresh the FCM token + report SIMs
    if (mounted) setState(() => _enrolled = ok);
  }

  @override
  Widget build(BuildContext context) {
    if (_enrolled == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    if (_enrolled == false) {
      return EnrollScreen(onEnrolled: _refresh);
    }
    return HomeScreen(onUnenrolled: _refresh);
  }
}

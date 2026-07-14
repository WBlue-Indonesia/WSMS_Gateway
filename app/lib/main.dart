import 'package:flutter/material.dart';

import 'src/enroll_screen.dart';
import 'src/home_screen.dart';
import 'src/storage.dart';

void main() {
  runApp(const WsmsApp());
}

class WsmsApp extends StatelessWidget {
  const WsmsApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'WSMS Sender',
      theme: ThemeData(colorSchemeSeed: Colors.indigo, useMaterial3: true),
      home: const _Root(),
    );
  }
}

class _Root extends StatefulWidget {
  const _Root();

  @override
  State<_Root> createState() => _RootState();
}

class _RootState extends State<_Root> {
  final _storage = Storage();
  bool? _enrolled;

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  Future<void> _refresh() async {
    final ok = await _storage.isEnrolled;
    if (mounted) setState(() => _enrolled = ok);
  }

  @override
  Widget build(BuildContext context) {
    if (_enrolled == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    if (_enrolled == false) {
      return EnrollScreen(storage: _storage, onEnrolled: _refresh);
    }
    return HomeScreen(storage: _storage, onUnenrolled: _refresh);
  }
}

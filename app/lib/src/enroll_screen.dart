import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;

import 'permissions.dart';
import 'scan_screen.dart';
import 'storage.dart';
import 'telephony.dart';

class EnrollScreen extends StatefulWidget {
  const EnrollScreen({super.key, required this.storage, required this.onEnrolled});

  final Storage storage;
  final VoidCallback onEnrolled;

  @override
  State<EnrollScreen> createState() => _EnrollScreenState();
}

class _EnrollScreenState extends State<EnrollScreen> {
  final _url = TextEditingController(text: 'http://10.0.2.2:8080');
  final _token = TextEditingController();
  final _name = TextEditingController(text: 'HP-A');
  final _telephony = Telephony();
  bool _busy = false;
  String? _error;

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Pair device')),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: ListView(
          children: [
            const Text('Scan the pairing QR on the admin Enrollment page — or enter the details manually.'),
            const SizedBox(height: 16),
            FilledButton.icon(
              onPressed: _busy ? null : _scanAndPair,
              icon: const Icon(Icons.qr_code_scanner),
              label: const Text('Scan QR to pair'),
            ),
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 16),
              child: Row(children: [
                Expanded(child: Divider()),
                Padding(padding: EdgeInsets.symmetric(horizontal: 8), child: Text('or manually')),
                Expanded(child: Divider()),
              ]),
            ),
            TextField(controller: _url, decoration: const InputDecoration(labelText: 'Server URL')),
            TextField(controller: _name, decoration: const InputDecoration(labelText: 'Device name')),
            TextField(controller: _token, decoration: const InputDecoration(labelText: 'Enrollment token')),
            const SizedBox(height: 16),
            if (_error != null) Text(_error!, style: const TextStyle(color: Colors.red)),
            FilledButton(
              onPressed: _busy ? null : _enroll,
              child: _busy ? const CircularProgressIndicator() : const Text('Pair'),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _scanAndPair() async {
    final raw = await Navigator.of(context).push<String>(
      MaterialPageRoute(builder: (_) => const ScanScreen()),
    );
    if (raw == null) return;
    try {
      final data = jsonDecode(raw) as Map<String, dynamic>;
      final url = (data['url'] ?? '') as String;
      final token = (data['token'] ?? '') as String;
      if (url.isEmpty || token.isEmpty) throw 'missing url/token';
      _url.text = url;
      _token.text = token;
    } catch (_) {
      setState(() => _error = 'QR not recognized — use a WSMS pairing QR');
      return;
    }
    await _enroll();
  }

  Future<void> _enroll() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final granted = await requestSenderPermissions();
      if (!granted) {
        throw 'SMS + phone permissions are required';
      }
      await requestIgnoreBatteryOptimizations();

      final sims = await _telephony.listSims();
      final url = _url.text.trim();
      final resp = await http.post(
        Uri.parse('$url/v1/device/enroll'),
        headers: {'Content-Type': 'application/json'},
        body: jsonEncode({
          'token': _token.text.trim(),
          'name': _name.text.trim(),
          'os': 'android',
          'model': 'unknown',
          'sims': sims.map((s) => s.toReportJson()).toList(),
        }),
      );
      if (resp.statusCode != 200) {
        throw 'enroll failed (${resp.statusCode}): ${resp.body}';
      }
      final body = jsonDecode(resp.body) as Map<String, dynamic>;
      await widget.storage.saveEnrollment(
        serverUrl: url,
        deviceId: body['device_id'] as String,
        deviceSecret: body['device_secret'] as String,
      );
      widget.onEnrolled();
    } catch (e) {
      setState(() => _error = '$e');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }
}

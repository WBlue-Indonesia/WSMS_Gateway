import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;

import 'permissions.dart';
import 'scan_screen.dart';
import 'storage.dart';
import 'telephony.dart';
import 'theme.dart';

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
    final scheme = Theme.of(context).colorScheme;
    return Scaffold(
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.fromLTRB(20, 32, 20, 24),
          children: [
            // Brand header
            Row(
              children: [
                Container(
                  width: 44, height: 44,
                  decoration: BoxDecoration(color: AppTheme.brandNavy, borderRadius: BorderRadius.circular(12)),
                  alignment: Alignment.center,
                  child: const Text('W', style: TextStyle(color: AppTheme.brandCyan, fontWeight: FontWeight.w900, fontSize: 24, height: 1)),
                ),
                const SizedBox(width: 12),
                Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text('WSMS Sender', style: Theme.of(context).textTheme.titleLarge?.copyWith(fontWeight: FontWeight.w800)),
                    Text('Pair this phone with your gateway',
                        style: Theme.of(context).textTheme.bodySmall?.copyWith(color: scheme.onSurfaceVariant)),
                  ],
                ),
              ],
            ),
            const SizedBox(height: 28),

            FilledButton.icon(
              onPressed: _busy ? null : _scanAndPair,
              icon: const Icon(Icons.qr_code_scanner),
              label: const Text('Scan pairing QR'),
            ),
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 20),
              child: Row(children: [
                Expanded(child: Divider()),
                Padding(padding: EdgeInsets.symmetric(horizontal: 12), child: Text('or enter manually')),
                Expanded(child: Divider()),
              ]),
            ),

            Card(
              child: Padding(
                padding: const EdgeInsets.all(16),
                child: Column(
                  children: [
                    TextField(controller: _url, keyboardType: TextInputType.url,
                        decoration: const InputDecoration(labelText: 'Server URL', prefixIcon: Icon(Icons.dns_outlined))),
                    const SizedBox(height: 12),
                    TextField(controller: _name,
                        decoration: const InputDecoration(labelText: 'Device name', prefixIcon: Icon(Icons.smartphone_outlined))),
                    const SizedBox(height: 12),
                    TextField(controller: _token,
                        decoration: const InputDecoration(labelText: 'Enrollment token', prefixIcon: Icon(Icons.vpn_key_outlined))),
                  ],
                ),
              ),
            ),

            if (_error != null)
              Container(
                margin: const EdgeInsets.only(top: 16),
                padding: const EdgeInsets.all(12),
                decoration: BoxDecoration(
                  color: scheme.errorContainer,
                  borderRadius: BorderRadius.circular(12),
                ),
                child: Row(
                  children: [
                    Icon(Icons.error_outline, color: scheme.onErrorContainer, size: 20),
                    const SizedBox(width: 10),
                    Expanded(child: Text(_error!, style: TextStyle(color: scheme.onErrorContainer))),
                  ],
                ),
              ),

            const SizedBox(height: 20),
            FilledButton.icon(
              onPressed: _busy ? null : _enroll,
              icon: _busy
                  ? const SizedBox(width: 18, height: 18, child: CircularProgressIndicator(strokeWidth: 2))
                  : const Icon(Icons.link),
              label: Text(_busy ? 'Pairing…' : 'Pair device'),
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

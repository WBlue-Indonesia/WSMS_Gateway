import 'package:flutter/material.dart';

import 'foreground.dart';
import 'gateway.dart';
import 'storage.dart';
import 'telephony.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.storage, required this.onUnenrolled});

  final Storage storage;
  final VoidCallback onUnenrolled;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  late final Gateway _gateway;
  List<SimInfo> _sims = [];

  @override
  void initState() {
    super.initState();
    _gateway = Gateway(widget.storage, Telephony());
    _gateway.start();
    ForegroundService.start(); // keep the process alive in the background (best-effort)
    _loadSims();
  }

  @override
  void dispose() {
    _gateway.stop();
    super.dispose();
  }

  Future<void> _loadSims() async {
    final sims = await Telephony().listSims();
    if (mounted) setState(() => _sims = sims);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('WSMS Sender'),
        actions: [
          ValueListenableBuilder<ConnState>(
            valueListenable: _gateway.state,
            builder: (_, s, __) => Padding(
              padding: const EdgeInsets.only(right: 12),
              child: Center(child: _statusChip(s)),
            ),
          ),
        ],
      ),
      body: Column(
        children: [
          _simsCard(),
          const Divider(height: 1),
          Expanded(child: _logView()),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () async {
          await _gateway.reportSims();
          await _loadSims();
        },
        icon: const Icon(Icons.sim_card),
        label: const Text('Re-report SIMs'),
      ),
      bottomNavigationBar: Padding(
        padding: const EdgeInsets.all(8),
        child: OutlinedButton.icon(
          onPressed: () async {
            await _gateway.stop();
            await ForegroundService.stop();
            await widget.storage.clear();
            widget.onUnenrolled();
          },
          icon: const Icon(Icons.link_off),
          label: const Text('Unpair this device'),
        ),
      ),
    );
  }

  Widget _statusChip(ConnState s) {
    final (color, label) = switch (s) {
      ConnState.connected => (Colors.green, 'online'),
      ConnState.connecting => (Colors.orange, 'connecting'),
      ConnState.disconnected => (Colors.red, 'offline'),
    };
    return Chip(
      backgroundColor: color.withValues(alpha: 0.15),
      label: Text(label, style: TextStyle(color: color)),
    );
  }

  Widget _simsCard() {
    return Card(
      margin: const EdgeInsets.all(8),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('SIMs (${_sims.length})', style: Theme.of(context).textTheme.titleMedium),
            const SizedBox(height: 4),
            if (_sims.isEmpty)
              const Text('No SIMs detected (grant phone permission).')
            else
              ..._sims.map((s) => Text('slot ${s.slot} · sub ${s.subscriptionId} · ${s.carrierName}')),
          ],
        ),
      ),
    );
  }

  Widget _logView() {
    return ValueListenableBuilder<List<String>>(
      valueListenable: _gateway.log,
      builder: (_, lines, __) => ListView.builder(
        reverse: true,
        itemCount: lines.length,
        itemBuilder: (_, i) => Padding(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 2),
          child: Text(lines[lines.length - 1 - i], style: const TextStyle(fontFamily: 'monospace', fontSize: 12)),
        ),
      ),
    );
  }
}

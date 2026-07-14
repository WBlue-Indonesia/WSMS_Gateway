import 'dart:async';

import 'package:flutter/material.dart';

import 'activity.dart';
import 'device_bridge.dart';
import 'sim_state.dart';
import 'theme.dart';
import 'theme_controller.dart';

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.onUnenrolled});

  final VoidCallback onUnenrolled;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> with WidgetsBindingObserver {
  final _state = ValueNotifier<DeviceState>(DeviceState.empty());
  int _tab = 0;
  Timer? _poll;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    _refresh();
    _poll = Timer.periodic(const Duration(seconds: 2), (_) => _refresh());
  }

  @override
  void dispose() {
    _poll?.cancel();
    WidgetsBinding.instance.removeObserver(this);
    _state.dispose();
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) _refresh();
  }

  Future<void> _refresh() async {
    try {
      _state.value = await DeviceBridge.getState();
    } catch (_) {}
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        titleSpacing: 16,
        title: Row(
          children: [
            const _BrandMark(),
            const SizedBox(width: 10),
            Text('WSMS', style: Theme.of(context).textTheme.titleLarge?.copyWith(fontWeight: FontWeight.w800)),
            Text(' Sender', style: Theme.of(context).textTheme.titleLarge?.copyWith(color: AppTheme.brandCyan, fontWeight: FontWeight.w700)),
          ],
        ),
        actions: [
          IconButton(
            tooltip: 'Toggle theme',
            onPressed: () => toggleTheme(context),
            icon: Icon(Theme.of(context).brightness == Brightness.dark ? Icons.light_mode_outlined : Icons.dark_mode_outlined),
          ),
          PopupMenuButton<String>(
            onSelected: (v) async {
              if (v == 'rescan') {
                await DeviceBridge.reportSims();
                await Future.delayed(const Duration(seconds: 1));
                _refresh();
              } else if (v == 'unpair') {
                _confirmUnpair();
              }
            },
            itemBuilder: (_) => const [
              PopupMenuItem(value: 'rescan', child: ListTile(leading: Icon(Icons.sim_card_outlined), title: Text('Re-scan SIMs'), contentPadding: EdgeInsets.zero)),
              PopupMenuItem(value: 'unpair', child: ListTile(leading: Icon(Icons.link_off), title: Text('Unpair device'), contentPadding: EdgeInsets.zero)),
            ],
          ),
        ],
      ),
      body: SafeArea(
        child: ValueListenableBuilder<DeviceState>(
          valueListenable: _state,
          builder: (_, st, _) => IndexedStack(
            index: _tab,
            children: [_statusTab(st), _activityTab(st)],
          ),
        ),
      ),
      bottomNavigationBar: NavigationBar(
        selectedIndex: _tab,
        onDestinationSelected: (i) => setState(() => _tab = i),
        destinations: const [
          NavigationDestination(icon: Icon(Icons.dashboard_outlined), selectedIcon: Icon(Icons.dashboard), label: 'Status'),
          NavigationDestination(icon: Icon(Icons.receipt_long_outlined), selectedIcon: Icon(Icons.receipt_long), label: 'Activity'),
        ],
      ),
    );
  }

  // ---- Status tab ----------------------------------------------------------

  Widget _statusTab(DeviceState st) {
    final ready = st.sims.where((s) => s.status.toUpperCase() == 'READY').length;
    return RefreshIndicator(
      onRefresh: () async {
        await DeviceBridge.reportSims();
        await Future.delayed(const Duration(seconds: 1));
        await _refresh();
      },
      child: ListView(
        padding: const EdgeInsets.fromLTRB(14, 14, 14, 24),
        children: [
          _pushCard(ready),
          const SizedBox(height: 18),
          Padding(
            padding: const EdgeInsets.only(left: 4, bottom: 8),
            child: Text('SIM cards', style: Theme.of(context).textTheme.titleMedium?.copyWith(fontWeight: FontWeight.w700)),
          ),
          if (st.sims.isEmpty)
            _emptyCard('No SIMs reported yet', 'Pull to refresh — the phone reports SIMs to the server.')
          else
            ...st.sims.map(_simStateCard),
        ],
      ),
    );
  }

  Widget _pushCard(int ready) {
    final ok = StatusColors.ok(context);
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(18),
        child: Row(
          children: [
            Container(
              width: 52, height: 52,
              decoration: BoxDecoration(color: ok.withValues(alpha: 0.15), borderRadius: BorderRadius.circular(15)),
              child: Icon(Icons.bolt, color: ok, size: 28),
            ),
            const SizedBox(width: 16),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('Ready · push-driven', style: Theme.of(context).textTheme.titleLarge?.copyWith(fontWeight: FontWeight.w700, color: ok)),
                  const SizedBox(height: 2),
                  Text('The server pushes each SMS via FCM — no need to keep the app open. $ready SIM(s) ready.',
                      style: Theme.of(context).textTheme.bodyMedium?.copyWith(color: StatusColors.muted(context))),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _simStateCard(SimState s) {
    final color = StatusColors.forStatus(context, s.status);
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                _slotBadge(s.slot),
                const SizedBox(width: 10),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(s.operator, style: Theme.of(context).textTheme.titleMedium?.copyWith(fontWeight: FontWeight.w700)),
                      if (s.msisdn.isNotEmpty)
                        Text(s.msisdn, style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
                    ],
                  ),
                ),
                _statusChip(s.status, color),
              ],
            ),
            const SizedBox(height: 14),
            Row(
              children: [
                Text('Quota', style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
                const Spacer(),
                Text('${s.sentToday} / ${s.dailyQuota}',
                    style: const TextStyle(fontWeight: FontWeight.w700, fontFeatures: [FontFeature.tabularFigures()])),
                Text('  seg/day', style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
              ],
            ),
            const SizedBox(height: 8),
            ClipRRect(
              borderRadius: BorderRadius.circular(999),
              child: LinearProgressIndicator(
                value: s.quotaFraction,
                minHeight: 8,
                backgroundColor: Theme.of(context).colorScheme.surfaceContainerHighest,
                valueColor: AlwaysStoppedAnimation(
                  s.quotaFraction >= 0.85 ? StatusColors.bad(context) : s.quotaFraction >= 0.6 ? StatusColors.warn(context) : AppTheme.brandCyan,
                ),
              ),
            ),
            const SizedBox(height: 14),
            Row(
              children: [
                _healthPill(s.healthScore),
                const Spacer(),
                FilledButton.tonalIcon(
                  onPressed: () => _editQuota(s),
                  icon: const Icon(Icons.tune, size: 18),
                  label: const Text('Set quota'),
                  style: FilledButton.styleFrom(minimumSize: const Size(0, 40)),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget _slotBadge(int slot) => Container(
        width: 40, height: 40,
        decoration: BoxDecoration(color: AppTheme.brandCyan.withValues(alpha: 0.14), borderRadius: BorderRadius.circular(12)),
        alignment: Alignment.center,
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            const Icon(Icons.sim_card, size: 16, color: AppTheme.brandCyan),
            Text('$slot', style: const TextStyle(fontSize: 10, fontWeight: FontWeight.w700, color: AppTheme.brandCyan)),
          ],
        ),
      );

  Widget _statusChip(String status, Color color) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
        decoration: BoxDecoration(color: color.withValues(alpha: 0.15), borderRadius: BorderRadius.circular(999)),
        child: Text(status, style: TextStyle(color: color, fontWeight: FontWeight.w700, fontSize: 11)),
      );

  Widget _healthPill(int health) {
    final c = health >= 70 ? StatusColors.ok(context) : health >= 40 ? StatusColors.warn(context) : StatusColors.bad(context);
    return Row(
      children: [
        Icon(Icons.favorite, size: 14, color: c),
        const SizedBox(width: 5),
        Text('$health', style: TextStyle(color: c, fontWeight: FontWeight.w700)),
        Text(' health', style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
      ],
    );
  }

  Future<void> _editQuota(SimState s) async {
    int value = s.dailyQuota;
    await showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      showDragHandle: true,
      builder: (ctx) => Padding(
        padding: EdgeInsets.fromLTRB(20, 4, 20, 20 + MediaQuery.of(ctx).viewInsets.bottom),
        child: StatefulBuilder(
          builder: (ctx, setSheet) => Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text('Daily quota — ${s.operator}',
                  style: Theme.of(ctx).textTheme.titleLarge?.copyWith(fontWeight: FontWeight.w700)),
              const SizedBox(height: 2),
              Text('Segments per day for slot ${s.slot}. Sent today: ${s.sentToday}.',
                  style: Theme.of(ctx).textTheme.bodySmall?.copyWith(color: StatusColors.muted(ctx))),
              const SizedBox(height: 18),
              Row(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  IconButton.filledTonal(
                    onPressed: () => setSheet(() => value = (value - 50).clamp(0, 5000)),
                    icon: const Icon(Icons.remove),
                  ),
                  Expanded(
                    child: Center(
                      child: Text('$value',
                          style: Theme.of(ctx).textTheme.displaySmall?.copyWith(fontWeight: FontWeight.w800, fontFeatures: const [FontFeature.tabularFigures()])),
                    ),
                  ),
                  IconButton.filledTonal(
                    onPressed: () => setSheet(() => value = (value + 50).clamp(0, 5000)),
                    icon: const Icon(Icons.add),
                  ),
                ],
              ),
              Slider(
                value: value.toDouble().clamp(0, 2000),
                min: 0, max: 2000, divisions: 40,
                label: '$value',
                onChanged: (v) => setSheet(() => value = v.round()),
              ),
              Wrap(
                spacing: 8,
                children: [100, 200, 500, 1000].map((p) => ActionChip(
                      label: Text('$p'),
                      onPressed: () => setSheet(() => value = p),
                    )).toList(),
              ),
              const SizedBox(height: 18),
              FilledButton.icon(
                onPressed: () async {
                  await DeviceBridge.setQuota(s.subscriptionId, value);
                  if (ctx.mounted) Navigator.of(ctx).pop();
                  await Future.delayed(const Duration(milliseconds: 800));
                  _refresh();
                  if (mounted) {
                    ScaffoldMessenger.of(context).showSnackBar(
                      SnackBar(content: Text('Quota set to $value/day for ${s.operator}'), behavior: SnackBarBehavior.floating),
                    );
                  }
                },
                icon: const Icon(Icons.check),
                label: const Text('Apply quota'),
              ),
            ],
          ),
        ),
      ),
    );
  }

  // ---- Activity tab --------------------------------------------------------

  Widget _activityTab(DeviceState st) {
    if (st.activity.isEmpty) {
      return _emptyCentered(Icons.receipt_long_outlined, 'No activity yet', 'Sends and delivery reports will appear here.');
    }
    final reversed = st.activity.reversed.toList();
    return ListView.separated(
      padding: const EdgeInsets.all(14),
      itemCount: reversed.length,
      separatorBuilder: (_, _) => const SizedBox(height: 8),
      itemBuilder: (_, i) => _activityTile(reversed[i]),
    );
  }

  Widget _activityTile(ActivityEvent e) {
    final (icon, color) = _activityStyle(e.kind);
    return Card(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 12),
        child: Row(
          children: [
            Container(
              width: 38, height: 38,
              decoration: BoxDecoration(color: color.withValues(alpha: 0.14), borderRadius: BorderRadius.circular(11)),
              child: Icon(icon, color: color, size: 20),
            ),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(e.title, style: const TextStyle(fontWeight: FontWeight.w600)),
                  if (e.subtitle != null && e.subtitle!.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.only(top: 2),
                      child: Text(e.subtitle!,
                          maxLines: 2, overflow: TextOverflow.ellipsis,
                          style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
                    ),
                ],
              ),
            ),
            const SizedBox(width: 8),
            Text(_ago(e.time), style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
          ],
        ),
      ),
    );
  }

  (IconData, Color) _activityStyle(ActivityKind k) => switch (k) {
        ActivityKind.connect => (Icons.wifi, StatusColors.ok(context)),
        ActivityKind.disconnect => (Icons.wifi_off, StatusColors.warn(context)),
        ActivityKind.send => (Icons.north_east, AppTheme.brandCyan),
        ActivityKind.sent => (Icons.done, StatusColors.muted(context)),
        ActivityKind.delivered => (Icons.done_all, StatusColors.ok(context)),
        ActivityKind.failed => (Icons.error_outline, StatusColors.bad(context)),
        ActivityKind.rejected => (Icons.block, StatusColors.bad(context)),
        ActivityKind.sim => (Icons.sim_card, AppTheme.brandCyan),
        ActivityKind.info => (Icons.info_outline, StatusColors.muted(context)),
      };

  // ---- shared bits ---------------------------------------------------------

  Widget _emptyCard(String title, String sub) => Card(
        child: Padding(
          padding: const EdgeInsets.all(20),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(title, style: Theme.of(context).textTheme.titleMedium),
              const SizedBox(height: 4),
              Text(sub, style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
            ],
          ),
        ),
      );

  Widget _emptyCentered(IconData icon, String title, String sub) => Center(
        child: Padding(
          padding: const EdgeInsets.all(32),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Icon(icon, size: 48, color: StatusColors.muted(context)),
              const SizedBox(height: 14),
              Text(title, style: Theme.of(context).textTheme.titleMedium),
              const SizedBox(height: 4),
              Text(sub, textAlign: TextAlign.center, style: Theme.of(context).textTheme.bodySmall?.copyWith(color: StatusColors.muted(context))),
            ],
          ),
        ),
      );

  Future<void> _confirmUnpair() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Unpair this device?'),
        content: const Text('The phone will stop receiving send commands. You will need a new enrollment token to pair again.'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('Cancel')),
          FilledButton(
            style: FilledButton.styleFrom(backgroundColor: Theme.of(ctx).colorScheme.error),
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Unpair'),
          ),
        ],
      ),
    );
    if (ok != true) return;
    await DeviceBridge.unpair();
    widget.onUnenrolled();
  }

  static String _ago(DateTime t) {
    final d = DateTime.now().difference(t);
    if (d.inSeconds < 5) return 'now';
    if (d.inSeconds < 60) return '${d.inSeconds}s';
    if (d.inMinutes < 60) return '${d.inMinutes}m';
    if (d.inHours < 24) return '${d.inHours}h';
    final h = t.hour.toString().padLeft(2, '0');
    final m = t.minute.toString().padLeft(2, '0');
    return '$h:$m';
  }
}

/// Small WBlue brand mark (a cyan "W" on navy) matching the admin/public logo.
class _BrandMark extends StatelessWidget {
  const _BrandMark();

  @override
  Widget build(BuildContext context) {
    return Container(
      width: 30, height: 30,
      decoration: BoxDecoration(color: AppTheme.brandNavy, borderRadius: BorderRadius.circular(8)),
      alignment: Alignment.center,
      child: const Text('W', style: TextStyle(color: AppTheme.brandCyan, fontWeight: FontWeight.w900, fontSize: 17, height: 1)),
    );
  }
}

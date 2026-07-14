/// A structured activity/log entry, replacing the old plain string log so the UI can
/// render icons, colors, and relative times instead of a monospace console dump.
enum ActivityKind { connect, disconnect, send, sent, delivered, failed, rejected, sim, info }

class ActivityEvent {
  final DateTime time;
  final ActivityKind kind;
  final String title;
  final String? subtitle;

  ActivityEvent(this.kind, this.title, {this.subtitle, DateTime? time})
      : time = time ?? DateTime.now();

  factory ActivityEvent.fromJson(Map<dynamic, dynamic> m) {
    final name = (m['kind'] ?? 'info') as String;
    final kind = ActivityKind.values.firstWhere(
      (e) => e.name == name,
      orElse: () => ActivityKind.info,
    );
    final ts = m['ts'];
    final time = ts is int ? DateTime.fromMillisecondsSinceEpoch(ts) : DateTime.now();
    return ActivityEvent(kind, (m['title'] ?? '') as String, subtitle: m['subtitle'] as String?, time: time);
  }
}

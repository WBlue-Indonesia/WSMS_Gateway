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
}

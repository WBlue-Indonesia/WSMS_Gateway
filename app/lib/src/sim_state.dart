/// SimState mirrors the server's ws.SimState frame: the authoritative per-SIM view
/// (operator, status, quota, sent-today) the server pushes to this device so the app
/// can display and adjust quota. Keyed to a local SIM by [subscriptionId].
class SimState {
  final String simId;
  final int subscriptionId;
  final int slot;
  final String operator;
  final String msisdn;
  final String status;
  final int dailyQuota;
  final int sentToday;
  final int healthScore;

  const SimState({
    required this.simId,
    required this.subscriptionId,
    required this.slot,
    required this.operator,
    required this.msisdn,
    required this.status,
    required this.dailyQuota,
    required this.sentToday,
    required this.healthScore,
  });

  factory SimState.fromMap(Map<dynamic, dynamic> m) => SimState(
        simId: (m['sim_id'] ?? '') as String,
        subscriptionId: (m['subscription_id'] ?? -1) as int,
        slot: (m['slot'] ?? 0) as int,
        operator: (m['operator'] ?? 'UNKNOWN') as String,
        msisdn: (m['msisdn'] ?? '') as String,
        status: (m['status'] ?? 'UNKNOWN') as String,
        dailyQuota: (m['daily_quota'] ?? 0) as int,
        sentToday: (m['sent_today'] ?? 0) as int,
        healthScore: (m['health_score'] ?? 0) as int,
      );

  double get quotaFraction => dailyQuota <= 0 ? 0 : (sentToday / dailyQuota).clamp(0, 1).toDouble();
}

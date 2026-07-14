import 'package:flutter/services.dart';

import 'config.dart';

/// A dual-SIM subscription as reported by Android SubscriptionManager.
class SimInfo {
  final int subscriptionId;
  final int slot;
  final String carrierName;
  final String mcc;
  final String mnc;
  final String number;

  SimInfo({
    required this.subscriptionId,
    required this.slot,
    required this.carrierName,
    required this.mcc,
    required this.mnc,
    required this.number,
  });

  factory SimInfo.fromMap(Map<dynamic, dynamic> m) => SimInfo(
        subscriptionId: (m['subscription_id'] ?? -1) as int,
        slot: (m['slot'] ?? 0) as int,
        carrierName: (m['carrier_name'] ?? '') as String,
        mcc: (m['mcc'] ?? '') as String,
        mnc: (m['mnc'] ?? '') as String,
        number: (m['number'] ?? '') as String,
      );

  Map<String, dynamic> toReportJson() => {
        'subscription_id': subscriptionId,
        'slot': slot,
        'carrier_name': carrierName,
        'mcc': mcc,
        'mnc': mnc,
        'number': number,
      };
}

/// A delivery/send outcome pushed up from the Kotlin PendingIntent receivers.
class TelephonyEvent {
  final String messageId;
  final String phase; // "sent" | "delivered" | "failed"
  final bool ok;
  final String? reason;

  TelephonyEvent({required this.messageId, required this.phase, required this.ok, this.reason});

  factory TelephonyEvent.fromMap(Map<dynamic, dynamic> m) => TelephonyEvent(
        messageId: (m['message_id'] ?? '') as String,
        phase: (m['phase'] ?? '') as String,
        ok: (m['ok'] ?? false) as bool,
        reason: m['reason'] as String?,
      );
}

/// Result of handing a message to SmsManager. accepted == pre-flight OK (radio took it).
class SendAcceptance {
  final bool accepted;
  final int parts;
  final String? error;
  SendAcceptance({required this.accepted, this.parts = 0, this.error});
}

class Telephony {
  static const _method = MethodChannel(Channels.method);
  static const _events = EventChannel(Channels.events);

  /// Stream of SENT/DELIVERED/FAILED outcomes.
  Stream<TelephonyEvent> events() => _events
      .receiveBroadcastStream()
      .map((e) => TelephonyEvent.fromMap(e as Map));

  Future<List<SimInfo>> listSims() async {
    final res = await _method.invokeMethod<List<dynamic>>('listSims') ?? [];
    return res.map((e) => SimInfo.fromMap(e as Map)).toList();
  }

  /// Hands the message to the SmsManager bound to [subscriptionId]. Throwing / accepted:false
  /// means it never reached the radio (caller replies send_ack:rejected — reroute allowed).
  Future<SendAcceptance> sendSms({
    required String messageId,
    required int subscriptionId,
    required String target,
    required String body,
  }) async {
    try {
      final res = await _method.invokeMethod<Map<dynamic, dynamic>>('sendSms', {
        'message_id': messageId,
        'subscription_id': subscriptionId,
        'target': target,
        'body': body,
      });
      return SendAcceptance(
        accepted: (res?['accepted'] ?? false) as bool,
        parts: (res?['parts'] ?? 0) as int,
      );
    } on PlatformException catch (e) {
      return SendAcceptance(accepted: false, error: e.message);
    }
  }
}

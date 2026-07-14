import 'dart:convert';

import 'package:flutter/services.dart';

import 'activity.dart';
import 'sim_state.dart';

/// Pull-based view of the native push-driven device state. There is no socket: the
/// native side (FcmService) does the sending; the UI reads state and issues actions
/// over the method channel.
class DeviceState {
  final bool enrolled;
  final List<SimState> sims;
  final List<ActivityEvent> activity;

  DeviceState(this.enrolled, this.sims, this.activity);

  factory DeviceState.fromJson(Map<String, dynamic> m) => DeviceState(
        (m['enrolled'] ?? false) as bool,
        ((m['sims'] as List?) ?? const []).map((e) => SimState.fromMap(e as Map)).toList(),
        ((m['activity'] as List?) ?? const []).map((e) => ActivityEvent.fromJson(e as Map)).toList(),
      );

  static DeviceState empty() => DeviceState(false, const [], const []);
}

class DeviceBridge {
  static const _m = MethodChannel('wsms/device');

  static Future<bool> isEnrolled() async =>
      (await _m.invokeMethod<bool>('isEnrolled')) ?? false;

  static Future<DeviceState> getState() async {
    final s = await _m.invokeMethod<String>('getState');
    return DeviceState.fromJson(jsonDecode(s ?? '{}') as Map<String, dynamic>);
  }

  static Future<void> saveCredsAndRegister(String url, String id, String secret) =>
      _m.invokeMethod('saveCredsAndRegister', {
        'server_url': url,
        'device_id': id,
        'device_secret': secret,
      });

  static Future<void> registerFcm() => _m.invokeMethod('registerFcm');
  static Future<void> reportSims() => _m.invokeMethod('reportSims');
  static Future<void> refreshSims() => _m.invokeMethod('refreshSims');
  static Future<void> setQuota(int subscriptionId, int dailyQuota) =>
      _m.invokeMethod('setQuota', {'subscription_id': subscriptionId, 'daily_quota': dailyQuota});
  static Future<void> unpair() => _m.invokeMethod('unpair');
}

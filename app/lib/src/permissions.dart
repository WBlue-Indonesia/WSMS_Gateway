import 'package:permission_handler/permission_handler.dart';

/// Requests the runtime permissions the sender needs. Returns true only if SMS +
/// phone-state (required to send and to enumerate SIMs) are granted.
Future<bool> requestSenderPermissions() async {
  final results = await [
    Permission.sms,
    Permission.phone,
    Permission.notification,
  ].request();

  final smsOk = results[Permission.sms]?.isGranted ?? false;
  final phoneOk = results[Permission.phone]?.isGranted ?? false;
  return smsOk && phoneOk;
}

Future<void> requestIgnoreBatteryOptimizations() async {
  final status = await Permission.ignoreBatteryOptimizations.status;
  if (!status.isGranted) {
    await Permission.ignoreBatteryOptimizations.request();
  }
}

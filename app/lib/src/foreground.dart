import 'package:flutter_foreground_task/flutter_foreground_task.dart';

/// Foreground service that keeps the app process (and therefore the main-isolate
/// WebSocket in Gateway) alive while the phone acts as a sender (docs/05 §3).
///
/// NOTE (amendment F8): survival is best-effort. Android 15 caps a dataSync FGS at
/// ~6h/24h and OEM killers can still tear the process down; the server's fast
/// reconnect + FCM wake + presence-driven requeue is the real delivery guarantee.
/// The task handler below runs in the service isolate purely to hold the service;
/// the Gateway itself stays in the main isolate because the telephony platform
/// channel is registered on the main FlutterEngine.

@pragma('vm:entry-point')
void startCallback() {
  FlutterForegroundTask.setTaskHandler(_KeepAliveHandler());
}

class _KeepAliveHandler extends TaskHandler {
  @override
  Future<void> onStart(DateTime timestamp, TaskStarter starter) async {}

  @override
  void onRepeatEvent(DateTime timestamp) {
    // Nudge the UI/main isolate that the service is alive.
    FlutterForegroundTask.sendDataToMain('tick');
  }

  @override
  Future<void> onDestroy(DateTime timestamp) async {}
}

class ForegroundService {
  static void init() {
    FlutterForegroundTask.init(
      androidNotificationOptions: AndroidNotificationOptions(
        channelId: 'wsms_service',
        channelName: 'WSMS Sender',
        channelDescription: 'Keeps the SMS gateway connection alive.',
        channelImportance: NotificationChannelImportance.LOW,
        priority: NotificationPriority.LOW,
      ),
      iosNotificationOptions: const IOSNotificationOptions(),
      foregroundTaskOptions: ForegroundTaskOptions(
        eventAction: ForegroundTaskEventAction.repeat(30000),
        autoRunOnBoot: true,
        autoRunOnMyPackageReplaced: true,
        allowWakeLock: true,
        allowWifiLock: true,
      ),
    );
  }

  static Future<void> start() async {
    if (await FlutterForegroundTask.isRunningService) return;
    await FlutterForegroundTask.startService(
      notificationTitle: 'WSMS Sender active',
      notificationText: 'Connected to the gateway.',
      callback: startCallback,
    );
  }

  static Future<void> stop() async {
    if (await FlutterForegroundTask.isRunningService) {
      await FlutterForegroundTask.stopService();
    }
  }
}

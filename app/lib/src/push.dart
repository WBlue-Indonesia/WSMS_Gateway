import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_messaging/firebase_messaging.dart';

/// FCM integration. The server sends a high-priority "wake" data message when it
/// has queued work for an offline device (docs/01 §7); Android delivers it and the
/// foreground service + Gateway re-establish the socket.
///
/// Everything is best-effort and failure-swallowing: if no Firebase project is
/// configured (no valid google-services.json), init() returns null and the app runs
/// normally — the server simply cannot wake this device (amendment F6/F8).

@pragma('vm:entry-point')
Future<void> _backgroundHandler(RemoteMessage message) async {
  // A data-only wake message. Delivery of it revives the process; the running
  // foreground service + Gateway reconnect the WebSocket. No work needed here.
}

class Push {
  static String? token;

  static Future<String?> init() async {
    try {
      await Firebase.initializeApp();
      FirebaseMessaging.onBackgroundMessage(_backgroundHandler);
      final fm = FirebaseMessaging.instance;
      await fm.requestPermission();
      token = await fm.getToken();
      return token;
    } catch (_) {
      return null; // Firebase not configured — run without wake.
    }
  }
}

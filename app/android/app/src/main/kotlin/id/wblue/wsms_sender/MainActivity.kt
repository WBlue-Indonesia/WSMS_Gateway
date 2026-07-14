package id.wblue.wsms_sender

import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val handler = TelephonyHandler(applicationContext)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, TelephonyHandler.METHOD_CHANNEL)
            .setMethodCallHandler(handler)
        EventChannel(flutterEngine.dartExecutor.binaryMessenger, TelephonyHandler.EVENT_CHANNEL)
            .setStreamHandler(handler)
    }
}

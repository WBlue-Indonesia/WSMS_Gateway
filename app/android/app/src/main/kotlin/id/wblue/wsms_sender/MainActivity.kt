package id.wblue.wsms_sender

import android.util.Log
import com.google.firebase.messaging.FirebaseMessaging
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * UI ↔ device bridge for the push-driven model. There is no persistent socket: the UI
 * saves credentials, registers the FCM token, reports SIMs, reads state, and adjusts
 * quota — all over REST. Actual sending happens in FcmService when woken by FCM.
 */
class MainActivity : FlutterActivity() {

    private val channel = "wsms/device"
    private val io = Executors.newSingleThreadExecutor()

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, channel).setMethodCallHandler { call, result ->
            val store = Store(this)
            when (call.method) {
                "isEnrolled" -> result.success(store.isEnrolled)
                "getState" -> result.success(store.snapshot())
                "saveCredsAndRegister" -> {
                    val url = call.argument<String>("server_url") ?: ""
                    val id = call.argument<String>("device_id") ?: ""
                    val secret = call.argument<String>("device_secret") ?: ""
                    store.saveCreds(url, id, secret)
                    io.execute { registerAndReport(store) }
                    result.success(true)
                }
                "registerFcm" -> { io.execute { registerAndReport(store) }; result.success(true) }
                "refreshSims" -> { io.execute { DeviceApi(store).refreshSims() }; result.success(true) }
                "reportSims" -> { io.execute { DeviceApi(store).reportSims(listSims()) }; result.success(true) }
                "setQuota" -> {
                    val sub = call.argument<Int>("subscription_id") ?: -1
                    val quota = call.argument<Int>("daily_quota") ?: 0
                    io.execute { DeviceApi(store).setQuota(sub, quota) }
                    result.success(true)
                }
                "unpair" -> { store.clear(); result.success(true) }
                else -> result.notImplemented()
            }
        }
    }

    /** Reports SIMs, then registers the FCM token so the server can push send commands. */
    private fun registerAndReport(store: Store) {
        if (!store.isEnrolled) return
        val api = DeviceApi(store)
        api.reportSims(listSims())
        try {
            FirebaseMessaging.getInstance().token.addOnCompleteListener { task ->
                if (task.isSuccessful && task.result != null) {
                    Log.i("WsmsFcm", "fcm token: ${task.result}")
                    io.execute { api.registerToken(task.result) }
                }
            }
        } catch (_: Exception) {
            // Firebase not available — the server can't push. The app is otherwise fine.
        }
    }

    private fun listSims(): JSONArray {
        val arr = JSONArray()
        for (s in Telephony(this).listSims()) {
            arr.put(JSONObject()
                .put("subscription_id", s.subscriptionId).put("slot", s.slot)
                .put("carrier_name", s.carrierName).put("mcc", s.mcc).put("mnc", s.mnc).put("number", s.number))
        }
        return arr
    }
}

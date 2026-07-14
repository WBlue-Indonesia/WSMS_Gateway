package id.wblue.wsms_sender

import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * The heart of the push-driven model. A high-priority FCM data message carries the send
 * command; Play Services wakes even a frozen app and delivers it here. We send the SMS
 * natively and confirm the result over REST — no persistent socket, so aggressive-OEM
 * background suspension is irrelevant.
 */
class FcmService : FirebaseMessagingService() {

    companion object { const val TAG = "WsmsFcm" }

    override fun onNewToken(token: String) {
        Log.i(TAG, "onNewToken")
        val store = Store(this)
        if (store.isEnrolled) DeviceApi(store).registerToken(token)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        val data = message.data
        val type = data["type"] ?: return
        Log.i(TAG, "fcm type=$type")
        val store = Store(this)
        if (!store.isEnrolled) return
        val api = DeviceApi(store)
        when (type) {
            "send" -> handleSend(data, store, api)
            "report_sims" -> { api.reportSims(listSims()); store.addActivity("sim", "Reported SIMs", null) }
            "wake" -> api.refreshSims()
        }
    }

    private fun handleSend(data: Map<String, String>, store: Store, api: DeviceApi) {
        val messageId = data["message_id"] ?: return
        val target = data["target"] ?: ""
        val body = data["body"] ?: ""
        val subId = data["subscription_id"]?.toIntOrNull() ?: -1
        val expiresAtMs = data["expires_at_ms"]?.toLongOrNull() ?: 0L
        Log.i(TAG, "send $messageId -> $target sub=$subId")

        // 1. Dedup against the local ledger (F1/F5).
        val phase = store.ledgerPhase(messageId)
        if (phase != null) {
            api.ack(messageId, "duplicate", "already $phase")
            api.delivery(messageId, if (phase == "delivered") "delivered" else "sent", null)
            return
        }
        // 2. Expiry guard (F7).
        if (expiresAtMs > 0 && System.currentTimeMillis() > expiresAtMs) {
            api.ack(messageId, "rejected", "expired")
            return
        }

        // Run the SENT/DELIVERED receivers on a background thread so their REST POSTs
        // don't hit the main thread. Block this FCM callback until SENT (or timeout) so
        // the process isn't reclaimed before the result reaches the server.
        val ht = HandlerThread("wsms-sms").apply { start() }
        val cbHandler = Handler(ht.looper)
        val done = CountDownLatch(1)

        Telephony(this).sendSms(messageId, subId, target, body,
            onResult = { accepted, parts, error ->
                if (accepted) {
                    store.setLedgerPhase(messageId, "sent") // commit AFTER acceptance (F5)
                    api.ack(messageId, "accepted", null)
                    store.addActivity("send", "Sending → $target", "sub $subId · $parts part(s)")
                } else {
                    api.ack(messageId, "rejected", error ?: "not accepted by radio")
                    store.addActivity("rejected", "Rejected", error)
                    done.countDown()
                }
            },
            onPhase = { p, _, _, reason ->
                when (p) {
                    "sent" -> {
                        api.delivery(messageId, "sent", null)
                        store.addActivity("sent", "Left the radio", messageId.take(8))
                        done.countDown()
                    }
                    "delivered" -> {
                        store.setLedgerPhase(messageId, "delivered")
                        api.delivery(messageId, "delivered", null)
                        store.addActivity("delivered", "Delivered", messageId.take(8))
                    }
                    else -> {
                        api.delivery(messageId, "failed", reason)
                        store.addActivity("failed", "Delivery failed", reason)
                        done.countDown()
                    }
                }
            },
            callbackHandler = cbHandler)

        try { done.await(20, TimeUnit.SECONDS) } catch (_: Exception) {}
        ht.quitSafely()
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

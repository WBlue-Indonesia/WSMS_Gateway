package id.wblue.wsms_sender

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.telephony.SmsManager
import android.telephony.SubscriptionManager
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel

/**
 * Bridges Flutter <-> Android telephony:
 *  - listSims: enumerate active SIMs (subscriptionId, slot, carrier, mcc/mnc, number)
 *  - sendSms:  send via the SmsManager bound to a specific subscriptionId (dual-SIM),
 *              multipart-aware, reporting SENT then DELIVERED back over the event channel.
 *
 * Implements docs/05 §5-6. subscriptionId is device-local; the server addresses SIMs by
 * its own sim_id and echoes the current subscription_id in send_command.
 */
class TelephonyHandler(private val context: Context) :
    MethodChannel.MethodCallHandler, EventChannel.StreamHandler {

    companion object {
        const val METHOD_CHANNEL = "wsms/telephony"
        const val EVENT_CHANNEL = "wsms/telephony_events"
    }

    private var events: EventChannel.EventSink? = null
    private val main = Handler(Looper.getMainLooper())

    override fun onListen(arguments: Any?, sink: EventChannel.EventSink?) { events = sink }
    override fun onCancel(arguments: Any?) { events = null }

    private fun emit(map: Map<String, Any?>) {
        main.post { events?.success(map) }
    }

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            "listSims" -> result.success(listSims())
            "sendSms" -> {
                val messageId = call.argument<String>("message_id") ?: ""
                val subId = call.argument<Int>("subscription_id") ?: -1
                val target = call.argument<String>("target") ?: ""
                val body = call.argument<String>("body") ?: ""
                sendSms(messageId, subId, target, body, result)
            }
            else -> result.notImplemented()
        }
    }

    @SuppressLint("MissingPermission")
    private fun listSims(): List<Map<String, Any?>> {
        val out = ArrayList<Map<String, Any?>>()
        val sm = context.getSystemService(Context.TELEPHONY_SUBSCRIPTION_SERVICE) as? SubscriptionManager
            ?: return out
        val subs = try { sm.activeSubscriptionInfoList } catch (e: SecurityException) { null } ?: return out
        for (info in subs) {
            out.add(
                mapOf(
                    "subscription_id" to info.subscriptionId,
                    "slot" to info.simSlotIndex,
                    "carrier_name" to (info.carrierName?.toString() ?: ""),
                    "mcc" to (if (Build.VERSION.SDK_INT >= 29) info.mccString ?: "" else info.mcc.toString()),
                    "mnc" to (if (Build.VERSION.SDK_INT >= 29) info.mncString ?: "" else info.mnc.toString()),
                    "number" to (info.number ?: "")
                )
            )
        }
        return out
    }

    @SuppressLint("MissingPermission")
    private fun sendSms(messageId: String, subId: Int, target: String, body: String, result: MethodChannel.Result) {
        val sm = smsManagerFor(subId)
        if (sm == null) {
            result.error("NO_SMS_MANAGER", "could not resolve SmsManager for sub $subId", null)
            return
        }

        val sentAction = "id.wblue.wsms_sender.SENT.$messageId"
        val deliveredAction = "id.wblue.wsms_sender.DELIVERED.$messageId"

        registerOnce(sentAction) { code ->
            if (code == android.app.Activity.RESULT_OK) {
                emit(mapOf("message_id" to messageId, "phase" to "sent", "ok" to true))
            } else {
                emit(mapOf("message_id" to messageId, "phase" to "failed", "ok" to false, "code" to code, "reason" to sentReason(code)))
            }
        }
        registerOnce(deliveredAction) { code ->
            emit(mapOf("message_id" to messageId, "phase" to "delivered", "ok" to (code == android.app.Activity.RESULT_OK), "code" to code))
        }

        try {
            val parts = sm.divideMessage(body)
            val flags = if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_IMMUTABLE else 0
            val sentIntents = ArrayList<PendingIntent>(parts.size)
            val delIntents = ArrayList<PendingIntent>(parts.size)
            for (i in parts.indices) {
                sentIntents.add(PendingIntent.getBroadcast(context, i, Intent(sentAction).setPackage(context.packageName), flags))
                delIntents.add(PendingIntent.getBroadcast(context, i, Intent(deliveredAction).setPackage(context.packageName), flags))
            }
            sm.sendMultipartTextMessage(target, null, parts, sentIntents, delIntents)
            // "accepted" = handed to SmsManager without throwing (radio pre-flight OK — amendment F5).
            result.success(mapOf("accepted" to true, "parts" to parts.size))
        } catch (e: Exception) {
            // Never reached the radio → the app-side send_ack should be "rejected" (reroute allowed).
            result.error("SEND_FAILED", e.message, null)
        }
    }

    private fun smsManagerFor(subId: Int): SmsManager? {
        return try {
            if (Build.VERSION.SDK_INT >= 31) {
                context.getSystemService(SmsManager::class.java).createForSubscriptionId(subId)
            } else {
                @Suppress("DEPRECATION")
                SmsManager.getSmsManagerForSubscriptionId(subId)
            }
        } catch (e: Exception) {
            null
        }
    }

    /** Registers a one-shot receiver that self-unregisters after the first broadcast. */
    private fun registerOnce(action: String, onResult: (Int) -> Unit) {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context, intent: Intent) {
                onResult(resultCode)
                try { ctx.unregisterReceiver(this) } catch (_: Exception) {}
            }
        }
        val filter = IntentFilter(action)
        if (Build.VERSION.SDK_INT >= 33) {
            context.registerReceiver(receiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            context.registerReceiver(receiver, filter)
        }
    }

    private fun sentReason(code: Int): String = when (code) {
        SmsManager.RESULT_ERROR_GENERIC_FAILURE -> "generic_failure"
        SmsManager.RESULT_ERROR_NO_SERVICE -> "no_service"
        SmsManager.RESULT_ERROR_NULL_PDU -> "null_pdu"
        SmsManager.RESULT_ERROR_RADIO_OFF -> "radio_off"
        else -> "error_$code"
    }
}

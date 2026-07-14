package id.wblue.wsms_sender

import android.annotation.SuppressLint
import android.app.Activity
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Handler
import android.telephony.SmsManager
import android.telephony.SubscriptionManager

/**
 * Reusable telephony core (SIM enumeration + dual-SIM SMS send). Called directly by the
 * native SenderService — no Flutter platform channel is involved, so it works whether or
 * not the UI is running.
 */
class Telephony(private val context: Context) {

    data class Sim(
        val subscriptionId: Int,
        val slot: Int,
        val carrierName: String,
        val mcc: String,
        val mnc: String,
        val number: String,
    )

    @SuppressLint("MissingPermission")
    fun listSims(): List<Sim> {
        val out = ArrayList<Sim>()
        val sm = context.getSystemService(Context.TELEPHONY_SUBSCRIPTION_SERVICE) as? SubscriptionManager
            ?: return out
        val subs = try { sm.activeSubscriptionInfoList } catch (e: SecurityException) { null } ?: return out
        for (info in subs) {
            out.add(
                Sim(
                    subscriptionId = info.subscriptionId,
                    slot = info.simSlotIndex,
                    carrierName = info.carrierName?.toString() ?: "",
                    mcc = if (Build.VERSION.SDK_INT >= 29) info.mccString ?: "" else info.mcc.toString(),
                    mnc = if (Build.VERSION.SDK_INT >= 29) info.mncString ?: "" else info.mnc.toString(),
                    number = info.number ?: "",
                )
            )
        }
        return out
    }

    /**
     * Hands [body] to the SmsManager bound to [subId]. [onResult] reports the pre-flight
     * outcome (accepted = radio took it, F5). [onPhase] later reports SENT / DELIVERED /
     * FAILED from the one-shot broadcast receivers.
     */
    @SuppressLint("MissingPermission")
    fun sendSms(
        messageId: String,
        subId: Int,
        target: String,
        body: String,
        onResult: (accepted: Boolean, parts: Int, error: String?) -> Unit,
        onPhase: (phase: String, ok: Boolean, code: Int, reason: String?) -> Unit,
        callbackHandler: Handler? = null, // where the SENT/DELIVERED receivers run (off main)
    ) {
        val sm = smsManagerFor(subId)
        if (sm == null) {
            onResult(false, 0, "could not resolve SmsManager for sub $subId")
            return
        }
        val sentAction = "id.wblue.wsms_sender.SENT.$messageId"
        val deliveredAction = "id.wblue.wsms_sender.DELIVERED.$messageId"

        registerOnce(sentAction, callbackHandler) { code ->
            if (code == Activity.RESULT_OK) onPhase("sent", true, code, null)
            else onPhase("failed", false, code, sentReason(code))
        }
        registerOnce(deliveredAction, callbackHandler) { code ->
            onPhase("delivered", code == Activity.RESULT_OK, code, null)
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
            onResult(true, parts.size, null)
        } catch (e: Exception) {
            onResult(false, 0, e.message ?: "send failed")
        }
    }

    private fun smsManagerFor(subId: Int): SmsManager? = try {
        if (Build.VERSION.SDK_INT >= 31) {
            context.getSystemService(SmsManager::class.java).createForSubscriptionId(subId)
        } else {
            @Suppress("DEPRECATION")
            SmsManager.getSmsManagerForSubscriptionId(subId)
        }
    } catch (e: Exception) {
        null
    }

    private fun registerOnce(action: String, handler: Handler?, onResult: (Int) -> Unit) {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context, intent: Intent) {
                onResult(resultCode)
                try { ctx.unregisterReceiver(this) } catch (_: Exception) {}
            }
        }
        val filter = IntentFilter(action)
        if (Build.VERSION.SDK_INT >= 33) {
            context.registerReceiver(receiver, filter, null, handler, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            context.registerReceiver(receiver, filter, null, handler)
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

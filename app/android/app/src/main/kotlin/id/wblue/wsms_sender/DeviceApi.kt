package id.wblue.wsms_sender

import android.util.Log
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/** Device-authed REST client for the push-driven model. All calls are synchronous —
 *  they run on the FCM service's worker thread, so blocking is fine. */
class DeviceApi(private val store: Store) {

    companion object { const val TAG = "WsmsFcm" }

    private val json = "application/json; charset=utf-8".toMediaType()
    private val client = OkHttpClient.Builder()
        .callTimeout(25, TimeUnit.SECONDS)
        .build()

    private fun base() = (store.serverUrl ?: "").trimEnd('/')
    private fun auth() = "Bearer dev_${store.deviceId}.${store.deviceSecret}"

    private fun post(path: String, body: JSONObject): String? {
        if (!store.isEnrolled) return null
        val req = Request.Builder()
            .url(base() + path)
            .addHeader("Authorization", auth())
            .post(body.toString().toRequestBody(json))
            .build()
        return try {
            client.newCall(req).execute().use { resp ->
                if (resp.isSuccessful) resp.body?.string() else {
                    Log.w(TAG, "POST $path -> ${resp.code}"); null
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "POST $path failed: ${e.message}"); null
        }
    }

    private fun get(path: String): String? {
        if (!store.isEnrolled) return null
        val req = Request.Builder().url(base() + path).addHeader("Authorization", auth()).get().build()
        return try {
            client.newCall(req).execute().use { resp -> if (resp.isSuccessful) resp.body?.string() else null }
        } catch (e: Exception) { Log.w(TAG, "GET $path failed: ${e.message}"); null }
    }

    fun registerToken(token: String) = post("/v1/device/token", JSONObject().put("push_token", token))

    /** Reports SIMs and stores the returned authoritative sim state. */
    fun reportSims(sims: JSONArray) {
        val body = post("/v1/device/report-sims", JSONObject().put("sims", sims)) ?: return
        storeSims(body)
    }

    fun refreshSims() {
        val body = get("/v1/device/sims") ?: return
        storeSims(body)
    }

    fun ack(messageId: String, result: String, reason: String?) =
        post("/v1/device/ack", JSONObject().put("message_id", messageId).put("result", result)
            .apply { if (reason != null) put("reason", reason) })

    fun delivery(messageId: String, status: String, reason: String?) =
        post("/v1/device/delivery", JSONObject().put("message_id", messageId).put("status", status)
            .apply { if (reason != null) put("reason", reason) })

    fun setQuota(subscriptionId: Int, dailyQuota: Int) {
        val body = post("/v1/device/set-quota", JSONObject().put("subscription_id", subscriptionId).put("daily_quota", dailyQuota)) ?: return
        storeSims(body)
    }

    private fun storeSims(responseBody: String) {
        try {
            val sims = JSONObject(responseBody).optJSONArray("sims") ?: return
            store.simsJson = sims.toString()
        } catch (_: Exception) {}
    }
}

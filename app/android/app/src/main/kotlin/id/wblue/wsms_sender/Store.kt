package id.wblue.wsms_sender

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

/** Shared on-device state (SharedPreferences): enrollment creds, the send ledger, the
 *  latest SIM state, and a small activity log. Used by both the FCM service and the UI. */
class Store(ctx: Context) {
    private val prefs = ctx.getSharedPreferences(NAME, Context.MODE_PRIVATE)

    companion object {
        const val NAME = "wsms_sender"
        private const val MAX_ACTIVITY = 100
    }

    // ---- enrollment ----
    var serverUrl: String? get() = prefs.getString("server_url", null); set(v) { prefs.edit().putString("server_url", v).apply() }
    var deviceId: String? get() = prefs.getString("device_id", null); set(v) { prefs.edit().putString("device_id", v).apply() }
    var deviceSecret: String? get() = prefs.getString("device_secret", null); set(v) { prefs.edit().putString("device_secret", v).apply() }
    val isEnrolled get() = !serverUrl.isNullOrEmpty() && !deviceId.isNullOrEmpty() && !deviceSecret.isNullOrEmpty()

    fun saveCreds(url: String, id: String, secret: String) {
        prefs.edit().putString("server_url", url).putString("device_id", id).putString("device_secret", secret).apply()
    }

    fun clear() = prefs.edit().clear().apply()

    // ---- send ledger (dedup, F1/F5) ----
    fun ledgerPhase(messageId: String): String? = prefs.getString("L_$messageId", null)
    fun setLedgerPhase(messageId: String, phase: String) { prefs.edit().putString("L_$messageId", phase).apply() }
    // Drop the ledger entry so a server retry of the SAME message id actually re-sends
    // (used when the radio rejected the send — nothing left the device).
    fun clearLedgerPhase(messageId: String) { prefs.edit().remove("L_$messageId").apply() }

    // ---- latest sim state (from the server) ----
    var simsJson: String get() = prefs.getString("sims", "[]") ?: "[]"; set(v) { prefs.edit().putString("sims", v).apply() }

    // ---- activity log ----
    fun addActivity(kind: String, title: String, subtitle: String?) {
        val arr = try { JSONArray(prefs.getString("activity", "[]")) } catch (e: Exception) { JSONArray() }
        val e = JSONObject().put("kind", kind).put("title", title).put("ts", System.currentTimeMillis())
        if (subtitle != null) e.put("subtitle", subtitle)
        arr.put(e)
        // trim to the most recent MAX_ACTIVITY
        val trimmed = if (arr.length() > MAX_ACTIVITY) {
            val t = JSONArray()
            for (i in (arr.length() - MAX_ACTIVITY) until arr.length()) t.put(arr.get(i))
            t
        } else arr
        prefs.edit().putString("activity", trimmed.toString()).apply()
    }

    fun activityJson(): String = prefs.getString("activity", "[]") ?: "[]"

    /** Snapshot for the UI: { enrolled, sims, activity }. */
    fun snapshot(): String = JSONObject()
        .put("enrolled", isEnrolled)
        .put("sims", JSONArray(simsJson))
        .put("activity", JSONArray(activityJson()))
        .toString()
}

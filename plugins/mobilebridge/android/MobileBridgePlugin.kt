package dev.mygo

import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

/**
 * MyGo native plugin bridging Android TransferForegroundService and ShareReceiverActivity
 * to TailChat's Go mobilebridge controller.
 */
class MobileBridgePlugin : MyGoPlugin {

    companion object {
        private const val PLUGIN_ID = "mobilebridge"
        private val lock = Any()
        private val pendingShares = ArrayList<JSONObject>()
        @Volatile
        private var goReady = false

        /**
         * Called by ShareReceiverActivity after copying shared ContentResolver URIs.
         * Emits immediately when Go has drained the initial queue, or buffers until Go boots.
         */
        fun emitShare(text: String?, files: List<String>) {
            val payload = JSONObject().apply {
                put("id", UUID.randomUUID().toString())
                if (!text.isNullOrEmpty()) put("text", text)
                put("files", JSONArray(files))
                put("source", "android_share")
            }
            val shouldEmitNow: Boolean
            synchronized(lock) {
                shouldEmitNow = goReady
                if (!goReady) {
                    pendingShares.add(payload)
                }
            }
            if (shouldEmitNow) {
                PluginHost.emit(PLUGIN_ID, "share", payload)
            }
        }
    }

    override fun call(method: String, args: JSONObject, reply: PluginReply) {
        val ctx = PluginHost.context
        when (method) {
            "startForeground" -> {
                val title = args.optString("title", "TailChat Transfer")
                val detail = args.optString("detail", "Syncing files...")
                val progress = args.optInt("progress_pct", 0)
                runCatching {
                    TransferForegroundService.start(ctx, title, detail, progress)
                }.onSuccess {
                    reply.success(null)
                }.onFailure { err ->
                    reply.failure(PluginFailure("ForegroundError", err.message ?: err.toString()))
                }
            }

            "updateForeground" -> {
                val title = args.optString("title", "TailChat Transfer")
                val detail = args.optString("detail", "Syncing files...")
                val progress = args.optInt("progress_pct", 0)
                runCatching {
                    TransferForegroundService.update(ctx, title, detail, progress)
                }.onSuccess {
                    reply.success(null)
                }.onFailure { err ->
                    reply.failure(PluginFailure("ForegroundError", err.message ?: err.toString()))
                }
            }

            "stopForeground" -> {
                TransferForegroundService.stop(ctx)
                reply.success(null)
            }

            "setActiveTransfers" -> {
                // Handled via TransferForegroundService on Android.
                reply.success(null)
            }

            "drainOutbox" -> {
                val drained = JSONArray()
                synchronized(lock) {
                    goReady = true
                    for (item in pendingShares) {
                        drained.put(item)
                    }
                    pendingShares.clear()
                }
                reply.success(drained)
            }

            else -> reply.failure(PluginFailure("UnknownMethod", method))
        }
    }
}

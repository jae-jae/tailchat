package dev.mygo

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import java.io.File
import java.io.FileOutputStream
import java.util.UUID

/**
 * Transparent Android share target receiving ACTION_SEND and ACTION_SEND_MULTIPLE
 * intents from other apps. Copies shared ContentResolver URIs into cacheDir/shared-inbox,
 * boots or notifies the Go TailChat engine, and brings MyGoActivity to the foreground.
 */
class ShareReceiverActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        handleShareIntent(intent)
        launchMainActivity()
        finish()
    }

    private fun handleShareIntent(intent: Intent?) {
        if (intent == null) return
        val action = intent.action ?: return
        if (action != Intent.ACTION_SEND && action != Intent.ACTION_SEND_MULTIPLE) return

        val sharedText = intent.getStringExtra(Intent.EXTRA_TEXT)?.trim()
        val copiedPaths = ArrayList<String>()

        when (action) {
            Intent.ACTION_SEND -> {
                getStreamUri(intent)?.let { uri ->
                    copyUriToInbox(uri)?.let { copiedPaths.add(it) }
                }
            }
            Intent.ACTION_SEND_MULTIPLE -> {
                getStreamUris(intent).forEach { uri ->
                    copyUriToInbox(uri)?.let { copiedPaths.add(it) }
                }
            }
        }

        if (sharedText.isNullOrEmpty() && copiedPaths.isEmpty()) {
            return
        }

        // Start Go headlessly if the process was cold-launched by the Share Sheet.
        HostImpl.startHeadless(applicationContext)
        MobileBridgePlugin.emitShare(sharedText, copiedPaths)
    }

    @Suppress("DEPRECATION")
    private fun getStreamUri(intent: Intent): Uri? {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
        } else {
            intent.getParcelableExtra(Intent.EXTRA_STREAM)
        }
    }

    @Suppress("DEPRECATION")
    private fun getStreamUris(intent: Intent): List<Uri> {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java) ?: emptyList()
        } else {
            intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM) ?: emptyList()
        }
    }

    private fun copyUriToInbox(uri: Uri): String? {
        val inboxDir = File(cacheDir, "shared-inbox/${UUID.randomUUID()}")
        if (!inboxDir.exists()) {
            inboxDir.mkdirs()
        }
        val fileName = sanitizeFileName(resolveDisplayName(uri) ?: "shared-file")
        val target = File(inboxDir, fileName)

        return runCatching {
            contentResolver.openInputStream(uri)?.use { input ->
                FileOutputStream(target).use { output ->
                    input.copyTo(output, bufferSize = 64 * 1024)
                }
            } ?: return null
            target.absolutePath
        }.getOrNull()
    }

    private fun resolveDisplayName(uri: Uri): String? {
        runCatching {
            contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { cursor ->
                if (cursor.moveToFirst()) {
                    val idx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    if (idx >= 0) return cursor.getString(idx)
                }
            }
        }
        return uri.lastPathSegment
    }

    private fun sanitizeFileName(raw: String): String {
        val base = raw.substringAfterLast('/').substringAfterLast('\\').trim()
        return if (base.isEmpty() || base == "." || base == "..") "shared-file" else base
    }

    private fun launchMainActivity() {
        val mainIntent = Intent(this, MyGoActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        startActivity(mainIntent)
    }
}

package dev.mygo

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder

/**
 * Keeps TailChat's Go sync engine and Tailcat P2P transfers alive in the background
 * with a dataSync foreground notification while file chunks are actively transferring.
 */
class TransferForegroundService : Service() {

    companion object {
        const val CHANNEL_ID = "tailchat_transfers"
        const val NOTIF_ID = 0x5443_01

        const val ACTION_START = "dev.tailchat.action.START_TRANSFER_FG"
        const val ACTION_UPDATE = "dev.tailchat.action.UPDATE_TRANSFER_FG"
        const val ACTION_STOP = "dev.tailchat.action.STOP_TRANSFER_FG"

        const val EXTRA_TITLE = "title"
        const val EXTRA_DETAIL = "detail"
        const val EXTRA_PROGRESS = "progress_pct"

        fun start(context: Context, title: String, detail: String, progressPct: Int) {
            val intent = Intent(context, TransferForegroundService::class.java).apply {
                action = ACTION_START
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_DETAIL, detail)
                putExtra(EXTRA_PROGRESS, progressPct)
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }

        fun update(context: Context, title: String, detail: String, progressPct: Int) {
            val intent = Intent(context, TransferForegroundService::class.java).apply {
                action = ACTION_UPDATE
                putExtra(EXTRA_TITLE, title)
                putExtra(EXTRA_DETAIL, detail)
                putExtra(EXTRA_PROGRESS, progressPct)
            }
            context.startService(intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, TransferForegroundService::class.java).apply {
                action = ACTION_STOP
            }
            runCatching { context.stopService(intent) }
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        // Ensure Go runtime is initialized even if started headlessly by the OS.
        HostImpl.startHeadless(applicationContext)

        when (intent?.action) {
            ACTION_STOP -> {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
                    stopForeground(STOP_FOREGROUND_REMOVE)
                } else {
                    @Suppress("DEPRECATION")
                    stopForeground(true)
                }
                stopSelf()
                return START_NOT_STICKY
            }
            ACTION_UPDATE -> {
                val notif = buildNotification(
                    title = intent.getStringExtra(EXTRA_TITLE) ?: "TailChat Transfer",
                    detail = intent.getStringExtra(EXTRA_DETAIL) ?: "Syncing files...",
                    progressPct = intent.getIntExtra(EXTRA_PROGRESS, 0),
                )
                val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
                nm.notify(NOTIF_ID, notif)
                return START_STICKY
            }
            else -> {
                val notif = buildNotification(
                    title = intent?.getStringExtra(EXTRA_TITLE) ?: "TailChat Transfer",
                    detail = intent?.getStringExtra(EXTRA_DETAIL) ?: "Syncing files...",
                    progressPct = intent?.getIntExtra(EXTRA_PROGRESS, 0) ?: 0,
                )
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                    startForeground(
                        NOTIF_ID,
                        notif,
                        ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
                    )
                } else {
                    startForeground(NOTIF_ID, notif)
                }
                return START_STICKY
            }
        }
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (nm.getNotificationChannel(CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            CHANNEL_ID,
            "TailChat File Transfers",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Shows progress for active peer-to-peer file transfers"
            setShowBadge(false)
        }
        nm.createNotificationChannel(channel)
    }

    private fun buildNotification(title: String, detail: String, progressPct: Int): Notification {
        val openIntent = Intent(this, MyGoActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        val piFlags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        } else {
            PendingIntent.FLAG_UPDATE_CURRENT
        }
        val pendingIntent = PendingIntent.getActivity(this, 0, openIntent, piFlags)

        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }

        val clamped = progressPct.coerceIn(0, 100)
        return builder
            .setContentTitle(title)
            .setContentText(detail)
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setContentIntent(pendingIntent)
            .setProgress(100, clamped, clamped == 0)
            .build()
    }
}

package org.lam.app

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * Keeps the connection to the host alive while the app is in the background and says,
 * quietly, when an agent finishes. The ongoing notification is low importance: no sound,
 * no pop-up, just a line in the shade. It stops when the connection is closed.
 */
class LamService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var job: Job? = null
    private val detector = FinishDetector()
    private var shown = ""
    private var nextId = 100
    private var failures = 0

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_DISCONNECT) {
            Conn.close()
            stopSelf()
            return START_NOT_STICKY
        }
        channels()
        val first = notification(Conn.label, "Connected")
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(ONGOING, first, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(ONGOING, first)
        }
        if (job == null) job = scope.launch { watch() }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private suspend fun watch() {
        while (scope.isActive) {
            val client = Conn.client
            if (client == null) { stopSelf(); return }
            val list = runCatching {
                val l = client.sessions()
                (0 until l.len().toInt()).map {
                    val s = l.get(it.toLong())
                    SessionItem(s.name, s.folder, s.command, s.active, s.unread)
                }
            }.getOrNull()
            if (list == null) {
                failures++
                val dead = failures >= 3 && !runCatching { client.alive() }.getOrDefault(false)
                if (dead) {
                    Conn.close()
                    notify(ONGOING, notification(Conn.label, "Connection lost"))
                    stopForeground(STOP_FOREGROUND_DETACH) // leave the note, drop the service
                    stopSelf()
                    return
                }
            } else {
                failures = 0
                val working = list.count { it.working && !FinishDetector.isShell(it.command) }
                val text = when {
                    working > 0 -> "$working working · ${list.size} sessions"
                    else -> "${list.size} sessions"
                }
                if (text != shown) {
                    shown = text
                    notify(ONGOING, notification(Conn.label, text))
                }
                for (s in detector.update(list, System.currentTimeMillis())) finished(s)
            }
            delay(10_000)
        }
    }

    private fun finished(s: SessionItem) {
        if (TerminalActivity.onScreen == s.name) return
        val open = Intent(this, TerminalActivity::class.java).putExtra("name", s.name).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        val tap = PendingIntent.getActivity(this, s.name.hashCode(), open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val n = NotificationCompat.Builder(this, CHANNEL_DONE)
            .setSmallIcon(R.drawable.ic_stat_lam)
            .setContentTitle("${s.name} finished")
            .setContentText(project(s.folder).ifEmpty { Conn.label })
            .setContentIntent(tap)
            .setAutoCancel(true)
            .setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
        notify(nextId++, n)
    }

    private fun notification(title: String, text: String): Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, LamService::class.java).setAction(ACTION_DISCONNECT), PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(this, CHANNEL_LINK)
            .setSmallIcon(R.drawable.ic_stat_lam)
            .setContentTitle(title.ifEmpty { "lam" })
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .setSilent(true)
            .setOnlyAlertOnce(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .addAction(0, "Disconnect", stop)
            .build()
    }

    private fun notify(id: Int, n: Notification) {
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        getSystemService(NotificationManager::class.java).notify(id, n)
    }

    private fun channels() {
        val m = getSystemService(NotificationManager::class.java)
        m.createNotificationChannel(NotificationChannel(CHANNEL_LINK, "Connection", NotificationManager.IMPORTANCE_LOW).apply {
            description = "Shown while lam is connected, so the link stays up in the background"
            setShowBadge(false)
        })
        m.createNotificationChannel(NotificationChannel(CHANNEL_DONE, "Agent finished", NotificationManager.IMPORTANCE_LOW).apply {
            description = "A quiet note when an agent has finished"
            setSound(null, null)
            enableVibration(false)
        })
    }

    companion object {
        private const val CHANNEL_LINK = "link"
        private const val CHANNEL_DONE = "done"
        private const val ONGOING = 1
        private const val ACTION_DISCONNECT = "org.lam.app.DISCONNECT"

        fun start(context: Context) {
            ContextCompat.startForegroundService(context, Intent(context, LamService::class.java))
        }
    }
}

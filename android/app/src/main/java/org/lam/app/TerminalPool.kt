package org.lam.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import com.termux.terminal.RemoteSession
import com.termux.terminal.TerminalColors
import com.termux.terminal.TerminalSession
import com.termux.terminal.TerminalSessionClient
import com.termux.terminal.TextStyle
import com.termux.view.TerminalView
import lammobile.Output
import lammobile.Terminal

/** One attached session: its emulator (scrollback) and its connection to the host. */
class Held(val name: String) {
    lateinit var remote: RemoteSession
    @Volatile var terminal: Terminal? = null
    @Volatile var attaching = false
    @Volatile var dead = false
    @Volatile var sentCols = 0
    @Volatile var sentRows = 0

    val output = object : Output {
        override fun onOutput(data: ByteArray) = remote.feed(data)
        override fun onClosed(reason: String) {
            dead = true
            terminal = null
            remote.feed("\r\n\u001b[2m[session closed: $reason]\u001b[0m\r\n".toByteArray())
        }
    }

    /** Attaches on the first size and resizes afterwards. Call from any thread. */
    @Synchronized
    fun sized(cols: Int, rows: Int) {
        val client = Conn.client ?: return
        val t = terminal
        if (t == null) {
            if (attaching || dead) return
            attaching = true
            Thread {
                try {
                    val attached = client.attach(name, cols.toLong(), rows.toLong(), output)
                    synchronized(this) {
                        sentCols = cols
                        sentRows = rows
                        terminal = attached
                    }
                } catch (e: Exception) {
                    dead = true
                    remote.feed("\r\n[attach failed: ${e.message}]\r\n".toByteArray())
                } finally {
                    attaching = false
                }
            }.start()
        } else if (cols != sentCols || rows != sentRows) {
            sentCols = cols
            sentRows = rows
            Thread { runCatching { t.resize(cols.toLong(), rows.toLong()) } }.start()
        }
    }

    fun close() {
        val t = terminal
        terminal = null
        remote.close()
        if (t != null) Thread { runCatching { t.close() } }.start()
    }
}

/**
 * Attached sessions, kept for the life of the process so that switching back is
 * instant with scrollback intact. At most [MAX]; the least recently used goes first.
 */
object Pool {
    const val MAX = 8

    private val held = LinkedHashMap<String, Held>(16, 0.75f, true)
    private lateinit var appContext: Context

    /** The view currently showing a session, if any. */
    @Volatile var view: TerminalView? = null

    fun init(context: Context) {
        appContext = context.applicationContext
        // lam's dark palette instead of Termux's black and white.
        val scheme = TerminalColors.COLOR_SCHEME.mDefaultColors
        scheme[TextStyle.COLOR_INDEX_BACKGROUND] = Theme.bg
        scheme[TextStyle.COLOR_INDEX_FOREGROUND] = Theme.text
        scheme[TextStyle.COLOR_INDEX_CURSOR] = Theme.accent
    }

    @Synchronized
    fun get(name: String): Held {
        held[name]?.let { h ->
            if (!h.dead) return h
            h.close()
            held.remove(name)
        }
        val h = Held(name)
        h.remote = RemoteSession(client, { data, off, n ->
            val t = h.terminal
            if (t != null) runCatching { t.write(data.copyOfRange(off, off + n)) }
        }, 80, 24, 5000)
        held[name] = h
        while (held.size > MAX) {
            val eldest = held.keys.first { it != name }
            held.remove(eldest)?.close()
        }
        return h
    }

    @Synchronized
    fun closeAll() {
        held.values.forEach { it.close() }
        held.clear()
    }

    private val client = object : TerminalSessionClient {
        override fun onTextChanged(changedSession: TerminalSession) {
            view?.let { if (it.currentSession === changedSession) it.onScreenUpdated() }
        }

        override fun onTitleChanged(changedSession: TerminalSession) {}
        override fun onSessionFinished(finishedSession: TerminalSession) {}
        override fun onCopyTextToClipboard(session: TerminalSession, text: String?) {
            (appContext.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager)
                .setPrimaryClip(ClipData.newPlainText("lam", text ?: ""))
        }

        override fun onPasteTextFromClipboard(session: TerminalSession?) {
            val cm = appContext.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
            val text = cm.primaryClip?.getItemAt(0)?.coerceToText(appContext)?.toString()
            if (!text.isNullOrEmpty()) session?.emulator?.paste(text)
        }

        override fun onBell(session: TerminalSession) {}
        override fun onColorsChanged(session: TerminalSession) {}
        override fun onTerminalCursorStateChange(state: Boolean) {}
        override fun getTerminalCursorStyle(): Int? = null
        override fun logError(tag: String?, message: String?) {}
        override fun logWarn(tag: String?, message: String?) {}
        override fun logInfo(tag: String?, message: String?) {}
        override fun logDebug(tag: String?, message: String?) {}
        override fun logVerbose(tag: String?, message: String?) {}
        override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) {}
        override fun logStackTrace(tag: String?, e: Exception?) {}
    }
}

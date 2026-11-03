package org.lam.app

import android.content.Context
import android.content.Intent
import android.graphics.Typeface
import android.os.Bundle
import android.util.TypedValue
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.inputmethod.InputMethodManager
import android.widget.FrameLayout
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.DrawerValue
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalDrawerSheet
import androidx.compose.material3.ModalNavigationDrawer
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberDrawerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.viewmodel.compose.viewModel
import com.termux.terminal.TerminalSession
import com.termux.view.TerminalView
import com.termux.view.TerminalViewClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Full-screen terminal with an extra-keys row above the soft keyboard and a drawer
 * (opened with the menu button; the left edge is left to the system back gesture) listing the host's sessions. Sessions
 * stay attached in [Pool], so switching is instant. The chrome is Compose; the
 * terminal itself is Termux's view.
 */
class TerminalActivity : ComponentActivity() {
    companion object {
        /** The session showing right now, or "": a note that it finished would only repeat the screen. */
        @Volatile var onScreen = ""
    }

    private lateinit var view: TerminalView
    private lateinit var ime: LamTerminalView
    private var current: Held? = null
    private var currentName by mutableStateOf("")
    private var pending by mutableStateOf("")

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val name = intent.getStringExtra("name")
        if (name == null || Conn.client == null) { finish(); return }
        enableEdgeToEdge()
        Pool.init(this)

        view = TerminalView(this, null).apply {
            setTerminalViewClient(viewClient)
            setTextSize(TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_SP, 13f, resources.displayMetrics).toInt())
            setTypeface(Typeface.MONOSPACE)
            isFocusable = false // the keyboard view below holds focus
        }
        // Scrolling back through the history puts the keyboard away, so the screen is
        // free to read; a tap on the terminal brings it back.
        var downY = 0f
        var hidden = false
        val slop = 24 * resources.displayMetrics.density
        view.setOnTouchListener { _, e ->
            when (e.actionMasked) {
                MotionEvent.ACTION_DOWN -> { downY = e.y; hidden = false }
                MotionEvent.ACTION_MOVE -> if (!hidden && e.y - downY > slop) { hidden = true; hideKeyboard() }
            }
            false // the terminal still scrolls
        }
        ime = LamTerminalView(this, view).apply { onPending = { text -> pending = text } }
        Pool.view = view

        setContent { LamTheme { Screen() } }

        switchTo(name) // the keyboard stays away until the person taps the prompt or the ⌨ button
    }

    @Composable
    private fun Screen() {
        val drawer = rememberDrawerState(DrawerValue.Closed)
        val scope = rememberCoroutineScope()
        val vm: SessionsViewModel = viewModel()
        val state by vm.state.collectAsState()
        val open = drawer.currentValue == DrawerValue.Open

        // The list is read while the drawer is showing; a dead link ends the screen.
        LaunchedEffect(open) {
            if (!open) return@LaunchedEffect
            hideKeyboard()
            while (true) {
                vm.refresh()
                delay(2000)
                val alive = withContext(Dispatchers.IO) { Conn.client?.alive() == true }
                if (!alive) { linkLost(); return@LaunchedEffect }
            }
        }
        BackHandler(open) { scope.launch { drawer.close() } }
        // Disconnect from the notification closes the link under an open terminal.
        LaunchedEffect(Unit) {
            while (true) {
                delay(1000)
                if (Conn.client == null) { Pool.closeAll(); finish(); return@LaunchedEffect }
            }
        }

        ModalNavigationDrawer(
            drawerState = drawer,
            gesturesEnabled = open, // a closed drawer must not take the terminal's swipes
            drawerContent = {
                ModalDrawerSheet(drawerContainerColor = MaterialTheme.colorScheme.background) {
                    Text(
                        Conn.label,
                        modifier = Modifier.padding(start = 20.dp, top = 24.dp, bottom = 8.dp),
                        fontSize = 20.sp,
                        fontWeight = FontWeight.Bold,
                        color = MaterialTheme.colorScheme.primary,
                    )
                    LazyColumn(
                        contentPadding = androidx.compose.foundation.layout.PaddingValues(12.dp),
                        verticalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(6.dp),
                    ) {
                        items(state.sessions, key = { it.name }) { s ->
                            SessionRow(s, selected = s.name == currentName) {
                                scope.launch { drawer.close() }
                                if (s.name != currentName) switchTo(s.name)
                            }
                        }
                    }
                }
            },
        ) {
            Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background).safeDrawingPadding()) {
                TopBar { scope.launch { drawer.open() } }
                Box(Modifier.weight(1f)) {
                    AndroidView(
                        modifier = Modifier.fillMaxSize(),
                        factory = { context ->
                            FrameLayout(context).apply {
                                addView(view, FrameLayout.LayoutParams(-1, -1))
                                addView(ime, FrameLayout.LayoutParams(1, 1))
                            }
                        },
                    )
                    if (pending.isNotEmpty()) {
                        Text(
                            pending.takeLast(300),
                            modifier = Modifier
                                .align(Alignment.BottomStart)
                                .fillMaxWidth()
                                .background(MaterialTheme.colorScheme.surfaceContainerHigh)
                                .padding(horizontal = 10.dp, vertical = 4.dp),
                            color = MaterialTheme.colorScheme.primary,
                            fontFamily = FontFamily.Monospace,
                            fontSize = 15.sp,
                            maxLines = 3,
                        )
                    }
                }
                ExtraKeys()
            }
        }
    }

    /** The session's name, the keyboard, and the session list: it opens from here, not from an edge swipe (that is the back gesture). */
    @Composable
    private fun TopBar(openSessions: () -> Unit) {
        Surface(color = MaterialTheme.colorScheme.surfaceContainer, modifier = Modifier.fillMaxWidth().height(40.dp)) {
            Row(Modifier.fillMaxSize(), verticalAlignment = Alignment.CenterVertically) {
                Text(
                    currentName,
                    modifier = Modifier.weight(1f).padding(start = 14.dp),
                    color = MaterialTheme.colorScheme.onSurface,
                    fontSize = 15.sp,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                )
                TextButton(onClick = { showKeyboard() }) { Text("⌨", fontSize = 18.sp, color = MaterialTheme.colorScheme.onSurface) }
                IconButton(onClick = openSessions) {
                    Icon(Icons.Filled.Menu, contentDescription = "Sessions", tint = MaterialTheme.colorScheme.onSurface)
                }
            }
        }
    }

    @Composable
    private fun ExtraKeys() {
        Surface(color = MaterialTheme.colorScheme.surfaceContainer, modifier = Modifier.fillMaxWidth().height(44.dp)) {
            Row(Modifier.fillMaxSize(), verticalAlignment = Alignment.CenterVertically) {
                @Composable
                fun Key(text: String, action: () -> Unit) {
                    TextButton(
                        onClick = action,
                        modifier = Modifier.weight(1f).fillMaxHeight(),
                        contentPadding = androidx.compose.foundation.layout.PaddingValues(0.dp),
                        colors = androidx.compose.material3.ButtonDefaults.textButtonColors(contentColor = MaterialTheme.colorScheme.onSurface),
                        shape = androidx.compose.ui.graphics.RectangleShape,
                    ) { Text(text, fontSize = 14.sp) }
                }
                Key("Ctrl+C") { ime.clearPending(); send(byteArrayOf(0x03)) }
                Key("Esc") { send(byteArrayOf(0x1b)) }
                Key("←") { key(KeyEvent.KEYCODE_DPAD_LEFT) }
                Key("↓") { key(KeyEvent.KEYCODE_DPAD_DOWN) }
                Key("↑") { key(KeyEvent.KEYCODE_DPAD_UP) }
                Key("→") { key(KeyEvent.KEYCODE_DPAD_RIGHT) }
            }
        }
    }

    private fun switchTo(name: String) {
        ime.clearPending() // what was typed was for the session just left
        val h = Pool.get(name)
        current = h
        currentName = name
        view.attachSession(h.remote.session)
        view.onScreenUpdated()
        // A session that was already attached shows its own screen; the size may differ.
        view.post { emulatorSized() }
    }

    private fun showKeyboard() {
        if (!::ime.isInitialized) return
        ime.requestFocus()
        (getSystemService(Context.INPUT_METHOD_SERVICE) as InputMethodManager).showSoftInput(ime, 0)
    }

    private fun hideKeyboard() {
        (getSystemService(Context.INPUT_METHOD_SERVICE) as InputMethodManager).hideSoftInputFromWindow(view.windowToken, 0)
    }

    private fun send(bytes: ByteArray) {
        current?.remote?.session?.write(bytes, 0, bytes.size)
    }

    private fun key(keyCode: Int) {
        view.handleKeyCode(keyCode, 0)
    }

    /** The view settled on a size for the current session: attach it or resize it. */
    private fun emulatorSized() {
        // The keyboard sliding in resizes the view every frame; tell the host once it settles.
        view.removeCallbacks(sizeReport)
        view.postDelayed(sizeReport, 150)
    }

    private val sizeReport = Runnable {
        val h = current ?: return@Runnable
        val e = h.remote.session.emulator ?: return@Runnable
        h.sized(e.mColumns, e.mRows)
    }

    private fun linkLost() {
        Conn.close()
        Pool.closeAll()
        finish()
    }

    private val viewClient = object : TerminalViewClient {
        override fun onScale(scale: Float): Float = 1f
        /** A tap brings the keyboard back only where text is typed: on the cursor's line or just beside it. */
        override fun onSingleTapUp(e: MotionEvent?) {
            val m = view.mEmulator ?: return
            val tap = e ?: return
            val row = view.getColumnAndRow(tap, false)[1]
            if (kotlin.math.abs(row - (m.cursorRow - view.topRow)) <= 2) showKeyboard()
        }
        override fun shouldBackButtonBeMappedToEscape() = false
        override fun shouldEnforceCharBasedInput() = false
        override fun shouldUseCtrlSpaceWorkaround() = false
        override fun isTerminalViewSelected() = true
        override fun copyModeChanged(copyMode: Boolean) {}
        override fun onKeyDown(keyCode: Int, e: KeyEvent?, session: TerminalSession?) = false
        override fun onKeyUp(keyCode: Int, e: KeyEvent?) = false
        override fun onLongPress(event: MotionEvent?) = false
        override fun readControlKey() = false
        override fun readAltKey() = false
        override fun readShiftKey() = false
        override fun readFnKey() = false
        override fun onCodePoint(codePoint: Int, ctrlDown: Boolean, session: TerminalSession?) = false

        override fun onEmulatorSet() = emulatorSized()
        override fun logError(tag: String?, message: String?) {}
        override fun logWarn(tag: String?, message: String?) {}
        override fun logInfo(tag: String?, message: String?) {}
        override fun logDebug(tag: String?, message: String?) {}
        override fun logVerbose(tag: String?, message: String?) {}
        override fun logStackTraceWithMessage(tag: String?, message: String?, e: Exception?) {}
        override fun logStackTrace(tag: String?, e: Exception?) {}
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        intent.getStringExtra("name")?.let { if (it != currentName) switchTo(it) }
    }

    override fun onStart() {
        super.onStart()
        onScreen = currentName
    }

    override fun onStop() {
        onScreen = ""
        super.onStop()
    }

    override fun onDestroy() {
        if (Pool.view === view) Pool.view = null // sessions stay attached in the pool
        super.onDestroy()
    }
}

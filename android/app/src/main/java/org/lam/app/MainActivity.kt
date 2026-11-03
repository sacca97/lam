package org.lam.app

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.material3.Surface
import androidx.compose.material3.MaterialTheme
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController

/** The one screen host: connect, then the sessions of the host. Terminals open in their own activity. */
class MainActivity : ComponentActivity() {
    private val connect: ConnectViewModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        askForNotifications()
        setContent {
            LamTheme {
                Surface(color = MaterialTheme.colorScheme.background) { Root(connect) }
            }
        }
    }

    /** Android 13 and later ask before a notification may show; once is enough. */
    private fun askForNotifications() {
        if (Build.VERSION.SDK_INT < 33) return
        val prefs = getSharedPreferences("lam", MODE_PRIVATE)
        if (prefs.getBoolean("askedNotifications", false)) return
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) return
        prefs.edit().putBoolean("askedNotifications", true).apply()
        registerForActivityResult(ActivityResultContracts.RequestPermission()) {}.launch(Manifest.permission.POST_NOTIFICATIONS)
    }
}

@Composable
private fun Root(connect: ConnectViewModel) {
    val nav = rememberNavController()
    val connected by connect.connected.collectAsState()
    LaunchedEffect(connected) {
        val onSessions = nav.currentDestination?.route == "sessions"
        if (connected && !onSessions) nav.navigate("sessions")
        if (!connected && onSessions) nav.popBackStack("connect", false)
    }
    NavHost(nav, startDestination = if (Conn.client != null) "sessions" else "connect") {
        composable("connect") { ConnectScreen(connect) }
        composable("sessions") { SessionsScreen(onDisconnect = { Conn.close(); connect.disconnected() }) }
    }
}

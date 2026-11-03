package org.lam.app

import android.content.Intent
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import kotlinx.coroutines.launch
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.FilterChip
import androidx.compose.material3.AlertDialog
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive

/** What an agent is, as people call it. */
internal fun provider(command: String): String {
    val kind = command.trim().substringBefore(' ').substringAfterLast('/').lowercase()
    return when {
        kind.startsWith("claude") -> "Claude Code"
        kind.startsWith("codex") -> "Codex"
        kind.startsWith("opencode") -> "opencode"
        kind.startsWith("gemini") -> "Gemini"
        kind == "agy" -> "Antigravity"
        kind == "pi" -> "pi"
        kind.isEmpty() || kind in setOf("sh", "bash", "zsh", "fish") -> "Shell"
        else -> kind
    }
}

internal fun project(folder: String): String =
    folder.trimEnd('/').substringAfterLast('/').ifEmpty { folder.ifEmpty { "other" } }

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionsScreen(onDisconnect: () -> Unit, vm: SessionsViewModel = viewModel()) {
    val state by vm.state.collectAsState()
    val context = LocalContext.current
    val lifecycle = LocalLifecycleOwner.current.lifecycle

    // Poll while the screen is showing; stop when it is not.
    LaunchedEffect(lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            if (Conn.client == null) { onDisconnect(); return@repeatOnLifecycle }
            while (isActive) {
                vm.refresh()
                delay(3000)
            }
        }
    }

    var creating by remember { mutableStateOf(false) }
    if (creating) NewSessionDialog(vm, onDismiss = { creating = false }) { name ->
        creating = false
        vm.refresh()
        context.startActivity(Intent(context, TerminalActivity::class.java).putExtra("name", name))
    }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        floatingActionButton = {
            ExtendedFloatingActionButton(onClick = { creating = true }, containerColor = MaterialTheme.colorScheme.primary) {
                Text("+ New session", fontWeight = FontWeight.SemiBold)
            }
        },
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Text(Conn.label, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.primary)
                        val n = state.sessions.size
                        if (state.loaded) Text(
                            if (n == 0) "No sessions" else "$n session${if (n == 1) "" else "s"}",
                            fontSize = 12.sp,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                },
                actions = { TextButton(onClick = onDisconnect) { Text("Disconnect") } },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.background),
            )
        },
    ) { inner ->
        Box(Modifier.fillMaxSize().padding(inner)) {
            val groups = state.sessions.groupBy { it.folder }.toList().sortedBy { project(it.first) }
            LazyColumn(contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 88.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                groups.forEach { (folder, items) ->
                    item(key = "h:$folder") {
                        Text(
                            project(folder).uppercase(),
                            modifier = Modifier.padding(start = 4.dp, top = 12.dp, bottom = 2.dp),
                            fontSize = 12.sp,
                            fontWeight = FontWeight.SemiBold,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                    items(items, key = { it.name }) { s ->
                        SessionRow(s) {
                            context.startActivity(Intent(context, TerminalActivity::class.java).putExtra("name", s.name))
                        }
                    }
                }
            }
            if (state.error != null) {
                Surface(
                    modifier = Modifier.align(Alignment.BottomCenter).padding(16.dp),
                    shape = RoundedCornerShape(12.dp),
                    color = MaterialTheme.colorScheme.surfaceContainerHigh,
                ) {
                    Row(Modifier.padding(start = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(state.error ?: "", color = MaterialTheme.colorScheme.error, modifier = Modifier.weight(1f), fontSize = 13.sp)
                        TextButton(onClick = vm::refresh) { Text("Retry") }
                    }
                }
            }
        }
    }
}

@Composable
internal fun SessionRow(s: SessionItem, selected: Boolean = false, onClick: () -> Unit) {
    val scheme = MaterialTheme.colorScheme
    val (status, color) = when {
        s.working -> "working" to scheme.primary
        s.unread -> "needs you" to scheme.tertiary
        else -> "" to scheme.onSurfaceVariant
    }
    Surface(
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(14.dp)).clickable(onClick = onClick),
        shape = RoundedCornerShape(14.dp),
        color = if (selected) scheme.surfaceContainerHigh else scheme.surfaceContainer,
    ) {
        Row(Modifier.padding(horizontal = 16.dp, vertical = 12.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(
                Modifier.size(10.dp).clip(CircleShape)
                    .background(if (s.working || s.unread) color else Color.Transparent)
                    .let { if (s.working || s.unread) it else it.background(scheme.outlineVariant) },
            )
            Column(Modifier.padding(start = 14.dp)) {
                Text(s.name, fontSize = 16.sp, fontWeight = FontWeight.Medium, color = if (selected) scheme.primary else scheme.onSurface)
                Row {
                    Text(provider(s.command), fontSize = 12.sp, color = scheme.onSurfaceVariant)
                    if (status.isNotEmpty()) Text("  ·  $status", fontSize = 12.sp, color = color, fontWeight = FontWeight.Medium)
                }
            }
        }
    }
}

internal fun launcherLabel(launcher: String) = when (launcher) {
    "terminal" -> "Terminal"
    "claude" -> "Claude Code"
    "codex" -> "Codex"
    "opencode" -> "opencode"
    "pi" -> "pi"
    "agy" -> "Antigravity"
    else -> launcher
}

/** Asks for an agent and a folder on the host, starts the session there and reports its name. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun NewSessionDialog(vm: SessionsViewModel, onDismiss: () -> Unit, onCreated: (String) -> Unit) {
    val prefs = LocalContext.current.getSharedPreferences("lam", android.content.Context.MODE_PRIVATE)
    var choices by remember { mutableStateOf<List<String>?>(null) }
    var home by remember { mutableStateOf("") }
    var launcher by remember { mutableStateOf("") }
    var dir by remember { mutableStateOf("") }
    var suggestions by remember { mutableStateOf<List<String>>(emptyList()) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    LaunchedEffect(Unit) {
        vm.newSessionChoices().onSuccess { (agents, h) ->
            val all = agents + "terminal"
            choices = all
            home = h
            launcher = prefs.getString("lastLauncher", null)?.takeIf { it in all } ?: all.first()
        }.onFailure { error = it.message ?: "could not ask the host" }
    }
    // Suggestions follow the typing, a moment after it stops.
    LaunchedEffect(dir, home) {
        if (home.isEmpty() || dir.isEmpty()) { suggestions = emptyList(); return@LaunchedEffect }
        delay(200)
        suggestions = vm.completeDir(dir, home).take(6)
    }

    AlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        title = { Text("New session") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                val list = choices
                if (list == null) {
                    Text(error ?: "Asking the host…", color = if (error != null) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant)
                } else {
                    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        list.forEach { l ->
                            FilterChip(selected = l == launcher, onClick = { launcher = l }, label = { Text(launcherLabel(l)) })
                        }
                    }
                    OutlinedTextField(
                        value = dir,
                        onValueChange = { dir = it; error = null },
                        label = { Text("Folder") },
                        placeholder = { Text(home) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    suggestions.forEach { d ->
                        Text(
                            d,
                            modifier = Modifier.fillMaxWidth().clickable { dir = d }.padding(vertical = 6.dp),
                            fontSize = 14.sp,
                            color = MaterialTheme.colorScheme.primary,
                        )
                    }
                    if (error != null) Text(error ?: "", color = MaterialTheme.colorScheme.error, fontSize = 13.sp)
                }
            }
        },
        confirmButton = {
            TextButton(enabled = choices != null && !busy, onClick = {
                busy = true
                scope.launch {
                    vm.createSession(launcher, dir.trim()).onSuccess {
                        if (launcher != "terminal") prefs.edit().putString("lastLauncher", launcher).apply()
                        onCreated(it)
                    }.onFailure { error = it.message ?: "could not start the session" }
                    busy = false
                }
            }) { Text(if (busy) "Starting…" else "Start") }
        },
        dismissButton = { TextButton(enabled = !busy, onClick = onDismiss) { Text("Cancel") } },
    )
}

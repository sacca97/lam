package org.lam.app

import android.content.Intent
import android.net.Uri

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SuggestionChip
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.material3.Checkbox
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

@Composable
fun ConnectScreen(vm: ConnectViewModel) {
    val state by vm.state.collectAsState()
    val clipboard = LocalClipboardManager.current
    var copied by remember { mutableStateOf(false) }

    Column(
        Modifier
            .fillMaxSize()
            .safeDrawingPadding()
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 20.dp, vertical = 24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        state.update?.let { update ->
            val context = LocalContext.current
            Surface(shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.primaryContainer) {
                Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, top = 4.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("lam ${update.tag} is available", Modifier.weight(1f), color = MaterialTheme.colorScheme.onPrimaryContainer)
                    TextButton(onClick = { vm.dismissUpdate() }) { Text("Dismiss") }
                    TextButton(onClick = {
                        runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(update.apkUrl))) }
                    }) { Text("Update") }
                }
            }
        }

        Column {
            Text("lam", fontSize = 32.sp, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.primary)
            Text("Sessions on your hosts, from your phone", color = MaterialTheme.colorScheme.onSurfaceVariant)
        }

        OutlinedTextField(
            value = state.host,
            onValueChange = vm::setHost,
            modifier = Modifier.fillMaxWidth(),
            label = { Text("Host") },
            placeholder = { Text("user@host[:port]") },
            singleLine = true,
            enabled = !state.busy,
            isError = state.error != null,
            supportingText = state.error?.let { { Text(it) } },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Go),
            keyboardActions = KeyboardActions(onGo = { vm.connect() }),
        )

        if (state.recent.isNotEmpty()) {
            LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                items(state.recent) { host ->
                    SuggestionChip(onClick = { vm.connect(host) }, label = { Text(host) }, enabled = !state.busy)
                }
            }
        }

        Button(onClick = { vm.connect() }, enabled = !state.busy && state.host.isNotBlank(), modifier = Modifier.fillMaxWidth().height(48.dp)) {
            if (state.busy) CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp, color = MaterialTheme.colorScheme.onPrimary)
            else Text("Connect")
        }

        Surface(shape = RoundedCornerShape(16.dp), color = MaterialTheme.colorScheme.surfaceContainer) {
            Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("This phone's key", fontWeight = FontWeight.SemiBold)
                Text(
                    "Logging in uses this key, no password. Add it to ~/.ssh/authorized_keys on the host once.",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    fontSize = 13.sp,
                )
                Text(state.deviceKey.ifEmpty { "…" }, fontFamily = FontFamily.Monospace, fontSize = 11.sp)
                TextButton(
                    onClick = { clipboard.setText(AnnotatedString(state.deviceKey)); copied = true },
                    enabled = state.deviceKey.startsWith("ssh-"),
                ) { Text(if (copied) "Copied" else "Copy key") }
            }
        }

        if (state.version.isNotEmpty()) {
            Text("lam v${state.version}", Modifier.fillMaxWidth(), fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        }
    }

    when (val q = state.question) {
        is Question.TrustHost -> AlertDialog(
            onDismissRequest = {},
            title = { Text("Trust this host?") },
            text = { Text("${q.host}\n${q.keyType}\n${q.fingerprint}", fontFamily = FontFamily.Monospace, fontSize = 13.sp) },
            confirmButton = { TextButton(onClick = { vm.answer(q, true) }) { Text("Trust") } },
            dismissButton = { TextButton(onClick = { vm.answer(q, false) }) { Text("Cancel") } },
        )
        is Question.Password -> {
            var password by remember(q) { mutableStateOf("") }
            var remember by remember(q) { mutableStateOf(false) }
            AlertDialog(
                onDismissRequest = {},
                title = { Text("Password for ${q.user}@${q.host}") },
                text = {
                    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text("The phone's key was not accepted.", color = MaterialTheme.colorScheme.onSurfaceVariant, fontSize = 13.sp)
                        OutlinedTextField(
                            value = password,
                            onValueChange = { password = it },
                            singleLine = true,
                            label = { Text("Password") },
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, imeAction = ImeAction.Go),
                            keyboardActions = KeyboardActions(onGo = { vm.answerPassword(q, password, remember) }),
                        )
                        Row(
                            Modifier.fillMaxWidth().clickable { remember = !remember },
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Checkbox(checked = remember, onCheckedChange = { remember = it })
                            Text("Remember on this phone", fontSize = 14.sp)
                        }
                    }
                },
                confirmButton = { TextButton(onClick = { vm.answerPassword(q, password, remember) }) { Text("Log in") } },
                dismissButton = { TextButton(onClick = { vm.answer(q, "") }) { Text("Cancel") } },
            )
        }
        null -> {}
    }
}

package org.lam.app

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import lammobile.HostPrompt
import lammobile.Lammobile

/** A question the connection asks the person; answering completes [reply]. */
sealed interface Question {
    data class TrustHost(val host: String, val keyType: String, val fingerprint: String, val reply: CompletableDeferred<Boolean>) : Question
    data class Password(val user: String, val host: String, val reply: CompletableDeferred<String>) : Question
}

data class ConnectState(
    val host: String = "",
    val recent: List<String> = emptyList(),
    val deviceKey: String = "",
    val saved: List<String> = emptyList(), // user@host with a remembered password
    val busy: Boolean = false,
    val error: String? = null,
    val question: Question? = null,
    val version: String = "",
    val update: UpdateInfo? = null,
)

class ConnectViewModel(app: Application) : AndroidViewModel(app) {
    private val prefs = app.getSharedPreferences("lam", Application.MODE_PRIVATE)
    private val vault = Vault(app)
    private val _state = MutableStateFlow(
        ConnectState(host = prefs.getString("host", "") ?: "", recent = recentHosts(), version = versionName(), saved = vault.accounts())
    )
    val state: StateFlow<ConnectState> = _state.asStateFlow()

    /** Set when a connection is open and the sessions should show. */
    private val _connected = MutableStateFlow(Conn.client != null)
    val connected: StateFlow<Boolean> = _connected.asStateFlow()

    init {
        checkForUpdate()
        viewModelScope.launch(Dispatchers.IO) {
            val key = runCatching {
                Lammobile.setDataDir(getApplication<Application>().filesDir.absolutePath)
                Lammobile.publicKey()
            }.getOrElse { "error: ${it.message}" }
            _state.update { it.copy(deviceKey = key) }
        }
    }

    private fun versionName(): String = runCatching {
        getApplication<Application>().packageManager.getPackageInfo(getApplication<Application>().packageName, 0).versionName
    }.getOrNull() ?: ""

    private fun debuggable() =
        getApplication<Application>().applicationInfo.flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE != 0

    /** At most one request a day; in between the last answer is reused. Failures are silent. */
    private fun checkForUpdate() {
        val current = _state.value.version
        viewModelScope.launch(Dispatchers.IO) {
            val now = System.currentTimeMillis()
            var info = prefs.getString("updateTag", null)?.let { tag ->
                prefs.getString("updateUrl", null)?.let { UpdateInfo(tag, it) }
            }
            // A debug build can be told a release exists, to see the banner.
            val fake = if (debuggable()) prefs.getString("debugUpdateTag", null) else null
            if (fake != null) {
                info = UpdateInfo(fake, "https://github.com/sacca97/lam/releases/latest")
            } else if (now - prefs.getLong("updateCheckedAt", 0) > 24 * 60 * 60 * 1000) {
                val fetched = fetchLatestRelease()
                if (fetched != null) {
                    info = fetched
                    prefs.edit().putLong("updateCheckedAt", now).putString("updateTag", fetched.tag).putString("updateUrl", fetched.apkUrl).apply()
                } else {
                    prefs.edit().putLong("updateCheckedAt", now - 23 * 60 * 60 * 1000).apply() // unreachable or rate-limited: try again in an hour
                }
            }
            val show = info != null && isNewer(info.tag, current) && prefs.getString("updateDismissed", null) != info.tag
            _state.update { it.copy(update = if (show) info else null) }
        }
    }

    fun dismissUpdate() {
        val tag = _state.value.update?.tag ?: return
        prefs.edit().putString("updateDismissed", tag).apply()
        _state.update { it.copy(update = null) }
    }

    private fun recentHosts(): List<String> =
        (prefs.getString("recent", "") ?: "").split('\n').filter { it.isNotBlank() }

    fun setHost(host: String) = _state.update { it.copy(host = host, error = null) }

    fun connect(target: String = _state.value.host) {
        val host = target.trim()
        if (host.isEmpty() || _state.value.busy) return
        val recent = (listOf(host) + recentHosts().filter { it != host }).take(6)
        prefs.edit().putString("host", host).putString("recent", recent.joinToString("\n")).apply()
        _state.update { it.copy(host = host, recent = recent, busy = true, error = null) }
        open { Lammobile.connect(host, Prompt()) to host }
    }

    private fun open(dial: () -> Pair<lammobile.Client, String>) {
        viewModelScope.launch {
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    Lammobile.setDataDir(getApplication<Application>().filesDir.absolutePath)
                    dial()
                }
            }
            result.onSuccess { (client, label) ->
                Conn.close()
                Conn.client = client
                Conn.label = label
                LamService.start(getApplication())
                _state.update { it.copy(busy = false) }
                _connected.value = true
            }.onFailure { e ->
                _state.update { it.copy(busy = false, error = e.message ?: e.toString()) }
            }
        }
    }

    fun forgetPassword(account: String) {
        vault.forget(account)
        _state.update { it.copy(saved = vault.accounts()) }
    }

    /** The password typed in the dialog; kept in the vault when the person asked for that. */
    fun answerPassword(q: Question.Password, password: String, remember: Boolean) {
        if (remember && password.isNotEmpty()) {
            vault.put(Vault.account(q.user, q.host), password)
            _state.update { it.copy(saved = vault.accounts()) }
        }
        answer(q, password)
    }

    fun answer(question: Question, value: Any) {
        _state.update { it.copy(question = null) }
        when (question) {
            is Question.TrustHost -> question.reply.complete(value as Boolean)
            is Question.Password -> question.reply.complete(value as String)
        }
    }

    fun disconnected() {
        _connected.value = false
    }

    /** The connecting thread blocks here until the person answers. */
    private inner class Prompt : HostPrompt {
        override fun confirmHost(host: String, keyType: String, fingerprint: String): Boolean {
            val reply = CompletableDeferred<Boolean>()
            _state.update { it.copy(question = Question.TrustHost(host, keyType, fingerprint, reply)) }
            return runBlocking { reply.await() }
        }

        private val triedStored = HashSet<String>()

        override fun requestPassword(user: String, host: String): String {
            // A remembered password goes first, without a dialog. Asked again for the same
            // account means the host refused it: forget it and ask the person.
            val account = Vault.account(user, host)
            vault.get(account)?.let { stored ->
                if (triedStored.add(account)) return stored
                vault.forget(account)
            }
            val reply = CompletableDeferred<String>()
            _state.update { it.copy(question = Question.Password(user, host, reply)) }
            return runBlocking { reply.await() }
        }
    }

    override fun onCleared() {
        // A connection waiting on an answer must not hang forever.
        when (val q = _state.value.question) {
            is Question.TrustHost -> q.reply.complete(false)
            is Question.Password -> q.reply.complete("")
            null -> {}
        }
    }
}

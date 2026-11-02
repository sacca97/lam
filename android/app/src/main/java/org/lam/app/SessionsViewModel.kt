package org.lam.app

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

data class SessionItem(
    val name: String,
    val folder: String,
    val command: String,
    val working: Boolean,
    val unread: Boolean,
)

data class SessionsState(
    val sessions: List<SessionItem> = emptyList(),
    val loaded: Boolean = false,
    val error: String? = null,
)

class SessionsViewModel : ViewModel() {
    private val _state = MutableStateFlow(SessionsState())
    val state: StateFlow<SessionsState> = _state.asStateFlow()
    private var loading = false

    fun refresh() {
        val client = Conn.client ?: return
        if (loading) return
        loading = true
        viewModelScope.launch {
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    val l = client.sessions()
                    (0 until l.len().toInt()).map {
                        val s = l.get(it.toLong())
                        SessionItem(s.name, s.folder, s.command, s.active, s.unread)
                    }
                }
            }
            loading = false
            result.onSuccess { list ->
                _state.value = SessionsState(sessions = list, loaded = true)
            }.onFailure { e ->
                _state.update { it.copy(error = e.message ?: "connection lost") }
            }
        }
    }
}

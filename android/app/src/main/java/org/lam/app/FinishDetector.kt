package org.lam.app

/**
 * Turns the sessions listed over time into "this agent finished" events, like the
 * desktop window does: an agent counts as done when it worked for at least
 * [minWorkMs] and then stayed quiet for [quietMs]. A pause between two steps of one
 * task is shorter than that, and a redraw after attaching is shorter than the work.
 * Shells have no task that ends, so they are left out.
 */
class FinishDetector(
    private val minWorkMs: Long = 15_000,
    private val quietMs: Long = 25_000,
    private val maxGapMs: Long = 45_000,
) {
    private val activeSince = HashMap<String, Long>()
    private val quietSince = HashMap<String, Long>()
    private var last = 0L

    fun update(sessions: List<SessionItem>, now: Long): List<SessionItem> {
        val finished = ArrayList<SessionItem>()
        // The phone slept or the link stalled: what was recorded says nothing about now.
        if (last != 0L && now - last > maxGapMs) {
            activeSince.clear()
            quietSince.clear()
        }
        last = now
        val seen = HashSet<String>()
        for (s in sessions) {
            seen += s.name
            if (isShell(s.command)) continue
            val since = activeSince[s.name]
            if (s.working) {
                quietSince.remove(s.name)
                if (since == null) activeSince[s.name] = now
            } else if (since != null) {
                val quiet = quietSince.getOrPut(s.name) { now }
                if (now - quiet >= quietMs) {
                    activeSince.remove(s.name)
                    quietSince.remove(s.name)
                    if (quiet - since >= minWorkMs) finished += s
                }
            }
        }
        activeSince.keys.retainAll(seen)
        quietSince.keys.retainAll(seen)
        return finished
    }

    companion object {
        fun isShell(command: String): Boolean {
            val kind = command.trim().substringBefore(' ').substringAfterLast('/').lowercase()
            return kind.isEmpty() || kind == "exec" || kind in setOf("sh", "bash", "zsh", "fish")
        }
    }
}

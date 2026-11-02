package org.lam.app

/**
 * What the soft keyboard (typing, swipe, voice dictation) means for the terminal,
 * with no Android types so it can be unit-tested. Composing text is held back and
 * shown in an overlay; only committed text reaches the session.
 */
class ImeLogic(
    private val sendText: (String) -> Unit,
    private val sendBackspace: () -> Unit,
    private val onComposing: (String) -> Unit,
) {
    var composing = ""
        private set

    private fun setComposing(text: String) {
        composing = text
        onComposing(text)
    }

    fun commitText(text: CharSequence) {
        if (composing.isNotEmpty()) setComposing("")
        if (text.isNotEmpty()) sendText(text.toString())
    }

    fun setComposingText(text: CharSequence) = setComposing(text.toString())

    fun finishComposingText() {
        val pending = composing
        if (pending.isEmpty()) return
        setComposing("")
        sendText(pending)
    }

    fun deleteSurroundingText(before: Int, @Suppress("UNUSED_PARAMETER") after: Int) {
        var n = before
        if (composing.isNotEmpty() && n > 0) {
            val drop = minOf(n, composing.length)
            setComposing(composing.dropLast(drop))
            n -= drop
        }
        repeat(n) { sendBackspace() }
    }

    fun editorAction() {
        finishComposingText()
        sendText("\r")
    }

    /** A key event from the keyboard. Returns true when handled here; false means dispatch it normally. */
    fun keyDown(keyCode: Int): Boolean {
        if (composing.isEmpty()) return false
        if (keyCode == KEYCODE_DEL) {
            setComposing(composing.dropLast(1))
            return true
        }
        finishComposingText()
        return false
    }

    companion object {
        const val KEYCODE_DEL = 67
    }
}

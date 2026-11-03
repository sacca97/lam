package org.lam.app

import android.content.Context
import android.content.Context.INPUT_METHOD_SERVICE
import android.text.InputType
import android.text.Selection
import android.view.KeyEvent
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.inputmethod.InputMethodManager
import android.view.View
import com.termux.view.TerminalView

/**
 * Termux's view declares a raw input type, which turns off suggestions, swipe typing
 * and the keyboard's microphone, and it is final, so it cannot be subclassed. This
 * invisible view owns the keyboard instead: it is a normal text field to the keyboard
 * (no auto-correct flag, so commands are not mangled). What is typed stays in the
 * field's own buffer, shown by [onPending], and goes to [target]'s session when Enter
 * is pressed. It holds focus, so hardware key events arrive here and are handed to
 * the terminal view unchanged.
 */
class LamTerminalView(context: Context, private val target: TerminalView) : View(context) {
    private val currentSession get() = target.currentSession

    init {
        isFocusable = true
        isFocusableInTouchMode = true
    }

    override fun onCheckIsTextEditor() = true
    override fun onKeyDown(keyCode: Int, event: KeyEvent) = target.onKeyDown(keyCode, event)
    override fun onKeyUp(keyCode: Int, event: KeyEvent) = target.onKeyUp(keyCode, event)

    /** Called with the line being typed, empty once it is sent or cleared. */
    var onPending: (String) -> Unit = {}

    private var connection: Connection? = null

    /**
     * True: what is typed waits for Enter, so the keyboard sees the whole line.
     * False: each word goes to the terminal as it is finished, for programs that
     * read keys as they come (vim, less, menus, tab completion).
     */
    var lineMode = true

    /** Drops what was typed without sending it (Ctrl+C does this). */
    fun clearPending() = connection?.empty()

    private fun write(text: String) {
        val b = text.toByteArray()
        currentSession?.write(b, 0, b.size)
    }

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_ACTION_SEND
        // Keyboards offer suggestions only for a field that says where its cursor is;
        // the default, -1, means "unknown".
        outAttrs.initialSelStart = 0
        outAttrs.initialSelEnd = 0
        onPending("") // a new connection starts with an empty line
        return Connection().also { connection = it }
    }

    /**
     * A real editable holds the line, so the keyboard's questions about the text
     * around the cursor get true answers and suggestions, corrections and dictation
     * work as in any text field. Every change is reported back to the keyboard.
     */
    private inner class Connection : BaseInputConnection(this, true) {
        private val imm = context.getSystemService(INPUT_METHOD_SERVICE) as InputMethodManager

        private fun changed() {
            val e = editable!!
            imm.updateSelection(
                this@LamTerminalView, Selection.getSelectionStart(e), Selection.getSelectionEnd(e),
                getComposingSpanStart(e), getComposingSpanEnd(e),
            )
            onPending(e.toString())
        }

        fun empty() {
            val e = editable!!
            e.clear()
            Selection.setSelection(e, 0)
            changed()
        }

        /** Enter: the line goes to the terminal. */
        private fun submit() {
            write(PendingInput.lineToSend(editable!!)) // in live mode this is just what is still being composed, and the return
            empty()
        }

        private fun backspaceOne() {
            if (editable!!.isEmpty()) write("\u007f") else super.deleteSurroundingText(1, 0)
            changed()
        }

        /** Live mode: whatever is finished goes straight to the terminal. */
        private fun flushIfLive() {
            if (lineMode) return
            val e = editable!!
            val composing = getComposingSpanStart(e) >= 0 && getComposingSpanEnd(e) > getComposingSpanStart(e)
            if (e.isNotEmpty() && !composing) {
                write(e.toString().replace('\n', '\r'))
                empty()
            }
        }

        override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
            val ok = super.commitText(text, newCursorPosition)
            changed()
            flushIfLive()
            return ok
        }

        override fun setComposingText(text: CharSequence, newCursorPosition: Int): Boolean {
            val ok = super.setComposingText(text, newCursorPosition)
            changed()
            return ok
        }

        override fun finishComposingText(): Boolean {
            val ok = super.finishComposingText()
            changed()
            flushIfLive()
            return ok
        }

        override fun setComposingRegion(start: Int, end: Int): Boolean {
            val ok = super.setComposingRegion(start, end)
            changed()
            return ok
        }

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
            val held = Selection.getSelectionStart(editable!!).coerceAtLeast(0)
            repeat(PendingInput.backspacesBeyondBuffer(held, beforeLength)) { write("\u007f") }
            val ok = super.deleteSurroundingText(minOf(beforeLength, held), afterLength)
            changed()
            return ok
        }

        override fun deleteSurroundingTextInCodePoints(beforeLength: Int, afterLength: Int) =
            deleteSurroundingText(beforeLength, afterLength)

        override fun performEditorAction(editorAction: Int): Boolean {
            submit()
            return true
        }

        override fun sendKeyEvent(event: KeyEvent): Boolean {
            if (event.action == KeyEvent.ACTION_DOWN) {
                when (event.keyCode) {
                    KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> { submit(); return true }
                    KeyEvent.KEYCODE_DEL -> { backspaceOne(); return true }
                }
            }
            return super.sendKeyEvent(event)
        }
    }
}

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
    var onPending: (String, Int) -> Unit = { _, _ -> }

    private var connection: Connection? = null

    /**
     * The line being typed. It belongs to this view, not to one input connection: the
     * keyboard drops and reopens its connection when it hides (a scroll does that), and
     * what was typed must survive it.
     */
    private val line: android.text.Editable = android.text.SpannableStringBuilder().also { Selection.setSelection(it, 0) }

    /**
     * True: what is typed waits for Enter, so the keyboard sees the whole line.
     * False: each word goes to the terminal as it is finished, for programs that
     * read keys as they come (vim, less, menus, tab completion).
     */
    var lineMode = true

    /** Moves the caret within the typed line; false when there is none, so the key goes to the terminal. */
    fun moveCaret(delta: Int): Boolean {
        if (line.isEmpty()) return false
        Selection.setSelection(line, (Selection.getSelectionStart(line) + delta).coerceIn(0, line.length))
        onPending(line.toString(), Selection.getSelectionStart(line))
        connection?.report()
        return true
    }

    /** Drops what was typed without sending it (Ctrl+C does this). */
    fun clearPending() {
        line.clear()
        Selection.setSelection(line, 0)
        onPending("", 0)
        connection?.report()
    }

    private fun write(text: String) {
        val b = text.toByteArray()
        currentSession?.write(b, 0, b.size)
    }

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_ACTION_SEND
        // Keyboards offer suggestions only for a field that says where its cursor is;
        // the default, -1, means "unknown".
        BaseInputConnection.removeComposingSpans(line) // a word the old connection was composing is final now
        outAttrs.initialSelStart = Selection.getSelectionStart(line).coerceAtLeast(0)
        outAttrs.initialSelEnd = Selection.getSelectionEnd(line).coerceAtLeast(0)
        onPending(line.toString(), outAttrs.initialSelStart)
        return Connection().also { connection = it }
    }

    /**
     * A real editable holds the line, so the keyboard's questions about the text
     * around the cursor get true answers and suggestions, corrections and dictation
     * work as in any text field. Every change is reported back to the keyboard.
     */
    private inner class Connection : BaseInputConnection(this, true) {
        private val imm = context.getSystemService(INPUT_METHOD_SERVICE) as InputMethodManager

        override fun getEditable(): android.text.Editable = line

        fun report() {
            val e = editable!!
            imm.updateSelection(
                this@LamTerminalView, Selection.getSelectionStart(e), Selection.getSelectionEnd(e),
                getComposingSpanStart(e), getComposingSpanEnd(e),
            )
        }

        private fun changed() {
            val e = editable!!
            imm.updateSelection(
                this@LamTerminalView, Selection.getSelectionStart(e), Selection.getSelectionEnd(e),
                getComposingSpanStart(e), getComposingSpanEnd(e),
            )
            onPending(e.toString(), Selection.getSelectionStart(e).coerceIn(0, e.length))
        }

        fun empty() {
            val e = editable!!
            e.clear()
            Selection.setSelection(e, 0)
            changed()
        }

        /** Enter: the line goes to the terminal. */
        private fun submit() {
            val text = editable!!.toString()
            if (text.isNotEmpty()) write(PendingInput.lineToSend(text)) // in live mode, just what is still being composed
            // Its own keypress, after the text: see PendingInput.lineToSend.
            val session = currentSession // the one the line went to, even if the person switches meanwhile
            postDelayed({ session?.write(byteArrayOf(13), 0, 1) }, PendingInput.enterDelayMs(text))
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

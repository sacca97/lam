package org.lam.app

import android.content.Context
import android.text.InputType
import android.view.KeyEvent
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.view.View
import com.termux.view.TerminalView

/**
 * Termux's view declares a raw input type, which turns off suggestions, swipe typing
 * and the keyboard's microphone, and it is final, so it cannot be subclassed. This
 * invisible view owns the keyboard instead: it is a normal text field to the keyboard
 * (no auto-correct flag, so commands are not mangled) and turns what it commits
 * into terminal input for [target]. It holds focus, so hardware key events arrive
 * here and are handed to the terminal view unchanged.
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

    /** Called with the text being composed (dictation, swipe), empty when done. */
    var onComposing: (String) -> Unit = {}

    private val ime = ImeLogic(
        sendText = { sendText(it) },
        sendBackspace = {
            val b = byteArrayOf(0x7f)
            currentSession?.write(b, 0, 1)
        },
        onComposing = { text -> post { onComposing(text) } },
    )

    private fun sendText(text: String) {
        val session = currentSession ?: return
        val first = text.codePointAt(0)
        if (text.length == Character.charCount(first)) {
            target.inputCodePoint(first, false, false) // one character: honours the Ctrl key
        } else {
            val bytes = text.replace('\n', '\r').toByteArray()
            session.write(bytes, 0, bytes.size)
        }
    }

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_FLAG_NO_EXTRACT_UI or EditorInfo.IME_ACTION_NONE
        return Connection()
    }

    private inner class Connection : BaseInputConnection(this, false) {
        override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
            ime.commitText(text)
            return true
        }

        override fun setComposingText(text: CharSequence, newCursorPosition: Int): Boolean {
            ime.setComposingText(text)
            return true
        }

        override fun finishComposingText(): Boolean {
            ime.finishComposingText()
            return true
        }

        override fun setComposingRegion(start: Int, end: Int) = true
        override fun commitCompletion(text: android.view.inputmethod.CompletionInfo?) = true

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
            ime.deleteSurroundingText(beforeLength, afterLength)
            return true
        }

        override fun deleteSurroundingTextInCodePoints(beforeLength: Int, afterLength: Int): Boolean {
            ime.deleteSurroundingText(beforeLength, afterLength)
            return true
        }

        override fun performEditorAction(editorAction: Int): Boolean {
            ime.editorAction()
            return true
        }

        override fun sendKeyEvent(event: KeyEvent): Boolean {
            if (event.action == KeyEvent.ACTION_DOWN && ime.keyDown(event.keyCode)) return true
            return super.sendKeyEvent(event)
        }
    }
}

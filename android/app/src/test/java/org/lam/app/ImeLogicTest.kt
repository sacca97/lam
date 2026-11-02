package org.lam.app

import org.junit.Assert.assertEquals
import org.junit.Test

class ImeLogicTest {
    private val sent = StringBuilder()
    private var overlay = ""
    private val ime = ImeLogic(
        sendText = { sent.append(it) },
        sendBackspace = { sent.append("<BS>") },
        onComposing = { overlay = it },
    )

    @Test fun dictationComposesThenCommits() {
        // Gboard voice typing: partial results as composing text, then a commit.
        ime.setComposingText("hello")
        assertEquals("hello", overlay)
        assertEquals("", sent.toString())
        ime.setComposingText("hello wor")
        ime.setComposingText("hello world")
        assertEquals("", sent.toString())
        ime.commitText("hello world ")
        assertEquals("hello world ", sent.toString())
        assertEquals("", overlay)
    }

    @Test fun finishComposingSendsPendingText() {
        ime.setComposingText("ls -la")
        ime.finishComposingText()
        assertEquals("ls -la", sent.toString())
        assertEquals("", overlay)
        ime.finishComposingText() // nothing pending: no duplicate
        assertEquals("ls -la", sent.toString())
    }

    @Test fun commitWithoutComposing() {
        ime.commitText("git status")
        assertEquals("git status", sent.toString())
    }

    @Test fun deleteEditsComposingFirstThenSessionBackspaces() {
        ime.setComposingText("abc")
        ime.deleteSurroundingText(1, 0)
        assertEquals("ab", overlay)
        ime.deleteSurroundingText(3, 0)
        assertEquals("", overlay)
        assertEquals("<BS>", sent.toString()) // 3 asked, 2 came out of the composing text
    }

    @Test fun backspaceKeyEditsComposing() {
        ime.setComposingText("ab")
        assertEquals(true, ime.keyDown(ImeLogic.KEYCODE_DEL))
        assertEquals("a", overlay)
        assertEquals(true, ime.keyDown(ImeLogic.KEYCODE_DEL))
        assertEquals(false, ime.keyDown(ImeLogic.KEYCODE_DEL)) // nothing composing: normal key
    }

    @Test fun otherKeysFlushComposingFirst() {
        ime.setComposingText("echo hi")
        assertEquals(false, ime.keyDown(66)) // Enter dispatched normally after the flush
        assertEquals("echo hi", sent.toString())
    }

    @Test fun editorActionFlushesAndSendsEnter() {
        ime.setComposingText("pwd")
        ime.editorAction()
        assertEquals("pwd\r", sent.toString())
    }
}

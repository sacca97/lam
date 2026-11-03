package org.lam.app

/**
 * Typing is collected in the keyboard's own text buffer and only goes to the
 * terminal when Enter is pressed, so the keyboard sees the whole line (suggestions,
 * corrections, dictation) and nothing half-typed reaches the program. These are the
 * pure parts, for unit tests.
 */
object PendingInput {
    /** What Enter sends: the line, then a carriage return. */
    fun lineToSend(text: CharSequence): String = text.toString().replace('\n', '\r') + "\r"

    /**
     * A delete of [before] characters when [buffered] are waiting: the buffer takes what
     * it holds, the rest are backspaces for text the terminal already has.
     */
    fun backspacesBeyondBuffer(buffered: Int, before: Int): Int = maxOf(0, before - buffered)
}

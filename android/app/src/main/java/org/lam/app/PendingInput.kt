package org.lam.app

/**
 * Typing is collected in the keyboard's own text buffer and only goes to the
 * terminal when Enter is pressed, so the keyboard sees the whole line (suggestions,
 * corrections, dictation) and nothing half-typed reaches the program. These are the
 * pure parts, for unit tests.
 */
object PendingInput {
    /**
     * The line as the terminal gets it. The Enter that submits it is sent on its own, a
     * moment later: a program such as claude reads text that arrives in one burst as a
     * paste, and a return inside a paste is a new line in its box, not a submit.
     */
    fun lineToSend(text: CharSequence): String = text.toString().replace('\n', '\r')

    /** How long after the line the submitting Enter goes; none when there is no line. */
    fun enterDelayMs(text: CharSequence): Long = if (text.isEmpty()) 0 else 80

    /**
     * A delete of [before] characters when [buffered] are waiting: the buffer takes what
     * it holds, the rest are backspaces for text the terminal already has.
     */
    fun backspacesBeyondBuffer(buffered: Int, before: Int): Int = maxOf(0, before - buffered)
}

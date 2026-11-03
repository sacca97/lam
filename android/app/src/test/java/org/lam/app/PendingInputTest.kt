package org.lam.app

import org.junit.Assert.assertEquals
import org.junit.Test

class PendingInputTest {
    @Test fun enterSendsTheLineWithAReturn() {
        assertEquals("ls -la\r", PendingInput.lineToSend("ls -la"))
        assertEquals("\r", PendingInput.lineToSend(""))
        assertEquals("a\rb\r", PendingInput.lineToSend("a\nb"))
    }

    @Test fun deletesEatTheBufferFirst() {
        assertEquals(0, PendingInput.backspacesBeyondBuffer(5, 1))
        assertEquals(0, PendingInput.backspacesBeyondBuffer(3, 3))
        assertEquals(2, PendingInput.backspacesBeyondBuffer(1, 3))
        assertEquals(1, PendingInput.backspacesBeyondBuffer(0, 1))
    }
}

package org.lam.app

import org.junit.Assert.assertEquals
import org.junit.Test

class PendingInputTest {
    @Test fun theLineGoesWithoutTheReturn() {
        assertEquals("ls -la", PendingInput.lineToSend("ls -la"))
        assertEquals("", PendingInput.lineToSend(""))
        assertEquals("a\rb", PendingInput.lineToSend("a\nb"))
    }

    @Test fun theReturnFollowsAfterAPauseOnlyWhenThereWasALine() {
        assertEquals(80L, PendingInput.enterDelayMs("ls"))
        assertEquals(0L, PendingInput.enterDelayMs(""))
    }

    @Test fun deletesEatTheBufferFirst() {
        assertEquals(0, PendingInput.backspacesBeyondBuffer(5, 1))
        assertEquals(0, PendingInput.backspacesBeyondBuffer(3, 3))
        assertEquals(2, PendingInput.backspacesBeyondBuffer(1, 3))
        assertEquals(1, PendingInput.backspacesBeyondBuffer(0, 1))
    }
}

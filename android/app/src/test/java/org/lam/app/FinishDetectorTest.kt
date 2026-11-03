package org.lam.app

import org.junit.Assert.assertEquals
import org.junit.Test

class FinishDetectorTest {
    private fun s(working: Boolean, command: String = "claude") = SessionItem("a", "/p", command, working, false)
    private val sec = 1000L

    @Test fun finishesAfterLongWorkAndQuiet() {
        val d = FinishDetector()
        var t = 100 * sec
        for (i in 0..3) { assertEquals(0, d.update(listOf(s(true)), t).size); t += 10 * sec } // 30 s of work
        assertEquals(0, d.update(listOf(s(false)), t).size) // quiet starts
        t += 10 * sec
        assertEquals(0, d.update(listOf(s(false)), t).size)
        t += 10 * sec
        assertEquals(0, d.update(listOf(s(false)), t).size) // 20 s quiet
        t += 10 * sec
        assertEquals(1, d.update(listOf(s(false)), t).size) // 30 s quiet
    }

    @Test fun aShortBurstIsNotAFinish() {
        val d = FinishDetector()
        var t = 100 * sec
        d.update(listOf(s(true)), t); t += 5 * sec
        for (i in 0..5) { assertEquals(0, d.update(listOf(s(false)), t).size); t += 10 * sec }
    }

    @Test fun aPauseBetweenStepsIsNotAFinish() {
        val d = FinishDetector()
        var t = 100 * sec
        for (i in 0..3) { d.update(listOf(s(true)), t); t += 10 * sec }
        d.update(listOf(s(false)), t); t += 10 * sec
        assertEquals(0, d.update(listOf(s(true)), t).size) // back to work before 25 s
        t += 10 * sec
        assertEquals(0, d.update(listOf(s(false)), t).size)
    }

    @Test fun shellsNeverFinish() {
        val d = FinishDetector()
        var t = 100 * sec
        for (i in 0..3) { d.update(listOf(s(true, "exec")), t); t += 10 * sec }
        for (i in 0..5) { assertEquals(0, d.update(listOf(s(false, "exec")), t).size); t += 10 * sec }
    }

    @Test fun aLongGapForgetsTheWork() {
        val d = FinishDetector()
        var t = 100 * sec
        for (i in 0..3) { d.update(listOf(s(true)), t); t += 10 * sec }
        t += 120 * sec // the phone slept
        for (i in 0..5) { assertEquals(0, d.update(listOf(s(false)), t).size); t += 10 * sec }
    }
}

package org.lam.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateCheckTest {
    @Test fun newerVersionsCount() {
        assertTrue(isNewer("v0.2.1", "0.2.0"))
        assertTrue(isNewer("v0.10.0", "0.9.9"))
        assertTrue(isNewer("v1.0.0", "0.99.99"))
    }

    @Test fun sameOrOlderDoNot() {
        assertFalse(isNewer("v0.2.0", "0.2.0"))
        assertFalse(isNewer("v0.1.9", "0.2.0"))
    }

    @Test fun nonSemverIsIgnored() {
        assertFalse(isNewer("nightly", "0.2.0"))
        assertFalse(isNewer("v0.3.0-rc1", "0.2.0"))
        assertFalse(isNewer("v0.3", "0.2.0"))
        assertFalse(isNewer("v0.3.0", "dev"))
    }
}

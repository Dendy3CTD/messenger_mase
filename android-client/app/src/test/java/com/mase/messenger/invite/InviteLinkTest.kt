package com.mase.messenger.invite

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class InviteLinkTest {

    @Test
    fun `builds a prod link with the mase scheme`() {
        assertEquals("mase://invite?username=alice", InviteLink.build("mase", "alice"))
    }

    @Test
    fun `builds a dev link with the mase-dev scheme`() {
        assertEquals("mase-dev://invite?username=alice", InviteLink.build("mase-dev", "alice"))
    }

    @Test
    fun `lowercases and percent-encodes the username`() {
        assertEquals("mase://invite?username=al%20ice", InviteLink.build("mase", "Al Ice"))
    }

    @Test
    fun `parses the username when scheme and host match`() {
        assertEquals("alice", InviteLink.usernameFrom("mase", "mase", "invite", "alice"))
    }

    @Test
    fun `trims and lowercases the parsed username`() {
        assertEquals("alice", InviteLink.usernameFrom("mase", "mase", "invite", "  Alice "))
    }

    @Test
    fun `a dev build ignores prod links`() {
        assertNull(InviteLink.usernameFrom("mase-dev", "mase", "invite", "alice"))
    }

    @Test
    fun `a prod build ignores dev links`() {
        assertNull(InviteLink.usernameFrom("mase", "mase-dev", "invite", "alice"))
    }

    @Test
    fun `rejects a wrong host`() {
        assertNull(InviteLink.usernameFrom("mase", "mase", "join", "alice"))
    }

    @Test
    fun `rejects a missing or blank username`() {
        assertNull(InviteLink.usernameFrom("mase", "mase", "invite", null))
        assertNull(InviteLink.usernameFrom("mase", "mase", "invite", "   "))
    }

    @Test
    fun `keeps digits and underscores in the username`() {
        val link = InviteLink.build("mase-dev", "bob_1")
        assertEquals("mase-dev://invite?username=bob_1", link)
    }
}

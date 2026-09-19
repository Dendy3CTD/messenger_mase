package com.mase.messenger.invite

import java.net.URLEncoder

/**
 * Invite links look like `<scheme>://invite?username=<name>`. The scheme differs per
 * build flavor (prod: `mase`, dev: `mase-dev`) so both apps can be installed side by side
 * without fighting over the same link.
 */
object InviteLink {
    const val HOST = "invite"

    fun build(scheme: String, username: String): String {
        val encoded = URLEncoder.encode(username.lowercase(), "UTF-8").replace("+", "%20")
        return "$scheme://$HOST?username=$encoded"
    }

    /** Returns the normalised username, or null when the link is not an invite for this build. */
    fun usernameFrom(expectedScheme: String, scheme: String?, host: String?, usernameParam: String?): String? {
        if (scheme != expectedScheme || host != HOST) return null
        return usernameParam?.trim()?.lowercase()?.takeIf { it.isNotEmpty() }
    }
}

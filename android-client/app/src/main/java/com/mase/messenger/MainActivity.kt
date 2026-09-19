package com.mase.messenger

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.getValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.mase.messenger.data.session.SessionSnapshot
import com.mase.messenger.messaging.MessengerEngine
import com.mase.messenger.ui.AppRoot
import com.mase.messenger.ui.theme.MaseTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val engine = (application as MaseApplication).engine
        handleInviteIntent(intent, engine)
        setContent {
            val session by engine.sessionRepository.snapshot.collectAsStateWithLifecycle(SessionSnapshot.Empty)
            MaseTheme(darkTheme = session.darkTheme) {
                AppRoot(engine)
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleInviteIntent(intent, (application as MaseApplication).engine)
    }

    private fun handleInviteIntent(intent: Intent?, engine: MessengerEngine) {
        val uri: Uri = intent?.data ?: return
        if (uri.scheme != "mase" || uri.host != "invite") return
        val username = uri.getQueryParameter("username")?.trim()?.lowercase() ?: return
        engine.setInviteUsername(username)
    }
}

package com.mase.messenger.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Chat
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.mase.messenger.data.session.SessionSnapshot
import com.mase.messenger.messaging.AuthPhase
import com.mase.messenger.messaging.MessengerEngine
import com.mase.messenger.ui.components.ConnectionBanner
import com.mase.messenger.ui.screens.ChatScreen
import com.mase.messenger.ui.screens.ChatsListScreen
import com.mase.messenger.ui.screens.ContactsScreen
import com.mase.messenger.ui.screens.LoginScreen
import com.mase.messenger.ui.screens.RegisterScreen
import com.mase.messenger.ui.screens.ProfileSetupScreen
import com.mase.messenger.ui.screens.SettingsScreen

@Composable
fun AppRoot(engine: MessengerEngine) {
    val session by engine.sessionRepository.snapshot.collectAsStateWithLifecycle(SessionSnapshot.Empty)
    val phase by engine.authPhase.collectAsStateWithLifecycle()
    val conn by engine.connection.collectAsStateWithLifecycle()

    Column(Modifier.fillMaxSize()) {
        ConnectionBanner(conn)
        when (phase) {
            AuthPhase.Login    -> LoginScreen(engine)
            AuthPhase.Register -> RegisterScreen(engine)
            AuthPhase.Profile  -> ProfileSetupScreen(engine)
            AuthPhase.App      -> MainApp(engine, session)
        }
    }
}

@Composable
private fun MainApp(engine: MessengerEngine, session: SessionSnapshot) {
    var currentTab by rememberSaveable { mutableIntStateOf(0) }
    var openChatId by rememberSaveable { mutableStateOf<Long?>(null) }
    val navChat by engine.navigateToChatId.collectAsStateWithLifecycle()

    LaunchedEffect(navChat) {
        val id = navChat ?: return@LaunchedEffect
        openChatId = id
        engine.consumeNavigateToChat()
    }

    // Chat screen takes full space (no bottom nav)
    if (openChatId != null) {
        val cid = openChatId!!
        val chats by engine.chats.collectAsStateWithLifecycle()
        val chat = chats.find { it.chatId == cid }
        ChatScreen(
            engine = engine,
            session = session,
            chatId = cid,
            title = chat?.title ?: "Чат",
            peerUserId = chat?.peerUserId,
            onBack = { openChatId = null }
        )
        return
    }

    // Main tabs with bottom navigation
    Scaffold(
        bottomBar = {
            NavigationBar {
                NavigationBarItem(
                    selected = currentTab == 0,
                    onClick = { currentTab = 0 },
                    icon = { Icon(Icons.Default.Chat, contentDescription = null) },
                    label = { Text("Чаты") }
                )
                NavigationBarItem(
                    selected = currentTab == 1,
                    onClick = { currentTab = 1 },
                    icon = { Icon(Icons.Default.Person, contentDescription = null) },
                    label = { Text("Контакты") }
                )
                NavigationBarItem(
                    selected = currentTab == 2,
                    onClick = { currentTab = 2 },
                    icon = { Icon(Icons.Default.Settings, contentDescription = null) },
                    label = { Text("Настройки") }
                )
            }
        }
    ) { paddingValues ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(paddingValues)
        ) {
            when (currentTab) {
                0 -> ChatsListScreen(engine = engine, onOpenChat = { openChatId = it })
                1 -> ContactsScreen(engine = engine, onOpenChat = { openChatId = it })
                2 -> SettingsScreen(engine = engine, session = session)
            }
        }
    }
}

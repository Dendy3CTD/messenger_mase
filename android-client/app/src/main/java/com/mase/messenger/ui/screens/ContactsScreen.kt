package com.mase.messenger.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.PersonAdd
import androidx.compose.material.icons.filled.PersonSearch
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Divider
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.mase.messenger.data.session.PublicUserProfile
import com.mase.messenger.messaging.MessengerEngine
import com.mase.messenger.ui.components.AvatarView

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ContactsScreen(
    engine: MessengerEngine,
    onOpenChat: (Long) -> Unit
) {
    val friends by engine.friends.collectAsStateWithLifecycle()
    val presence by engine.presence.collectAsStateWithLifecycle()
    val lookup by engine.lastLookupUser.collectAsStateWithLifecycle()
    var query by rememberSaveable { mutableStateOf("") }
    var showSearchDialog by remember { mutableStateOf(false) }
    var searchUsername by remember { mutableStateOf("") }
    var showAddDialog by remember { mutableStateOf(false) }

    // Invite via deep link lookup
    val pendingInvite by engine.pendingInviteUsername.collectAsStateWithLifecycle()
    LaunchedEffect(pendingInvite) {
        if (!pendingInvite.isNullOrBlank()) {
            engine.lookupUsername(pendingInvite!!)
            engine.consumeInviteUsername()
        }
    }
    LaunchedEffect(lookup) {
        if (lookup != null) showAddDialog = true
    }

    if (showAddDialog && lookup != null) {
        val u = lookup!!
        AlertDialog(
            onDismissRequest = { showAddDialog = false; engine.clearLookupUser() },
            title = { Text("Добавить контакт") },
            text = {
                Column {
                    AvatarView(name = u.displayName.ifBlank { u.username }, size = 56.dp,
                        modifier = Modifier.align(Alignment.CenterHorizontally))
                    Spacer(Modifier.height(12.dp))
                    Text(
                        u.displayName.ifBlank { u.username },
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier.align(Alignment.CenterHorizontally)
                    )
                    Text(
                        "@${u.username}",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.align(Alignment.CenterHorizontally)
                    )
                }
            },
            confirmButton = {
                Button(onClick = {
                    engine.addFriend(u.id)
                    showAddDialog = false
                    engine.clearLookupUser()
                }) { Text("Добавить") }
            },
            dismissButton = {
                TextButton(onClick = { showAddDialog = false; engine.clearLookupUser() }) {
                    Text("Отмена")
                }
            }
        )
    }

    if (showSearchDialog) {
        AlertDialog(
            onDismissRequest = { showSearchDialog = false; searchUsername = "" },
            title = { Text("Найти пользователя") },
            text = {
                OutlinedTextField(
                    value = searchUsername,
                    onValueChange = { searchUsername = it.lowercase().trimStart('@') },
                    label = { Text("Username") },
                    prefix = { Text("@") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )
            },
            confirmButton = {
                Button(
                    onClick = {
                        if (searchUsername.isNotBlank()) {
                            engine.lookupUsername(searchUsername)
                            showSearchDialog = false
                            searchUsername = ""
                        }
                    },
                    enabled = searchUsername.isNotBlank()
                ) { Text("Найти") }
            },
            dismissButton = {
                TextButton(onClick = { showSearchDialog = false; searchUsername = "" }) {
                    Text("Отмена")
                }
            }
        )
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Контакты", fontWeight = FontWeight.SemiBold) },
                actions = {
                    TextButton(onClick = { showSearchDialog = true }) {
                        Icon(Icons.Default.PersonAdd, null)
                        Spacer(Modifier.width(4.dp))
                        Text("Добавить")
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.surface
                )
            )
        }
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
        ) {
            // Search bar
            OutlinedTextField(
                value = query,
                onValueChange = { query = it },
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp),
                placeholder = { Text("Поиск") },
                leadingIcon = { Icon(Icons.Default.PersonSearch, null) },
                singleLine = true,
                shape = RoundedCornerShape(24.dp)
            )

            val filteredFriends = friends.filter { f ->
                query.isBlank() || f.username.contains(query, true) || f.displayName.contains(query, true)
            }

            val onlineIds = presence.map { it.id }.toSet()

            if (filteredFriends.isEmpty() && query.isBlank()) {
                EmptyContactsState(
                    modifier = Modifier.fillMaxSize(),
                    onAdd = { showSearchDialog = true }
                )
            } else {
                LazyColumn(modifier = Modifier.fillMaxSize()) {
                    if (filteredFriends.isNotEmpty()) {
                        item {
                            Text(
                                "Контакты (${filteredFriends.size})",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)
                            )
                        }
                        items(filteredFriends, key = { it.id }) { friend ->
                            ContactRow(
                                profile = friend,
                                isOnline = friend.id in onlineIds,
                                onOpenChat = {
                                    engine.openDirectChat(friend.id)
                                    // navigation handled by engine navigateToChatId
                                }
                            )
                            Divider(
                                modifier = Modifier.padding(start = 80.dp),
                                color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f)
                            )
                        }
                    }

                    // Online users not yet in contacts
                    val onlineNotFriends = presence.filter { p ->
                        friends.none { it.id == p.id } &&
                        (query.isBlank() || p.username.contains(query, true) || p.displayName.contains(query, true))
                    }
                    if (onlineNotFriends.isNotEmpty()) {
                        item {
                            Text(
                                "Сейчас в сети",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)
                            )
                        }
                        items(onlineNotFriends, key = { "p_${it.id}" }) { p ->
                            ContactRow(
                                profile = p,
                                isOnline = true,
                                onOpenChat = { engine.openDirectChat(p.id) }
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun ContactRow(
    profile: PublicUserProfile,
    isOnline: Boolean,
    onOpenChat: () -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onOpenChat)
            .padding(horizontal = 16.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Box {
            AvatarView(name = profile.displayName.ifBlank { profile.username }, size = 50.dp)
            if (isOnline) {
                Surface(
                    modifier = Modifier
                        .size(14.dp)
                        .align(Alignment.BottomEnd)
                        .clip(CircleShape),
                    color = MaterialTheme.colorScheme.surface
                ) {
                    Surface(
                        modifier = Modifier
                            .padding(2.dp)
                            .fillMaxSize()
                            .clip(CircleShape),
                        color = MaterialTheme.colorScheme.primary
                    ) {}
                }
            }
        }

        Spacer(Modifier.width(12.dp))

        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = profile.displayName.ifBlank { profile.username },
                style = MaterialTheme.typography.bodyLarge,
                fontWeight = FontWeight.Medium,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
            Text(
                text = if (isOnline) "в сети" else "@${profile.username}",
                style = MaterialTheme.typography.bodySmall,
                color = if (isOnline) MaterialTheme.colorScheme.primary
                        else MaterialTheme.colorScheme.onSurfaceVariant
            )
        }

        TextButton(onClick = onOpenChat) {
            Text("Написать", style = MaterialTheme.typography.labelMedium)
        }
    }
}

@Composable
private fun EmptyContactsState(modifier: Modifier, onAdd: () -> Unit) {
    Column(
        modifier = modifier,
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center
    ) {
        Icon(
            Icons.Default.PersonSearch,
            contentDescription = null,
            modifier = Modifier.size(72.dp),
            tint = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.4f)
        )
        Spacer(Modifier.height(16.dp))
        Text(
            "Нет контактов",
            style = MaterialTheme.typography.titleMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant
        )
        Spacer(Modifier.height(6.dp))
        Text(
            "Найдите друзей по имени пользователя",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.7f)
        )
        Spacer(Modifier.height(20.dp))
        Button(onClick = onAdd) {
            Icon(Icons.Default.PersonAdd, null, modifier = Modifier.size(18.dp))
            Spacer(Modifier.width(8.dp))
            Text("Найти пользователя")
        }
    }
}

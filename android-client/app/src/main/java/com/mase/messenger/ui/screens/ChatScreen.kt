package com.mase.messenger.ui.screens

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.AttachFile
import androidx.compose.material.icons.filled.BrokenImage
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Snackbar
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
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
import kotlinx.coroutines.delay
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import coil.compose.AsyncImage
import coil.request.ImageRequest
import com.mase.messenger.data.local.MessageEntity
import com.mase.messenger.data.session.SessionSnapshot
import com.mase.messenger.media.MediaUploader
import com.mase.messenger.messaging.MessengerEngine
import com.mase.messenger.messaging.PhotoUploadState
import com.mase.messenger.ui.components.AvatarView
import com.mase.messenger.ui.theme.BubbleIncoming
import com.mase.messenger.ui.theme.BubbleIncomingDark
import com.mase.messenger.ui.theme.BubbleOutgoing
import com.mase.messenger.ui.theme.BubbleOutgoingDark
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@OptIn(ExperimentalMaterial3Api::class, ExperimentalFoundationApi::class)
@Composable
fun ChatScreen(
    engine: MessengerEngine,
    session: SessionSnapshot,
    chatId: Long,
    title: String,
    peerUserId: Long?,
    isGroup: Boolean = false,
    onBack: () -> Unit,
    onGroupInfo: (() -> Unit)? = null,
) {
    val messages by engine.messagesFlow(chatId).collectAsStateWithLifecycle(emptyList())
    val photoUploadState by engine.photoUploadState.collectAsStateWithLifecycle()
    val presence by engine.presence.collectAsStateWithLifecycle()
    val friends by engine.friends.collectAsStateWithLifecycle()
    val typingUsers by engine.typingUsersInChat(chatId).collectAsStateWithLifecycle(emptySet())
    var input by rememberSaveable { mutableStateOf("") }
    val listState = rememberLazyListState()
    val myId = session.userId
    val dark = session.darkTheme
    val snackbarHostState = remember { SnackbarHostState() }

    val isOnline = peerUserId != null && presence.any { it.id == peerUserId }

    // Typing label: resolve user names
    val typingLabel: String? = remember(typingUsers, friends, presence) {
        if (typingUsers.isEmpty()) return@remember null
        val allKnown = (friends + presence).distinctBy { it.id }
        val names = typingUsers.mapNotNull { uid ->
            allKnown.firstOrNull { it.id == uid }?.displayName?.ifBlank { null }
        }
        when {
            names.isEmpty() -> "печатает..."
            names.size == 1 -> "${names[0]} печатает..."
            else -> "${names.take(2).joinToString(", ")} печатают..."
        }
    }

    // Mark chat as read when opened
    LaunchedEffect(chatId) {
        engine.activeChatId = chatId
        engine.markChatRead(chatId)
    }
    // Clear on leave
    LaunchedEffect(Unit) {
        return@LaunchedEffect
    }

    // Auto-scroll to bottom on new messages
    LaunchedEffect(messages.size) {
        if (messages.isNotEmpty()) {
            listState.animateScrollToItem(messages.lastIndex)
        }
    }

    // Send typing indicator with debounce (stop after 3s of inactivity)
    LaunchedEffect(input) {
        if (input.isNotEmpty()) {
            engine.sendTyping(chatId)
            delay(3_000)
            // After 3s with no new input, stop typing
            // Server auto-clears on client side, nothing extra needed
        }
    }

    // Show upload error
    LaunchedEffect(photoUploadState) {
        if (photoUploadState is PhotoUploadState.Error) {
            snackbarHostState.showSnackbar((photoUploadState as PhotoUploadState.Error).msg)
            engine.clearPhotoUploadError()
        }
    }

    // Photo picker
    val photoPicker = rememberLauncherForActivityResult(
        ActivityResultContracts.PickVisualMedia()
    ) { uri: Uri? ->
        if (uri != null) {
            if (isGroup) engine.sendGroupPhoto(chatId, uri)
            else if (peerUserId != null) engine.sendPhoto(peerUserId, uri)
        }
    }

    Scaffold(
        snackbarHost = {
            SnackbarHost(snackbarHostState) { data ->
                Snackbar(
                    action = { TextButton(onClick = { data.dismiss() }) { Text("OK") } }
                ) { Text(data.visuals.message) }
            }
        },
        topBar = {
            TopAppBar(
                title = {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        AvatarView(name = title, size = 36.dp)
                        Spacer(Modifier.padding(horizontal = 8.dp))
                        Column {
                            Text(
                                title,
                                style = MaterialTheme.typography.titleMedium,
                                fontWeight = FontWeight.SemiBold
                            )
                            if (peerUserId != null || chatId == 1L) {
                                val subtitle = typingLabel
                                    ?: if (peerUserId != null) (if (isOnline) "в сети" else "не в сети")
                                    else null
                                if (subtitle != null) {
                                    Text(
                                        subtitle,
                                        style = MaterialTheme.typography.bodySmall,
                                        color = if (typingLabel != null) MaterialTheme.colorScheme.primary
                                                else if (isOnline) MaterialTheme.colorScheme.primary
                                                else MaterialTheme.colorScheme.onSurfaceVariant
                                    )
                                }
                            }
                        }
                    }
                },
                navigationIcon = {
                    IconButton(onClick = {
                        engine.activeChatId = null
                        onBack()
                    }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, "Назад")
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.surface
                )
            )
        }
    ) { paddingValues ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(paddingValues)
                .imePadding()
        ) {
            LazyColumn(
                state = listState,
                modifier = Modifier
                    .weight(1f)
                    .fillMaxWidth()
                    .padding(horizontal = 8.dp),
                verticalArrangement = Arrangement.spacedBy(4.dp)
            ) {
                item { Spacer(Modifier.height(8.dp)) }
                items(messages, key = { it.id }) { m ->
                    MessageBubble(
                        message = m,
                        myId = myId,
                        dark = dark,
                        mediaBaseUrl = engine.mediaBaseUrl,
                    )
                }
                item { Spacer(Modifier.height(4.dp)) }
            }

            // Upload indicator
            if (photoUploadState is PhotoUploadState.Uploading) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(MaterialTheme.colorScheme.primaryContainer)
                        .padding(horizontal = 16.dp, vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(12.dp)
                ) {
                    CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
                    Text("Отправка фото…", style = MaterialTheme.typography.bodySmall)
                }
            }

            // Input row
            Surface(
                shadowElevation = 8.dp,
                color = MaterialTheme.colorScheme.surface
            ) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 8.dp, vertical = 6.dp),
                    verticalAlignment = Alignment.Bottom,
                    horizontalArrangement = Arrangement.spacedBy(4.dp)
                ) {
                    // Attachment button (direct or group chats, not global)
                    if (chatId != 1L && (peerUserId != null || isGroup)) {
                        IconButton(
                            onClick = {
                                photoPicker.launch(PickVisualMediaRequest(
                                    ActivityResultContracts.PickVisualMedia.ImageOnly
                                ))
                            },
                            modifier = Modifier.size(48.dp)
                        ) {
                            Icon(
                                Icons.Default.AttachFile,
                                "Прикрепить фото",
                                tint = MaterialTheme.colorScheme.onSurfaceVariant
                            )
                        }
                    }

                    OutlinedTextField(
                        value = input,
                        onValueChange = { input = it },
                        modifier = Modifier.weight(1f),
                        placeholder = { Text("Сообщение") },
                        maxLines = 5,
                        shape = RoundedCornerShape(24.dp),
                        colors = OutlinedTextFieldDefaults.colors(
                            unfocusedBorderColor = MaterialTheme.colorScheme.outline.copy(alpha = 0.4f)
                        )
                    )

                    IconButton(
                        onClick = {
                            val text = input.trim()
                            if (text.isEmpty()) return@IconButton
                            when {
                                chatId == 1L       -> engine.sendGlobalMessage(text)
                                isGroup            -> engine.sendGroupMessage(chatId, text)
                                peerUserId != null -> engine.sendDirectMessage(chatId, peerUserId, text)
                            }
                            input = ""
                        },
                        modifier = Modifier
                            .size(48.dp)
                            .clip(CircleShape)
                            .background(
                                if (input.isNotBlank()) MaterialTheme.colorScheme.primary
                                else MaterialTheme.colorScheme.surfaceVariant
                            )
                    ) {
                        Icon(
                            Icons.AutoMirrored.Filled.Send,
                            "Отправить",
                            tint = if (input.isNotBlank()) Color.White
                                   else MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun MessageBubble(
    message: MessageEntity,
    myId: Long,
    dark: Boolean,
    mediaBaseUrl: String,
) {
    val outgoing = message.senderId == myId
    val bg = when {
        outgoing && dark -> BubbleOutgoingDark
        outgoing         -> BubbleOutgoing
        dark             -> BubbleIncomingDark
        else             -> BubbleIncoming
    }
    val align = if (outgoing) Alignment.CenterEnd else Alignment.CenterStart
    val time = SimpleDateFormat("HH:mm", Locale.getDefault()).format(Date(message.ts))
    val shape = if (outgoing) {
        RoundedCornerShape(topStart = 16.dp, topEnd = 4.dp, bottomStart = 16.dp, bottomEnd = 16.dp)
    } else {
        RoundedCornerShape(topStart = 4.dp, topEnd = 16.dp, bottomStart = 16.dp, bottomEnd = 16.dp)
    }

    Box(
        modifier = Modifier
            .fillMaxWidth()
            .padding(
                start = if (outgoing) 48.dp else 0.dp,
                end = if (outgoing) 0.dp else 48.dp
            ),
        contentAlignment = align
    ) {
        Column(
            modifier = Modifier
                .widthIn(max = 280.dp)
                .clip(shape)
                .background(bg)
                .padding(8.dp)
        ) {
            when (message.msgType) {
                "photo" -> {
                    PhotoMessageContent(
                        mediaId = message.mediaId,
                        mediaBaseUrl = mediaBaseUrl,
                    )
                }
                else -> {
                    Text(
                        text = message.body,
                        style = MaterialTheme.typography.bodyMedium
                    )
                }
            }

            Row(
                modifier = Modifier.align(Alignment.End),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(3.dp)
            ) {
                Text(
                    text = time,
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                if (outgoing) {
                    Text(
                        text = when (message.status) {
                            "delivered" -> "✓✓"
                            else        -> "✓"
                        },
                        style = MaterialTheme.typography.labelSmall,
                        color = if (message.status == "delivered")
                            MaterialTheme.colorScheme.primary
                        else
                            MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
        }
    }
}

@Composable
private fun PhotoMessageContent(
    mediaId: String,
    mediaBaseUrl: String,
) {
    if (mediaId.isEmpty() || mediaBaseUrl.isEmpty()) {
        Box(
            modifier = Modifier
                .size(200.dp, 150.dp)
                .background(Color.Gray.copy(alpha = 0.3f), RoundedCornerShape(8.dp)),
            contentAlignment = Alignment.Center
        ) {
            Icon(Icons.Default.BrokenImage, null, tint = Color.Gray)
        }
        return
    }

    val url = MediaUploader.mediaUrl(mediaBaseUrl, mediaId)
    AsyncImage(
        model = ImageRequest.Builder(LocalContext.current)
            .data(url)
            .crossfade(true)
            .build(),
        contentDescription = "Фото",
        contentScale = ContentScale.Crop,
        modifier = Modifier
            .widthIn(max = 240.dp)
            .height(180.dp)
            .clip(RoundedCornerShape(8.dp))
    )
}

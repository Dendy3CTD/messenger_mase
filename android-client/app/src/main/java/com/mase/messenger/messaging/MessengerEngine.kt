package com.mase.messenger.messaging

import android.app.Application
import android.net.Uri
import android.util.Log
import com.mase.messenger.data.local.ChatEntity
import com.mase.messenger.data.local.MaseDatabase
import com.mase.messenger.data.local.MessageEntity
import com.mase.messenger.data.session.PublicUserProfile
import com.mase.messenger.data.session.SessionRepository
import com.mase.messenger.BuildConfig
import com.mase.messenger.invite.InviteLink
import com.mase.messenger.media.MediaUploader
import com.mase.messenger.network.WsMessengerClient
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import kotlin.math.min

sealed class ConnectionUi {
    data object Idle : ConnectionUi()
    data object Discovering : ConnectionUi()
    data object Connecting : ConnectionUi()
    data object Online : ConnectionUi()
    data class Problem(val text: String) : ConnectionUi()
}

sealed class AuthPhase {
    data object Login : AuthPhase()
    data object Register : AuthPhase()
    data object Profile : AuthPhase()
    data object App : AuthPhase()
}

sealed class PhotoUploadState {
    data object Idle : PhotoUploadState()
    data object Uploading : PhotoUploadState()
    data class Error(val msg: String) : PhotoUploadState()
}

class MessengerEngine(private val app: Application) {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    val sessionRepository = SessionRepository(app)
    val db = MaseDatabase.build(app)
    private val ws = WsMessengerClient()

    // Server URLs — taken from BuildConfig (set in build.gradle.kts)
    val mediaBaseUrl: String get() = BuildConfig.MEDIA_URL

    private val _connection = MutableStateFlow<ConnectionUi>(ConnectionUi.Idle)
    val connection: StateFlow<ConnectionUi> = _connection.asStateFlow()

    private val _authPhase = MutableStateFlow<AuthPhase>(AuthPhase.Login)
    val authPhase: StateFlow<AuthPhase> = _authPhase.asStateFlow()

    private val _authError = MutableStateFlow<String?>(null)
    val authError: StateFlow<String?> = _authError.asStateFlow()
    fun clearAuthError() { _authError.value = null }

    private val _authPending = MutableStateFlow(false)
    val authPending: StateFlow<Boolean> = _authPending.asStateFlow()

    private val _friends = MutableStateFlow<List<PublicUserProfile>>(emptyList())
    val friends: StateFlow<List<PublicUserProfile>> = _friends.asStateFlow()

    private val _presence = MutableStateFlow<List<PublicUserProfile>>(emptyList())
    val presence: StateFlow<List<PublicUserProfile>> = _presence.asStateFlow()

    private val _pendingInviteUsername = MutableStateFlow<String?>(null)
    val pendingInviteUsername: StateFlow<String?> = _pendingInviteUsername.asStateFlow()

    private val _navigateToChatId = MutableStateFlow<Long?>(null)
    val navigateToChatId: StateFlow<Long?> = _navigateToChatId.asStateFlow()

    private val _photoUploadState = MutableStateFlow<PhotoUploadState>(PhotoUploadState.Idle)
    val photoUploadState: StateFlow<PhotoUploadState> = _photoUploadState.asStateFlow()

    // chatId -> set of userIds currently typing
    private val _typingInChat = MutableStateFlow<Map<Long, Set<Long>>>(emptyMap())
    fun typingUsersInChat(chatId: Long): kotlinx.coroutines.flow.Flow<Set<Long>> =
        _typingInChat.map { it[chatId] ?: emptySet() }

    // Per-user auto-stop jobs for typing timeout
    private val typingStopJobs = mutableMapOf<Long, Job>()
    private val typingJobsMutex = java.util.concurrent.locks.ReentrantLock()

    @Volatile var activeChatId: Long? = null

    fun consumeNavigateToChat() { _navigateToChatId.value = null }

    val chats: StateFlow<List<ChatEntity>> =
        db.chatDao().observeChats().stateIn(scope, SharingStarted.Eagerly, emptyList())

    @Volatile private var currentToken: String? = null

    fun start() {
        scope.launch {
            sessionRepository.snapshot.collect { snap ->
                currentToken = snap.accessToken.takeIf { it.isNotEmpty() }
                syncAuthPhase(snap)
            }
        }
        scope.launch { ensureGlobalChatRow() }
        scope.launch { runNetworkLoop() }
    }

    fun messagesFlow(chatId: Long) = db.messageDao().observe(chatId)

    fun setInviteUsername(username: String?) {
        _pendingInviteUsername.value = username?.trim()?.lowercase()
    }

    fun consumeInviteUsername(): String? {
        val v = _pendingInviteUsername.value
        _pendingInviteUsername.value = null
        return v
    }

    private suspend fun ensureGlobalChatRow() {
        withContext(Dispatchers.IO) {
            db.chatDao().upsert(ChatEntity(
                chatId = 1L, kind = "global", title = "Общий чат",
                peerUserId = null, lastSnippet = "", lastTs = Long.MAX_VALUE
            ))
        }
    }

    private fun syncAuthPhase(snap: com.mase.messenger.data.session.SessionSnapshot) {
        when {
            !snap.isLoggedIn -> {
                if (_authPhase.value !is AuthPhase.Register) _authPhase.value = AuthPhase.Login
            }
            else -> {
                val p = snap.profile()
                _authPhase.value =
                    if (p != null && p.displayName.isBlank()) AuthPhase.Profile else AuthPhase.App
            }
        }
    }

    private suspend fun runNetworkLoop() {
        var backoff = 2000L
        while (scope.isActive) {
            _connection.value = ConnectionUi.Connecting

            ws.connect(
                url = BuildConfig.WS_URL,
                onOpen = {
                    _connection.value = ConnectionUi.Online
                    scope.launch { onSocketReady() }
                },
                onMessage = { msg -> scope.launch { handleServerLine(msg) } },
                onClosed = { err ->
                    Log.w("MaseEngine", "ws closed: ${err?.message}")
                    _connection.value = ConnectionUi.Problem("Нет соединения")
                    _authPending.value = false
                }
            )

            // ws.connect blocks until closed via callbacks; wait for retry
            delay(backoff)
            backoff = min(30_000L, backoff * 2)
        }
    }

    private suspend fun onSocketReady() {
        val tok = currentToken
        if (tok == null) return
        sendRaw(JSONObject().put("type", "friends.list").put("token", tok))
        sendRaw(JSONObject().put("type", "chat.history").put("token", tok).put("chatId", 1))
    }

    private fun sendRaw(o: JSONObject) {
        try { ws.send(o.toString()) }
        catch (e: Exception) { Log.w("MaseEngine", "send fail", e) }
    }

    private suspend fun handleServerLine(line: String) {
        val o = runCatching { JSONObject(line) }.getOrNull() ?: return
        when (o.optString("type")) {
            "auth.session" -> {
                val token = o.optString("token")
                val user = o.optJSONObject("user") ?: return
                sessionRepository.setSession(token, user.toString(), user.optString("phone"))
                currentToken = token
                _authError.value = null
                _authPending.value = false
                _authPhase.value =
                    if (PublicUserProfile.fromJson(user).displayName.isBlank()) AuthPhase.Profile else AuthPhase.App
                sendRaw(JSONObject().put("type", "friends.list").put("token", token))
                sendRaw(JSONObject().put("type", "chat.history").put("token", token).put("chatId", 1))
            }
            "friends.list" -> {
                val arr = o.optJSONArray("friends") ?: return
                _friends.value = parseUsers(arr)
            }
            "friends.ok" -> {
                val tok = currentToken ?: return
                sendRaw(JSONObject().put("type", "friends.list").put("token", tok))
            }
            "chat.history" -> {
                val cid = o.optLong("chatId")
                val arr = o.optJSONArray("messages") ?: return
                withContext(Dispatchers.IO) {
                    for (i in 0 until arr.length()) {
                        val m = arr.optJSONObject(i) ?: continue
                        db.messageDao().insert(MessageEntity(
                            id = m.optLong("id"),
                            chatId = cid,
                            senderId = m.optLong("senderId"),
                            body = m.optString("body"),
                            ts = m.optLong("ts"),
                            status = m.optString("status", "sent"),
                            msgType = m.optString("msgType", "text"),
                            mediaId = m.optString("mediaId", "")
                        ))
                    }
                    if (arr.length() > 0) {
                        val last = arr.getJSONObject(arr.length() - 1)
                        val snippet = if (last.optString("msgType") == "photo") "Фото"
                                      else last.optString("body")
                        if (cid == 1L) {
                            db.chatDao().upsert(ChatEntity(
                                chatId = 1L, kind = "global", title = "Общий чат",
                                peerUserId = null, lastSnippet = snippet, lastTs = last.optLong("ts")
                            ))
                        }
                    }
                }
            }
            "evt.message" -> {
                val m = o.optJSONObject("message") ?: return
                val id = m.optLong("id")
                val cid = m.optLong("chatId")
                val sender = m.optLong("senderId")
                val body = m.optString("body")
                val ts = m.optLong("ts")
                val st = m.optString("status", "sent")
                val msgType = m.optString("msgType", "text")
                val mediaId = m.optString("mediaId", "")
                val otherUserId = m.optLong("otherUserId")
                val myId = sessionRepository.snapshot.first().userId

                val senderName = m.optString("senderName", "")
                withContext(Dispatchers.IO) {
                    db.messageDao().insert(MessageEntity(id, cid, sender, body, ts, st, msgType, mediaId, senderName))
                    val snippet = if (msgType == "photo") "Фото" else body

                    val existing = db.chatDao().getById(cid)
                    when {
                        cid == 1L -> {
                            db.chatDao().upsert(ChatEntity(
                                chatId = 1L, kind = "global", title = "Общий чат",
                                peerUserId = null, lastSnippet = snippet, lastTs = ts
                            ))
                        }
                        existing?.kind == "group" -> {
                            // Group chat — just update snippet
                            db.chatDao().upsert(existing.copy(lastSnippet = snippet, lastTs = ts))
                            if (activeChatId != cid && sender != myId) {
                                db.chatDao().incrementUnread(cid)
                            }
                        }
                        else -> {
                            // Direct chat
                            val peerId = when {
                                otherUserId > 0L -> otherUserId
                                sender != myId   -> sender
                                else             -> null
                            }
                            val peerProfile = peerId?.let { pid ->
                                _friends.value.firstOrNull { it.id == pid }
                            }
                            val peerName = peerProfile?.displayName?.ifBlank { "@${peerProfile.username}" } ?: "Диалог"
                            val resolvedTitle = if (existing != null && existing.title != "Диалог")
                                existing.title else peerName
                            db.chatDao().upsert(ChatEntity(
                                chatId = cid, kind = "direct",
                                title = resolvedTitle, peerDisplayName = peerName,
                                peerUserId = peerId, lastSnippet = snippet, lastTs = ts,
                                unreadCount = existing?.unreadCount ?: 0
                            ))
                            if (activeChatId != cid && sender != myId) {
                                db.chatDao().incrementUnread(cid)
                            }
                        }
                    }
                }

                val tok = currentToken
                if (tok != null && cid != 1L && myId != sender) {
                    sendRaw(JSONObject().put("type", "chat.receipt")
                        .put("token", tok).put("messageId", id))
                }
            }
            "evt.receipt" -> {
                val mid = o.optLong("messageId")
                val st = o.optString("status", "delivered")
                withContext(Dispatchers.IO) { db.messageDao().updateStatus(mid, st) }
            }
            "evt.presence" -> {
                val arr = o.optJSONArray("online") ?: return
                _presence.value = parseUsers(arr)
            }
            "evt.typing" -> {
                val userId = o.optLong("userId")
                val chatId = o.optLong("chatId")
                val isTyping = o.optBoolean("isTyping", false)
                val myId = sessionRepository.snapshot.first().userId
                if (userId == myId) return
                _typingInChat.value = _typingInChat.value.toMutableMap().apply {
                    val current = this[chatId]?.toMutableSet() ?: mutableSetOf()
                    if (isTyping) current.add(userId) else current.remove(userId)
                    if (current.isEmpty()) remove(chatId) else this[chatId] = current
                }
                if (isTyping) {
                    // Auto-clear after 5s if no update
                    typingJobsMutex.lock()
                    val oldJob = typingStopJobs[userId]
                    typingJobsMutex.unlock()
                    oldJob?.cancel()
                    val job = scope.launch {
                        delay(5_000)
                        _typingInChat.value = _typingInChat.value.toMutableMap().apply {
                            val s = this[chatId]?.toMutableSet() ?: return@apply
                            s.remove(userId)
                            if (s.isEmpty()) remove(chatId) else this[chatId] = s
                        }
                        typingJobsMutex.lock()
                        typingStopJobs.remove(userId)
                        typingJobsMutex.unlock()
                    }
                    typingJobsMutex.lock()
                    typingStopJobs[userId] = job
                    typingJobsMutex.unlock()
                }
            }
            "evt.profile" -> {
                val tok = currentToken ?: return
                sendRaw(JSONObject().put("type", "friends.list").put("token", tok))
            }
            "profile.updated" -> {
                val user = o.optJSONObject("user") ?: return
                sessionRepository.updateUserJson(user.toString())
            }
            "chat.open" -> {
                val cid = o.optLong("chatId")
                val peer = o.optJSONObject("peer") ?: return
                val p = PublicUserProfile.fromJson(peer)
                val peerName = p.displayName.ifBlank { "@${p.username}" }
                withContext(Dispatchers.IO) {
                    db.chatDao().upsert(ChatEntity(
                        chatId = cid, kind = "direct",
                        title = peerName, peerDisplayName = peerName,
                        peerUserId = p.id,
                        lastSnippet = "", lastTs = System.currentTimeMillis()
                    ))
                }
                val tok = currentToken ?: return
                sendRaw(JSONObject().put("type", "chat.history").put("token", tok).put("chatId", cid))
                _navigateToChatId.value = cid
            }
            "chat.group.created", "chat.group.opened" -> {
                val cid = o.optLong("chatId")
                val g = o.optJSONObject("group") ?: return
                val title = g.optString("title", "Группа")
                withContext(Dispatchers.IO) {
                    db.chatDao().upsert(ChatEntity(
                        chatId = cid, kind = "group",
                        title = title, peerUserId = null,
                        lastSnippet = "", lastTs = System.currentTimeMillis()
                    ))
                }
                if (o.optString("type") == "chat.group.created") {
                    val tok = currentToken ?: return
                    sendRaw(JSONObject().put("type", "chat.history").put("token", tok).put("chatId", cid))
                    _navigateToChatId.value = cid
                }
            }
            "group.info" -> {
                // handled by consumers (GroupInfoScreen)
            }
            "group.member_added", "group.member_left" -> {
                // optional: could update local member list cache
            }
            "user.lookup" -> {
                val user = o.optJSONObject("user") ?: return
                _lastLookupUser.value = PublicUserProfile.fromJson(user)
            }
            "err" -> {
                val code = o.optString("code")
                Log.w("MaseEngine", "server err: $code")
                _authPending.value = false
                val msg = when (code) {
                    "bad_credentials"  -> "Неверный номер или пароль"
                    "phone_taken"      -> "Этот номер уже зарегистрирован"
                    "username_taken"   -> "Это имя пользователя уже занято"
                    "bad_request"      -> "Проверьте введённые данные"
                    "auth_required"    -> "Ошибка соединения, попробуйте снова"
                    "unknown_type"     -> "Ошибка соединения, попробуйте снова"
                    "session_expired"  -> null  // тихо сбрасываем сессию
                    else               -> null
                }
                if (msg != null) _authError.value = msg
            }
        }
    }

    private val _lastLookupUser = MutableStateFlow<PublicUserProfile?>(null)
    val lastLookupUser: StateFlow<PublicUserProfile?> = _lastLookupUser.asStateFlow()
    fun clearLookupUser() { _lastLookupUser.value = null }

    private fun parseUsers(arr: JSONArray): List<PublicUserProfile> = buildList {
        for (i in 0 until arr.length()) {
            val o = arr.optJSONObject(i) ?: continue
            add(PublicUserProfile.fromJson(o))
        }
    }

    fun showRegister() { _authPhase.value = AuthPhase.Register }
    fun showLogin()    { _authPhase.value = AuthPhase.Login }

    fun login(phone: String, password: String) {
        _authError.value = null
        _authPending.value = true
        sendRaw(JSONObject().put("type", "auth.login")
            .put("phone", phone.trim())
            .put("password", password))
    }

    fun register(phone: String, password: String, displayName: String, username: String) {
        _authError.value = null
        _authPending.value = true
        sendRaw(JSONObject().put("type", "auth.register")
            .put("phone", phone.trim())
            .put("password", password)
            .put("displayName", displayName.trim())
            .put("username", username.trim().lowercase()))
    }

    fun completeProfile(displayName: String, username: String, bio: String) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "profile.update").put("token", tok)
            .put("displayName", displayName.trim())
            .put("username", username.trim().lowercase())
            .put("bio", bio.trim()))
    }

    fun updateProfile(displayName: String, username: String, bio: String) =
        completeProfile(displayName, username, bio)

    fun sendGlobalMessage(text: String) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "chat.global.send").put("token", tok).put("body", text.trim()))
    }

    fun sendDirectMessage(chatId: Long, peerUserId: Long, text: String) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "chat.direct.send").put("token", tok)
            .put("peerUserId", peerUserId).put("body", text.trim()))
    }

    fun sendPhoto(peerUserId: Long, uri: Uri) {
        val tok = currentToken ?: return
        scope.launch {
            _photoUploadState.value = PhotoUploadState.Uploading
            val mediaId = MediaUploader.uploadPhoto(app, BuildConfig.MEDIA_URL, tok, uri)
            if (mediaId == null) {
                _photoUploadState.value = PhotoUploadState.Error("Не удалось загрузить фото")
                return@launch
            }
            sendRaw(JSONObject().put("type", "chat.photo.send").put("token", tok)
                .put("peerUserId", peerUserId).put("mediaId", mediaId))
            _photoUploadState.value = PhotoUploadState.Idle
        }
    }

    fun clearPhotoUploadError() { _photoUploadState.value = PhotoUploadState.Idle }

    // Typing indicator: called from UI on input change (debounce handled by UI)
    fun sendTyping(chatId: Long) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "typing").put("token", tok)
            .put("chatId", chatId).put("isTyping", 1))
    }

    fun uploadAvatar(uri: Uri) {
        val tok = currentToken ?: return
        scope.launch {
            _photoUploadState.value = PhotoUploadState.Uploading
            val mediaId = MediaUploader.uploadPhoto(app, BuildConfig.MEDIA_URL, tok, uri)
            if (mediaId == null) {
                _photoUploadState.value = PhotoUploadState.Error("Не удалось загрузить аватар")
                return@launch
            }
            sendRaw(JSONObject().put("type", "profile.set_avatar").put("token", tok)
                .put("avatarMediaId", mediaId))
            _photoUploadState.value = PhotoUploadState.Idle
        }
    }

    fun openDirectChat(peerUserId: Long) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "chat.direct.open").put("token", tok).put("peerUserId", peerUserId))
    }

    fun createGroup(title: String, memberIds: List<Long>) {
        val tok = currentToken ?: return
        val arr = JSONArray().apply { memberIds.forEach { put(it) } }
        sendRaw(JSONObject().put("type", "chat.group.create").put("token", tok)
            .put("title", title.trim()).put("memberIds", arr))
    }

    fun sendGroupMessage(chatId: Long, text: String) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "chat.group.send").put("token", tok)
            .put("chatId", chatId).put("body", text.trim()))
    }

    fun sendGroupPhoto(chatId: Long, uri: android.net.Uri) {
        val tok = currentToken ?: return
        scope.launch {
            _photoUploadState.value = PhotoUploadState.Uploading
            val mediaId = MediaUploader.uploadPhoto(app, BuildConfig.MEDIA_URL, tok, uri)
            if (mediaId == null) {
                _photoUploadState.value = PhotoUploadState.Error("Не удалось загрузить фото")
                return@launch
            }
            sendRaw(JSONObject().put("type", "chat.group.photo").put("token", tok)
                .put("chatId", chatId).put("mediaId", mediaId))
            _photoUploadState.value = PhotoUploadState.Idle
        }
    }

    fun addGroupMember(chatId: Long, userId: Long) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "group.add_member").put("token", tok)
            .put("chatId", chatId).put("userId", userId))
    }

    fun leaveGroup(chatId: Long) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "group.leave").put("token", tok).put("chatId", chatId))
        scope.launch(Dispatchers.IO) { db.chatDao().deleteById(chatId) }
    }

    fun requestGroupInfo(chatId: Long) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "group.info").put("token", tok).put("chatId", chatId))
    }

    fun addFriend(userId: Long) {
        val tok = currentToken ?: return
        sendRaw(JSONObject().put("type", "friends.add").put("token", tok).put("friendUserId", userId))
    }

    fun lookupUsername(username: String) {
        val tok = currentToken ?: return
        _lastLookupUser.value = null
        sendRaw(JSONObject().put("type", "user.lookup").put("token", tok).put("username", username.lowercase()))
    }

    fun markChatRead(chatId: Long) {
        scope.launch(Dispatchers.IO) { db.chatDao().resetUnread(chatId) }
    }

    fun setDarkTheme(v: Boolean) { scope.launch { sessionRepository.setDarkTheme(v) } }

    fun logout() {
        scope.launch {
            ws.close()
            sessionRepository.logout()
            currentToken = null
            _friends.value = emptyList()
            _presence.value = emptyList()
            _authPhase.value = AuthPhase.Login
        }
    }

    fun inviteLinkFor(username: String): String =
        InviteLink.build(BuildConfig.INVITE_SCHEME, username)
}

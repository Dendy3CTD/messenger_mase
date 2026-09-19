package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/mase/server/internal/auth"
	"github.com/mase/server/internal/db"
	"github.com/mase/server/internal/hub"
	"github.com/mase/server/internal/push"
)

// Client is a local alias so handler can call hub methods.
type connCtx struct {
	client *hub.Client
}

// Handle is the entry point for every WebSocket message.
func Handle(c *hub.Client, raw []byte) {
	var base struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		sendTo(c, errMsg("bad_json"))
		return
	}

	msgType := base.Type
	token := base.Token

	// ── Unauthenticated routes ────────────────────────────────────────────────
	switch msgType {
	case "auth.login":
		handleLogin(c, raw)
		return
	case "auth.register":
		handleRegister(c, raw)
		return
	case "auth.resume":
		handleResume(c, token)
		return
	}

	// ── Require auth ─────────────────────────────────────────────────────────
	if c.UserID == 0 {
		// Try token from message
		if token == "" {
			sendTo(c, errMsg("auth_required"))
			return
		}
		uid, err := db.GetUserIDByToken(token)
		if err != nil {
			sendTo(c, errMsg("auth_required"))
			return
		}
		hub.H.Authenticate(c, uid, token)
		pushPresence()
	}

	uid := c.UserID

	switch msgType {
	case "ping":
		sendTo(c, `{"type":"pong"}`)
	case "profile.update":
		handleProfileUpdate(c, uid, raw)
	case "profile.set_avatar":
		handleSetAvatar(c, uid, raw)
	case "friends.add":
		handleFriendsAdd(c, uid, raw)
	case "friends.list":
		handleFriendsList(c, uid)
	case "user.lookup":
		handleUserLookup(c, raw)
	case "chat.global.send":
		handleGlobalSend(c, uid, raw)
	case "chat.direct.send":
		handleDirectSend(c, uid, raw)
	case "chat.direct.open":
		handleDirectOpen(c, uid, raw)
	case "chat.photo.send":
		handlePhotoSend(c, uid, raw)
	case "chat.history":
		handleHistory(c, uid, raw)
	case "chat.receipt":
		handleReceipt(c, uid, raw)
	case "chat.message.delete":
		handleDeleteMessage(c, uid, raw)
	case "chat.message.edit":
		handleEditMessage(c, uid, raw)
	case "typing":
		handleTyping(c, uid, raw)
	case "device.register_push":
		handleRegisterPush(c, uid, raw)
	// ── Group chats ──────────────────────────────────────────────────────────
	case "chat.group.create":
		handleGroupCreate(c, uid, raw)
	case "chat.group.send":
		handleGroupSend(c, uid, raw)
	case "chat.group.photo":
		handleGroupPhotoSend(c, uid, raw)
	case "chat.group.open":
		handleGroupOpen(c, uid, raw)
	case "group.add_member":
		handleGroupAddMember(c, uid, raw)
	case "group.leave":
		handleGroupLeave(c, uid, raw)
	case "group.info":
		handleGroupInfo(c, uid, raw)
	case "group.list":
		handleGroupList(c, uid)
	default:
		sendTo(c, errMsg("unknown_type"))
	}
}

// OnClose is called when a client disconnects.
func OnClose(c *hub.Client) {
	if c.UserID != 0 {
		log.Printf("[HUB] user %d disconnected", c.UserID)
		pushPresence()
	}
}

// ─── Auth handlers ────────────────────────────────────────────────────────────

func handleLogin(c *hub.Client, raw []byte) {
	var req struct {
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || len(req.Phone) < 4 || req.Password == "" {
		sendTo(c, errMsg("bad_request"))
		return
	}

	_, uid, storedHash, err := db.GetUserByPhone(req.Phone)
	if err != nil {
		log.Printf("[AUTH] login fail phone=%s reason=not_found", req.Phone)
		sendTo(c, errMsg("bad_credentials"))
		return
	}
	if auth.HashPassword(req.Password) != storedHash {
		log.Printf("[AUTH] login fail phone=%s reason=wrong_password", req.Phone)
		sendTo(c, errMsg("bad_credentials"))
		return
	}

	log.Printf("[AUTH] login OK phone=%s uid=%d", req.Phone, uid)
	issueSession(c, uid)
}

func handleRegister(c *hub.Client, raw []byte) {
	var req struct {
		Phone       string `json:"phone"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
		Username    string `json:"username"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || len(req.Phone) < 4 || len(req.Password) < 4 {
		sendTo(c, errMsg("bad_request"))
		return
	}

	phoneTaken, _ := db.PhoneExists(req.Phone)
	if phoneTaken {
		sendTo(c, errMsg("phone_taken"))
		return
	}

	username := strings.ToLower(req.Username)
	if username == "" {
		username = "u" + auth.RandomDigits(7)
	}

	usernameTaken, _ := db.UsernameExists(username)
	if usernameTaken {
		sendTo(c, errMsg("username_taken"))
		return
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = username
	}

	uid, err := db.CreateUser(req.Phone, displayName, username, auth.HashPassword(req.Password))
	if err != nil {
		log.Printf("[AUTH] register fail: %v", err)
		sendTo(c, errMsg("server_error"))
		return
	}

	log.Printf("[AUTH] register OK phone=%s username=%s uid=%d", req.Phone, username, uid)
	issueSession(c, uid)
}

func handleResume(c *hub.Client, token string) {
	if token == "" {
		sendTo(c, errMsg("bad_token"))
		return
	}
	uid, err := db.GetUserIDByToken(token)
	if err != nil {
		sendTo(c, errMsg("session_expired"))
		return
	}
	hub.H.Authenticate(c, uid, token)

	u, err := db.GetUserByID(uid)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	sendTo(c, fmt.Sprintf(`{"type":"auth.session","token":%s,"user":%s}`,
		jsonStr(token), userJSON(u)))
	pushPresence()
}

func issueSession(c *hub.Client, uid int64) {
	token, err := auth.NewToken()
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	if err := db.StoreToken(token, uid); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	hub.H.Authenticate(c, uid, token)

	u, err := db.GetUserByID(uid)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	sendTo(c, fmt.Sprintf(`{"type":"auth.session","token":%s,"user":%s}`,
		jsonStr(token), userJSON(u)))
	pushPresence()
}

// ─── Profile ─────────────────────────────────────────────────────────────────

func handleProfileUpdate(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		DisplayName string `json:"displayName"`
		Username    string `json:"username"`
		Bio         string `json:"bio"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		sendTo(c, errMsg("bad_request"))
		return
	}
	username := strings.ToLower(req.Username)
	if username != "" {
		existing, _ := db.GetUserByUsername(username)
		if existing != nil && existing.ID != uid {
			sendTo(c, errMsg("username_taken"))
			return
		}
	}
	if err := db.UpdateProfile(uid, req.DisplayName, username, req.Bio); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	u, _ := db.GetUserByID(uid)
	// Broadcast profile update to all
	hub.H.BroadcastAuthenticated([]byte(fmt.Sprintf(`{"type":"evt.profile","user":%s}`, userJSON(u))), -1)
}

func handleSetAvatar(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		AvatarMediaID string `json:"avatarMediaId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.AvatarMediaID == "" {
		sendTo(c, errMsg("bad_request"))
		return
	}
	if err := db.UpdateAvatarMediaID(uid, req.AvatarMediaID); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	u, _ := db.GetUserByID(uid)
	hub.H.BroadcastAuthenticated([]byte(fmt.Sprintf(`{"type":"evt.profile","user":%s}`, userJSON(u))), -1)
}

// ─── Friends ─────────────────────────────────────────────────────────────────

func handleFriendsAdd(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		FriendUserID int64 `json:"friendUserId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.FriendUserID == 0 || req.FriendUserID == uid {
		sendTo(c, errMsg("bad_request"))
		return
	}
	if err := db.AddFriend(uid, req.FriendUserID); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	handleFriendsList(c, uid)
}

func handleFriendsList(c *hub.Client, uid int64) {
	friends, err := db.GetFriends(uid)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	parts := make([]string, len(friends))
	for i, u := range friends {
		u2 := u
		parts[i] = userJSON(&u2)
	}
	sendTo(c, fmt.Sprintf(`{"type":"friends.list","friends":[%s]}`, strings.Join(parts, ",")))
}

// ─── User lookup ─────────────────────────────────────────────────────────────

func handleUserLookup(c *hub.Client, raw []byte) {
	var req struct {
		Username string `json:"username"`
		UserID   int64  `json:"userId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		sendTo(c, errMsg("bad_request"))
		return
	}
	var u *db.User
	var err error
	if req.Username != "" {
		u, err = db.GetUserByUsername(strings.ToLower(req.Username))
	} else if req.UserID != 0 {
		u, err = db.GetUserByID(req.UserID)
	} else {
		sendTo(c, errMsg("bad_request"))
		return
	}
	if err != nil {
		sendTo(c, errMsg("not_found"))
		return
	}
	sendTo(c, fmt.Sprintf(`{"type":"user.lookup","user":%s}`, userJSON(u)))
}

// ─── Chat: global ────────────────────────────────────────────────────────────

func handleGlobalSend(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Body == "" {
		return
	}
	ts := auth.NowMilli()
	mid, err := db.InsertMessage(db.GlobalChatID, uid, req.Body, "text", "", ts, 0, 0)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	msg := buildMsgJSON(mid, db.GlobalChatID, uid, 0, req.Body, "text", "", ts, "sent", 0, 0, 0)
	hub.H.BroadcastAuthenticated([]byte(fmt.Sprintf(`{"type":"evt.message","message":%s}`, msg)), -1)
}

// ─── Chat: direct ─────────────────────────────────────────────────────────────

func handleDirectSend(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		PeerUserID int64  `json:"peerUserId"`
		Body       string `json:"body"`
		ReplyToID  int64  `json:"replyToId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Body == "" || req.PeerUserID == 0 {
		sendTo(c, errMsg("bad_dm"))
		return
	}
	chatID, err := db.EnsureDirectChat(uid, req.PeerUserID)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	ts := auth.NowMilli()
	mid, err := db.InsertMessage(chatID, uid, req.Body, "text", "", ts, req.ReplyToID, 0)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}

	msgForSender := buildMsgJSON(mid, chatID, uid, req.PeerUserID, req.Body, "text", "", ts, "sent", req.ReplyToID, 0, 0)
	msgForPeer := buildMsgJSON(mid, chatID, uid, uid, req.Body, "text", "", ts, "sent", req.ReplyToID, 0, 0)

	sendTo(c, fmt.Sprintf(`{"type":"evt.message","message":%s}`, msgForSender))
	hub.H.Send(req.PeerUserID, []byte(fmt.Sprintf(`{"type":"evt.message","message":%s}`, msgForPeer)))

	// Push notification if peer offline
	if !hub.H.IsOnline(req.PeerUserID) {
		sender, _ := db.GetUserByID(uid)
		name := "Mase"
		if sender != nil {
			name = sender.DisplayName
		}
		push.SendNotification(req.PeerUserID, name, req.Body, chatID)
	}
}

func handleDirectOpen(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		PeerUserID int64 `json:"peerUserId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.PeerUserID == 0 || req.PeerUserID == uid {
		sendTo(c, errMsg("bad_peer"))
		return
	}
	chatID, err := db.EnsureDirectChat(uid, req.PeerUserID)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	peer, err := db.GetUserByID(req.PeerUserID)
	if err != nil {
		sendTo(c, errMsg("not_found"))
		return
	}
	sendTo(c, fmt.Sprintf(`{"type":"chat.open","chatId":%d,"peer":%s}`, chatID, userJSON(peer)))
}

func handlePhotoSend(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		PeerUserID int64  `json:"peerUserId"`
		MediaID    string `json:"mediaId"`
		ReplyToID  int64  `json:"replyToId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.MediaID == "" || req.PeerUserID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	chatID, err := db.EnsureDirectChat(uid, req.PeerUserID)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	ts := auth.NowMilli()
	mid, err := db.InsertMessage(chatID, uid, "", "photo", req.MediaID, ts, req.ReplyToID, 0)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}

	msgForSender := buildMsgJSON(mid, chatID, uid, req.PeerUserID, "", "photo", req.MediaID, ts, "sent", req.ReplyToID, 0, 0)
	msgForPeer := buildMsgJSON(mid, chatID, uid, uid, "", "photo", req.MediaID, ts, "sent", req.ReplyToID, 0, 0)

	sendTo(c, fmt.Sprintf(`{"type":"evt.message","message":%s}`, msgForSender))
	hub.H.Send(req.PeerUserID, []byte(fmt.Sprintf(`{"type":"evt.message","message":%s}`, msgForPeer)))
}

// ─── Chat: history ───────────────────────────────────────────────────────────

func handleHistory(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID int64 `json:"chatId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ChatID == 0 {
		return
	}
	if req.ChatID != db.GlobalChatID {
		ok, err := db.IsChatMember(req.ChatID, uid)
		if err != nil || !ok {
			sendTo(c, errMsg("forbidden"))
			return
		}
	}
	msgs, err := db.GetHistory(req.ChatID, 200)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = buildMsgJSON(m.ID, m.ChatID, m.SenderID, 0, m.Body, m.MsgType, m.MediaID,
			m.Ts, m.Status, m.ReplyToID, m.EditedAt, m.DurationSec)
	}
	sendTo(c, fmt.Sprintf(`{"type":"chat.history","chatId":%d,"messages":[%s]}`,
		req.ChatID, strings.Join(parts, ",")))
}

// ─── Chat: receipt ───────────────────────────────────────────────────────────

func handleReceipt(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		MessageID int64 `json:"messageId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.MessageID == 0 {
		return
	}
	msgs, _ := db.GetHistory(0, 0) // just to find sender — simplified
	_ = msgs
	db.UpdateMessageStatus(req.MessageID, "read")
	// Ideally we'd notify the sender. Simplified: broadcast receipt.
	hub.H.BroadcastAuthenticated(
		[]byte(fmt.Sprintf(`{"type":"evt.receipt","messageId":%d,"status":"read"}`, req.MessageID)),
		uid)
}

// ─── Message operations ───────────────────────────────────────────────────────

func handleDeleteMessage(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		MessageID int64 `json:"messageId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.MessageID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	if err := db.DeleteMessage(req.MessageID, uid); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	hub.H.BroadcastAuthenticated(
		[]byte(fmt.Sprintf(`{"type":"evt.message.deleted","messageId":%d}`, req.MessageID)), -1)
}

func handleEditMessage(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		MessageID int64  `json:"messageId"`
		Body      string `json:"body"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.MessageID == 0 || req.Body == "" {
		sendTo(c, errMsg("bad_request"))
		return
	}
	if err := db.EditMessage(req.MessageID, uid, req.Body); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	hub.H.BroadcastAuthenticated(
		[]byte(fmt.Sprintf(`{"type":"evt.message.edited","messageId":%d,"body":%s,"editedAt":%d}`,
			req.MessageID, jsonStr(req.Body), auth.NowMilli())), -1)
}

// ─── Typing ───────────────────────────────────────────────────────────────────

func handleTyping(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID   int64 `json:"chatId"`
		IsTyping bool  `json:"isTyping"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return
	}
	msg := fmt.Sprintf(`{"type":"evt.typing","userId":%d,"chatId":%d,"isTyping":%v}`,
		uid, req.ChatID, req.IsTyping)
	hub.H.BroadcastAuthenticated([]byte(msg), uid)
}

// ─── Push token ───────────────────────────────────────────────────────────────

func handleRegisterPush(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		FCMToken string `json:"fcmToken"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.FCMToken == "" {
		return
	}
	db.UpsertPushToken(uid, req.FCMToken)
	log.Printf("[PUSH] registered token for uid=%d", uid)
}

// ─── Group chats ─────────────────────────────────────────────────────────────

func handleGroupCreate(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		Title     string  `json:"title"`
		MemberIDs []int64 `json:"memberIds"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || strings.TrimSpace(req.Title) == "" {
		sendTo(c, errMsg("bad_request"))
		return
	}
	chatID, err := db.CreateGroup(uid, strings.TrimSpace(req.Title), req.MemberIDs)
	if err != nil {
		log.Printf("[GROUP] create fail: %v", err)
		sendTo(c, errMsg("server_error"))
		return
	}
	g, _ := db.GetGroup(chatID)
	members, _ := db.GetGroupMembers(chatID)
	groupJ := groupJSON(g)
	membersJ := usersJSON(members)

	// Notify all members (including creator)
	memberIDs, _ := db.GetGroupMemberIDs(chatID)
	msg := fmt.Sprintf(`{"type":"chat.group.created","chatId":%d,"group":%s,"members":[%s]}`,
		chatID, groupJ, membersJ)
	for _, mid := range memberIDs {
		hub.H.Send(mid, []byte(msg))
	}
	log.Printf("[GROUP] created chatId=%d title=%q creator=%d members=%d", chatID, g.Title, uid, len(memberIDs))
}

func handleGroupSend(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID    int64  `json:"chatId"`
		Body      string `json:"body"`
		ReplyToID int64  `json:"replyToId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Body == "" || req.ChatID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	ok, err := db.IsGroupMember(req.ChatID, uid)
	if err != nil || !ok {
		sendTo(c, errMsg("forbidden"))
		return
	}
	ts := auth.NowMilli()
	mid, err := db.InsertMessage(req.ChatID, uid, req.Body, "text", "", ts, req.ReplyToID, 0)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	broadcastGroupMessage(req.ChatID, uid, mid, req.Body, "text", "", ts, req.ReplyToID)
}

func handleGroupPhotoSend(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID    int64  `json:"chatId"`
		MediaID   string `json:"mediaId"`
		ReplyToID int64  `json:"replyToId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.MediaID == "" || req.ChatID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	ok, err := db.IsGroupMember(req.ChatID, uid)
	if err != nil || !ok {
		sendTo(c, errMsg("forbidden"))
		return
	}
	ts := auth.NowMilli()
	mid, err := db.InsertMessage(req.ChatID, uid, "", "photo", req.MediaID, ts, req.ReplyToID, 0)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	broadcastGroupMessage(req.ChatID, uid, mid, "", "photo", req.MediaID, ts, req.ReplyToID)
}

func handleGroupOpen(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID int64 `json:"chatId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ChatID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	ok, err := db.IsGroupMember(req.ChatID, uid)
	if err != nil || !ok {
		sendTo(c, errMsg("forbidden"))
		return
	}
	g, err := db.GetGroup(req.ChatID)
	if err != nil {
		sendTo(c, errMsg("not_found"))
		return
	}
	members, _ := db.GetGroupMembers(req.ChatID)
	sendTo(c, fmt.Sprintf(`{"type":"chat.group.opened","chatId":%d,"group":%s,"members":[%s]}`,
		req.ChatID, groupJSON(g), usersJSON(members)))
	// Load history
	tok := c.Token
	_ = tok
	msgs, _ := db.GetHistory(req.ChatID, 200)
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = buildMsgJSON(m.ID, m.ChatID, m.SenderID, 0, m.Body, m.MsgType, m.MediaID,
			m.Ts, m.Status, m.ReplyToID, m.EditedAt, m.DurationSec)
	}
	sendTo(c, fmt.Sprintf(`{"type":"chat.history","chatId":%d,"messages":[%s]}`,
		req.ChatID, strings.Join(parts, ",")))
}

func handleGroupAddMember(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID int64 `json:"chatId"`
		UserID int64 `json:"userId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ChatID == 0 || req.UserID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	ok, err := db.IsGroupMember(req.ChatID, uid)
	if err != nil || !ok {
		sendTo(c, errMsg("forbidden"))
		return
	}
	if err := db.AddGroupMember(req.ChatID, req.UserID); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	memberIDs, _ := db.GetGroupMemberIDs(req.ChatID)
	msg := fmt.Sprintf(`{"type":"group.member_added","chatId":%d,"userId":%d}`, req.ChatID, req.UserID)
	for _, mid := range memberIDs {
		hub.H.Send(mid, []byte(msg))
	}
}

func handleGroupLeave(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID int64 `json:"chatId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ChatID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	memberIDs, _ := db.GetGroupMemberIDs(req.ChatID)
	if err := db.RemoveGroupMember(req.ChatID, uid); err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	msg := fmt.Sprintf(`{"type":"group.member_left","chatId":%d,"userId":%d}`, req.ChatID, uid)
	for _, mid := range memberIDs {
		hub.H.Send(mid, []byte(msg))
	}
}

func handleGroupInfo(c *hub.Client, uid int64, raw []byte) {
	var req struct {
		ChatID int64 `json:"chatId"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ChatID == 0 {
		sendTo(c, errMsg("bad_request"))
		return
	}
	ok, err := db.IsGroupMember(req.ChatID, uid)
	if err != nil || !ok {
		sendTo(c, errMsg("forbidden"))
		return
	}
	g, err := db.GetGroup(req.ChatID)
	if err != nil {
		sendTo(c, errMsg("not_found"))
		return
	}
	members, _ := db.GetGroupMembers(req.ChatID)
	sendTo(c, fmt.Sprintf(`{"type":"group.info","chatId":%d,"group":%s,"members":[%s]}`,
		req.ChatID, groupJSON(g), usersJSON(members)))
}

func handleGroupList(c *hub.Client, uid int64) {
	groups, err := db.GetUserGroups(uid)
	if err != nil {
		sendTo(c, errMsg("server_error"))
		return
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		g2 := g
		parts[i] = groupJSON(&g2)
	}
	sendTo(c, fmt.Sprintf(`{"type":"group.list","groups":[%s]}`, strings.Join(parts, ",")))
}

// broadcastGroupMessage sends evt.message to all group members.
func broadcastGroupMessage(chatID, senderID, msgID int64, body, msgType, mediaID string, ts int64, replyToID int64) {
	sender, _ := db.GetUserByID(senderID)
	senderName := ""
	if sender != nil {
		senderName = sender.DisplayName
	}
	memberIDs, _ := db.GetGroupMemberIDs(chatID)
	for _, mid := range memberIDs {
		msgJ := buildGroupMsgJSON(msgID, chatID, senderID, senderName, body, msgType, mediaID, ts, "sent", replyToID)
		hub.H.Send(mid, []byte(fmt.Sprintf(`{"type":"evt.message","message":%s}`, msgJ)))
	}
}

func buildGroupMsgJSON(id, chatID, senderID int64, senderName, body, msgType, mediaID string,
	ts int64, status string, replyToID int64) string {

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		`{"id":%d,"chatId":%d,"senderId":%d,"senderName":%s`,
		id, chatID, senderID, jsonStr(senderName)))
	sb.WriteString(fmt.Sprintf(`,"body":%s,"msgType":%s,"mediaId":%s,"ts":%d,"status":%s`,
		jsonStr(body), jsonStr(msgType), jsonStr(mediaID), ts, jsonStr(status)))
	if replyToID != 0 {
		sb.WriteString(fmt.Sprintf(`,"replyToId":%d`, replyToID))
	}
	sb.WriteString("}")
	return sb.String()
}

func groupJSON(g *db.Group) string {
	if g == nil {
		return "null"
	}
	return fmt.Sprintf(`{"id":%d,"title":%s,"avatarMediaId":%s,"createdBy":%d}`,
		g.ID, jsonStr(g.Title), jsonStr(g.AvatarMediaID), g.CreatedBy)
}

func usersJSON(users []db.User) string {
	parts := make([]string, len(users))
	for i, u := range users {
		u2 := u
		parts[i] = userJSON(&u2)
	}
	return strings.Join(parts, ",")
}

// ─── Presence ─────────────────────────────────────────────────────────────────

func pushPresence() {
	ids := hub.H.OnlineUserIDs()
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	msg := fmt.Sprintf(`{"type":"evt.presence","online":[%s]}`, strings.Join(parts, ","))
	hub.H.BroadcastAuthenticated([]byte(msg), -1)
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

func userJSON(u *db.User) string {
	if u == nil {
		return "null"
	}
	return fmt.Sprintf(
		`{"id":%d,"displayName":%s,"username":%s,"bio":%s,"avatarMediaId":%s}`,
		u.ID, jsonStr(u.DisplayName), jsonStr(u.Username), jsonStr(u.Bio), jsonStr(u.AvatarMediaID),
	)
}

func buildMsgJSON(id, chatID, senderID, otherUserID int64, body, msgType, mediaID string,
	ts int64, status string, replyToID, editedAt int64, durationSec int) string {

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`{"id":%d,"chatId":%d,"senderId":%d`, id, chatID, senderID))
	if otherUserID != 0 {
		sb.WriteString(fmt.Sprintf(`,"otherUserId":%d`, otherUserID))
	}
	sb.WriteString(fmt.Sprintf(`,"body":%s,"msgType":%s,"mediaId":%s,"ts":%d,"status":%s`,
		jsonStr(body), jsonStr(msgType), jsonStr(mediaID), ts, jsonStr(status)))
	if replyToID != 0 {
		sb.WriteString(fmt.Sprintf(`,"replyToId":%d`, replyToID))
	}
	if editedAt != 0 {
		sb.WriteString(fmt.Sprintf(`,"editedAt":%d`, editedAt))
	}
	if durationSec > 0 {
		sb.WriteString(fmt.Sprintf(`,"durationSec":%d`, durationSec))
	}
	sb.WriteString("}")
	return sb.String()
}

func errMsg(code string) string {
	return fmt.Sprintf(`{"type":"err","code":%s}`, jsonStr(code))
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func sendTo(c *hub.Client, msg string) {
	hub.H.SendTo(c, []byte(msg))
}

package handler_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/mase/server/internal/db"
	"github.com/mase/server/internal/hub"
	"github.com/mase/server/internal/testutil"
)

const quiet = 300 * time.Millisecond

// user registers account number n (1..9) on a new connection.
func user(t *testing.T, s *testutil.Server, n int) *testutil.Client {
	t.Helper()
	d := string(rune('0' + n))
	c := s.Dial(t)
	c.Register("+7000000000"+d, "secret"+d, "User "+d, "user"+d)
	return c
}

func phone(n int) string { return "+7000000000" + string(rune('0'+n)) }
func pass(n int) string  { return "secret" + string(rune('0'+n)) }

func msgOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	m, ok := frame["message"].(map[string]any)
	if !ok {
		t.Fatalf("в кадре нет message: %v", frame)
	}
	return m
}

func num(m map[string]any, k string) int64  { v, _ := m[k].(float64); return int64(v) }
func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func expectErr(t *testing.T, c *testutil.Client, code string) {
	t.Helper()
	if m := c.Recv("err"); m["code"] != code {
		t.Fatalf("ожидалась ошибка %q, получено %v", code, m)
	}
}

func expectNothing(t *testing.T, c *testutil.Client, typ string) {
	t.Helper()
	if m, ok := c.TryRecv(typ, quiet); ok {
		t.Fatalf("неожиданный кадр %q: %v", typ, m)
	}
}

// lastPresence returns the ids of the latest evt.presence frame received within the quiet period.
func lastPresence(c *testutil.Client) map[int64]bool {
	var last map[int64]bool
	for _, f := range c.Drain(quiet) {
		if f["type"] != "evt.presence" {
			continue
		}
		last = map[int64]bool{}
		for _, v := range f["online"].([]any) {
			last[int64(v.(float64))] = true
		}
	}
	return last
}

// ─── Authentication ──────────────────────────────────────────────────────────

func TestRegister(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.Dial(t)
	c.Send(map[string]any{"type": "auth.register", "phone": "+70000000001", "password": "secret1",
		"displayName": "Alice", "username": "AliCe"})
	m := c.Recv("auth.session")
	u := m["user"].(map[string]any)
	if u["username"] != "alice" || u["displayName"] != "Alice" || str(m, "token") == "" {
		t.Fatalf("регистрация: %v", m)
	}
	if _, leaked := u["phone"]; leaked {
		t.Error("телефон не должен уходить в кадре пользователя")
	}

	// пустые username и displayName: имя генерируется, displayName = username
	c2 := s.Dial(t)
	c2.Send(map[string]any{"type": "auth.register", "phone": "+70000000002", "password": "secret2"})
	u2 := c2.Recv("auth.session")["user"].(map[string]any)
	if !regexp.MustCompile(`^u[0-9]{7}$`).MatchString(str(u2, "username")) || u2["displayName"] != u2["username"] {
		t.Fatalf("сгенерированное имя: %v", u2)
	}
}

func TestRegisterRejections(t *testing.T) {
	s := testutil.NewServer(t)
	user(t, s, 1)
	c := s.Dial(t)
	for name, req := range map[string]map[string]any{
		"короткий телефон": {"type": "auth.register", "phone": "+7", "password": "secret1"},
		"короткий пароль":  {"type": "auth.register", "phone": "+70000000002", "password": "abc"},
	} {
		c.Send(req)
		if m := c.Recv("err"); m["code"] != "bad_request" {
			t.Errorf("%s: %v", name, m)
		}
	}
	c.Send(map[string]any{"type": "auth.register", "phone": phone(1), "password": "secret1"})
	expectErr(t, c, "phone_taken")
	c.Send(map[string]any{"type": "auth.register", "phone": "+70000000003", "password": "secret3", "username": "USER1"})
	expectErr(t, c, "username_taken")
}

func TestLogin(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)

	c := s.Dial(t)
	c.Login(phone(1), pass(1))
	if c.ID != a.ID {
		t.Fatalf("вход дал другого пользователя: %d и %d", c.ID, a.ID)
	}

	bad := s.Dial(t)
	bad.Send(map[string]any{"type": "auth.login", "phone": phone(1), "password": "wrong"})
	expectErr(t, bad, "bad_credentials")
	bad.Send(map[string]any{"type": "auth.login", "phone": "+79999999999", "password": "whatever"})
	expectErr(t, bad, "bad_credentials") // неизвестный телефон неотличим от неверного пароля
	bad.Send(map[string]any{"type": "auth.login", "phone": "+7", "password": "x"})
	expectErr(t, bad, "bad_request")
	bad.Send(map[string]any{"type": "auth.login", "phone": phone(1), "password": ""})
	expectErr(t, bad, "bad_request")
}

func TestResume(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)

	c := s.Dial(t)
	c.Send(map[string]any{"type": "auth.resume", "token": a.Token})
	m := c.Recv("auth.session")
	if str(m, "token") != a.Token || num(m["user"].(map[string]any), "id") != a.ID {
		t.Fatalf("resume: %v", m)
	}
	c.Send(map[string]any{"type": "ping"})
	c.Recv("pong")

	bad := s.Dial(t)
	bad.Send(map[string]any{"type": "auth.resume", "token": "no-such-token"})
	expectErr(t, bad, "session_expired")
	bad.Send(map[string]any{"type": "auth.resume", "token": ""})
	expectErr(t, bad, "bad_token")
}

func TestAuthRequiredAndTokenInFrame(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)

	c := s.Dial(t)
	c.Send(map[string]any{"type": "chat.global.send", "body": "hi"})
	expectErr(t, c, "auth_required")
	c.Send(map[string]any{"type": "ping", "token": "bogus"})
	expectErr(t, c, "auth_required")

	// действующий токен в кадре авторизует соединение без отдельного входа
	c.Send(map[string]any{"type": "ping", "token": a.Token})
	c.Recv("pong")
}

func TestMalformedInput(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)
	a.SendRaw("this is not json")
	expectErr(t, a, "bad_json")
	a.Send(map[string]any{"type": "no.such.type"})
	expectErr(t, a, "unknown_type")
	a.Send(map[string]any{"type": "chat.direct.send", "peerUserId": 2}) // без текста
	expectErr(t, a, "bad_dm")
	a.Send(map[string]any{"type": "ping"}) // соединение живо
	a.Recv("pong")
}

// ─── Profile, lookup, friends ────────────────────────────────────────────────

func TestProfileUpdateIsBroadcast(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "profile.update", "displayName": "Alice A", "username": "NewAlice", "bio": "hi"})
	for _, c := range []*testutil.Client{a, b} {
		u := c.Recv("evt.profile")["user"].(map[string]any)
		if u["displayName"] != "Alice A" || u["username"] != "newalice" || u["bio"] != "hi" {
			t.Fatalf("evt.profile: %v", u)
		}
	}
	b.Send(map[string]any{"type": "profile.update", "displayName": "B", "username": "newalice"})
	expectErr(t, b, "username_taken")

	a.Send(map[string]any{"type": "profile.set_avatar", "avatarMediaId": "av.png"})
	if u := b.Recv("evt.profile")["user"].(map[string]any); u["avatarMediaId"] != "av.png" {
		t.Fatalf("аватар: %v", u)
	}
	a.Send(map[string]any{"type": "profile.set_avatar"})
	expectErr(t, a, "bad_request")
}

func TestUserLookup(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "user.lookup", "username": "USER2"})
	if u := a.Recv("user.lookup")["user"].(map[string]any); num(u, "id") != b.ID {
		t.Fatalf("поиск по username (без учёта регистра): %v", u)
	}
	a.Send(map[string]any{"type": "user.lookup", "userId": b.ID})
	if u := a.Recv("user.lookup")["user"].(map[string]any); u["username"] != "user2" {
		t.Fatalf("поиск по id: %v", u)
	}
	a.Send(map[string]any{"type": "user.lookup", "username": "nobody"})
	expectErr(t, a, "not_found")
	a.Send(map[string]any{"type": "user.lookup"})
	expectErr(t, a, "bad_request")
}

func TestFriends(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "friends.add", "friendUserId": b.ID})
	if l := a.Recv("friends.list")["friends"].([]any); len(l) != 1 {
		t.Fatalf("список друзей: %v", l)
	}
	a.Send(map[string]any{"type": "friends.add", "friendUserId": b.ID}) // повтор
	if l := a.Recv("friends.list")["friends"].([]any); len(l) != 1 {
		t.Fatalf("повторное добавление дублирует: %v", l)
	}
	a.Send(map[string]any{"type": "friends.add", "friendUserId": a.ID})
	expectErr(t, a, "bad_request")
	b.Send(map[string]any{"type": "friends.list"})
	if l := b.Recv("friends.list")["friends"].([]any); len(l) != 0 {
		t.Fatalf("дружба односторонняя: %v", l)
	}
}

func TestRegisterPushToken(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)
	a.Send(map[string]any{"type": "device.register_push", "fcmToken": "device-token-123"})
	a.Send(map[string]any{"type": "ping"})
	a.Recv("pong") // кадры обрабатываются по порядку: токен уже записан
	if tok, _ := db.GetPushToken(a.ID); tok != "device-token-123" {
		t.Fatalf("push-токен: %q", tok)
	}
}

// ─── Global and direct chats ─────────────────────────────────────────────────

func TestGlobalChat(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "chat.global.send", "body": "всем привет"})
	for _, c := range []*testutil.Client{a, b} {
		m := msgOf(t, c.Recv("evt.message"))
		if m["body"] != "всем привет" || num(m, "chatId") != 1 || num(m, "senderId") != a.ID {
			t.Fatalf("общий чат: %v", m)
		}
	}
	a.Send(map[string]any{"type": "chat.global.send", "body": ""})
	expectNothing(t, b, "evt.message")

	b.Send(map[string]any{"type": "chat.history", "chatId": 1})
	h := b.Recv("chat.history")["messages"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["body"] != "всем привет" {
		t.Fatalf("история общего чата: %v", h)
	}
}

func TestDirectOpen(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "chat.direct.open", "peerUserId": b.ID})
	m := a.Recv("chat.open")
	if num(m, "chatId") <= 1 || num(m["peer"].(map[string]any), "id") != b.ID {
		t.Fatalf("chat.open: %v", m)
	}
	first := num(m, "chatId")
	b.Send(map[string]any{"type": "chat.direct.open", "peerUserId": a.ID})
	if num(b.Recv("chat.open"), "chatId") != first {
		t.Error("оба собеседника должны получить один чат")
	}
	a.Send(map[string]any{"type": "chat.direct.open", "peerUserId": a.ID})
	expectErr(t, a, "bad_peer")
	a.Send(map[string]any{"type": "chat.direct.open", "peerUserId": 9999})
	expectErr(t, a, "not_found")
}

func TestDirectMessageDeliveryAndHistory(t *testing.T) {
	s := testutil.NewServer(t)
	a, b, c := user(t, s, 1), user(t, s, 2), user(t, s, 3)

	a.Send(map[string]any{"type": "chat.direct.send", "peerUserId": b.ID, "body": "привет, Bob"})
	ma := msgOf(t, a.Recv("evt.message"))
	mb := msgOf(t, b.Recv("evt.message"))
	if ma["body"] != "привет, Bob" || num(ma, "senderId") != a.ID || num(ma, "otherUserId") != b.ID || ma["status"] != "sent" {
		t.Fatalf("сообщение у отправителя: %v", ma)
	}
	if num(mb, "id") != num(ma, "id") || num(mb, "chatId") != num(ma, "chatId") || num(mb, "otherUserId") != a.ID {
		t.Fatalf("сообщение у получателя: %v", mb)
	}
	chat := num(ma, "chatId")
	if chat <= 1 {
		t.Fatalf("личный чат не должен быть общим: %d", chat)
	}
	expectNothing(t, c, "evt.message") // посторонний ничего не получает

	b.Send(map[string]any{"type": "chat.history", "chatId": chat})
	h := b.Recv("chat.history")["messages"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["body"] != "привет, Bob" {
		t.Fatalf("история: %v", h)
	}
	c.Send(map[string]any{"type": "chat.history", "chatId": chat})
	expectErr(t, c, "forbidden") // чужой личный чат
}

func TestDirectMessageToOfflinePeerIsStored(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)
	b := user(t, s, 2)
	bID := b.ID
	b.Close()
	for hub.H.IsOnline(bID) {
		time.Sleep(10 * time.Millisecond)
	}

	a.Send(map[string]any{"type": "chat.direct.send", "peerUserId": bID, "body": "when you're back"})
	chat := num(msgOf(t, a.Recv("evt.message")), "chatId")

	b2 := s.Dial(t)
	b2.Login(phone(2), pass(2))
	b2.Send(map[string]any{"type": "chat.history", "chatId": chat})
	h := b2.Recv("chat.history")["messages"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["body"] != "when you're back" {
		t.Fatalf("сообщение офлайн-получателю не сохранено: %v", h)
	}
}

func TestReplyAndPhoto(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "chat.direct.send", "peerUserId": b.ID, "body": "вопрос"})
	first := num(msgOf(t, a.Recv("evt.message")), "id")
	b.Recv("evt.message")

	b.Send(map[string]any{"type": "chat.direct.send", "peerUserId": a.ID, "body": "ответ", "replyToId": first})
	if m := msgOf(t, a.Recv("evt.message")); num(m, "replyToId") != first {
		t.Fatalf("replyToId потерян: %v", m)
	}
	b.Recv("evt.message")

	a.Send(map[string]any{"type": "chat.photo.send", "peerUserId": b.ID, "mediaId": "pic.jpg"})
	if m := msgOf(t, b.Recv("evt.message")); m["msgType"] != "photo" || m["mediaId"] != "pic.jpg" || m["body"] != "" {
		t.Fatalf("фото: %v", m)
	}
	a.Send(map[string]any{"type": "chat.photo.send", "peerUserId": b.ID})
	expectErr(t, a, "bad_request")
}

func TestEditDeleteAndReceiptByParticipants(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "chat.direct.send", "peerUserId": b.ID, "body": "черновик"})
	m := msgOf(t, a.Recv("evt.message"))
	id, chat := num(m, "id"), num(m, "chatId")
	b.Recv("evt.message")

	a.Send(map[string]any{"type": "chat.message.edit", "messageId": id, "body": "готово"})
	ev := a.Recv("evt.message.edited")
	if num(ev, "messageId") != id || ev["body"] != "готово" || num(ev, "editedAt") == 0 {
		t.Fatalf("evt.message.edited: %v", ev)
	}
	a.Send(map[string]any{"type": "chat.message.edit", "messageId": id, "body": ""})
	expectErr(t, a, "bad_request")

	b.Send(map[string]any{"type": "chat.receipt", "messageId": id})
	b.Send(map[string]any{"type": "chat.history", "chatId": chat})
	h := b.Recv("chat.history")["messages"].([]any)[0].(map[string]any)
	if h["body"] != "готово" || h["status"] != "read" || num(h, "editedAt") == 0 {
		t.Fatalf("история после правки и квитанции: %v", h)
	}

	a.Send(map[string]any{"type": "chat.message.delete", "messageId": id})
	if num(a.Recv("evt.message.deleted"), "messageId") != id {
		t.Fatal("evt.message.deleted")
	}
	b.Send(map[string]any{"type": "chat.history", "chatId": chat})
	if h := b.Recv("chat.history")["messages"].([]any)[0].(map[string]any); h["body"] != "" {
		t.Fatalf("удалённое сообщение сохранило текст: %v", h)
	}
}

func TestTypingReachesThePeer(t *testing.T) {
	s := testutil.NewServer(t)
	a, b := user(t, s, 1), user(t, s, 2)
	a.Send(map[string]any{"type": "chat.direct.open", "peerUserId": b.ID})
	chat := num(a.Recv("chat.open"), "chatId")
	a.Send(map[string]any{"type": "typing", "chatId": chat, "isTyping": true})
	ev := b.Recv("evt.typing")
	if num(ev, "userId") != a.ID || num(ev, "chatId") != chat || ev["isTyping"] != true {
		t.Fatalf("evt.typing: %v", ev)
	}
}

// ─── Group chats ─────────────────────────────────────────────────────────────

func TestGroupLifecycle(t *testing.T) {
	s := testutil.NewServer(t)
	a, b, c := user(t, s, 1), user(t, s, 2), user(t, s, 3)

	a.Send(map[string]any{"type": "chat.group.create", "title": "  Проект  ", "memberIds": []int64{b.ID}})
	created := a.Recv("chat.group.created")
	gid := num(created, "chatId")
	if created["group"].(map[string]any)["title"] != "Проект" || len(created["members"].([]any)) != 2 {
		t.Fatalf("chat.group.created: %v", created)
	}
	b.Recv("chat.group.created")
	expectNothing(t, c, "chat.group.created")

	a.Send(map[string]any{"type": "chat.group.create", "title": "   "})
	expectErr(t, a, "bad_request")

	// сообщение группе: все участники получают его с именем отправителя
	a.Send(map[string]any{"type": "chat.group.send", "chatId": gid, "body": "в группу"})
	for _, m := range []*testutil.Client{a, b} {
		msg := msgOf(t, m.Recv("evt.message"))
		if msg["body"] != "в группу" || msg["senderName"] != "User 1" || num(msg, "chatId") != gid {
			t.Fatalf("групповое сообщение: %v", msg)
		}
	}
	expectNothing(t, c, "evt.message")

	// посторонний не может ни писать, ни читать сведения
	for _, req := range []map[string]any{
		{"type": "chat.group.send", "chatId": gid, "body": "x"},
		{"type": "chat.group.photo", "chatId": gid, "mediaId": "p.jpg"},
		{"type": "group.info", "chatId": gid},
		{"type": "chat.group.open", "chatId": gid},
		{"type": "group.add_member", "chatId": gid, "userId": c.ID},
	} {
		c.Send(req)
		expectErr(t, c, "forbidden")
	}

	// b добавляет c: все, включая нового участника, получают уведомление
	b.Send(map[string]any{"type": "group.add_member", "chatId": gid, "userId": c.ID})
	for _, m := range []*testutil.Client{a, b, c} {
		ev := m.Recv("group.member_added")
		if num(ev, "chatId") != gid || num(ev, "userId") != c.ID {
			t.Fatalf("group.member_added: %v", ev)
		}
	}
	c.Send(map[string]any{"type": "chat.group.photo", "chatId": gid, "mediaId": "p.jpg"})
	if msg := msgOf(t, a.Recv("evt.message")); msg["msgType"] != "photo" || msg["senderName"] != "User 3" {
		t.Fatalf("фото в группу: %v", msg)
	}
	b.Recv("evt.message")
	c.Recv("evt.message")

	// сведения, список и открытие с историей
	a.Send(map[string]any{"type": "group.info", "chatId": gid})
	if info := a.Recv("group.info"); len(info["members"].([]any)) != 3 {
		t.Fatalf("group.info: %v", info)
	}
	a.Send(map[string]any{"type": "group.list"})
	if l := a.Recv("group.list")["groups"].([]any); len(l) != 1 {
		t.Fatalf("group.list: %v", l)
	}
	a.Send(map[string]any{"type": "chat.group.open", "chatId": gid})
	a.Recv("chat.group.opened")
	if h := a.Recv("chat.history")["messages"].([]any); len(h) != 2 {
		t.Fatalf("история группы при открытии: %v", h)
	}

	// b выходит: участники получают уведомление, писать он больше не может
	b.Send(map[string]any{"type": "group.leave", "chatId": gid})
	for _, m := range []*testutil.Client{a, b, c} {
		if ev := m.Recv("group.member_left"); num(ev, "userId") != b.ID {
			t.Fatalf("group.member_left: %v", ev)
		}
	}
	b.Send(map[string]any{"type": "chat.group.send", "chatId": gid, "body": "я ушёл"})
	expectErr(t, b, "forbidden")
}

// ─── Hub behaviour (changed in dca36cf) ──────────────────────────────────────

func TestPresenceFollowsConnections(t *testing.T) {
	s := testutil.NewServer(t)
	a := user(t, s, 1)
	b := user(t, s, 2)
	if p := lastPresence(a); !p[a.ID] || !p[b.ID] || len(p) != 2 {
		t.Fatalf("присутствие после входа b: %v", p)
	}
	b.Close()
	if p := lastPresence(a); !p[a.ID] || len(p) != 1 {
		t.Fatalf("присутствие после ухода b: %v", p)
	}
}

// A newer login of the same user replaces the older connection: the old socket is closed, the
// user stays online through the new one and receives every message exactly once.
func TestReplacedSocketIsClosedAndNewOneReceivesOnce(t *testing.T) {
	s := testutil.NewServer(t)
	first := user(t, s, 1)
	second := s.Dial(t)
	second.Login(phone(1), pass(1))
	if !first.WaitClosed(3 * time.Second) {
		t.Fatal("замещённое соединение не закрыто")
	}
	if !hub.H.IsOnline(first.ID) {
		t.Fatal("пользователь должен остаться онлайн через новое соединение")
	}

	other := user(t, s, 2)
	other.Send(map[string]any{"type": "chat.direct.send", "peerUserId": first.ID, "body": "один раз"})
	second.Recv("evt.message")
	expectNothing(t, second, "evt.message")

	second.Close()
	for hub.H.IsOnline(first.ID) {
		time.Sleep(10 * time.Millisecond)
	}
}

// A socket that signs in as a second user stops being the first user: the first is not online,
// presence lists only the second, and broadcasts reach the socket once, not once per identity.
func TestReauthenticatedSocketIsOnlyTheNewUser(t *testing.T) {
	s := testutil.NewServer(t)
	observer := user(t, s, 3)

	bobConn := user(t, s, 2)
	bob := bobConn.ID
	bobConn.Close()
	for hub.H.IsOnline(bob) {
		time.Sleep(10 * time.Millisecond)
	}
	lastPresence(observer) // очистить накопленное

	x := user(t, s, 1) // X входит как user1
	alice := x.ID
	x.Login(phone(2), pass(2)) // и на том же сокете как user2

	if hub.H.IsOnline(alice) {
		t.Fatal("user1 не должен числиться онлайн: его сокет теперь user2")
	}
	if !hub.H.IsOnline(bob) {
		t.Fatal("user2 должен быть онлайн через сокет X")
	}
	if p := lastPresence(observer); !p[observer.ID] || !p[bob] || p[alice] || len(p) != 2 {
		t.Fatalf("присутствие: %v (ожидались наблюдатель и user2)", p)
	}

	observer.Send(map[string]any{"type": "chat.global.send", "body": "рассылка"})
	x.Recv("evt.message")
	expectNothing(t, x, "evt.message") // без дубля

	x.Close()
	for hub.H.IsOnline(bob) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.H.IsOnline(alice) {
		t.Error("user1 не должен вернуться в хаб после закрытия сокета")
	}
}

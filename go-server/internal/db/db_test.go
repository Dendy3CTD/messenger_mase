package db_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/mase/server/internal/db"
	"github.com/mase/server/internal/testutil"
)

func newUser(t *testing.T, n int) int64 {
	t.Helper()
	phone := "+7000000000" + string(rune('0'+n))
	name := "user" + string(rune('0'+n))
	id, err := db.CreateUser(phone, "User "+name, name, "hash"+name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUsers(t *testing.T) {
	testutil.NewDB(t)
	id, err := db.CreateUser("+70000000001", "Alice", "alice", "h1")
	if err != nil {
		t.Fatal(err)
	}

	u, err := db.GetUserByID(id)
	if err != nil || u.Phone != "+70000000001" || u.DisplayName != "Alice" || u.Username != "alice" || u.Bio != "" {
		t.Fatalf("GetUserByID: %+v %v", u, err)
	}
	if u, err := db.GetUserByUsername("alice"); err != nil || u.ID != id {
		t.Fatalf("GetUserByUsername: %+v %v", u, err)
	}
	u, gotID, hash, err := db.GetUserByPhone("+70000000001")
	if err != nil || gotID != id || hash != "h1" || u.DisplayName != "Alice" {
		t.Fatalf("GetUserByPhone: %+v %d %q %v", u, gotID, hash, err)
	}
	if _, err := db.GetUserByID(999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("отсутствующий пользователь: %v", err)
	}
	if _, _, _, err := db.GetUserByPhone("+79999999999"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("отсутствующий телефон: %v", err)
	}

	if ok, _ := db.PhoneExists("+70000000001"); !ok {
		t.Error("PhoneExists должен найти телефон")
	}
	if ok, _ := db.PhoneExists("+70000000002"); ok {
		t.Error("PhoneExists нашёл несуществующий телефон")
	}
	if ok, _ := db.UsernameExists("alice"); !ok {
		t.Error("UsernameExists должен найти имя")
	}
	if ok, _ := db.UsernameExists("nobody"); ok {
		t.Error("UsernameExists нашёл несуществующее имя")
	}

	if _, err := db.CreateUser("+70000000001", "Dup", "dup", "h"); err == nil {
		t.Error("второй пользователь с тем же телефоном должен быть отклонён (UNIQUE)")
	}
	if _, err := db.CreateUser("+70000000002", "Dup", "alice", "h"); err == nil {
		t.Error("второй пользователь с тем же username должен быть отклонён (UNIQUE)")
	}
}

func TestUpdateProfileAndAvatar(t *testing.T) {
	testutil.NewDB(t)
	id := newUser(t, 1)
	if err := db.UpdateProfile(id, "Новое имя", "newname", "о себе"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateAvatarMediaID(id, "abc.png"); err != nil {
		t.Fatal(err)
	}
	u, _ := db.GetUserByID(id)
	if u.DisplayName != "Новое имя" || u.Username != "newname" || u.Bio != "о себе" || u.AvatarMediaID != "abc.png" {
		t.Fatalf("профиль: %+v", u)
	}
}

func TestTokens(t *testing.T) {
	testutil.NewDB(t)
	id := newUser(t, 1)
	if err := db.StoreToken("tok1", id); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetUserIDByToken("tok1"); err != nil || got != id {
		t.Fatalf("GetUserIDByToken: %d %v", got, err)
	}
	if _, err := db.GetUserIDByToken("unknown"); err == nil {
		t.Error("неизвестный токен должен давать ошибку")
	}
	if err := db.DeleteToken("tok1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetUserIDByToken("tok1"); err == nil {
		t.Error("удалённый токен не должен работать")
	}
}

func TestDirectChats(t *testing.T) {
	testutil.NewDB(t)
	a, b, c := newUser(t, 1), newUser(t, 2), newUser(t, 3)

	chat, err := db.EnsureDirectChat(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := db.EnsureDirectChat(a, b); again != chat {
		t.Errorf("повторный вызов вернул другой чат: %d и %d", chat, again)
	}
	if swapped, _ := db.EnsureDirectChat(b, a); swapped != chat {
		t.Errorf("чат не должен зависеть от порядка пары: %d и %d", chat, swapped)
	}
	if other, _ := db.EnsureDirectChat(a, c); other == chat {
		t.Error("разные пары получили один чат")
	}

	for _, tc := range []struct {
		user int64
		want bool
	}{{a, true}, {b, true}, {c, false}} {
		if got, err := db.IsChatMember(chat, tc.user); err != nil || got != tc.want {
			t.Errorf("IsChatMember(user %d) = %v, %v; ожидалось %v", tc.user, got, err, tc.want)
		}
	}
	if ok, _ := db.IsChatMember(db.GlobalChatID, c); !ok {
		t.Error("в общем чате состоят все")
	}
	if _, err := db.IsChatMember(9999, a); err == nil {
		t.Error("несуществующий чат должен давать ошибку")
	}
	if peer, err := db.GetChatPeer(chat, a); err != nil || peer != b {
		t.Errorf("GetChatPeer(a) = %d, %v", peer, err)
	}
	if peer, _ := db.GetChatPeer(chat, b); peer != a {
		t.Errorf("GetChatPeer(b) = %d", peer)
	}
}

func TestMessagesHistoryOrderAndLimit(t *testing.T) {
	testutil.NewDB(t)
	a, b := newUser(t, 1), newUser(t, 2)
	chat, _ := db.EnsureDirectChat(a, b)

	var ids []int64
	for i, body := range []string{"one", "two", "three", "four"} {
		id, err := db.InsertMessage(chat, a, body, "text", "", int64(1000+i), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	h, err := db.GetHistory(chat, 100)
	if err != nil || len(h) != 4 {
		t.Fatalf("история: %v %v", h, err)
	}
	for i, m := range h {
		if m.ID != ids[i] {
			t.Errorf("история не в хронологическом порядке: позиция %d = %d", i, m.ID)
		}
	}
	if h[0].Status != "sent" || h[0].MsgType != "text" || h[0].SenderID != a || h[0].Ts != 1000 {
		t.Errorf("поля сообщения: %+v", h[0])
	}
	last2, _ := db.GetHistory(chat, 2)
	if len(last2) != 2 || last2[0].Body != "three" || last2[1].Body != "four" {
		t.Errorf("limit 2 должен вернуть два последних по порядку: %+v", last2)
	}
	if empty, _ := db.GetHistory(9999, 10); len(empty) != 0 {
		t.Errorf("история пустого чата: %v", empty)
	}
}

func TestMessageReplyPhotoAndDuration(t *testing.T) {
	testutil.NewDB(t)
	a, b := newUser(t, 1), newUser(t, 2)
	chat, _ := db.EnsureDirectChat(a, b)
	first, _ := db.InsertMessage(chat, a, "q", "text", "", 1, 0, 0)
	if _, err := db.InsertMessage(chat, b, "", "photo", "pic.jpg", 2, first, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertMessage(chat, b, "", "voice", "v.m4a", 3, 0, 42); err != nil {
		t.Fatal(err)
	}
	h, _ := db.GetHistory(chat, 10)
	if h[0].ReplyToID != 0 || h[1].ReplyToID != first || h[1].MsgType != "photo" || h[1].MediaID != "pic.jpg" {
		t.Errorf("ответ и фото: %+v %+v", h[0], h[1])
	}
	if h[2].DurationSec != 42 || h[2].MsgType != "voice" {
		t.Errorf("голосовое: %+v", h[2])
	}
}

func TestEditDeleteAndStatus(t *testing.T) {
	testutil.NewDB(t)
	a, b := newUser(t, 1), newUser(t, 2)
	chat, _ := db.EnsureDirectChat(a, b)
	id, _ := db.InsertMessage(chat, a, "original", "text", "", 1, 0, 0)

	get := func() db.Message { h, _ := db.GetHistory(chat, 10); return h[0] }

	// чужая правка ничего не меняет
	db.EditMessage(id, b, "hijack")
	if m := get(); m.Body != "original" || m.EditedAt != 0 {
		t.Errorf("чужая правка изменила сообщение: %+v", m)
	}
	before := time.Now().UnixMilli()
	db.EditMessage(id, a, "edited")
	if m := get(); m.Body != "edited" || m.EditedAt < before {
		t.Errorf("правка автора: %+v", m)
	}

	if err := db.UpdateMessageStatus(id, "read"); err != nil {
		t.Fatal(err)
	}
	if get().Status != "read" {
		t.Error("статус не обновился")
	}

	// чужое удаление ничего не меняет
	db.DeleteMessage(id, b)
	if m := get(); m.IsDeleted || m.Body != "edited" {
		t.Errorf("чужое удаление изменило сообщение: %+v", m)
	}
	db.DeleteMessage(id, a)
	if m := get(); !m.IsDeleted || m.Body != "" {
		t.Errorf("удаление автора: %+v", m)
	}
	// удалённое не редактируется
	db.EditMessage(id, a, "zombie")
	if get().Body != "" {
		t.Error("удалённое сообщение отредактировано")
	}
}

func TestFriends(t *testing.T) {
	testutil.NewDB(t)
	a, b, c := newUser(t, 1), newUser(t, 2), newUser(t, 3)
	db.AddFriend(a, b)
	db.AddFriend(a, b) // идемпотентно
	db.AddFriend(a, c)
	f, err := db.GetFriends(a)
	if err != nil || len(f) != 2 {
		t.Fatalf("друзья a: %v %v", f, err)
	}
	if none, _ := db.GetFriends(b); len(none) != 0 {
		t.Errorf("дружба односторонняя, у b друзей нет: %v", none)
	}
}

func TestPushToken(t *testing.T) {
	testutil.NewDB(t)
	a := newUser(t, 1)
	if tok, err := db.GetPushToken(a); err != nil || tok != "" {
		t.Fatalf("токена ещё нет: %q %v", tok, err)
	}
	db.UpsertPushToken(a, "first")
	db.UpsertPushToken(a, "second")
	if tok, _ := db.GetPushToken(a); tok != "second" {
		t.Errorf("токен должен замениться: %q", tok)
	}
}

func TestGroups(t *testing.T) {
	testutil.NewDB(t)
	a, b, c := newUser(t, 1), newUser(t, 2), newUser(t, 3)

	gid, err := db.CreateGroup(a, "Проект", []int64{b, a, b}) // создатель и повтор не дублируются
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.GetGroup(gid)
	if err != nil || g.Title != "Проект" || g.CreatedBy != a {
		t.Fatalf("GetGroup: %+v %v", g, err)
	}
	ids, _ := db.GetGroupMemberIDs(gid)
	if len(ids) != 2 {
		t.Fatalf("участники: %v", ids)
	}
	members, _ := db.GetGroupMembers(gid)
	if len(members) != 2 {
		t.Fatalf("GetGroupMembers: %v", members)
	}

	for _, tc := range []struct {
		user int64
		want bool
	}{{a, true}, {b, true}, {c, false}} {
		if got, _ := db.IsGroupMember(gid, tc.user); got != tc.want {
			t.Errorf("IsGroupMember(%d) = %v", tc.user, got)
		}
	}
	if ok, kind, err := db.IsChatMemberFull(gid, b); err != nil || !ok || kind != "group" {
		t.Errorf("IsChatMemberFull(group, b) = %v %q %v", ok, kind, err)
	}
	if ok, _, _ := db.IsChatMemberFull(gid, c); ok {
		t.Error("посторонний не участник группы")
	}
	if ok, kind, _ := db.IsChatMemberFull(db.GlobalChatID, c); !ok || kind != "global" {
		t.Errorf("общий чат: %v %q", ok, kind)
	}
	direct, _ := db.EnsureDirectChat(a, b)
	if ok, kind, _ := db.IsChatMemberFull(direct, a); !ok || kind != "direct" {
		t.Errorf("личный чат: %v %q", ok, kind)
	}
	if ok, _, _ := db.IsChatMemberFull(direct, c); ok {
		t.Error("посторонний не участник личного чата")
	}

	db.AddGroupMember(gid, c)
	db.AddGroupMember(gid, c) // идемпотентно
	if ids, _ := db.GetGroupMemberIDs(gid); len(ids) != 3 {
		t.Errorf("после добавления c: %v", ids)
	}
	groups, _ := db.GetUserGroups(c)
	if len(groups) != 1 || groups[0].ID != gid {
		t.Errorf("GetUserGroups(c): %v", groups)
	}
	db.RemoveGroupMember(gid, c)
	if ok, _ := db.IsGroupMember(gid, c); ok {
		t.Error("c должен покинуть группу")
	}
	if groups, _ := db.GetUserGroups(c); len(groups) != 0 {
		t.Errorf("у c не должно остаться групп: %v", groups)
	}
	if _, err := db.GetGroup(9999); err == nil {
		t.Error("несуществующая группа должна давать ошибку")
	}
}

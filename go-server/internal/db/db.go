package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DB is the global database handle used by all handlers.
var DB *sql.DB

// Open opens (or creates) the SQLite database at path and applies migrations.
func Open(path string) error {
	d, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	d.SetMaxOpenConns(1) // SQLite single writer
	DB = d
	return migrate()
}

func migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			phone         TEXT    UNIQUE NOT NULL,
			display_name  TEXT    NOT NULL DEFAULT '',
			username      TEXT    UNIQUE,
			bio           TEXT    NOT NULL DEFAULT '',
			password_hash TEXT    NOT NULL DEFAULT '',
			avatar_media_id TEXT  NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS tokens (
			token      TEXT    PRIMARY KEY,
			user_id    INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS chats (
			id    INTEGER PRIMARY KEY AUTOINCREMENT,
			kind  TEXT    NOT NULL DEFAULT 'direct',  -- 'direct' | 'group'
			u1    INTEGER,
			u2    INTEGER
		)`,
		// Global chat always has id=1
		`INSERT OR IGNORE INTO chats(id, kind, u1, u2) VALUES(1,'global',NULL,NULL)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id    INTEGER NOT NULL,
			sender_id  INTEGER NOT NULL,
			body       TEXT    NOT NULL DEFAULT '',
			msg_type   TEXT    NOT NULL DEFAULT 'text',  -- 'text'|'photo'|'voice'|'file'
			media_id   TEXT    NOT NULL DEFAULT '',
			status     TEXT    NOT NULL DEFAULT 'sent',  -- 'sent'|'delivered'|'read'
			ts         INTEGER NOT NULL,
			reply_to_id  INTEGER,
			edited_at    INTEGER,
			is_deleted   INTEGER NOT NULL DEFAULT 0,
			duration_sec INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS friends (
			user_id   INTEGER NOT NULL,
			friend_id INTEGER NOT NULL,
			PRIMARY KEY (user_id, friend_id)
		)`,
		`CREATE TABLE IF NOT EXISTS push_tokens (
			user_id    INTEGER PRIMARY KEY,
			fcm_token  TEXT    NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			id          INTEGER PRIMARY KEY,  -- same id as chats.id
			title       TEXT    NOT NULL,
			avatar_media_id TEXT NOT NULL DEFAULT '',
			created_by  INTEGER NOT NULL,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS group_members (
			group_id  INTEGER NOT NULL,
			user_id   INTEGER NOT NULL,
			role      TEXT    NOT NULL DEFAULT 'member',  -- 'admin' | 'member'
			joined_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			PRIMARY KEY (group_id, user_id)
		)`,
		// Indexes for performance
		`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_tokens_user   ON tokens(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_group_members  ON group_members(group_id)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return fmt.Errorf("migrate: %s\n  err: %w", s[:min(len(s), 60)], err)
		}
	}
	log.Println("[DB] schema OK")
	return nil
}

// ─── User ────────────────────────────────────────────────────────────────────

type User struct {
	ID            int64
	Phone         string
	DisplayName   string
	Username      string
	Bio           string
	AvatarMediaID string
}

func GetUserByID(id int64) (*User, error) {
	row := DB.QueryRow(
		`SELECT id, phone, display_name, username, bio, avatar_media_id FROM users WHERE id=?`, id)
	u := &User{}
	var un sql.NullString
	if err := row.Scan(&u.ID, &u.Phone, &u.DisplayName, &un, &u.Bio, &u.AvatarMediaID); err != nil {
		return nil, err
	}
	u.Username = un.String
	return u, nil
}

func GetUserByUsername(username string) (*User, error) {
	row := DB.QueryRow(
		`SELECT id, phone, display_name, username, bio, avatar_media_id FROM users WHERE username=?`, username)
	u := &User{}
	var un sql.NullString
	if err := row.Scan(&u.ID, &u.Phone, &u.DisplayName, &un, &u.Bio, &u.AvatarMediaID); err != nil {
		return nil, err
	}
	u.Username = un.String
	return u, nil
}

func GetUserByPhone(phone string) (*User, int64, string, error) {
	row := DB.QueryRow(
		`SELECT id, display_name, username, bio, avatar_media_id, password_hash FROM users WHERE phone=?`, phone)
	u := &User{Phone: phone}
	var un, hash sql.NullString
	var uid int64
	if err := row.Scan(&uid, &u.DisplayName, &un, &u.Bio, &u.AvatarMediaID, &hash); err != nil {
		return nil, 0, "", err
	}
	u.ID = uid
	u.Username = un.String
	return u, uid, hash.String, nil
}

func CreateUser(phone, displayName, username, passwordHash string) (int64, error) {
	res, err := DB.Exec(
		`INSERT INTO users(phone, display_name, username, bio, password_hash) VALUES(?,?,?,?,?)`,
		phone, displayName, username, "", passwordHash)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateProfile(uid int64, displayName, username, bio string) error {
	_, err := DB.Exec(
		`UPDATE users SET display_name=?, username=?, bio=? WHERE id=?`,
		displayName, username, bio, uid)
	return err
}

func UpdateAvatarMediaID(uid int64, mediaID string) error {
	_, err := DB.Exec(`UPDATE users SET avatar_media_id=? WHERE id=?`, mediaID, uid)
	return err
}

func UsernameExists(username string) (bool, error) {
	var n int
	err := DB.QueryRow(`SELECT COUNT(1) FROM users WHERE username=?`, username).Scan(&n)
	return n > 0, err
}

func PhoneExists(phone string) (bool, error) {
	var n int
	err := DB.QueryRow(`SELECT COUNT(1) FROM users WHERE phone=?`, phone).Scan(&n)
	return n > 0, err
}

// ─── Tokens ──────────────────────────────────────────────────────────────────

func StoreToken(token string, userID int64) error {
	_, err := DB.Exec(`INSERT OR REPLACE INTO tokens(token, user_id, created_at) VALUES(?,?,?)`,
		token, userID, time.Now().Unix())
	return err
}

func GetUserIDByToken(token string) (int64, error) {
	var uid int64
	err := DB.QueryRow(`SELECT user_id FROM tokens WHERE token=?`, token).Scan(&uid)
	return uid, err
}

func DeleteToken(token string) error {
	_, err := DB.Exec(`DELETE FROM tokens WHERE token=?`, token)
	return err
}

// ─── Chats ───────────────────────────────────────────────────────────────────

const GlobalChatID int64 = 1

// EnsureDirectChat returns the chat id for the (u1, u2) pair, creating it if needed.
func EnsureDirectChat(u1, u2 int64) (int64, error) {
	a, b := u1, u2
	if a > b {
		a, b = b, a
	}
	var id int64
	err := DB.QueryRow(
		`SELECT id FROM chats WHERE kind='direct' AND u1=? AND u2=?`, a, b).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := DB.Exec(`INSERT INTO chats(kind,u1,u2) VALUES('direct',?,?)`, a, b)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func IsChatMember(chatID, userID int64) (bool, error) {
	if chatID == GlobalChatID {
		return true, nil
	}
	var u1, u2 sql.NullInt64
	err := DB.QueryRow(`SELECT u1, u2 FROM chats WHERE id=?`, chatID).Scan(&u1, &u2)
	if err != nil {
		return false, err
	}
	return (u1.Valid && u1.Int64 == userID) || (u2.Valid && u2.Int64 == userID), nil
}

func GetChatPeer(chatID, myUserID int64) (int64, error) {
	var u1, u2 int64
	err := DB.QueryRow(`SELECT u1, u2 FROM chats WHERE id=? AND kind='direct'`, chatID).Scan(&u1, &u2)
	if err != nil {
		return 0, err
	}
	if u1 == myUserID {
		return u2, nil
	}
	return u1, nil
}

// ─── Messages ────────────────────────────────────────────────────────────────

type Message struct {
	ID          int64
	ChatID      int64
	SenderID    int64
	Body        string
	MsgType     string
	MediaID     string
	Status      string
	Ts          int64
	ReplyToID   int64
	EditedAt    int64
	IsDeleted   bool
	DurationSec int
}

func InsertMessage(chatID, senderID int64, body, msgType, mediaID string, ts int64, replyToID int64, durationSec int) (int64, error) {
	res, err := DB.Exec(
		`INSERT INTO messages(chat_id, sender_id, body, msg_type, media_id, status, ts, reply_to_id, duration_sec)
		 VALUES(?,?,?,?,?,'sent',?,?,?)`,
		chatID, senderID, body, msgType, mediaID, ts, nullInt(replyToID), durationSec)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func GetHistory(chatID int64, limit int) ([]Message, error) {
	rows, err := DB.Query(
		`SELECT id, chat_id, sender_id, body, msg_type, media_id, status, ts,
		        COALESCE(reply_to_id,0), COALESCE(edited_at,0), is_deleted, duration_sec
		 FROM messages WHERE chat_id=? ORDER BY id DESC LIMIT ?`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var m Message
		var del int
		if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.Body, &m.MsgType, &m.MediaID,
			&m.Status, &m.Ts, &m.ReplyToID, &m.EditedAt, &del, &m.DurationSec); err != nil {
			return nil, err
		}
		m.IsDeleted = del != 0
		msgs = append(msgs, m)
	}
	// Reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func UpdateMessageStatus(msgID int64, status string) error {
	_, err := DB.Exec(`UPDATE messages SET status=? WHERE id=?`, status, msgID)
	return err
}

func DeleteMessage(msgID, senderID int64) error {
	_, err := DB.Exec(`UPDATE messages SET is_deleted=1, body='' WHERE id=? AND sender_id=?`, msgID, senderID)
	return err
}

func EditMessage(msgID, senderID int64, newBody string) error {
	_, err := DB.Exec(
		`UPDATE messages SET body=?, edited_at=? WHERE id=? AND sender_id=? AND is_deleted=0`,
		newBody, time.Now().UnixMilli(), msgID, senderID)
	return err
}

// ─── Friends ─────────────────────────────────────────────────────────────────

func AddFriend(userID, friendID int64) error {
	_, err := DB.Exec(`INSERT OR IGNORE INTO friends(user_id, friend_id) VALUES(?,?)`, userID, friendID)
	return err
}

func GetFriends(userID int64) ([]User, error) {
	rows, err := DB.Query(
		`SELECT u.id, u.phone, u.display_name, COALESCE(u.username,''), u.bio, u.avatar_media_id
		 FROM friends f JOIN users u ON u.id=f.friend_id WHERE f.user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Phone, &u.DisplayName, &u.Username, &u.Bio, &u.AvatarMediaID); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// ─── Push tokens ─────────────────────────────────────────────────────────────

func UpsertPushToken(userID int64, token string) error {
	_, err := DB.Exec(
		`INSERT OR REPLACE INTO push_tokens(user_id, fcm_token, updated_at) VALUES(?,?,?)`,
		userID, token, time.Now().Unix())
	return err
}

func GetPushToken(userID int64) (string, error) {
	var tok string
	err := DB.QueryRow(`SELECT fcm_token FROM push_tokens WHERE user_id=?`, userID).Scan(&tok)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tok, err
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// ─── Groups ──────────────────────────────────────────────────────────────────

type Group struct {
	ID            int64
	Title         string
	AvatarMediaID string
	CreatedBy     int64
}

// CreateGroup inserts a new group chat and returns the chat ID.
// The creator is added as admin automatically.
func CreateGroup(creatorID int64, title string, memberIDs []int64) (int64, error) {
	tx, err := DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Create chat row (kind='group', u1=u2=null)
	res, err := tx.Exec(`INSERT INTO chats(kind) VALUES('group')`)
	if err != nil {
		return 0, err
	}
	chatID, _ := res.LastInsertId()

	// Create group metadata
	if _, err = tx.Exec(
		`INSERT INTO groups(id, title, created_by) VALUES(?,?,?)`,
		chatID, title, creatorID); err != nil {
		return 0, err
	}

	// Add creator as admin
	if _, err = tx.Exec(
		`INSERT INTO group_members(group_id, user_id, role) VALUES(?,?,'admin')`,
		chatID, creatorID); err != nil {
		return 0, err
	}

	// Add members
	for _, uid := range memberIDs {
		if uid == creatorID {
			continue
		}
		if _, err = tx.Exec(
			`INSERT OR IGNORE INTO group_members(group_id, user_id, role) VALUES(?,?,'member')`,
			chatID, uid); err != nil {
			return 0, err
		}
	}

	return chatID, tx.Commit()
}

func GetGroup(chatID int64) (*Group, error) {
	row := DB.QueryRow(
		`SELECT id, title, avatar_media_id, created_by FROM groups WHERE id=?`, chatID)
	g := &Group{}
	if err := row.Scan(&g.ID, &g.Title, &g.AvatarMediaID, &g.CreatedBy); err != nil {
		return nil, err
	}
	return g, nil
}

func GetGroupMembers(groupID int64) ([]User, error) {
	rows, err := DB.Query(
		`SELECT u.id, u.phone, u.display_name, COALESCE(u.username,''), u.bio, u.avatar_media_id
		 FROM group_members gm JOIN users u ON u.id=gm.user_id
		 WHERE gm.group_id=? ORDER BY gm.joined_at ASC`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Phone, &u.DisplayName, &u.Username, &u.Bio, &u.AvatarMediaID); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

func GetGroupMemberIDs(groupID int64) ([]int64, error) {
	rows, err := DB.Query(`SELECT user_id FROM group_members WHERE group_id=?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func IsGroupMember(groupID, userID int64) (bool, error) {
	var n int
	err := DB.QueryRow(
		`SELECT COUNT(1) FROM group_members WHERE group_id=? AND user_id=?`, groupID, userID).Scan(&n)
	return n > 0, err
}

func AddGroupMember(groupID, userID int64) error {
	_, err := DB.Exec(
		`INSERT OR IGNORE INTO group_members(group_id, user_id, role) VALUES(?,?,'member')`,
		groupID, userID)
	return err
}

func RemoveGroupMember(groupID, userID int64) error {
	_, err := DB.Exec(
		`DELETE FROM group_members WHERE group_id=? AND user_id=?`, groupID, userID)
	return err
}

// IsChatMember now also handles group chats.
func IsChatMemberFull(chatID, userID int64) (bool, string, error) {
	if chatID == GlobalChatID {
		return true, "global", nil
	}
	var kind string
	err := DB.QueryRow(`SELECT kind FROM chats WHERE id=?`, chatID).Scan(&kind)
	if err != nil {
		return false, "", err
	}
	switch kind {
	case "group":
		ok, err := IsGroupMember(chatID, userID)
		return ok, "group", err
	default:
		var u1, u2 sql.NullInt64
		err := DB.QueryRow(`SELECT u1, u2 FROM chats WHERE id=?`, chatID).Scan(&u1, &u2)
		if err != nil {
			return false, "", err
		}
		ok := (u1.Valid && u1.Int64 == userID) || (u2.Valid && u2.Int64 == userID)
		return ok, "direct", err
	}
}

func GetUserGroups(userID int64) ([]Group, error) {
	rows, err := DB.Query(
		`SELECT g.id, g.title, g.avatar_media_id, g.created_by
		 FROM groups g JOIN group_members gm ON gm.group_id=g.id
		 WHERE gm.user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Title, &g.AvatarMediaID, &g.CreatedBy); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func nullInt(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

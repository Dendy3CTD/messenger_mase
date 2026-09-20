package db

// legacyDDL is the schema exactly as the pre-migration code (db.migrate at go-server 0.3.0) created it.
// Tests compare a database built from it with one built by goose from the baseline migration.
var legacyDDL = []string{
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
	`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id, id)`,
	`CREATE INDEX IF NOT EXISTS idx_tokens_user   ON tokens(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_group_members  ON group_members(group_id)`,
}

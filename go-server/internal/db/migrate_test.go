package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func tempDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.sqlite")
	d, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// schemaSnapshot describes a database structurally: objects, columns, indexes and the
// seeded global chat. goose's own bookkeeping table is excluded.
func schemaSnapshot(t *testing.T, d *sql.DB) string {
	t.Helper()
	var b strings.Builder
	rows, err := d.Query(`SELECT type, name, tbl_name FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' AND name != 'goose_db_version' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	type obj struct{ typ, name, tbl string }
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.typ, &o.name, &o.tbl); err != nil {
			t.Fatal(err)
		}
		objs = append(objs, o)
	}
	rows.Close()
	for _, o := range objs {
		fmt.Fprintf(&b, "%s %s on %s\n", o.typ, o.name, o.tbl)
		if o.typ != "table" {
			continue
		}
		cols, err := d.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, o.name))
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "  col %d %s %s notnull=%d default=%q pk=%d\n", cid, name, typ, notnull, dflt.String, pk)
		}
		cols.Close()
	}
	var kind string
	if err := d.QueryRow(`SELECT kind FROM chats WHERE id=1`).Scan(&kind); err != nil {
		t.Fatalf("общий чат (id=1) не создан: %v", err)
	}
	fmt.Fprintf(&b, "global chat kind=%s\n", kind)
	return b.String()
}

func applyLegacy(t *testing.T, d *sql.DB) {
	t.Helper()
	for _, s := range legacyDDL {
		mustExec(t, d, s)
	}
}

func TestBaselineMatchesLegacySchemaOnEmptyDB(t *testing.T) {
	legacy, _ := tempDB(t)
	applyLegacy(t, legacy)

	fresh, _ := tempDB(t)
	if err := Migrate(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	if got, want := schemaSnapshot(t, fresh), schemaSnapshot(t, legacy); got != want {
		t.Fatalf("схема после baseline отличается от прежней\n--- baseline:\n%s\n--- прежняя:\n%s", got, want)
	}
}

func TestBaselineOnExistingDatabaseKeepsData(t *testing.T) {
	d, _ := tempDB(t)
	applyLegacy(t, d)
	mustExec(t, d, `INSERT INTO users(phone, display_name, username, password_hash) VALUES('+70000000001','A','a','h')`)
	mustExec(t, d, `INSERT INTO tokens(token, user_id) VALUES('t1', 1)`)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := tx.Exec(`INSERT INTO messages(chat_id, sender_id, body, ts) VALUES(1, 1, ?, ?)`, fmt.Sprintf("m%d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := schemaSnapshot(t, d)

	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"users": 1, "tokens": 1, "messages": 1000, "chats": 1} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: строк %d (err %v), было %d", table, n, err, want)
		}
	}
	if after := schemaSnapshot(t, d); after != before {
		t.Fatalf("схема существующей БД изменилась после baseline\n--- до:\n%s\n--- после:\n%s", before, after)
	}
	var ver int
	if err := d.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied`).Scan(&ver); err != nil || ver != BaselineVersion {
		t.Fatalf("версия %d (err %v), ожидалась %d", ver, err, BaselineVersion)
	}
	// второй запуск ничего не делает
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAppliesMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.sqlite")
	if err := Open(path); err != nil {
		t.Fatal(err)
	}
	defer DB.Close()
	st, err := MigrationStatus(context.Background(), DB)
	if err != nil || len(st) != 1 || st[0].State != "applied" {
		t.Fatalf("статус: %v %v", st, err)
	}
}

// A second migration exists only in the test file system: it lets us exercise up/down
// without shipping a fake migration.
func testFS() fstest.MapFS {
	base, _ := embeddedMigrations()
	b, err := fs.ReadFile(base, "00001_baseline.sql")
	if err != nil {
		panic(err)
	}
	return fstest.MapFS{
		"00001_baseline.sql": {Data: b},
		"00002_probe.sql":    {Data: []byte("-- +goose Up\nCREATE TABLE probe_t (id INTEGER PRIMARY KEY);\n\n-- +goose Down\nDROP TABLE probe_t;\n")},
	}
}

func tableExists(t *testing.T, d *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestUpDownUp(t *testing.T) {
	d, _ := tempDB(t)
	ctx := context.Background()
	if err := migrateUp(ctx, d, testFS()); err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, d, "probe_t") {
		t.Fatal("миграция 2 не применилась")
	}
	if err := migrateDown(ctx, d, testFS(), 1); err != nil {
		t.Fatal(err)
	}
	if tableExists(t, d, "probe_t") || !tableExists(t, d, "users") {
		t.Fatal("down 1 должен убрать probe_t и не трогать baseline")
	}
	if err := migrateUp(ctx, d, testFS()); err != nil {
		t.Fatal(err)
	}
	if !tableExists(t, d, "probe_t") {
		t.Fatal("повторный up не вернул probe_t")
	}
}

func TestDownBelowBaselineIsRefused(t *testing.T) {
	d, _ := tempDB(t)
	ctx := context.Background()
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO users(phone) VALUES('+70000000001')`)
	if err := MigrateDown(ctx, d, 1); !errors.Is(err, ErrBaseline) {
		t.Fatalf("ожидался ErrBaseline, получено %v", err)
	}
	// и цепочка «два шага» останавливается на baseline, данные целы
	if err := migrateUp(ctx, d, testFS()); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(ctx, d, testFS(), 2); !errors.Is(err, ErrBaseline) {
		t.Fatalf("down 2 должен остановиться на baseline: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("данные потеряны: %d %v", n, err)
	}
	if err := migrateDown(ctx, d, testFS(), 0); err == nil {
		t.Fatal("0 шагов должно быть ошибкой")
	}
}

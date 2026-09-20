package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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

func fixedNow(t *testing.T) {
	t.Helper()
	old := now
	now = func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = old })
}

func backups(t *testing.T, dbPath string) []string {
	t.Helper()
	m, err := filepath.Glob(dbPath + ".pre-migrate-*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNoBackupForEmptyDatabase(t *testing.T) {
	d, path := tempDB(t)
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if b := backups(t, path); len(b) != 0 {
		t.Fatalf("для пустой БД копия не нужна: %v", b)
	}
}

func TestBackupBeforeMigratingExistingData(t *testing.T) {
	fixedNow(t)
	d, path := tempDB(t)
	applyLegacy(t, d)
	mustExec(t, d, `INSERT INTO users(phone, display_name) VALUES('+70000000001','A')`)
	mustExec(t, d, `INSERT INTO messages(chat_id, sender_id, body, ts) VALUES(1,1,'hi',1)`)

	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	want := path + ".pre-migrate-0-to-1-20260920-120000.sqlite"
	b := backups(t, path)
	if len(b) != 1 || b[0] != want {
		t.Fatalf("копии: %v, ожидалась %s", b, want)
	}
	if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("права копии: %v %v", st, err)
	}
	cp, err := sql.Open("sqlite3", "file:"+want+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var users, msgs int
	cp.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	cp.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgs)
	if users != 1 || msgs != 1 {
		t.Fatalf("в копии users=%d messages=%d", users, msgs)
	}
	var goose int
	cp.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='goose_db_version'`).Scan(&goose)
	if goose != 0 {
		t.Fatal("копия должна отражать состояние ДО миграции")
	}
	// нечего применять — новой копии нет
	fixedNowLater := func() time.Time { return time.Date(2026, 9, 20, 12, 0, 5, 0, time.UTC) }
	now = fixedNowLater
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if b := backups(t, path); len(b) != 1 {
		t.Fatalf("без ожидающих миграций копия не нужна: %v", b)
	}
}

func TestBackupBeforeDownAndForNewMigration(t *testing.T) {
	fixedNow(t)
	d, path := tempDB(t)
	ctx := context.Background()
	applyLegacy(t, d)
	mustExec(t, d, `INSERT INTO users(phone) VALUES('+70000000001')`)
	if err := migrateUp(ctx, d, testFS()); err != nil { // baseline (копия 0→2) и миграция 2
		t.Fatal(err)
	}
	now = func() time.Time { return time.Date(2026, 9, 20, 12, 0, 9, 0, time.UTC) }
	if err := migrateDown(ctx, d, testFS(), 1); err != nil {
		t.Fatal(err)
	}
	got := backups(t, path)
	if len(got) != 2 {
		t.Fatalf("ожидались копии перед up и перед down: %v", got)
	}
	if !strings.HasSuffix(got[0], "-0-to-2-20260920-120000.sqlite") && !strings.HasSuffix(got[1], "-0-to-2-20260920-120000.sqlite") {
		t.Fatalf("нет копии up: %v", got)
	}
	if !strings.Contains(got[0]+got[1], "pre-migrate-2-to-1-20260920-120009.sqlite") {
		t.Fatalf("нет копии down: %v", got)
	}
}

func TestMigrationAbortsWhenBackupFails(t *testing.T) {
	fixedNow(t)
	d, path := tempDB(t)
	applyLegacy(t, d)
	mustExec(t, d, `INSERT INTO users(phone) VALUES('+70000000001')`)
	// имя копии занято: VACUUM INTO откажет, миграция не должна начаться
	busy := path + ".pre-migrate-0-to-1-20260920-120000.sqlite"
	if err := os.WriteFile(busy, []byte("занято"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), d); err == nil || !strings.Contains(err.Error(), "миграция не выполнена") {
		t.Fatalf("ожидался отказ из-за копии, получено %v", err)
	}
	if tableExists(t, d, "goose_db_version") {
		t.Fatal("миграция началась, хотя копию сделать не удалось")
	}
}

func TestConnectionPragmas(t *testing.T) {
	d, _ := tempDB(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"synchronous":  "1", // NORMAL
		"busy_timeout": "5000",
	} {
		var got string
		if err := d.QueryRow(`PRAGMA ` + pragma).Scan(&got); err != nil || got != want {
			t.Errorf("PRAGMA %s = %q (err %v), ожидалось %q", pragma, got, err, want)
		}
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	d, _ := tempDB(t)
	mustExec(t, d, `CREATE TABLE parent (id INTEGER PRIMARY KEY)`)
	mustExec(t, d, `CREATE TABLE child (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES parent(id))`)
	if _, err := d.Exec(`INSERT INTO child(pid) VALUES(42)`); err == nil {
		t.Fatal("нарушение внешнего ключа должно отклоняться")
	}
}

// The golden file pins the schema produced by the baseline. It was generated with the
// mattn/go-sqlite3 driver and must stay identical after switching to modernc.org/sqlite:
// this is the schema cross-check for the driver change. Regenerate deliberately with
// UPDATE_GOLDEN=1 only when a migration changes the schema.
func TestSchemaMatchesGolden(t *testing.T) {
	d, _ := tempDB(t)
	if err := Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got := schemaSnapshot(t, d)
	golden := filepath.Join("testdata", "schema_v1.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("схема отличается от эталона %s\n--- получено:\n%s\n--- эталон:\n%s", golden, got, want)
	}
}

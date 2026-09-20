package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

// now is replaced in tests to make backup names deterministic.
var now = time.Now

//go:embed migrations/*.sql
var migrationsFS embed.FS

// BaselineVersion is the migration that reproduces the schema which existed before
// migrations were introduced. It is never rolled back: doing so would drop every table.
const BaselineVersion = 1

// ErrBaseline is returned when a rollback would go below the baseline.
var ErrBaseline = errors.New("откат ниже baseline (версия 1) запрещён: он удалил бы все таблицы и данные")

func newProvider(d *sql.DB, fsys fs.FS) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectSQLite3, d, fsys)
}

func embeddedMigrations() (fs.FS, error) {
	return fs.Sub(migrationsFS, "migrations")
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, d *sql.DB) error {
	fsys, err := embeddedMigrations()
	if err != nil {
		return err
	}
	return migrateUp(ctx, d, fsys)
}

func migrateUp(ctx context.Context, d *sql.DB, fsys fs.FS) error {
	p, err := newProvider(d, fsys)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	applied, err := appliedVersions(ctx, d)
	if err != nil {
		return fmt.Errorf("migrations pending: %w", err)
	}
	target, pending := int64(0), false
	for _, src := range p.ListSources() {
		target = max(target, src.Version)
		pending = pending || !applied[src.Version]
	}
	if !pending {
		return nil
	}
	if err := backupBeforeMigrate(ctx, d, applied, target); err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations up: %w", err)
	}
	return nil
}

// MigrateDown rolls back the given number of migrations, one at a time, never below the baseline.
func MigrateDown(ctx context.Context, d *sql.DB, steps int) error {
	fsys, err := embeddedMigrations()
	if err != nil {
		return err
	}
	return migrateDown(ctx, d, fsys, steps)
}

func migrateDown(ctx context.Context, d *sql.DB, fsys fs.FS, steps int) error {
	if steps < 1 {
		return fmt.Errorf("число шагов отката должно быть ≥ 1, получено %d", steps)
	}
	p, err := newProvider(d, fsys)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	applied, err := appliedVersions(ctx, d)
	if err != nil {
		return fmt.Errorf("migrations version: %w", err)
	}
	if cur := latest(applied); cur > BaselineVersion {
		if err := backupBeforeMigrate(ctx, d, applied, max(cur-int64(steps), BaselineVersion)); err != nil {
			return err
		}
	}
	for i := 0; i < steps; i++ {
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			return fmt.Errorf("migrations version: %w", err)
		}
		if v <= BaselineVersion {
			return ErrBaseline
		}
		if _, err := p.Down(ctx); err != nil {
			return fmt.Errorf("migrations down: %w", err)
		}
	}
	return nil
}

// MigrationStatus describes every known migration and whether it is applied.
func MigrationStatus(ctx context.Context, d *sql.DB) ([]*goose.MigrationStatus, error) {
	fsys, err := embeddedMigrations()
	if err != nil {
		return nil, err
	}
	p, err := newProvider(d, fsys)
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	return p.Status(ctx)
}

// backupBeforeMigrate copies the database with VACUUM INTO before a migration changes it, and
// aborts the migration if the copy cannot be made or fails its integrity check. This is the
// safety net until the scheduled backups exist. Nothing is done for an empty database or an
// in-memory one. Old copies are never deleted automatically.
func backupBeforeMigrate(ctx context.Context, d *sql.DB, applied map[int64]bool, target int64) error {
	path, err := mainDatabaseFile(ctx, d)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if path == "" {
		return nil
	}
	has, err := hasData(ctx, d)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if !has {
		return nil
	}
	from := latest(applied)
	dest := fmt.Sprintf("%s.pre-migrate-%d-to-%d-%s.sqlite", path, from, target, now().Format("20060102-150405"))
	if _, err := d.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(dest, "'", "''")+"'"); err != nil {
		return fmt.Errorf("backup before migration (%s): %w — миграция не выполнена", dest, err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return fmt.Errorf("backup chmod: %w", err)
	}
	if err := checkCopy(ctx, dest); err != nil {
		return fmt.Errorf("backup %s не прошёл проверку: %w — миграция не выполнена", dest, err)
	}
	log.Printf("[DB] копия перед миграцией: %s", dest)
	return nil
}

func mainDatabaseFile(ctx context.Context, d *sql.DB) (string, error) {
	rows, err := d.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			return file, nil
		}
	}
	return "", rows.Err()
}

func hasData(ctx context.Context, d *sql.DB) (bool, error) {
	rows, err := d.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'`)
	if err != nil {
		return false, err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return false, err
		}
		tables = append(tables, n)
	}
	rows.Close()
	for _, t := range tables {
		var any int
		if err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM "`+strings.ReplaceAll(t, `"`, `""`)+`")`).Scan(&any); err != nil {
			return false, err
		}
		if any == 1 {
			return true, nil
		}
	}
	return false, nil
}

func checkCopy(ctx context.Context, path string) error {
	// read-only and without WAL: checking must not write next to the copy
	c, err := sql.Open(driverName, "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer c.Close()
	var res string
	if err := c.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("integrity_check: %s", res)
	}
	return nil
}

// appliedVersions reads goose's bookkeeping table without creating it: goose's own helpers create
// the table on first use, and the safety copy must be taken before the database is touched at all.
func appliedVersions(ctx context.Context, d *sql.DB) (map[int64]bool, error) {
	applied := map[int64]bool{}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return applied, nil
	}
	rows, err := d.QueryContext(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied AND version_id > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func latest(applied map[int64]bool) int64 {
	var v int64
	for k := range applied {
		v = max(v, k)
	}
	return v
}

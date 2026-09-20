package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

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

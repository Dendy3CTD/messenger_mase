package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/mase/server/internal/config"
	"github.com/mase/server/internal/db"
)

const migrateUsage = `Использование: mase-server migrate <команда> [-db путь]
  status        показать применённые и ожидающие миграции
  up            применить все ожидающие миграции
  down [N]      откатить N миграций (по умолчанию 1), но не ниже baseline (версия 1)

Путь к БД: флаг -db или MASE_DB, как у сервера. На несуществующей БД работает только up.
`

// runMigrate implements `mase-server migrate …` and returns the process exit code.
func runMigrate(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, migrateUsage)
		return 2
	}
	action, rest := args[0], args[1:]
	steps := 1
	if action == "down" && len(rest) > 0 {
		if n, err := strconv.Atoi(rest[0]); err == nil {
			steps, rest = n, rest[1:]
		}
	}
	if action != "status" && action != "up" && action != "down" {
		fmt.Fprintf(stderr, "mase-server: неизвестная команда migrate %q\n%s", action, migrateUsage)
		return 2
	}
	cfg, err := config.Load(rest, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "mase-server:", err)
		return 2
	}
	if _, err := os.Stat(cfg.DB); err != nil && action != "up" {
		fmt.Fprintf(stderr, "mase-server: БД %s не найдена; %s не создаёт новую БД\n", cfg.DB, action)
		return 1
	}
	fmt.Fprintf(stderr, "БД: %s (%s)\n", cfg.DB, cfg.Sources["db"])

	d, err := db.OpenRaw(cfg.DB)
	if err != nil {
		fmt.Fprintln(stderr, "mase-server:", err)
		return 1
	}
	defer d.Close()
	ctx := context.Background()

	switch action {
	case "up":
		err = db.Migrate(ctx, d)
	case "down":
		err = db.MigrateDown(ctx, d, steps)
	}
	if err != nil {
		fmt.Fprintln(stderr, "mase-server:", err)
		return 1
	}
	st, err := db.MigrationStatus(ctx, d)
	if err != nil {
		fmt.Fprintln(stderr, "mase-server:", err)
		return 1
	}
	for _, s := range st {
		applied := "—"
		if !s.AppliedAt.IsZero() {
			applied = s.AppliedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(stdout, "%05d  %-8s  %s  %s\n", s.Source.Version, s.State, applied, s.Source.Path)
	}
	return 0
}

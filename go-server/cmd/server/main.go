package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/mase/server/internal/config"
	"github.com/mase/server/internal/db"
	"github.com/mase/server/internal/media"
	"github.com/mase/server/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(runMigrate(os.Args[2:], os.Getenv, os.Stdout, os.Stderr))
	}
	cfg, err := config.Load(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mase-server:", err)
		os.Exit(2)
	}
	log.Printf("[CONFIG] addr=%s (%s) db=%s (%s) media=%s (%s) log-level=%s (%s)",
		cfg.Addr, cfg.Sources["addr"], cfg.DB, cfg.Sources["db"],
		cfg.Media, cfg.Sources["media"], cfg.LogLevel, cfg.Sources["log-level"])

	// ── Database ──────────────────────────────────────────────────────────────
	if err := db.Open(cfg.DB); err != nil {
		log.Fatalf("[DB] %v", err)
	}

	// ── Media ─────────────────────────────────────────────────────────────────
	if err := media.Init(cfg.Media); err != nil {
		log.Fatalf("[MEDIA] %v", err)
	}

	log.Printf("[SERVER] listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, server.NewMux()); err != nil {
		log.Fatalf("[SERVER] %v", err)
	}
}

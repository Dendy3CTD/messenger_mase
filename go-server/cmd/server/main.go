package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
	"github.com/mase/server/internal/config"
	"github.com/mase/server/internal/db"
	"github.com/mase/server/internal/handler"
	"github.com/mase/server/internal/hub"
	"github.com/mase/server/internal/media"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Allow all origins — Nginx handles TLS and restricts the domain
	CheckOrigin: func(r *http.Request) bool { return true },
}

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

	// ── HTTP mux ──────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[WS] upgrade error: %v", err)
			return
		}
		log.Printf("[WS] connected from %s", r.RemoteAddr)

		c := hub.NewClientFromConn(conn)
		hub.ServeClient(c,
			func(msg []byte) { handler.Handle(c, msg) },
			func() { handler.OnClose(c) },
		)
	})

	// Media upload/serve
	mux.Handle("/upload", media.Handler())
	mux.Handle("/media/", media.Handler())

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	log.Printf("[SERVER] listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		log.Fatalf("[SERVER] %v", err)
	}
}

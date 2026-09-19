package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
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
	addr     := flag.String("addr", ":8080", "HTTP listen address")
	dbPath   := flag.String("db", "mase.sqlite", "SQLite database path")
	mediaPath := flag.String("media", "./media", "Media storage directory")
	flag.Parse()

	// ── Database ──────────────────────────────────────────────────────────────
	if err := db.Open(*dbPath); err != nil {
		log.Fatalf("[DB] %v", err)
	}

	// ── Media ─────────────────────────────────────────────────────────────────
	if err := media.Init(*mediaPath); err != nil {
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

	log.Printf("[SERVER] listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("[SERVER] %v", err)
	}
}

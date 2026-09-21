// Package server builds the HTTP handler of the mase server: the WebSocket endpoint, media
// upload and download, and the health check. It is separate from cmd/server so that tests can
// run the real handler on a temporary database.
package server

import (
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
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

// active counts running WebSocket handlers. net/http does not wait for hijacked connections
// on Close, so tests use Wait to be sure no handler still touches the shared globals.
var active sync.WaitGroup

// Wait blocks until every WebSocket handler started through NewMux has returned.
func Wait() { active.Wait() }

// NewMux returns the handler for /ws, /upload, /media/ and /health. The database and the media
// directory must be initialised (db.Open, media.Init) before requests arrive.
func NewMux() *http.ServeMux {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Done()
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
	return mux
}

// Package testutil runs the real mase server handler on a temporary database for tests.
//
// The server keeps its state in package-level globals (db.DB, the media directory, hub.H), so
// only one test server can exist at a time. NewServer takes a process-wide lock that is released
// by t.Cleanup: tests using it run one after another even when they call t.Parallel.
package testutil

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mase/server/internal/db"
	"github.com/mase/server/internal/hub"
	"github.com/mase/server/internal/media"
	"github.com/mase/server/internal/server"
)

var global sync.Mutex

// RecvTimeout is how long Client.Recv waits for a frame.
const RecvTimeout = 3 * time.Second

// Server is a running test server with its own database and media directory.
type Server struct {
	HTTP     *httptest.Server
	DBPath   string
	MediaDir string
}

// NewServer opens a fresh database (all migrations applied), initialises media storage, resets
// the hub and starts the mux. Everything is torn down by t.Cleanup.
func NewServer(t testing.TB) *Server {
	t.Helper()
	global.Lock()
	dir := t.TempDir()
	s := &Server{DBPath: filepath.Join(dir, "test.sqlite"), MediaDir: filepath.Join(dir, "media")}

	prevOut := log.Writer()
	log.SetOutput(io.Discard)
	if err := db.Open(s.DBPath); err != nil {
		log.SetOutput(prevOut)
		global.Unlock()
		t.Fatalf("testutil: db.Open: %v", err)
	}
	if err := media.Init(s.MediaDir); err != nil {
		log.SetOutput(prevOut)
		global.Unlock()
		t.Fatalf("testutil: media.Init: %v", err)
	}
	hub.Reset()
	s.HTTP = httptest.NewServer(server.NewMux())

	t.Cleanup(func() {
		s.HTTP.CloseClientConnections()
		s.HTTP.Close()
		server.Wait() // hijacked WebSocket handlers are not waited for by Close
		db.DB.Close()
		hub.Reset()
		log.SetOutput(prevOut)
		global.Unlock()
	})
	return s
}

// WSURL is the WebSocket endpoint.
func (s *Server) WSURL() string { return "ws" + strings.TrimPrefix(s.HTTP.URL, "http") + "/ws" }

// Migrations reports the applied migration versions, to check the schema was built by migrations.
func (s *Server) Migrations(t testing.TB) []int64 {
	t.Helper()
	st, err := db.MigrationStatus(context.Background(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, m := range st {
		if m.State == "applied" {
			out = append(out, m.Source.Version)
		}
	}
	return out
}

// Client is a WebSocket connection speaking the mase protocol.
type Client struct {
	t     testing.TB
	c     *websocket.Conn
	in    chan map[string]any
	Token string
	ID    int64
}

// Dial opens a WebSocket connection to the test server.
func (s *Server) Dial(t testing.TB) *Client {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(s.WSURL(), nil)
	if err != nil {
		t.Fatalf("testutil: dial: %v", err)
	}
	c := &Client{t: t, c: conn, in: make(chan map[string]any, 256)}
	go func() {
		for {
			var m map[string]any
			if err := conn.ReadJSON(&m); err != nil {
				close(c.in)
				return
			}
			c.in <- m
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return c
}

// Send writes one frame; the session token is added automatically once known.
func (c *Client) Send(v map[string]any) {
	c.t.Helper()
	if _, ok := v["token"]; !ok && c.Token != "" {
		v["token"] = c.Token
	}
	if err := c.c.WriteJSON(v); err != nil {
		c.t.Fatalf("testutil: send: %v", err)
	}
}

// Recv returns the first frame of the given type, skipping others (presence events and so on).
// It fails the test if none arrives within RecvTimeout.
func (c *Client) Recv(typ string) map[string]any {
	c.t.Helper()
	m, ok := c.TryRecv(typ, RecvTimeout)
	if !ok {
		c.t.Fatalf("testutil: не пришёл кадр %q за %s", typ, RecvTimeout)
	}
	return m
}

// TryRecv is Recv that reports absence instead of failing.
func (c *Client) TryRecv(typ string, d time.Duration) (map[string]any, bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case m, ok := <-c.in:
			if !ok {
				return nil, false
			}
			if m["type"] == typ {
				return m, true
			}
		case <-timer.C:
			return nil, false
		}
	}
}

// Register creates an account and stores its token and id in the client.
func (c *Client) Register(phone, password, displayName, username string) {
	c.t.Helper()
	c.Send(map[string]any{"type": "auth.register", "phone": phone, "password": password,
		"displayName": displayName, "username": username})
	c.adoptSession(c.Recv("auth.session"))
}

// Login signs in and stores the token and id in the client.
func (c *Client) Login(phone, password string) {
	c.t.Helper()
	c.Send(map[string]any{"type": "auth.login", "phone": phone, "password": password})
	c.adoptSession(c.Recv("auth.session"))
}

func (c *Client) adoptSession(m map[string]any) {
	c.t.Helper()
	tok, _ := m["token"].(string)
	user, _ := m["user"].(map[string]any)
	id, _ := user["id"].(float64)
	if tok == "" || id == 0 {
		raw, _ := json.Marshal(m)
		c.t.Fatalf("testutil: неверный ответ сессии: %s", raw)
	}
	c.Token, c.ID = tok, int64(id)
}

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

// Env is a temporary database (all migrations applied) with media storage and a fresh hub.
type Env struct {
	DBPath   string
	MediaDir string
}

// Server is a running test server on top of an Env.
type Server struct {
	Env
	HTTP *httptest.Server
}

// NewDB prepares the process-wide state (db.DB, media directory, hub.H) for a test that needs no
// HTTP server. It is torn down by t.Cleanup and holds the process-wide lock until then.
func NewDB(t testing.TB) *Env {
	t.Helper()
	global.Lock()
	dir := t.TempDir()
	e := &Env{DBPath: filepath.Join(dir, "test.sqlite"), MediaDir: filepath.Join(dir, "media")}

	prevOut := log.Writer()
	log.SetOutput(io.Discard)
	if err := db.Open(e.DBPath); err != nil {
		log.SetOutput(prevOut)
		global.Unlock()
		t.Fatalf("testutil: db.Open: %v", err)
	}
	if err := media.Init(e.MediaDir); err != nil {
		log.SetOutput(prevOut)
		global.Unlock()
		t.Fatalf("testutil: media.Init: %v", err)
	}
	hub.Reset()

	t.Cleanup(func() {
		db.DB.Close()
		hub.Reset()
		log.SetOutput(prevOut)
		global.Unlock()
	})
	return e
}

// NewServer is NewDB plus the real mux behind an httptest server.
func NewServer(t testing.TB) *Server {
	t.Helper()
	s := &Server{Env: *NewDB(t)}
	s.HTTP = httptest.NewServer(server.NewMux())
	// cleanups run last-in first-out: this one runs before NewDB's, so the handlers stop
	// before the database is closed
	t.Cleanup(func() {
		s.HTTP.CloseClientConnections()
		s.HTTP.Close()
		// hijacked WebSocket handlers are not waited for by Close; one that never returns means
		// the hub is deadlocked, which must fail the test instead of hanging the whole run
		if !server.WaitTimeout(3 * time.Second) {
			t.Errorf("testutil: обработчики WebSocket не завершились за 3 с (взаимоблокировка хаба?)")
		}
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

// SendRaw writes a text frame as is (for malformed input).
func (c *Client) SendRaw(text string) {
	c.t.Helper()
	if err := c.c.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		c.t.Fatalf("testutil: send raw: %v", err)
	}
}

// Close closes the connection from the client side.
func (c *Client) Close() { c.c.Close() }

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

// Drain returns every frame that arrives until the connection has been quiet for quiet.
func (c *Client) Drain(quiet time.Duration) []map[string]any {
	var out []map[string]any
	for {
		select {
		case m, ok := <-c.in:
			if !ok {
				return out
			}
			out = append(out, m)
		case <-time.After(quiet):
			return out
		}
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

// WaitClosed reports whether the server closed this connection within d.
func (c *Client) WaitClosed(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-c.in:
			if !ok {
				return true
			}
		case <-timer.C:
			return false
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

// probeserver is a minimal stand-in for the mase server, just enough for netprobe:
// the WebSocket handshake with login and ping/pong, and /upload + /media over HTTP.
// It keeps everything in memory, has one fixed account and no database, so it can be
// thrown away after the measurements. It speaks plain HTTP on loopback; TLS is Caddy's job.
//
// Limits mirror the real server where they matter for the measurement: 512 KB per
// WebSocket frame (go-server/internal/hub/client.go) and ping frames every 54 s.
//
//	PROBE_ADDR      listen address, default 127.0.0.1:8081
//	PROBE_PHONE     account phone (required)
//	PROBE_PASSWORD  account password (required)
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxFrame      = 512 * 1024
	maxUpload     = 8 << 20
	maxStored     = 24 << 20
	maxTokens     = 32
	maxConns      = 8
	pongWait      = 60 * time.Second
	pingPeriod    = pongWait * 9 / 10
	writeDeadline = 15 * time.Second
)

type server struct {
	phone, password string

	mu      sync.Mutex
	tokens  []string
	files   map[string][]byte
	order   []string
	stored  int
	conns   chan struct{}
	upgrade websocket.Upgrader
}

func newServer(phone, password string) http.Handler {
	s := &server{
		phone: phone, password: password,
		files:   map[string][]byte{},
		conns:   make(chan struct{}, maxConns),
		upgrade: websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/media/", s.handleMedia)
	return mux
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *server) newToken() string {
	t := randHex(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, t)
	if len(s.tokens) > maxTokens {
		s.tokens = s.tokens[1:]
	}
	return t
}

func (s *server) validToken(t string) bool {
	if t == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(x), []byte(t)) == 1 {
			return true
		}
	}
	return false
}

func (s *server) credsOK(phone, password string) bool {
	a := subtle.ConstantTimeCompare([]byte(phone), []byte(s.phone))
	b := subtle.ConstantTimeCompare([]byte(password), []byte(s.password))
	return a&b == 1
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	select {
	case s.conns <- struct{}{}:
		defer func() { <-s.conns }()
	default:
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	c, err := s.upgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetReadLimit(maxFrame)
	c.SetReadDeadline(time.Now().Add(pongWait))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	var wmu sync.Mutex // one writer at a time: replies and server pings
	write := func(typ int, data []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		c.SetWriteDeadline(time.Now().Add(writeDeadline))
		return c.WriteMessage(typ, data)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(pingPeriod)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if write(websocket.PingMessage, nil) != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	authed := false
	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			Type     string `json:"type"`
			Token    string `json:"token"`
			Phone    string `json:"phone"`
			Password string `json:"password"`
		}
		if json.Unmarshal(raw, &m) != nil {
			write(websocket.TextMessage, []byte(`{"type":"err","code":"bad_json"}`))
			continue
		}
		switch m.Type {
		case "auth.login", "auth.register":
			if !s.credsOK(m.Phone, m.Password) {
				write(websocket.TextMessage, []byte(`{"type":"err","code":"bad_credentials"}`))
				continue
			}
			authed = true
			write(websocket.TextMessage, []byte(`{"type":"auth.session","token":"`+s.newToken()+
				`","user":{"id":1,"displayName":"netprobe","username":"netprobe"}}`))
		case "ping":
			if !authed && !s.validToken(m.Token) {
				write(websocket.TextMessage, []byte(`{"type":"err","code":"auth_required"}`))
				continue
			}
			authed = true
			write(websocket.TextMessage, []byte(`{"type":"pong"}`))
		default:
			write(websocket.TextMessage, []byte(`{"type":"err","code":"unknown_type"}`))
		}
	}
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validToken(bearer(r)) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUpload))
	if err != nil || len(data) == 0 {
		http.Error(w, `{"error":"bad_body"}`, http.StatusRequestEntityTooLarge)
		return
	}
	id := randHex(16)
	s.mu.Lock()
	s.files[id] = data
	s.order = append(s.order, id)
	s.stored += len(data)
	for s.stored > maxStored && len(s.order) > 1 {
		old := s.order[0]
		s.order = s.order[1:]
		s.stored -= len(s.files[old])
		delete(s.files, old)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"mediaId":"` + id + `"}`))
}

func (s *server) handleMedia(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(bearer(r)) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	data, ok := s.files[strings.TrimPrefix(r.URL.Path, "/media/")]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func main() {
	phone, password := os.Getenv("PROBE_PHONE"), os.Getenv("PROBE_PASSWORD")
	if phone == "" || password == "" {
		log.Fatal("нужны PROBE_PHONE и PROBE_PASSWORD")
	}
	addr := os.Getenv("PROBE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8081"
	}
	srv := &http.Server{Addr: addr, Handler: newServer(phone, password), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("probeserver: слушаю %s", addr)
	log.Fatal(srv.ListenAndServe())
}

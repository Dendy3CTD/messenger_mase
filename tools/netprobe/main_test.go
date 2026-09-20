package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A server that completes the handshake and then never reads: a large write must fail
// by deadline instead of hanging (regression: the 1 MB step hung through Cloudflare).
func TestPingFailsByDeadlineWhenPeerStopsReading(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		<-stop
	}))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := newWS(c)

	done := make(chan error, 1)
	go func() { done <- w.ping(64<<20, time.Second) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ожидалась ошибка по дедлайну записи")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ping завис: у записи нет дедлайна")
	}
}

// Frames up to 20 KB are answered, bigger ones are swallowed: the ladder must pass the
// small sizes, fail at 64 KB and stop there.
func TestLadderStopsAtFirstFailingSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if len(msg) <= 20<<10 {
				c.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`))
			}
		}
	}))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p := &probe{}
	if p.ladder(newWS(c), 500*time.Millisecond) {
		t.Fatal("лестница должна была провалиться на 64 КБ")
	}
	var got []string
	for _, r := range p.rows {
		got = append(got, r.step+"="+map[bool]string{true: "ok", false: "FAIL"}[r.ok])
	}
	want := "ws кадр 4 КБ=ok,ws кадр 16 КБ=ok,ws кадр 64 КБ=FAIL"
	if strings.Join(got, ",") != want {
		t.Fatalf("строки: %v, ожидалось %s", got, want)
	}
}

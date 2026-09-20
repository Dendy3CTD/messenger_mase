package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	return c
}

func roundTrip(t *testing.T, c *websocket.Conn, v map[string]any) map[string]any {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := c.ReadJSON(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLoginPingUploadDownload(t *testing.T) {
	srv := httptest.NewServer(newServer("+70000000099", "pw"))
	defer srv.Close()
	c := dial(t, srv)

	if m := roundTrip(t, c, map[string]any{"type": "ping"}); m["code"] != "auth_required" {
		t.Fatalf("ping без входа: %v", m)
	}
	if m := roundTrip(t, c, map[string]any{"type": "auth.login", "phone": "+70000000099", "password": "bad"}); m["code"] != "bad_credentials" {
		t.Fatalf("неверный пароль: %v", m)
	}
	m := roundTrip(t, c, map[string]any{"type": "auth.login", "phone": "+70000000099", "password": "pw"})
	token, _ := m["token"].(string)
	if m["type"] != "auth.session" || token == "" {
		t.Fatalf("вход: %v", m)
	}
	if m := roundTrip(t, c, map[string]any{"type": "ping", "token": token, "pad": strings.Repeat("x", 256<<10)}); m["type"] != "pong" {
		t.Fatalf("ping с кадром 256 КБ: %v", m)
	}

	body := bytes.Repeat([]byte{7}, 5<<20)
	req, _ := http.NewRequest("POST", srv.URL+"/upload", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("загрузка: %v %v", err, resp)
	}
	var up map[string]string
	json.NewDecoder(resp.Body).Decode(&up)
	resp.Body.Close()

	req, _ = http.NewRequest("GET", srv.URL+"/media/"+up["mediaId"], nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = http.DefaultClient.Do(req)
	var got bytes.Buffer
	got.ReadFrom(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got.Bytes(), body) {
		t.Fatal("скачанное не совпало с загруженным")
	}

	resp, _ = http.Get(srv.URL + "/media/" + up["mediaId"])
	if resp.StatusCode != 401 {
		t.Fatalf("скачивание без токена: %d", resp.StatusCode)
	}
}

func TestFrameOverLimitClosesConnection(t *testing.T) {
	srv := httptest.NewServer(newServer("p", "pw"))
	defer srv.Close()
	c := dial(t, srv)
	c.WriteJSON(map[string]any{"type": "ping", "pad": strings.Repeat("x", 600<<10)})
	var m map[string]any
	if err := c.ReadJSON(&m); err == nil {
		t.Fatalf("кадр 600 КБ должен закрыть соединение, пришло %v", m)
	}
}

func TestUploadOverLimitIsRejected(t *testing.T) {
	h := newServer("p", "pw")
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := dial(t, srv)
	m := roundTrip(t, c, map[string]any{"type": "auth.login", "phone": "p", "password": "pw"})
	req, _ := http.NewRequest("POST", srv.URL+"/upload", bytes.NewReader(make([]byte, 9<<20)))
	req.Header.Set("Authorization", "Bearer "+m["token"].(string))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("9 МБ должны быть отклонены (413): %v %v", err, resp)
	}
}

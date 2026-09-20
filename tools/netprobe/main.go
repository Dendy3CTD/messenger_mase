// netprobe measures whether a mase server is reachable from the network it runs on.
//
// Steps: DNS, TCP, WebSocket handshake, login, ping, 1 MB upstream over WebSocket,
// 5 MB upload and download over HTTPS (sha256 checked), then holding the connection
// with a ping every 30 s. Prints a table "step / ok / time / detail".
//
// Credentials come from the environment only (never flags, they land in shell history):
//
//	MASE_PROBE_PHONE, MASE_PROBE_PASSWORD   login of a dedicated probe account
//
// Use a dedicated account: the server keeps one connection per user, so probing as a real
// user kicks that user's app off. -register creates the probe account first (dev or a
// deliberately chosen probe account; the server has open registration).
//
// Side effects on the server: one session token, and one orphan file of -http-mb in media/.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// The server closes a connection on frames over 512 KB (hub/client.go), so 1 MB goes in chunks.
	wsChunk     = 256 * 1024
	replyTimout = 15 * time.Second
)

// client is replaced in main when -insecure is set.
var client = http.DefaultClient

type row struct {
	step, detail string
	ok           bool
	took         time.Duration
}

type probe struct {
	rows   []row
	failed bool
}

func (p *probe) add(step string, start time.Time, err error, detail string) bool {
	r := row{step: step, ok: err == nil, took: time.Since(start), detail: detail}
	if err != nil {
		r.detail = err.Error()
		p.failed = true
	}
	p.rows = append(p.rows, r)
	mark := "ok  "
	if !r.ok {
		mark = "FAIL"
	}
	fmt.Printf("%-4s %-28s %9s  %s\n", mark, r.step, r.took.Round(time.Millisecond), r.detail)
	return r.ok
}

type wsConn struct {
	c     *websocket.Conn
	in    chan map[string]any
	dead  chan error
	token string
}

func newWS(c *websocket.Conn) *wsConn {
	w := &wsConn{c: c, in: make(chan map[string]any, 64), dead: make(chan error, 1)}
	go func() {
		for {
			var m map[string]any
			if err := c.ReadJSON(&m); err != nil {
				w.dead <- err
				return
			}
			select {
			case w.in <- m:
			default: // presence events etc. are not needed
			}
		}
	}()
	return w
}

// await returns the first message of the given type (or an err frame).
func (w *wsConn) await(typ string, d time.Duration) (map[string]any, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case m := <-w.in:
			s, _ := m["type"].(string)
			if s == typ {
				return m, nil
			}
			if s == "err" {
				return nil, fmt.Errorf("сервер ответил ошибкой: %v", m["code"])
			}
		case err := <-w.dead:
			return nil, fmt.Errorf("соединение закрыто: %w", err)
		case <-t.C:
			return nil, fmt.Errorf("нет ответа %q за %s", typ, d)
		}
	}
}

// send writes with a deadline: without it a stalled path blocks in the kernel send
// buffer forever (seen through Cloudflare on 2026-09-20: the 1 MB step hung).
func (w *wsConn) send(v any, d time.Duration) error {
	w.c.SetWriteDeadline(time.Now().Add(d))
	return w.c.WriteJSON(v)
}

func (w *wsConn) ping(pad int, d time.Duration) error {
	m := map[string]any{"type": "ping", "token": w.token}
	if pad > 0 {
		m["pad"] = strings.Repeat("x", pad)
	}
	if err := w.send(m, d); err != nil {
		return err
	}
	_, err := w.await("pong", d)
	return err
}

func main() {
	wsURL := flag.String("url", "", "адрес сервера, например wss://mase.nemilk.ru/ws")
	label := flag.String("label", "", "подпись прогона (сеть/путь), попадёт в заголовок")
	hold := flag.Duration("hold", 30*time.Minute, "сколько держать соединение (0 — пропустить)")
	every := flag.Duration("ping-every", 30*time.Second, "период ping при удержании")
	httpMB := flag.Int("http-mb", 5, "размер файла для HTTPS-загрузки и скачивания, МБ (0 — пропустить)")
	insecure := flag.Bool("insecure", false, "не проверять сертификат сервера (только для пробного сервера по IP с самоподписанным сертификатом)")
	register := flag.Bool("register", false, "сначала зарегистрировать пробный аккаунт (телефон и пароль из окружения)")
	flag.Parse()

	phone, password := os.Getenv("MASE_PROBE_PHONE"), os.Getenv("MASE_PROBE_PASSWORD")
	u, err := url.Parse(*wsURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || phone == "" || password == "" {
		fmt.Fprintln(os.Stderr, "нужны -url ws(s)://хост/ws и переменные MASE_PROBE_PHONE, MASE_PROBE_PASSWORD")
		os.Exit(2)
	}
	base := *u
	base.Scheme = map[string]string{"ws": "http", "wss": "https"}[u.Scheme]
	base.Path, base.RawQuery = "", ""

	tlsConf := &tls.Config{InsecureSkipVerify: *insecure}
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf, Proxy: http.ProxyFromEnvironment}}
	fmt.Printf("netprobe %s  метка: %q  сервер: %s\n", time.Now().Format("2006-01-02 15:04:05 MST"), *label, u.Host)
	if *insecure {
		fmt.Println("ВНИМАНИЕ: -insecure, сертификат сервера НЕ проверяется")
	}
	p := &probe{}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = map[string]string{"ws": "80", "wss": "443"}[u.Scheme]
	}

	t := time.Now()
	addrs, err := net.LookupHost(host)
	p.add("dns", t, err, strings.Join(addrs, ","))
	if err != nil {
		p.finish()
	}

	t = time.Now()
	tc, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if p.add("tcp", t, err, tcRemote(tc)) {
		tc.Close()
	} else {
		p.finish()
	}

	t = time.Now()
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, TLSClientConfig: tlsConf, Proxy: http.ProxyFromEnvironment}
	c, _, err := d.Dial(u.String(), nil)
	if !p.add("ws (TLS+handshake)", t, err, "") {
		p.finish()
	}
	defer c.Close()
	w := newWS(c)

	t = time.Now()
	kind := "auth.login"
	if *register {
		kind = "auth.register"
	}
	req := map[string]any{"type": kind, "phone": phone, "password": password}
	if *register {
		req["displayName"], req["username"] = "netprobe", "netprobe"
	}
	if err = w.send(req, replyTimout); err == nil {
		var m map[string]any
		if m, err = w.await("auth.session", replyTimout); err == nil {
			w.token, _ = m["token"].(string)
		}
	}
	if !p.add(kind, t, err, "") {
		p.finish()
	}

	t = time.Now()
	p.add("ping", t, w.ping(0, replyTimout), "")

	if !p.ladder(w, replyTimout) {
		p.finish()
	}

	total := 1 << 20
	t = time.Now()
	var werr error
	for sent := 0; sent < total && werr == nil; sent += wsChunk {
		werr = w.ping(wsChunk, replyTimout)
	}
	p.add("ws upstream 1 МБ", t, werr, fmt.Sprintf("%d кадров по %d КБ", total/wsChunk, wsChunk>>10))

	if *httpMB > 0 {
		p.httpSteps(base.String(), w.token, *httpMB<<20)
	}
	if *hold > 0 {
		p.holdConn(w, *hold, *every)
	}
	p.finish()
}

// frameLadder tells "the path cuts by frame size" from "by total volume": every size is
// a separate ping frame, and the first failure stops the ladder (the connection is
// unusable after a write timeout anyway).
var frameLadder = []int{4 << 10, 16 << 10, 64 << 10, 128 << 10, 256 << 10}

func (p *probe) ladder(w *wsConn, d time.Duration) bool {
	sent := 0
	for _, size := range frameLadder {
		t := time.Now()
		err := w.ping(size, d)
		detail := fmt.Sprintf("всего отправлено %d КБ", (sent+size)>>10)
		if !p.add(fmt.Sprintf("ws кадр %d КБ", size>>10), t, err, detail) {
			return false
		}
		sent += size
	}
	return true
}

func tcRemote(c net.Conn) string {
	if c == nil {
		return ""
	}
	return c.RemoteAddr().String()
}

func (p *probe) httpSteps(base, token string, size int) {
	data := make([]byte, size)
	rand.Read(data)
	want := sha256.Sum256(data)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/upload", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	id, err := doJSONField(req, "mediaId")
	if !p.add(fmt.Sprintf("https upload %d МБ", size>>20), t, err, "") {
		return
	}

	t = time.Now()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/media/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	var got [32]byte
	if err == nil {
		h := sha256.New()
		var n int64
		n, err = io.Copy(h, resp.Body)
		resp.Body.Close()
		if err == nil && resp.StatusCode != 200 {
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else if err == nil && n != int64(size) {
			err = fmt.Errorf("получено %d байт из %d", n, size)
		}
		copy(got[:], h.Sum(nil))
	}
	if err == nil && got != want {
		err = fmt.Errorf("sha256 не совпал")
	}
	p.add(fmt.Sprintf("https download %d МБ", size>>20), t, err, "sha256 совпал")
}

func doJSONField(req *http.Request, field string) (string, error) {
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", err
	}
	s, _ := m[field].(string)
	if s == "" {
		return "", fmt.Errorf("в ответе нет поля %q", field)
	}
	return s, nil
}

func (p *probe) holdConn(w *wsConn, hold, every time.Duration) {
	start := time.Now()
	tick := time.NewTicker(every)
	defer tick.Stop()
	n := 0
	for time.Since(start) < hold {
		<-tick.C
		if err := w.ping(0, replyTimout); err != nil {
			p.add(fmt.Sprintf("hold %s", hold), start, fmt.Errorf("оборвано на %s после %d ping: %w",
				time.Since(start).Round(time.Second), n, err), "")
			return
		}
		n++
	}
	p.add(fmt.Sprintf("hold %s", hold), start, nil, fmt.Sprintf("%d ping, соединение живо", n))
}

func (p *probe) finish() {
	if p.failed {
		fmt.Println("\nИТОГ: есть провалы")
		os.Exit(1)
	}
	fmt.Println("\nИТОГ: все шаги прошли")
	os.Exit(0)
}

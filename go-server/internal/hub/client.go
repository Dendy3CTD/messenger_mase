package hub

import (
	"log"
	"runtime/debug"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512 * 1024 // 512 KB per frame
)

// ReadPump pumps messages from the WebSocket connection to the handler.
// Should run in its own goroutine.
func (c *Client) ReadPump(onMessage func([]byte), onClose func()) {
	defer func() {
		H.Unregister(c)
		c.conn.Close()
		c.guard("onClose", onClose)
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[WS] read error uid=%d: %v", c.UserID, err)
			}
			return
		}
		c.guard("onMessage", func() { onMessage(msg) })
	}
}

func (c *Client) closePolitely() {
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	c.conn.WriteMessage(websocket.CloseMessage, []byte{})
}

// guard runs f and turns a panic into a log entry (and an error frame for message handlers), so a
// bug in one handler cannot take down the connection loop or leave shared state locked.
func (c *Client) guard(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC] %s uid=%d: %v\n%s", what, c.UserID, r, debug.Stack())
			if what == "onMessage" {
				H.SendTo(c, []byte(`{"type":"err","code":"server_error"}`))
			}
		}
	}()
	f()
}

// WritePump pumps messages from the send channel to the WebSocket connection.
// Should run in its own goroutine.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-c.kick: // replaced by a newer login
			c.closePolitely()
			return

		case <-c.done: // unregistered
			c.closePolitely()
			return

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Serve starts the read and write pumps for this client.
// onMessage is called for each incoming message.
// onClose is called when the connection closes.
func Serve(conn *websocket.Conn, onMessage func([]byte), onClose func()) {
	c := newClient(conn)
	H.Register(c)
	go c.WritePump()
	c.ReadPump(onMessage, onClose) // blocks until closed
}

// ServeClient starts the pumps with an existing *Client.
// Used when the caller needs access to the Client before pumping starts.
func ServeClient(c *Client, onMessage func([]byte), onClose func()) {
	H.Register(c)
	go c.WritePump()
	c.ReadPump(onMessage, onClose)
}

// NewClientFromConn creates a client from an existing WebSocket connection.
func NewClientFromConn(conn *websocket.Conn) *Client {
	return newClient(conn)
}

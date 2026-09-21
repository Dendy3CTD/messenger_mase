package hub

import (
	"sync"

	"github.com/gorilla/websocket"
)

// Client represents one connected WebSocket session.
type Client struct {
	conn   *websocket.Conn
	UserID int64
	Token  string

	// send is never closed: closing a channel that other goroutines may still be sending to
	// panics (R-15). done marks a client that has been unregistered; senders check it and drop.
	send chan []byte
	done chan struct{}
	// kick asks the write pump to close the connection (a newer login replaced this one).
	kick     chan struct{}
	doneOnce sync.Once
	kickOnce sync.Once
}

func newClient(conn *websocket.Conn) *Client {
	return &Client{
		conn: conn,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
		kick: make(chan struct{}),
	}
}

// enqueue queues a message for the write pump. It never blocks and never panics; it reports
// false when the client is gone or its buffer is full.
func (c *Client) enqueue(msg []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

func (c *Client) markDone()     { c.doneOnce.Do(func() { close(c.done) }) }
func (c *Client) requestClose() { c.kickOnce.Do(func() { close(c.kick) }) }

// Hub keeps track of all connected clients.
type Hub struct {
	mu sync.RWMutex

	// fd→client (all connections, including unauthenticated)
	all map[*Client]struct{}

	// userID→client (authenticated only; one connection per user)
	byUser map[int64]*Client
}

// H is the process-wide hub used by the handlers.
var H = New()

// New returns an empty hub.
func New() *Hub {
	return &Hub{
		all:    make(map[*Client]struct{}),
		byUser: make(map[int64]*Client),
	}
}

// Reset replaces the process-wide hub with an empty one. It exists for tests, which share the
// global hub and must not see connections left over from a previous test.
func Reset() { H = New() }

// Register adds a new connection.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.all[c] = struct{}{}
	h.mu.Unlock()
}

// Unregister removes a connection and cleans up the user mapping.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	delete(h.all, c)
	// A socket can authenticate more than once; remove every entry that points at it.
	for uid, existing := range h.byUser {
		if existing == c {
			delete(h.byUser, uid)
		}
	}
	h.mu.Unlock()
	c.markDone()
}

// Authenticate binds a userID+token to the client and registers the user mapping.
func (h *Hub) Authenticate(c *Client, userID int64, token string) {
	h.mu.Lock()
	defer h.mu.Unlock() // a panic below must not leave the hub locked (R-15)
	// The same socket signing in as another user no longer belongs to the previous one.
	if c.UserID != 0 && c.UserID != userID && h.byUser[c.UserID] == c {
		delete(h.byUser, c.UserID)
	}
	// Disconnect any existing connection for this user
	if old := h.byUser[userID]; old != nil && old != c {
		old.requestClose()
	}
	c.UserID = userID
	c.Token = token
	h.byUser[userID] = c
}

// Send queues a message to a specific user. Returns false if offline.
func (h *Hub) Send(userID int64, msg []byte) bool {
	h.mu.RLock()
	c, ok := h.byUser[userID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	return c.enqueue(msg)
}

// SendTo sends directly to a client.
func (h *Hub) SendTo(c *Client, msg []byte) {
	c.enqueue(msg)
}

// BroadcastAuthenticated sends msg to all authenticated users except excludeUserID (-1 = send to all).
func (h *Hub) BroadcastAuthenticated(msg []byte, excludeUserID int64) {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.byUser))
	for uid, c := range h.byUser {
		if uid != excludeUserID {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.enqueue(msg)
	}
}

// OnlineUserIDs returns the set of authenticated user IDs.
func (h *Hub) OnlineUserIDs() []int64 {
	h.mu.RLock()
	ids := make([]int64, 0, len(h.byUser))
	for uid := range h.byUser {
		ids = append(ids, uid)
	}
	h.mu.RUnlock()
	return ids
}

// IsOnline reports whether a user is connected.
func (h *Hub) IsOnline(userID int64) bool {
	h.mu.RLock()
	_, ok := h.byUser[userID]
	h.mu.RUnlock()
	return ok
}

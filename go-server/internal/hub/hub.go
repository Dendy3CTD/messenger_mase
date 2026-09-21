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
	send   chan []byte
}

func newClient(conn *websocket.Conn) *Client {
	return &Client{
		conn: conn,
		send: make(chan []byte, 256),
	}
}

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
	if c.UserID != 0 {
		if existing, ok := h.byUser[c.UserID]; ok && existing == c {
			delete(h.byUser, c.UserID)
		}
	}
	h.mu.Unlock()
	close(c.send)
}

// Authenticate binds a userID+token to the client and registers the user mapping.
func (h *Hub) Authenticate(c *Client, userID int64, token string) {
	h.mu.Lock()
	// Disconnect any existing connection for this user
	if old := h.byUser[userID]; old != nil && old != c {
		select {
		case old.send <- nil: // signal close
		default:
		}
	}
	c.UserID = userID
	c.Token = token
	h.byUser[userID] = c
	h.mu.Unlock()
}

// Send queues a message to a specific user. Returns false if offline.
func (h *Hub) Send(userID int64, msg []byte) bool {
	h.mu.RLock()
	c, ok := h.byUser[userID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

// SendTo sends directly to a client.
func (h *Hub) SendTo(c *Client, msg []byte) {
	select {
	case c.send <- msg:
	default:
	}
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
		select {
		case c.send <- msg:
		default:
		}
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

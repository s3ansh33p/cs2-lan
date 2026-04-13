package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSHub manages topic-based WebSocket subscriptions. Clients subscribe to topics
// and receive messages published to those topics. Admin topics require authentication
// which is checked at subscribe time using the session cookie from the upgrade request.
type WSHub struct {
	mu      sync.RWMutex
	clients map[*wsClient]bool
	topics  map[string]map[*wsClient]bool // topic -> set of subscribers

	// authFn checks if a request has a valid admin session.
	// Set by the handler after construction.
	authFn func(r *http.Request) bool

	// snapshots[topic] returns the current state to send to a newly-subscribed
	// client. Returning nil data skips the initial send.
	snapshots map[string]SnapshotFn
}

// SnapshotFn produces the current state for a topic so a newly-subscribed
// client can be brought up to date immediately, without waiting for the next
// publish.
type SnapshotFn func() (msgType string, data any)

type wsClient struct {
	hub    *WSHub
	conn   *websocket.Conn
	send   chan []byte
	topics map[string]bool
	done   chan struct{}
	req    *http.Request // original upgrade request (for auth checks)
}

// wsSubscribeMsg is a client-to-server message for managing subscriptions.
type wsSubscribeMsg struct {
	Subscribe   string `json:"subscribe,omitempty"`
	Unsubscribe string `json:"unsubscribe,omitempty"`
}

// wsMessage is a server-to-client message with a topic and typed payload.
type wsMessage struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}

// NewWSHub creates a new hub ready for use.
func NewWSHub() *WSHub {
	return &WSHub{
		clients:   make(map[*wsClient]bool),
		topics:    make(map[string]map[*wsClient]bool),
		snapshots: make(map[string]SnapshotFn),
	}
}

// RegisterSnapshot installs a snapshot function for a topic. When a client
// subscribes to that topic, the snapshot is sent immediately to that client
// alone so it has current state without waiting for the next publish.
func (h *WSHub) RegisterSnapshot(topic string, fn SnapshotFn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snapshots[topic] = fn
}

// isAdminTopic returns true for topics that require authentication.
func isAdminTopic(topic string) bool {
	if topic == "dashboard" || topic == "tournaments" {
		return true
	}
	if strings.HasPrefix(topic, "tournament:") {
		return true
	}
	return false
}

// Register adds a client to the hub.
func (h *WSHub) Register(client *wsClient) {
	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()
}

// Deregister removes a client from the hub and all its topic subscriptions.
func (h *WSHub) Deregister(client *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for topic := range client.topics {
		if subs, ok := h.topics[topic]; ok {
			delete(subs, client)
			if len(subs) == 0 {
				delete(h.topics, topic)
			}
		}
	}
	delete(h.clients, client)
	close(client.send)
}

// Subscribe adds a client to a topic. Admin topics require a valid session.
// If a snapshot function is registered for the topic, the current state is
// sent to this client alone right after the subscription is recorded.
func (h *WSHub) Subscribe(client *wsClient, topic string) {
	if isAdminTopic(topic) {
		if h.authFn == nil || !h.authFn(client.req) {
			slog.Warn("wshub: subscribe denied (not authenticated)", "topic", topic, "ip", client.conn.RemoteAddr())
			return
		}
	}

	h.mu.Lock()
	client.topics[topic] = true
	if h.topics[topic] == nil {
		h.topics[topic] = make(map[*wsClient]bool)
	}
	h.topics[topic][client] = true
	snapFn := h.snapshots[topic]
	h.mu.Unlock()

	if snapFn == nil {
		return
	}
	msgType, data := snapFn()
	if data == nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Warn("wshub: snapshot marshal failed", "topic", topic, "err", err)
		return
	}
	envelope, err := json.Marshal(wsMessage{
		Topic: topic,
		Type:  msgType,
		Data:  json.RawMessage(payload),
	})
	if err != nil {
		slog.Warn("wshub: snapshot envelope marshal failed", "topic", topic, "err", err)
		return
	}
	select {
	case client.send <- envelope:
	default:
		// send buffer full — client will get the next regular publish
	}
}

// Unsubscribe removes a client from a topic.
func (h *WSHub) Unsubscribe(client *wsClient, topic string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(client.topics, topic)
	if subs, ok := h.topics[topic]; ok {
		delete(subs, client)
		if len(subs) == 0 {
			delete(h.topics, topic)
		}
	}
}

// SubscribedTournamentIDs returns unique tournament IDs that have active subscribers
// on bracket:{id} or tournament:{id} topics.
func (h *WSHub) SubscribedTournamentIDs() []int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := make(map[int64]bool)
	for topic := range h.topics {
		var idStr string
		if strings.HasPrefix(topic, "bracket:") {
			idStr = topic[len("bracket:"):]
		} else if strings.HasPrefix(topic, "tournament:") {
			idStr = topic[len("tournament:"):]
		} else {
			continue
		}
		var id int64
		for _, c := range idStr {
			if c < '0' || c > '9' {
				id = 0
				break
			}
			id = id*10 + int64(c-'0')
		}
		if id > 0 && !seen[id] {
			seen[id] = true
		}
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// Publish sends a message to all subscribers of the given topic.
func (h *WSHub) Publish(topic, msgType string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Warn("wshub: marshal failed", "topic", topic, "err", err)
		return
	}

	msg := wsMessage{
		Topic: topic,
		Type:  msgType,
		Data:  json.RawMessage(payload),
	}
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("wshub: envelope marshal failed", "topic", topic, "err", err)
		return
	}

	h.mu.RLock()
	subs := h.topics[topic]
	// Copy subscriber list to avoid holding lock during send
	clients := make([]*wsClient, 0, len(subs))
	for c := range subs {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	slog.Info("wshub: publish", "topic", topic, "subscribers", len(clients))

	for _, c := range clients {
		select {
		case c.send <- msgBytes:
		default:
			// Client send buffer full — skip this message for them
		}
	}
}

// readPump reads subscribe/unsubscribe messages from the client WebSocket.
func (c *wsClient) readPump() {
	defer func() {
		c.hub.Deregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		var msg wsSubscribeMsg
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		if msg.Subscribe != "" {
			c.hub.Subscribe(c, msg.Subscribe)
			slog.Info("wshub: subscribed", "topic", msg.Subscribe, "ip", c.conn.RemoteAddr())
		}
		if msg.Unsubscribe != "" {
			c.hub.Unsubscribe(c, msg.Unsubscribe)
			slog.Debug("wshub: unsubscribed", "topic", msg.Unsubscribe, "ip", c.conn.RemoteAddr())
		}
	}
}

// writePump drains the send channel and writes messages to the WebSocket.
func (c *wsClient) writePump() {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				// Hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

			// Drain queued messages in one write batch
			n := len(c.send)
			for i := 0; i < n; i++ {
				msg, ok := <-c.send
				if !ok {
					return
				}
				c.conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ServeWS handles WebSocket connections for the unified hub.
func (h *Handler) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("wshub: upgrade failed", "err", err)
		return
	}
	slog.Info("wshub: connected", "ip", r.RemoteAddr)

	client := &wsClient{
		hub:    h.hub,
		conn:   conn,
		send:   make(chan []byte, 64),
		topics: make(map[string]bool),
		done:   make(chan struct{}),
		req:    r,
	}

	h.hub.Register(client)

	go client.writePump()
	go client.readPump()
}

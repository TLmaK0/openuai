package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"openuai/internal/logger"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WSMessage is the envelope for all WebSocket messages.
type WSMessage struct {
	Event     string `json:"event"`
	RequestID string `json:"request_id,omitempty"`
	Data      any    `json:"data"`
}

// Hub manages all active WebSocket clients and broadcasts messages.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}
}

// Client wraps a single WebSocket connection.
type Client struct {
	conn *websocket.Conn
	send chan []byte
	hub  *Hub
}

func newHub() *Hub {
	return &Hub{
		clients: make(map[*Client]struct{}),
	}
}

func (h *Hub) register(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	logger.Info("WebSocket client connected (total: %d)", h.clientCount())
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	close(c.send)
	logger.Info("WebSocket client disconnected (total: %d)", h.clientCount())
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(event, requestID string, data any) {
	msg := WSMessage{Event: event, RequestID: requestID, Data: data}
	raw, err := json.Marshal(msg)
	if err != nil {
		logger.Error("WebSocket marshal error: %s", err.Error())
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- raw:
		default:
			// Client too slow, drop message
			logger.Debug("WebSocket: dropping message for slow client")
		}
	}
}

// handleWS upgrades the connection and manages the client lifecycle.
func (h *Hub) handleWS(c echo.Context) error {
	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}

	client := &Client{
		conn: conn,
		send: make(chan []byte, 64),
		hub:  h,
	}
	h.register(client)

	go client.writePump()
	go client.readPump()
	return nil
}

// writePump sends messages from the send channel to the WebSocket.
func (c *Client) writePump() {
	defer func() {
		c.conn.Close()
	}()

	for msg := range c.send {
		c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			c.hub.unregister(c)
			return
		}
	}
}

// readPump reads (and discards) incoming messages; closes on error.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

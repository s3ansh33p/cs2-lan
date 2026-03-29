package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"cs2-panel/internal/docker"

	"github.com/gorilla/websocket"
)

const (
	pingInterval = 15 * time.Second
	pongWait     = 20 * time.Second
	writeWait    = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (h *Handler) LogsWebSocket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	// Configure pong handler to extend read deadline
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader, isTTY, err := h.docker.StreamLogs(ctx, name)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer reader.Close()

	done := make(chan struct{})

	// Read from client (detect disconnect)
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Ping ticker to keep connection alive
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	lines := make(chan string, 64)
	go func() {
		docker.ReadLogLines(reader, isTTY, lines, done)
		close(lines)
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("ws ping %s: %v", name, err)
				return
			}
		case line, ok := <-lines:
			if !ok {
				log.Printf("ws log stream ended for %s", name)
				return
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		}
	}
}

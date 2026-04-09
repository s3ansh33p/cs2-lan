package web

import (
	"html"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// SetAnnouncement updates the current announcement and broadcasts to all clients.
func (h *Handler) SetAnnouncement(w http.ResponseWriter, r *http.Request) {
	msg := sanitize(r.FormValue("message"))
	link := sanitize(r.FormValue("link"))

	h.announceMu.Lock()
	h.announcement = msg
	h.announceLink = link
	h.announceMu.Unlock()

	h.announceBcast.notify()
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// AnnounceWebSocket pushes announcement updates to all connected clients.
func (h *Handler) AnnounceWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws announce upgrade: %v", err)
		return
	}
	defer conn.Close()

	updates, unsub := h.announceBcast.subscribe()
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	h.sendAnnouncement(conn)

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-updates:
			if err := h.sendAnnouncement(conn); err != nil {
				return
			}
		}
	}
}

func (h *Handler) sendAnnouncement(conn *websocket.Conn) error {
	h.announceMu.RLock()
	msg := h.announcement
	link := h.announceLink
	h.announceMu.RUnlock()

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Link    string `json:"link,omitempty"`
	}{
		Type:    "announcement",
		Message: html.EscapeString(msg),
		Link:    html.EscapeString(link),
	})
}

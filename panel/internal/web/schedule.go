package web

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"time"

	"unilan/internal/db"

	"github.com/gorilla/websocket"
)

// HomePage renders the public info and schedule page.
func (h *Handler) HomePage(w http.ResponseWriter, r *http.Request) {
	items, err := h.db.ListScheduleItems()
	if err != nil {
		log.Printf("list schedule: %v", err)
	}
	itemsJSON, _ := json.Marshal(items)

	h.render(w, "home.html", map[string]any{
		"Title":      "",
		"ItemsJSON":  template.JS(itemsJSON),
		"EventStart": h.db.GetSetting("event_start"),
		"EventEnd":   h.db.GetSetting("event_end"),
	})
}

// AdminSchedule renders the admin schedule management page.
func (h *Handler) AdminSchedule(w http.ResponseWriter, r *http.Request) {
	items, err := h.db.ListScheduleItems()
	if err != nil {
		log.Printf("list schedule: %v", err)
	}
	itemsJSON, _ := json.Marshal(items)

	h.render(w, "admin_schedule.html", map[string]any{
		"Title":      "Schedule",
		"ItemsJSON":  template.JS(itemsJSON),
		"EventStart": h.db.GetSetting("event_start"),
		"EventEnd":   h.db.GetSetting("event_end"),
	})
}

// AdminCreateScheduleItem handles adding a new schedule item.
func (h *Handler) AdminCreateScheduleItem(w http.ResponseWriter, r *http.Request) {
	startAt := sanitize(r.FormValue("start_at"))
	endAt := sanitize(r.FormValue("end_at"))
	title := sanitize(r.FormValue("title"))
	desc := sanitizeDesc(r.FormValue("description"))
	color := sanitize(r.FormValue("color"))

	if startAt == "" || title == "" {
		if isAJAX(r) {
			http.Error(w, "start_at and title required", http.StatusBadRequest)
		} else {
			http.Redirect(w, r, "/admin/schedule", http.StatusSeeOther)
		}
		return
	}

	if _, err := h.db.CreateScheduleItem(startAt, endAt, title, desc, color); err != nil {
		log.Printf("create schedule item: %v", err)
		if isAJAX(r) {
			http.Error(w, "Failed to create", http.StatusInternalServerError)
		} else {
			http.Redirect(w, r, "/admin/schedule", http.StatusSeeOther)
		}
		return
	}

	h.notifySchedule()

	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/schedule", http.StatusSeeOther)
}

// AdminUpdateScheduleItem handles editing an existing schedule item.
func (h *Handler) AdminUpdateScheduleItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	startAt := sanitize(r.FormValue("start_at"))
	endAt := sanitize(r.FormValue("end_at"))
	title := sanitize(r.FormValue("title"))
	desc := sanitizeDesc(r.FormValue("description"))
	color := sanitize(r.FormValue("color"))

	if err := h.db.UpdateScheduleItem(id, startAt, endAt, title, desc, color); err != nil {
		log.Printf("update schedule item: %v", err)
	}

	h.notifySchedule()

	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/schedule", http.StatusSeeOther)
}

// AdminDeleteScheduleItem handles removing a schedule item.
func (h *Handler) AdminDeleteScheduleItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	if err := h.db.DeleteScheduleItem(id); err != nil {
		log.Printf("delete schedule item: %v", err)
	}

	h.notifySchedule()

	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/schedule", http.StatusSeeOther)
}

// SetEventBounds saves the event start/end datetimes.
func (h *Handler) SetEventBounds(w http.ResponseWriter, r *http.Request) {
	start := sanitize(r.FormValue("event_start"))
	end := sanitize(r.FormValue("event_end"))
	h.db.SetSetting("event_start", start)
	h.db.SetSetting("event_end", end)
	h.notifySchedule()
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// notifySchedule builds the schedule JSON once and broadcasts to all WS clients.
func (h *Handler) notifySchedule() {
	data := h.buildScheduleJSON()
	h.schedMu.Lock()
	h.schedData = data
	h.schedMu.Unlock()
	h.scheduleBcast.notify()
}

func (h *Handler) getScheduleData() []byte {
	h.schedMu.RLock()
	defer h.schedMu.RUnlock()
	return h.schedData
}

func (h *Handler) buildScheduleJSON() []byte {
	items, _ := h.db.ListScheduleItems()
	msg := struct {
		Type       string            `json:"type"`
		Items      []db.ScheduleItem `json:"items"`
		EventStart string            `json:"eventStart"`
		EventEnd   string            `json:"eventEnd"`
	}{
		Type:       "schedule",
		Items:      items,
		EventStart: h.db.GetSetting("event_start"),
		EventEnd:   h.db.GetSetting("event_end"),
	}
	data, _ := json.Marshal(msg)
	return data
}

// ScheduleWebSocket pushes schedule updates to all connected clients.
func (h *Handler) ScheduleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws schedule upgrade: %v", err)
		return
	}
	defer conn.Close()

	updates, unsub := h.scheduleBcast.subscribe()
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Send initial state
	data := h.getScheduleData()
	if data == nil {
		data = h.buildScheduleJSON()
	}
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	conn.WriteMessage(websocket.TextMessage, data)

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
			data := h.getScheduleData()
			if data == nil {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

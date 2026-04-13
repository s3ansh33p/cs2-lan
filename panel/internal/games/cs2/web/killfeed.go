package cs2web

import (
	"net/http"

	"unilan/internal/games/cs2/tracker"
)

// ScoreboardPartial renders the CS2 live scoreboard for a server.
func (h *Handler) ScoreboardPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	state := h.Tracker.GetState(name)
	var scoreboard []tracker.PlayerStats
	if state != nil {
		scoreboard = state.GetScoreboard()
	}
	h.Render(w, "scoreboard.html", map[string]any{
		"Scoreboard": scoreboard,
	})
}

// KillfeedPartial renders the last 20 kill events for a CS2 server.
func (h *Handler) KillfeedPartial(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	state := h.Tracker.GetState(name)
	var killfeed []tracker.Kill
	if state != nil {
		killfeed = state.GetKillfeed(20)
	}
	h.Render(w, "killfeed.html", map[string]any{
		"Killfeed": killfeed,
	})
}

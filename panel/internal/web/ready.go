package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"unilan/internal/db"
)

// readyCancels tracks background goroutines per server for periodic RCON messages.
var (
	readyCancelsMu sync.Mutex
	readyCancels   = make(map[string]context.CancelFunc)
)

// setupReadyHook registers the .ready chat callback on the tracker.
func (h *Handler) setupReadyHook() {
	h.tracker.OnPlayerReady(func(serverName, playerName, team string) {
		h.handlePlayerReady(serverName, playerName, team)
	})
}

// RestartMatch handles POST /admin/server/{name}/restart-match.
// Sends RCON commands to restart and enter warmup, creates ready state.
func (h *Handler) RestartMatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slog.Info("ready: restart-match", "server", name, "ip", r.RemoteAddr)

	// Find the live game linked to this server
	game, err := h.db.GetGameByServer(name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "No live game linked to this server", http.StatusBadRequest)
		} else {
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	// Get server info for RCON
	info, err := h.docker.InspectServer(r.Context(), "cs2-"+name)
	if err != nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	addr := fmt.Sprintf("localhost:%d", info.Port)
	pw := info.RCONPassword

	// Send restart + warmup commands
	if _, err := h.rcon.Execute(addr, pw, "mp_restartgame 1"); err != nil {
		slog.Warn("ready: mp_restartgame failed", "server", name, "err", err)
	}
	time.Sleep(2 * time.Second)
	if _, err := h.rcon.Execute(addr, pw, "mp_warmup_start"); err != nil {
		slog.Warn("ready: mp_warmup_start failed", "server", name, "err", err)
	}
	if _, err := h.rcon.Execute(addr, pw, "mp_warmup_pausetimer 1"); err != nil {
		slog.Warn("ready: mp_warmup_pausetimer failed", "server", name, "err", err)
	}

	// Reset tracker stats for this server
	if state := h.tracker.GetState(name); state != nil {
		state.ResetStats()
	}

	// Create ready_state record
	rs, err := h.db.CreateReadyState(game.ID, name)
	if err != nil {
		slog.Error("ready: create ready state", "server", name, "err", err)
		http.Error(w, "Failed to create ready state", http.StatusInternalServerError)
		return
	}

	// Send initial RCON message
	h.rcon.Execute(addr, pw, `say "Match restarted! Type .ready in chat when you are ready."`)

	// Start background goroutine for periodic reminders
	h.startReadyReminder(name, addr, pw, rs.ID)

	slog.Info("ready: match restarted", "server", name, "game", game.ID, "ready_state", rs.ID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "readyStateID": rs.ID})
}

// ForceStart handles POST /admin/server/{name}/force-start.
// Skips ready check and immediately starts the match.
func (h *Handler) ForceStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slog.Info("ready: force-start", "server", name, "ip", r.RemoteAddr)

	rs, err := h.db.GetReadyStateByServer(name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "No active ready state for this server", http.StatusBadRequest)
		} else {
			http.Error(w, "Database error", http.StatusInternalServerError)
		}
		return
	}

	// Get server info for RCON
	info, err := h.docker.InspectServer(r.Context(), "cs2-"+name)
	if err != nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	addr := fmt.Sprintf("localhost:%d", info.Port)
	pw := info.RCONPassword

	h.db.UpdateReadyStatus(rs.ID, "force_started")
	h.stopReadyReminder(name)

	go h.startCountdown(name, addr, pw, rs.ID, true)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// ReadyState handles GET /admin/server/{name}/ready-state.
// Returns current ready state and player readiness as JSON.
func (h *Handler) ReadyState(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	rs, err := h.db.GetReadyStateByServer(name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"active": false})
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	players, _ := h.db.GetReadyPlayers(rs.ID)

	// Get roster info
	game, err := h.db.GetGameByServer(name)
	if err != nil {
		// Try any linked game
		game, err = h.db.GetGameByServerAny(name)
	}

	var roster []rosterPlayer
	if game != nil {
		if match, err := h.db.GetMatchByID(game.MatchID); err == nil {
			roster = h.buildRoster(match, game.Team1StartsCT)
		}
	}

	// Build response
	type playerJSON struct {
		Name    string `json:"name"`
		Team    string `json:"team"`
		IsReady bool   `json:"isReady"`
	}

	readyMap := make(map[string]bool)
	for _, p := range players {
		readyMap[strings.ToLower(p.PlayerName)] = p.IsReady
	}

	var ctPlayers, tPlayers []playerJSON
	ctReady, ctTotal := 0, 0
	tReady, tTotal := 0, 0

	for _, rp := range roster {
		pj := playerJSON{
			Name:    rp.name,
			Team:    rp.side,
			IsReady: readyMap[strings.ToLower(rp.name)],
		}
		if rp.side == "CT" {
			ctPlayers = append(ctPlayers, pj)
			ctTotal++
			if pj.IsReady {
				ctReady++
			}
		} else {
			tPlayers = append(tPlayers, pj)
			tTotal++
			if pj.IsReady {
				tReady++
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"active":   true,
		"status":   rs.Status,
		"ct":       ctPlayers,
		"t":        tPlayers,
		"ctReady":  ctReady,
		"ctTotal":  ctTotal,
		"tReady":   tReady,
		"tTotal":   tTotal,
		"gameID":   rs.GameID,
	})
}

// handlePlayerReady processes a ".ready" chat command from the tracker.
func (h *Handler) handlePlayerReady(serverName, playerName, team string) {
	rs, err := h.db.GetReadyStateByServer(serverName)
	if err != nil {
		return // no active ready state
	}
	if rs.Status != "waiting" {
		return // already started/countdown
	}

	// Get game and match info to check roster
	game, err := h.db.GetGameByServer(serverName)
	if err != nil {
		game, err = h.db.GetGameByServerAny(serverName)
		if err != nil {
			return
		}
	}

	match, err := h.db.GetMatchByID(game.MatchID)
	if err != nil {
		return
	}

	roster := h.buildRoster(match, game.Team1StartsCT)

	// Check if player is on the roster (case-insensitive)
	nameLower := strings.ToLower(playerName)
	var rosterTeam string
	for _, rp := range roster {
		if strings.ToLower(rp.name) == nameLower {
			rosterTeam = rp.side
			break
		}
	}

	if rosterTeam == "" {
		// Player not on roster, ignore
		slog.Debug("ready: player not on roster", "server", serverName, "player", playerName)
		return
	}

	// Mark player as ready
	if err := h.db.SetPlayerReady(rs.ID, playerName, rosterTeam); err != nil {
		slog.Error("ready: set player ready", "server", serverName, "player", playerName, "err", err)
		return
	}

	slog.Info("ready: player ready", "server", serverName, "player", playerName, "team", rosterTeam)

	// Count ready players
	players, _ := h.db.GetReadyPlayers(rs.ID)
	readyMap := make(map[string]bool)
	for _, p := range players {
		readyMap[strings.ToLower(p.PlayerName)] = p.IsReady
	}

	ctReady, ctTotal, tReady, tTotal := 0, 0, 0, 0
	for _, rp := range roster {
		if rp.side == "CT" {
			ctTotal++
			if readyMap[strings.ToLower(rp.name)] {
				ctReady++
			}
		} else {
			tTotal++
			if readyMap[strings.ToLower(rp.name)] {
				tReady++
			}
		}
	}

	// Send RCON status message
	info, err := h.docker.InspectServer(context.Background(), "cs2-"+serverName)
	if err != nil {
		return
	}
	addr := fmt.Sprintf("localhost:%d", info.Port)
	pw := info.RCONPassword

	msg := fmt.Sprintf(`say "%s is ready! (CT: %d/%d, T: %d/%d)"`,
		playerName, ctReady, ctTotal, tReady, tTotal)
	h.rcon.Execute(addr, pw, msg)

	// Check if all players are ready
	if ctReady >= ctTotal && tReady >= tTotal && ctTotal > 0 && tTotal > 0 {
		slog.Info("ready: all players ready", "server", serverName)
		h.db.UpdateReadyStatus(rs.ID, "ready")
		h.stopReadyReminder(serverName)
		go h.startCountdown(serverName, addr, pw, rs.ID, false)
	}
}

// startCountdown sends countdown messages and ends warmup.
func (h *Handler) startCountdown(serverName, addr, pw string, readyStateID int64, forced bool) {
	h.db.UpdateReadyStatus(readyStateID, "countdown")

	prefix := "All players ready!"
	if forced {
		prefix = "Force starting!"
	}

	h.rcon.Execute(addr, pw, fmt.Sprintf(`say "%s Match starting in 3..."`, prefix))
	time.Sleep(1 * time.Second)
	h.rcon.Execute(addr, pw, `say "2..."`)
	time.Sleep(1 * time.Second)
	h.rcon.Execute(addr, pw, `say "1..."`)
	time.Sleep(1 * time.Second)

	h.rcon.Execute(addr, pw, "mp_warmup_end")

	h.db.UpdateReadyStatus(readyStateID, "started")
	slog.Info("ready: match started", "server", serverName, "forced", forced)
}

// startReadyReminder starts a background goroutine that sends periodic RCON messages.
func (h *Handler) startReadyReminder(serverName, addr, pw string, readyStateID int64) {
	h.stopReadyReminder(serverName) // cancel any existing

	ctx, cancel := context.WithCancel(context.Background())
	readyCancelsMu.Lock()
	readyCancels[serverName] = cancel
	readyCancelsMu.Unlock()

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Check if ready state is still waiting
				rs, err := h.db.GetReadyStateByServer(serverName)
				if err != nil || rs.Status != "waiting" {
					return
				}
				h.rcon.Execute(addr, pw, `say "Waiting for players to ready up. Type .ready in chat!"`)
			}
		}
	}()
}

// stopReadyReminder cancels the background reminder goroutine for a server.
func (h *Handler) stopReadyReminder(serverName string) {
	readyCancelsMu.Lock()
	if cancel, ok := readyCancels[serverName]; ok {
		cancel()
		delete(readyCancels, serverName)
	}
	readyCancelsMu.Unlock()
}

// rosterPlayer represents a team member mapped to CT/T side.
type rosterPlayer struct {
	name string
	side string // "CT" or "T"
}

// buildRoster returns the full roster of both teams with CT/T assignments.
func (h *Handler) buildRoster(match *db.Match, team1StartsCT bool) []rosterPlayer {
	var roster []rosterPlayer
	if match.Team1ID != nil {
		members, _ := h.db.ListMembers(*match.Team1ID)
		side := "T"
		if team1StartsCT {
			side = "CT"
		}
		for _, m := range members {
			roster = append(roster, rosterPlayer{name: m.SteamName, side: side})
		}
	}
	if match.Team2ID != nil {
		members, _ := h.db.ListMembers(*match.Team2ID)
		side := "CT"
		if team1StartsCT {
			side = "T"
		}
		for _, m := range members {
			roster = append(roster, rosterPlayer{name: m.SteamName, side: side})
		}
	}
	return roster
}

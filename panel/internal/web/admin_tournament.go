package web

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cs2-panel/internal/db"

	"github.com/gorilla/websocket"
)

// parseTID extracts the tournament ID from the {tid} path value.
func parseTID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("tid"), 10, 64)
}

// adminTournamentRedirect returns the redirect URL for a tournament detail page.
func adminTournamentRedirect(tid int64) string {
	return fmt.Sprintf("/admin/tournament/%d", tid)
}

// AdminTournament shows the tournament list/selector page.
func (h *Handler) AdminTournament(w http.ResponseWriter, r *http.Request) {
	tournaments, _ := h.db.ListTournaments()
	deleted, _ := h.db.ListDeletedTournaments()
	activeID, _ := h.db.GetActiveTournamentID()

	hidden, _ := h.db.ListHiddenTournaments()

	h.render(w, "admin_tournament.html", map[string]any{
		"Title":       "Tournaments",
		"Tournaments": tournaments,
		"Deleted":     deleted,
		"Hidden":      hidden,
		"ActiveID":    activeID,
	})
}

// AdminTournamentDetail shows a specific tournament's settings, teams, and bracket.
func (h *Handler) AdminTournamentDetail(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	tournament, err := h.db.GetTournamentByID(tid)
	if err != nil || tournament == nil {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		return
	}

	teams, _ := h.db.ListTeams(tournament.ID)
	bracket, _ := h.db.GetBracket(tournament.ID)
	activeID, _ := h.db.GetActiveTournamentID()

	h.render(w, "admin_tournament.html", map[string]any{
		"Title":      tournament.Name,
		"Tournament": tournament,
		"Teams":      teams,
		"Bracket":    bracket,
		"IsActive":   tournament.ID == activeID,
		"IsHidden":   tournament.HiddenAt != nil,
		"ActiveID":   activeID,
	})
}

func (h *Handler) CreateTournament(w http.ResponseWriter, r *http.Request) {
	name := sanitize(r.FormValue("name"))
	if name == "" {
		name = "Tournament"
	}
	teamSize, _ := strconv.Atoi(r.FormValue("team_size"))
	if teamSize == 0 {
		teamSize = 5
	}
	gameMode := r.FormValue("game_mode")
	if gameMode == "" {
		gameMode = "competitive"
	}
	serverIP := r.FormValue("server_ip")
	serverPassword := r.FormValue("server_password")

	t, err := h.db.CreateTournament(name, teamSize, gameMode, serverIP, serverPassword)
	if err != nil {
		log.Printf("create tournament: %v", err)
		http.Error(w, "Failed to create tournament", http.StatusInternalServerError)
		return
	}

	h.tournamentListBcast.notify()
	http.Redirect(w, r, adminTournamentRedirect(t.ID), http.StatusSeeOther)
}

func (h *Handler) UpdateTournament(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	tournament, err := h.db.GetTournamentByID(tid)
	if err != nil || tournament == nil {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		return
	}

	name := sanitize(r.FormValue("name"))
	teamSize, _ := strconv.Atoi(r.FormValue("team_size"))
	if teamSize == 0 {
		teamSize = tournament.TeamSize
	}
	gameMode := r.FormValue("game_mode")
	if gameMode == "" {
		gameMode = tournament.GameMode
	}
	serverIP := r.FormValue("server_ip")
	serverPassword := r.FormValue("server_password")

	var regOpen, regClose *time.Time
	if v := r.FormValue("registration_open"); v != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
		if err == nil {
			regOpen = &t
		}
	}
	if v := r.FormValue("registration_close"); v != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
		if err == nil {
			regClose = &t
		}
	}

	if err := h.db.UpdateTournament(tid, name, teamSize, gameMode, regOpen, regClose, serverIP, serverPassword); err != nil {
		log.Printf("update tournament: %v", err)
	}
	h.tournamentListBcast.notify()

	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) tournamentAction(w http.ResponseWriter, r *http.Request, action func(int64) error, label string, doBracket bool) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}
	if err := action(tid); err != nil {
		log.Printf("%s tournament: %v", label, err)
	}
	if doBracket {
		h.notifyBracket()
	}
	h.tournamentListBcast.notify()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) SoftDeleteTournament(w http.ResponseWriter, r *http.Request) {
	h.tournamentAction(w, r, h.db.SoftDeleteTournament, "soft delete", true)
}

func (h *Handler) RestoreTournament(w http.ResponseWriter, r *http.Request) {
	h.tournamentAction(w, r, h.db.RestoreTournament, "restore", false)
}

func (h *Handler) HideTournament(w http.ResponseWriter, r *http.Request) {
	h.tournamentAction(w, r, h.db.HideTournament, "hide", false)
}

func (h *Handler) UnhideTournament(w http.ResponseWriter, r *http.Request) {
	h.tournamentAction(w, r, h.db.UnhideTournament, "unhide", false)
}

func (h *Handler) PurgeTournament(w http.ResponseWriter, r *http.Request) {
	h.tournamentAction(w, r, h.db.PurgeTournament, "purge", false)
}

func (h *Handler) SetTournamentStatus(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	status := r.FormValue("status")
	valid := map[string]bool{db.TournamentDraft: true, db.TournamentRegistration: true, db.TournamentLocked: true, db.TournamentActive: true, db.TournamentCompleted: true}
	if !valid[status] {
		http.Error(w, "Invalid status", http.StatusBadRequest)
		return
	}

	if err := h.db.SetTournamentStatus(tid, status); err != nil {
		log.Printf("set tournament status: %v", err)
	}
	h.notifyBracket()
	h.tournamentListBcast.notify()
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) SetActiveTournament(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}
	if err := h.db.SetActiveTournament(tid); err != nil {
		log.Printf("set active tournament: %v", err)
	}
	h.notifyBracket()
	h.tournamentListBcast.notify()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

// Admin team management

func (h *Handler) AdminCreateTeam(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	name := sanitize(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		return
	}

	teamID, err := h.db.CreateTeam(tid, name)
	if err != nil {
		log.Printf("create team: %v", err)
		if isAJAX(r) {
			http.Error(w, "Failed", http.StatusInternalServerError)
		} else {
			http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		}
		return
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": teamID, "name": name})
		return
	}
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) AdminDeleteTeam(w http.ResponseWriter, r *http.Request) {
	tid, _ := parseTID(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid team ID", http.StatusBadRequest)
		return
	}
	if err := h.db.DeleteTeam(id); err != nil {
		log.Printf("delete team: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) AdminAddMember(w http.ResponseWriter, r *http.Request) {
	tid, _ := parseTID(r)
	teamID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid team ID", http.StatusBadRequest)
		return
	}
	steamName := sanitize(r.FormValue("steam_name"))
	if steamName == "" {
		if isAJAX(r) {
			http.Error(w, "Name required", http.StatusBadRequest)
		} else {
			http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		}
		return
	}
	mid, err := h.db.AddMember(teamID, steamName)
	if err != nil {
		log.Printf("add member: %v", err)
		if isAJAX(r) {
			http.Error(w, "Failed", http.StatusInternalServerError)
		} else {
			http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		}
		return
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      mid,
			"team_id": teamID,
			"name":    steamName,
		})
		return
	}
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) AdminRemoveMember(w http.ResponseWriter, r *http.Request) {
	tid, _ := parseTID(r)
	id, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid member ID", http.StatusBadRequest)
		return
	}
	if err := h.db.RemoveMember(id); err != nil {
		log.Printf("remove member: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) AdminRenameTeam(w http.ResponseWriter, r *http.Request) {
	tid, _ := parseTID(r)
	teamID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid team ID", http.StatusBadRequest)
		return
	}
	name := sanitize(r.FormValue("name"))
	if name == "" {
		if isAJAX(r) {
			http.Error(w, "Name required", http.StatusBadRequest)
		} else {
			http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		}
		return
	}
	if err := h.db.UpdateTeam(teamID, name); err != nil {
		log.Printf("rename team: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func isAJAX(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// Bracket management

func (h *Handler) AdminDeleteBracket(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}
	if err := h.db.DeleteBracket(tid); err != nil {
		log.Printf("delete bracket: %v", err)
	}
	h.notifyBracket()
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) AdminSeedBracket(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	// Parse team IDs from form (ordered by seed)
	idsStr := r.FormValue("team_ids")
	var teamIDs []int64
	for _, s := range strings.Split(idsStr, ",") {
		s = strings.TrimSpace(s)
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			teamIDs = append(teamIDs, id)
		}
	}

	if len(teamIDs) < 2 {
		http.Error(w, "Need at least 2 teams", http.StatusBadRequest)
		return
	}

	// Validate all teams have at least one member
	for _, id := range teamIDs {
		members, _ := h.db.ListMembers(id)
		if len(members) == 0 {
			team, _ := h.db.GetTeam(id)
			name := fmt.Sprintf("ID %d", id)
			if team != nil {
				name = team.Name
			}
			http.Error(w, fmt.Sprintf("Team %q has no members. All teams must have players before generating the bracket.", name), http.StatusBadRequest)
			return
		}
	}

	if err := h.db.GenerateBracket(tid, teamIDs); err != nil {
		log.Printf("generate bracket: %v", err)
		http.Error(w, "Failed to generate bracket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.notifyBracket()
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

// Match/game handlers — these work by match/game ID (tournament-scoped via foreign keys)

func (h *Handler) AdminSetBestOf(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.FormValue("match_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	bestOf, _ := strconv.Atoi(r.FormValue("best_of"))
	if err := h.db.SetMatchBestOf(matchID, bestOf); err != nil {
		log.Printf("set best of: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminSetWinner(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.FormValue("match_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	winnerID, err := strconv.ParseInt(r.FormValue("winner_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid winner ID", http.StatusBadRequest)
		return
	}
	if err := h.db.SetMatchWinner(matchID, winnerID); err != nil {
		log.Printf("set winner: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminClearWinner(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.FormValue("match_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	if err := h.db.ClearMatchWinner(matchID); err != nil {
		log.Printf("clear winner: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminCreateGame(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	gameNumber, _ := strconv.Atoi(r.FormValue("game_number"))
	mapName := r.FormValue("map_name")
	team1StartsCT := r.FormValue("team1_starts_ct") != "0"
	if gameNumber == 0 {
		gameNumber = 1
	}

	if _, err := h.db.CreateGame(matchID, gameNumber, mapName, team1StartsCT); err != nil {
		log.Printf("create game: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminUpdateGame(w http.ResponseWriter, r *http.Request) {
	gameID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	team1Score, _ := strconv.Atoi(r.FormValue("team1_score"))
	team2Score, _ := strconv.Atoi(r.FormValue("team2_score"))

	var winnerID *int64
	if wid := r.FormValue("winner_id"); wid != "" {
		id, _ := strconv.ParseInt(wid, 10, 64)
		winnerID = &id
	}

	if err := h.db.UpdateGameScore(gameID, team1Score, team2Score, winnerID); err != nil {
		log.Printf("update game: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminResetGame(w http.ResponseWriter, r *http.Request) {
	gameID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	if err := h.db.ResetGame(gameID); err != nil {
		log.Printf("reset game: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminSetGameSide(w http.ResponseWriter, r *http.Request) {
	gameID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	t1ct := r.FormValue("team1_starts_ct")
	ct := 1
	if t1ct == "0" {
		ct = 0
	}
	h.db.Exec(`UPDATE games SET team1_starts_ct=? WHERE id=?`, ct, gameID)
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminDeleteGame(w http.ResponseWriter, r *http.Request) {
	gameID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	if err := h.db.DeleteGame(gameID); err != nil {
		log.Printf("delete game: %v", err)
	}
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

// AdminLaunchMatch redirects to launch page pre-filled with match details
func (h *Handler) AdminLaunchMatch(w http.ResponseWriter, r *http.Request) {
	matchID := r.PathValue("id")
	gameNumber := r.FormValue("game_number")
	mapName := r.FormValue("map_name")
	if gameNumber == "" {
		gameNumber = "1"
	}

	mid, _ := strconv.ParseInt(matchID, 10, 64)
	tournament, _ := h.db.GetTournamentByMatchID(mid)
	gameMode := "competitive"
	maxPlayers := 15
	password := ""
	if tournament != nil {
		if tournament.GameMode != "" {
			gameMode = tournament.GameMode
		}
		password = tournament.ServerPassword
	}

	params := url.Values{}
	params.Set("name", "match-"+matchID)
	params.Set("map", mapName)
	params.Set("mode", gameMode)
	params.Set("players", strconv.Itoa(maxPlayers))
	params.Set("match_id", matchID)
	params.Set("game_number", gameNumber)
	params.Set("password", password)
	http.Redirect(w, r, "/admin/launch?"+params.Encode(), http.StatusSeeOther)
}

func (h *Handler) AdminSwapTeams(w http.ResponseWriter, r *http.Request) {
	match1ID, err := strconv.ParseInt(r.FormValue("match1_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	slot1 := r.FormValue("slot1")
	match2ID, err := strconv.ParseInt(r.FormValue("match2_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	slot2 := r.FormValue("slot2")

	if err := h.db.SwapTeams(match1ID, slot1, match2ID, slot2); err != nil {
		log.Printf("swap teams: %v", err)
	}
	h.notifyBracket()
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

// AdminTournamentDetailWS pushes tournament detail updates to admin clients viewing a specific tournament.
func (h *Handler) AdminTournamentDetailWS(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws tournament detail upgrade: %v", err)
		return
	}
	defer conn.Close()

	updates, unsub := h.tournamentListBcast.subscribe()
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	h.sendTournamentDetail(conn, tid)

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
			if err := h.sendTournamentDetail(conn, tid); err != nil {
				return
			}
		}
	}
}

func (h *Handler) sendTournamentDetail(conn *websocket.Conn, tid int64) error {
	tournament, err := h.db.GetTournamentByID(tid)
	if err != nil || tournament == nil {
		return nil
	}
	activeID, _ := h.db.GetActiveTournamentID()

	var regOpen, regClose string
	if tournament.RegistrationOpen != nil {
		regOpen = tournament.RegistrationOpen.Format("2006-01-02T15:04")
	}
	if tournament.RegistrationClose != nil {
		regClose = tournament.RegistrationClose.Format("2006-01-02T15:04")
	}

	msg := struct {
		Type              string `json:"type"`
		Name              string `json:"name"`
		TeamSize          int    `json:"teamSize"`
		GameMode          string `json:"gameMode"`
		Status            string `json:"status"`
		Hidden            bool   `json:"hidden"`
		Active            bool   `json:"active"`
		RegistrationOpen  string `json:"registrationOpen"`
		RegistrationClose string `json:"registrationClose"`
		ServerIP          string `json:"serverIP"`
		ServerPassword    string `json:"serverPassword"`
	}{
		Type:              "tournament_detail",
		Name:              html.EscapeString(tournament.Name),
		TeamSize:          tournament.TeamSize,
		GameMode:          tournament.GameMode,
		Status:            tournament.Status,
		Hidden:            tournament.HiddenAt != nil,
		Active:            tournament.ID == activeID,
		RegistrationOpen:  regOpen,
		RegistrationClose: regClose,
		ServerIP:          tournament.ServerIP,
		ServerPassword:    tournament.ServerPassword,
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// AdminTournamentListWS pushes tournament list updates to admin clients.
func (h *Handler) AdminTournamentListWS(w http.ResponseWriter, r *http.Request) {
	conn, done, err := setupWSConn(w, r)
	if err != nil {
		log.Printf("ws tournament list upgrade: %v", err)
		return
	}
	defer conn.Close()

	updates, unsub := h.tournamentListBcast.subscribe()
	defer unsub()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	h.sendTournamentList(conn)

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
			if err := h.sendTournamentList(conn); err != nil {
				return
			}
		}
	}
}

func (h *Handler) sendTournamentList(conn *websocket.Conn) error {
	tournaments, _ := h.db.ListTournaments()
	deleted, _ := h.db.ListDeletedTournaments()
	hidden, _ := h.db.ListHiddenTournaments()
	activeID, _ := h.db.GetActiveTournamentID()

	type tournamentJSON struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		TeamSize  int    `json:"teamSize"`
		GameMode  string `json:"gameMode"`
		CreatedAt string `json:"createdAt"`
	}

	toJSON := func(t []db.Tournament) []tournamentJSON {
		out := make([]tournamentJSON, len(t))
		for i, v := range t {
			out[i] = tournamentJSON{ID: v.ID, Name: html.EscapeString(v.Name), Status: v.Status, TeamSize: v.TeamSize, GameMode: v.GameMode, CreatedAt: v.CreatedAt.Format("Jan 2, 2006")}
		}
		return out
	}

	msg := struct {
		Type        string           `json:"type"`
		Tournaments []tournamentJSON `json:"tournaments"`
		Deleted     []tournamentJSON `json:"deleted"`
		Hidden      []tournamentJSON `json:"hidden"`
		ActiveID    int64            `json:"activeID"`
	}{
		Type:        "tournament_list",
		Tournaments: toJSON(tournaments),
		Deleted:     toJSON(deleted),
		Hidden:      toJSON(hidden),
		ActiveID:    activeID,
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

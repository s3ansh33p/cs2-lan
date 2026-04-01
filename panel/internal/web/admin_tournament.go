package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cs2-panel/internal/db"
)

func (h *Handler) AdminTournament(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetTournament()
	if err != nil {
		log.Printf("get tournament: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var teams []db.Team
	var bracket []db.Match
	if tournament != nil {
		teams, _ = h.db.ListTeams(tournament.ID)
		bracket, _ = h.db.GetBracket(tournament.ID)
	}

	h.render(w, "admin_tournament.html", map[string]any{
		"Title":      "Tournament",
		"Tournament": tournament,
		"Teams":      teams,
		"Bracket":    bracket,
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

	_, err := h.db.CreateTournament(name, teamSize, gameMode, serverIP, serverPassword)
	if err != nil {
		log.Printf("create tournament: %v", err)
		http.Error(w, "Failed to create tournament", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) UpdateTournament(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetTournament()
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

	if err := h.db.UpdateTournament(tournament.ID, name, teamSize, gameMode, regOpen, regClose, serverIP, serverPassword); err != nil {
		log.Printf("update tournament: %v", err)
	}

	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) DeleteTournament(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetTournament()
	if err != nil || tournament == nil {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		return
	}
	if err := h.db.DeleteTournament(tournament.ID); err != nil {
		log.Printf("delete tournament: %v", err)
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) SetTournamentStatus(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetTournament()
	if err != nil || tournament == nil {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		return
	}

	status := r.FormValue("status")
	valid := map[string]bool{"draft": true, "registration": true, "locked": true, "active": true, "completed": true}
	if !valid[status] {
		http.Error(w, "Invalid status", http.StatusBadRequest)
		return
	}

	if err := h.db.SetTournamentStatus(tournament.ID, status); err != nil {
		log.Printf("set tournament status: %v", err)
	}
	h.notifyBracket()
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

// Admin team management
func (h *Handler) AdminCreateTeam(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetTournament()
	if err != nil || tournament == nil {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		return
	}

	name := sanitize(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		return
	}

	if _, err := h.db.CreateTeam(tournament.ID, name); err != nil {
		log.Printf("create team: %v", err)
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminDeleteTeam(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid team ID", http.StatusBadRequest)
		return
	}
	if err := h.db.DeleteTeam(id); err != nil {
		log.Printf("delete team: %v", err)
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminAddMember(w http.ResponseWriter, r *http.Request) {
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
			http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		}
		return
	}
	mid, err := h.db.AddMember(teamID, steamName)
	if err != nil {
		log.Printf("add member: %v", err)
		if isAJAX(r) {
			http.Error(w, "Failed", http.StatusInternalServerError)
		} else {
			http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
		}
		return
	}
	if isAJAX(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      mid,
			"team_id": teamID,
			"name":    steamName,
		})
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func (h *Handler) AdminRemoveMember(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid member ID", http.StatusBadRequest)
		return
	}
	if err := h.db.RemoveMember(id); err != nil {
		log.Printf("remove member: %v", err)
	}
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

func isAJAX(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// Bracket generation — takes comma-separated team IDs in seed order
func (h *Handler) AdminSeedBracket(w http.ResponseWriter, r *http.Request) {
	tournament, err := h.db.GetTournament()
	if err != nil || tournament == nil {
		http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
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

	if err := h.db.GenerateBracket(tournament.ID, teamIDs); err != nil {
		log.Printf("generate bracket: %v", err)
		http.Error(w, "Failed to generate bracket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.notifyBracket()
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

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

	tournament, _ := h.db.GetTournament()
	gameMode := "competitive"
	maxPlayers := 15
	password := ""
	if tournament != nil {
		if tournament.GameMode != "" {
			gameMode = tournament.GameMode
		}
		password = tournament.ServerPassword
	}

	query := fmt.Sprintf("/admin/launch?name=match-%s&map=%s&mode=%s&players=%d&match_id=%s&game_number=%s&password=%s",
		matchID, mapName, gameMode, maxPlayers, matchID, gameNumber, password)
	http.Redirect(w, r, query, http.StatusSeeOther)
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

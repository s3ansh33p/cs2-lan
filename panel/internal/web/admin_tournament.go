package web

import (
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"unilan/internal/db"

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

	h.renderPage(w, r, "admin_tournament.html", map[string]any{
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

	var standings []db.GroupStanding
	if tournament.BracketFormat == "round_robin" || tournament.BracketFormat == "hybrid" {
		standings, _ = h.db.GetGroupStandings(tournament.ID)
	}

	h.renderPage(w, r, "admin_tournament.html", map[string]any{
		"Title":      tournament.Name,
		"Tournament": tournament,
		"Teams":      teams,
		"Bracket":    bracket,
		"Standings":  standings,
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
	gameType := r.FormValue("game_type")
	if gameType == "other" {
		gameType = sanitize(r.FormValue("game_type_custom"))
	}
	if gameType == "" {
		gameType = "cs2"
	}
	bracketFormat := r.FormValue("bracket_format")
	serverIP := r.FormValue("server_ip")
	serverPassword := r.FormValue("server_password")

	t, err := h.db.CreateTournament(name, teamSize, gameMode, gameType, bracketFormat, serverIP, serverPassword)
	if err != nil {
		slog.Error("tournament: create failed", "err", err)
		http.Error(w, "Failed to create tournament", http.StatusInternalServerError)
		return
	}

	slog.Info("tournament: created", "id", t.ID, "name", name)
	h.notifyTournamentList(t.ID)
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
	gameType := r.FormValue("game_type")
	if gameType == "other" {
		gameType = sanitize(r.FormValue("game_type_custom"))
	}
	if gameType == "" {
		gameType = tournament.GameType
	}
	bracketFormat := r.FormValue("bracket_format")
	if bracketFormat == "" {
		bracketFormat = tournament.BracketFormat
	}
	serverIP := r.FormValue("server_ip")
	serverPassword := r.FormValue("server_password")
	vetoFormat := sanitize(r.FormValue("veto_format"))

	groupCount, _ := strconv.Atoi(r.FormValue("bracket_group_count"))
	advanceCount, _ := strconv.Atoi(r.FormValue("bracket_advance_count"))
	if advanceCount <= 0 {
		advanceCount = tournament.BracketAdvanceCount
	}
	if advanceCount <= 0 {
		advanceCount = 2
	}

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

	if err := h.db.UpdateTournament(tid, name, teamSize, gameMode, gameType, bracketFormat, vetoFormat, regOpen, regClose, serverIP, serverPassword, groupCount, advanceCount); err != nil {
		slog.Error("tournament: update failed", "id", tid, "err", err)
	} else {
		slog.Info("tournament: updated", "id", tid)
	}
	h.notifyTournamentList(tid)

	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) tournamentAction(w http.ResponseWriter, r *http.Request, action func(int64) error, label string, doBracket bool) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}
	if err := action(tid); err != nil {
		slog.Error("tournament: "+label+" failed", "id", tid, "err", err)
	} else {
		slog.Info("tournament: "+label, "id", tid)
	}
	if doBracket {
		h.notifyBracket(tid)
	}
	h.notifyTournamentList(tid)
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
		slog.Error("tournament: set status failed", "id", tid, "err", err)
	} else {
		slog.Info("tournament: status", "id", tid, "status", status)
	}
	h.notifyBracket(tid)
	h.notifyTournamentList(tid)
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) SetActiveTournament(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}
	if err := h.db.SetActiveTournament(tid); err != nil {
		slog.Error("tournament: set active failed", "id", tid, "err", err)
	} else {
		slog.Info("tournament: set active", "id", tid)
	}
	h.notifyBracket(tid)
	h.notifyTournamentList(tid)
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
		slog.Error("team: create failed", "tournament", tid, "err", err)
		if isAJAX(r) {
			http.Error(w, "Failed", http.StatusInternalServerError)
		} else {
			http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		}
		return
	}
	slog.Info("team: created", "id", teamID, "name", name, "tournament", tid)
	h.notifyBracket(tid)
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
		slog.Error("team: delete failed", "id", id, "err", err)
	} else {
		slog.Info("team: deleted", "id", id, "tournament", tid)
	}
	h.notifyBracket(tid)
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
		slog.Error("team: add member failed", "team", teamID, "err", err)
		if isAJAX(r) {
			http.Error(w, "Failed", http.StatusInternalServerError)
		} else {
			http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
		}
		return
	}
	slog.Info("team: member added", "team", teamID, "member", mid, "name", steamName)
	h.notifyBracket(tid)
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
		slog.Error("team: remove member failed", "member", id, "err", err)
	} else {
		slog.Info("team: member removed", "member", id)
	}
	h.notifyBracket(tid)
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
		slog.Error("team: rename failed", "id", teamID, "err", err)
	} else {
		slog.Info("team: renamed", "id", teamID, "name", name)
	}
	h.notifyBracket(tid)
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
		slog.Error("bracket: delete failed", "tournament", tid, "err", err)
	} else {
		slog.Info("bracket: deleted", "tournament", tid)
	}
	h.notifyBracket(tid)
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

	// Choose bracket generation based on tournament format
	tournament, err := h.db.GetTournamentByID(tid)
	if err != nil || tournament == nil {
		http.Error(w, "Tournament not found", http.StatusNotFound)
		return
	}

	switch tournament.BracketFormat {
	case "double_elim":
		err = h.db.GenerateDoubleElimBracket(tid, teamIDs)
	case "round_robin":
		groupCount, _ := strconv.Atoi(r.FormValue("group_count"))
		err = h.db.GenerateRoundRobin(tid, teamIDs, groupCount)
	case "hybrid":
		groupCount, _ := strconv.Atoi(r.FormValue("group_count"))
		advanceCount, _ := strconv.Atoi(r.FormValue("advance_count"))
		if advanceCount <= 0 {
			advanceCount = 2
		}
		err = h.db.GenerateHybridBracket(tid, teamIDs, groupCount, advanceCount)
	default:
		err = h.db.GenerateBracket(tid, teamIDs)
	}
	if err != nil {
		slog.Error("bracket: seed failed", "tournament", tid, "format", tournament.BracketFormat, "err", err)
		http.Error(w, "Failed to generate bracket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("bracket: seeded", "tournament", tid, "format", tournament.BracketFormat, "teams", len(teamIDs))
	h.notifyBracket(tid)
	http.Redirect(w, r, adminTournamentRedirect(tid), http.StatusSeeOther)
}

func (h *Handler) AdminGeneratePlayoffs(w http.ResponseWriter, r *http.Request) {
	tid, err := parseTID(r)
	if err != nil {
		http.Error(w, "Invalid tournament ID", http.StatusBadRequest)
		return
	}

	playoffFormat := r.FormValue("playoff_format")
	if playoffFormat != "double_elim" {
		playoffFormat = "single_elim"
	}

	if err := h.db.GeneratePlayoffBracket(tid, playoffFormat); err != nil {
		slog.Error("bracket: generate playoffs failed", "tournament", tid, "err", err)
		http.Error(w, "Failed to generate playoffs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("bracket: playoffs generated", "tournament", tid, "format", playoffFormat)
	h.notifyBracket(tid)
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
		slog.Error("bracket: bestof failed", "match", matchID, "err", err)
	} else {
		slog.Info("bracket: bestof", "match", matchID, "bestof", bestOf)
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
		slog.Error("bracket: set winner failed", "match", matchID, "err", err)
	} else {
		slog.Info("bracket: winner set", "match", matchID, "winner", winnerID)
	}
	h.updateGroupStandingsIfNeeded(matchID)
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
		slog.Error("bracket: clear winner failed", "match", matchID, "err", err)
	} else {
		slog.Info("bracket: winner cleared", "match", matchID)
	}
	h.updateGroupStandingsIfNeeded(matchID)
	h.notifyBracket()
	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/admin/tournament", http.StatusSeeOther)
}

// updateGroupStandingsIfNeeded recalculates group standings if the match belongs to a round-robin group.
func (h *Handler) updateGroupStandingsIfNeeded(matchID int64) {
	match, err := h.db.GetMatchByID(matchID)
	if err != nil || match == nil {
		return
	}
	if match.BracketSection != "group" {
		return
	}
	if err := h.db.UpdateGroupStandings(match.TournamentID, match.GroupID); err != nil {
		slog.Error("standings: update failed", "tournament", match.TournamentID, "group", match.GroupID, "err", err)
	}
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
		slog.Error("game: create failed", "match", matchID, "err", err)
	} else {
		slog.Info("game: created", "match", matchID, "number", gameNumber)
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
		slog.Error("game: update failed", "id", gameID, "err", err)
	} else {
		slog.Info("game: updated", "id", gameID, "score", fmt.Sprintf("%d-%d", team1Score, team2Score))
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
		slog.Error("game: reset failed", "id", gameID, "err", err)
	} else {
		slog.Info("game: reset", "id", gameID)
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
	slog.Info("game: side set", "id", gameID, "team1_starts_ct", ct == 1)
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
		slog.Error("game: delete failed", "id", gameID, "err", err)
	} else {
		slog.Info("game: deleted", "id", gameID)
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
		slog.Error("bracket: swap failed", "err", err)
	} else {
		slog.Info("bracket: teams swapped", "match1", match1ID, "match2", match2ID)
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
		slog.Warn("ws: tournament detail upgrade failed", "err", err)
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
	data := h.buildTournamentDetailJSON(tid)
	if data == nil {
		return nil
	}
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// buildTournamentDetailJSON builds tournament detail JSON for a specific tournament.
func (h *Handler) buildTournamentDetailJSON(tid int64) []byte {
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

	bracketFormat := tournament.BracketFormat
	if bracketFormat == "" {
		bracketFormat = "single_elim"
	}

	msg := struct {
		Type              string `json:"type"`
		Name              string `json:"name"`
		TeamSize          int    `json:"teamSize"`
		GameMode          string `json:"gameMode"`
		GameType          string `json:"gameType"`
		BracketFormat     string `json:"bracketFormat"`
		VetoFormat        string `json:"vetoFormat"`
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
		GameType:          tournament.GameType,
		BracketFormat:     bracketFormat,
		VetoFormat:        tournament.VetoFormat,
		Status:            tournament.Status,
		Hidden:            tournament.HiddenAt != nil,
		Active:            tournament.ID == activeID,
		RegistrationOpen:  regOpen,
		RegistrationClose: regClose,
		ServerIP:          tournament.ServerIP,
		ServerPassword:    tournament.ServerPassword,
	}

	data, _ := json.Marshal(msg)
	return data
}

// AdminTournamentListWS pushes tournament list updates to admin clients.
func (h *Handler) AdminTournamentListWS(w http.ResponseWriter, r *http.Request) {
	conn, done, err := setupWSConn(w, r)
	if err != nil {
		slog.Warn("ws: tournament list upgrade failed", "err", err)
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

// AdminRemapPlayer remaps an unmatched player stat to a roster member.
func (h *Handler) AdminRemapPlayer(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	gameID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	originalName := r.FormValue("original_name")
	targetMember := r.FormValue("target_member")
	if originalName == "" || targetMember == "" {
		http.Error(w, "Missing original_name or target_member", http.StatusBadRequest)
		return
	}

	match, err := h.db.GetMatchByID(matchID)
	if err != nil {
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	// Determine which team the target member belongs to
	var teamID int64
	if match.Team1ID != nil {
		members, _ := h.db.ListMembers(*match.Team1ID)
		for _, m := range members {
			if strings.EqualFold(m.SteamName, targetMember) {
				teamID = *match.Team1ID
				break
			}
		}
	}
	if teamID == 0 && match.Team2ID != nil {
		members, _ := h.db.ListMembers(*match.Team2ID)
		for _, m := range members {
			if strings.EqualFold(m.SteamName, targetMember) {
				teamID = *match.Team2ID
				break
			}
		}
	}
	if teamID == 0 {
		http.Error(w, "Target member not found in either team roster", http.StatusBadRequest)
		return
	}

	if err := h.db.RemapPlayerStat(gameID, originalName, targetMember, teamID); err != nil {
		slog.Error("remap: failed", "game", gameID, "original", originalName, "target", targetMember, "err", err)
		http.Error(w, "Remap failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("remap: success", "game", gameID, "original", originalName, "target", targetMember, "team", teamID)
	h.notifyBracket()

	if isAJAX(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/tournament"), http.StatusSeeOther)
}

// AdminGameStatsAdmin returns HTML with all player stats (matched + unmatched) for admin view.
func (h *Handler) AdminGameStatsAdmin(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}
	gameID, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}

	stats, _ := h.db.GetGameStatsAdmin(gameID)

	match, err := h.db.GetMatchByID(matchID)
	if err != nil {
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	// Collect all roster members for both teams
	type rosterMember struct {
		Name   string
		TeamID int64
	}
	var roster []rosterMember
	if match.Team1ID != nil {
		members, _ := h.db.ListMembers(*match.Team1ID)
		for _, m := range members {
			roster = append(roster, rosterMember{Name: m.SteamName, TeamID: *match.Team1ID})
		}
	}
	if match.Team2ID != nil {
		members, _ := h.db.ListMembers(*match.Team2ID)
		for _, m := range members {
			roster = append(roster, rosterMember{Name: m.SteamName, TeamID: *match.Team2ID})
		}
	}

	// Build set of already-matched player names
	matchedNames := make(map[string]bool)
	for _, s := range stats {
		if s.Matched {
			matchedNames[strings.ToLower(s.PlayerName)] = true
		}
	}

	// Find unmatched stats
	var unmatched []db.PlayerStat
	for _, s := range stats {
		if !s.Matched {
			unmatched = append(unmatched, s)
		}
	}

	w.Header().Set("Content-Type", "application/json")

	type unmatchedJSON struct {
		OriginalName string  `json:"originalName"`
		TeamID       int64   `json:"teamId"`
		Kills        int     `json:"kills"`
		Deaths       int     `json:"deaths"`
		Assists      int     `json:"assists"`
		HSPercent    float64 `json:"hsPercent"`
		KDR          float64 `json:"kdr"`
		ADR          float64 `json:"adr"`
	}
	type rosterJSON struct {
		Name   string `json:"name"`
		TeamID int64  `json:"teamId"`
	}
	type response struct {
		Unmatched []unmatchedJSON `json:"unmatched"`
		Roster    []rosterJSON    `json:"roster"`
	}

	resp := response{}
	for _, s := range unmatched {
		resp.Unmatched = append(resp.Unmatched, unmatchedJSON{
			OriginalName: s.OriginalName,
			TeamID:       s.TeamID,
			Kills:        s.Kills,
			Deaths:       s.Deaths,
			Assists:      s.Assists,
			HSPercent:    s.HSPercent,
			KDR:          s.KDR,
			ADR:          s.ADR,
		})
	}
	for _, rm := range roster {
		if !matchedNames[strings.ToLower(rm.Name)] {
			resp.Roster = append(resp.Roster, rosterJSON{Name: rm.Name, TeamID: rm.TeamID})
		}
	}

	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) sendTournamentList(conn *websocket.Conn) error {
	data := h.buildTournamentListJSON()
	if data == nil {
		return nil
	}
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// buildTournamentListJSON builds the tournament list JSON for admin clients.
func (h *Handler) buildTournamentListJSON() []byte {
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
		GameType  string `json:"gameType"`
		CreatedAt string `json:"createdAt"`
	}

	toJSON := func(t []db.Tournament) []tournamentJSON {
		out := make([]tournamentJSON, len(t))
		for i, v := range t {
			out[i] = tournamentJSON{ID: v.ID, Name: html.EscapeString(v.Name), Status: v.Status, TeamSize: v.TeamSize, GameMode: v.GameMode, GameType: v.GameType, CreatedAt: v.CreatedAt.Format("Jan 2, 2006")}
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

	data, _ := json.Marshal(msg)
	return data
}

// notifyTournamentList broadcasts tournament list updates via both the old broadcaster and the unified hub.
func (h *Handler) notifyTournamentList(tournamentIDs ...int64) {
	h.tournamentListBcast.notify()

	if h.hub == nil {
		return
	}

	data := h.buildTournamentListJSON()
	if data != nil {
		h.hub.Publish("tournaments", "tournament_list", json.RawMessage(data))
	}

	// Publish detail updates for the specific tournaments that changed.
	// This avoids relying on ListTournaments() which filters out hidden/deleted entries.
	for _, tid := range tournamentIDs {
		detail := h.buildTournamentDetailJSON(tid)
		if detail != nil {
			h.hub.Publish(fmt.Sprintf("tournament:%d", tid), "tournament_detail", json.RawMessage(detail))
		}
	}
}

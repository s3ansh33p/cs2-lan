package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"unilan/internal/db"
)

type vetoStepJSON struct {
	Step     int    `json:"step"`
	Action   string `json:"action"`
	TeamID   *int64 `json:"teamId"`
	TeamName string `json:"teamName"`
	MapName  string `json:"mapName"`
}

type vetoStateJSON struct {
	Vetoes        []vetoStepJSON `json:"vetoes"`
	NextStep      int            `json:"nextStep"`
	NextAction    string         `json:"nextAction"`
	NextTeamID    *int64         `json:"nextTeamId"`
	NextTeamName  string         `json:"nextTeamName"`
	AvailableMaps []string       `json:"availableMaps"`
	MapPool       []string       `json:"mapPool"`
	TotalSteps    int            `json:"totalSteps"`
	Complete      bool           `json:"complete"`
	Format        []string       `json:"format"`
}

// AdminGetVetoState returns the current veto state for a match as JSON.
func (h *Handler) AdminGetVetoState(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	state, err := h.buildVetoState(matchID)
	if err != nil {
		slog.Error("veto: get state failed", "match", matchID, "err", err)
		http.Error(w, "Failed to get veto state", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// AdminSubmitVetoStep handles the next veto step.
func (h *Handler) AdminSubmitVetoStep(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	mapName := r.FormValue("map_name")
	if mapName == "" {
		http.Error(w, "map_name required", http.StatusBadRequest)
		return
	}

	match, err := h.db.GetMatchByID(matchID)
	if err != nil || match == nil {
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}

	tournament, err := h.db.GetTournamentByID(match.TournamentID)
	if err != nil || tournament == nil {
		http.Error(w, "Tournament not found", http.StatusNotFound)
		return
	}

	format := db.ParseVetoFormat(tournament.VetoFormat)
	pool := db.MapPool(tournament.GameMode)
	vetoes, err := h.db.GetMatchVetoes(matchID)
	if err != nil {
		http.Error(w, "Failed to get vetoes", http.StatusInternalServerError)
		return
	}

	currentStep := len(vetoes)
	if currentStep >= len(format) {
		http.Error(w, "Veto already complete", http.StatusBadRequest)
		return
	}

	// Validate map is in pool and not already used
	inPool := false
	for _, m := range pool {
		if m == mapName {
			inPool = true
			break
		}
	}
	if !inPool {
		http.Error(w, "Map not in pool", http.StatusBadRequest)
		return
	}
	for _, v := range vetoes {
		if v.MapName == mapName {
			http.Error(w, "Map already used", http.StatusBadRequest)
			return
		}
	}

	action := format[currentStep]
	teamID := vetoTeamForStep(currentStep, format, match)

	if err := h.db.AddVetoStep(matchID, currentStep, action, mapName, teamID); err != nil {
		slog.Error("veto: add step failed", "match", matchID, "err", err)
		http.Error(w, "Failed to add veto step", http.StatusInternalServerError)
		return
	}

	slog.Info("veto: step added", "match", matchID, "step", currentStep, "action", action, "map", mapName)

	// If action is "pick", create a Game record
	if action == "pick" || action == "last" {
		gameNum := countPicksAndLasts(vetoes, action, mapName)
		if _, err := h.db.CreateGame(matchID, gameNum, mapName, true); err != nil {
			slog.Error("veto: create game failed", "match", matchID, "map", mapName, "err", err)
		}
	}

	// If action is "last" or next step would be "last", auto-handle remaining maps
	nextStep := currentStep + 1
	if nextStep < len(format) && format[nextStep] == "last" {
		// Auto-select the remaining map
		usedMaps := make(map[string]bool)
		for _, v := range vetoes {
			usedMaps[v.MapName] = true
		}
		usedMaps[mapName] = true

		var remaining string
		for _, m := range pool {
			if !usedMaps[m] {
				remaining = m
				break
			}
		}
		if remaining != "" {
			if err := h.db.AddVetoStep(matchID, nextStep, "last", remaining, nil); err != nil {
				slog.Error("veto: add last step failed", "match", matchID, "err", err)
			} else {
				slog.Info("veto: auto-last", "match", matchID, "map", remaining)
				// Create game for the last map
				allVetoes, _ := h.db.GetMatchVetoes(matchID)
				gameNum := countPicksAndLasts(allVetoes[:len(allVetoes)-1], "last", remaining)
				if _, err := h.db.CreateGame(matchID, gameNum, remaining, true); err != nil {
					slog.Error("veto: create game for last map failed", "match", matchID, "map", remaining, "err", err)
				}
			}
		}
	}

	h.notifyBracket()

	// Return updated state
	state, _ := h.buildVetoState(matchID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// AdminClearVeto clears all vetoes for a match and removes pending veto-created games.
func (h *Handler) AdminClearVeto(w http.ResponseWriter, r *http.Request) {
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid match ID", http.StatusBadRequest)
		return
	}

	// Delete pending games (no server linked, status=pending) for this match
	games, _ := h.db.GetMatchGames(matchID)
	for _, g := range games {
		if g.Status == db.GamePending && g.ServerName == "" {
			h.db.DeleteGame(g.ID)
		}
	}

	if err := h.db.ClearVetoes(matchID); err != nil {
		slog.Error("veto: clear failed", "match", matchID, "err", err)
		http.Error(w, "Failed to clear vetoes", http.StatusInternalServerError)
		return
	}

	slog.Info("veto: cleared", "match", matchID)
	h.notifyBracket()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// buildVetoState constructs the full veto state for a match.
func (h *Handler) buildVetoState(matchID int64) (*vetoStateJSON, error) {
	match, err := h.db.GetMatchByID(matchID)
	if err != nil {
		return nil, err
	}

	tournament, err := h.db.GetTournamentByID(match.TournamentID)
	if err != nil {
		return nil, err
	}

	format := db.ParseVetoFormat(tournament.VetoFormat)
	pool := db.MapPool(tournament.GameMode)
	vetoes, err := h.db.GetMatchVetoes(matchID)
	if err != nil {
		return nil, err
	}

	// Build used maps set
	usedMaps := make(map[string]bool)
	for _, v := range vetoes {
		usedMaps[v.MapName] = true
	}

	// Available maps
	var available []string
	for _, m := range pool {
		if !usedMaps[m] {
			available = append(available, m)
		}
	}

	// Build veto steps JSON
	var steps []vetoStepJSON
	for _, v := range vetoes {
		steps = append(steps, vetoStepJSON{
			Step:     v.Step,
			Action:   v.Action,
			TeamID:   v.TeamID,
			TeamName: v.TeamName,
			MapName:  v.MapName,
		})
	}

	state := &vetoStateJSON{
		Vetoes:        steps,
		MapPool:       pool,
		AvailableMaps: available,
		TotalSteps:    len(format),
		Complete:      len(vetoes) >= len(format),
		Format:        format,
	}

	// Determine next step info
	nextStep := len(vetoes)
	if nextStep < len(format) {
		state.NextStep = nextStep
		state.NextAction = format[nextStep]
		teamID := vetoTeamForStep(nextStep, format, match)
		state.NextTeamID = teamID
		if teamID != nil {
			if *teamID == derefInt64(match.Team1ID) {
				state.NextTeamName = match.Team1Name
			} else if *teamID == derefInt64(match.Team2ID) {
				state.NextTeamName = match.Team2Name
			}
		}
	}

	return state, nil
}

// vetoTeamForStep determines which team acts at a given step.
// Even steps (0,2,4,...) = team1, odd steps (1,3,5,...) = team2.
// For "last" action, no team (return nil).
func vetoTeamForStep(step int, format []string, match *db.Match) *int64 {
	if step >= len(format) || format[step] == "last" {
		return nil
	}
	if step%2 == 0 {
		return match.Team1ID
	}
	return match.Team2ID
}

// countPicksAndLasts counts picked/last maps up to (but not including) the current one,
// then returns the next game number (1-indexed).
func countPicksAndLasts(vetoes []db.MapVeto, action, mapName string) int {
	count := 0
	for _, v := range vetoes {
		if v.Action == "pick" || v.Action == "last" {
			count++
		}
	}
	// The new pick/last is the next game number
	return count + 1
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
